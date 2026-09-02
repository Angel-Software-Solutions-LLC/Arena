package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"arena-server/internal/accounts"
	"arena-server/internal/config"
	"arena-server/internal/db"
)

/*
 * Reading whether somebody subscribes, at the one moment Arena is allowed to.
 *
 * Arena holds no credential for the Accounts API. It never stores the access
 * token from a sign-in, and it does not ask for `offline_access`, so there is
 * no refresh token either — which means the only moment Arena can read
 * entitlements is the few hundred milliseconds it is holding a freshly minted
 * token in the callback, before that token goes out of scope for good.
 *
 * That is a deliberate trade, not an oversight. The alternative is a durable
 * credential for every customer sitting in Arena's database: a table that is
 * worth stealing, has to be encrypted at rest, has to be revoked on sign-out,
 * and grows a rotation problem. Reading once per sign-in avoids all of it, and
 * signing in is already one click.
 *
 * What it costs is freshness. Somebody who subscribes in the Accounts app and
 * comes straight back does not see it until the next read. Two things close
 * that gap, and the second is not built here:
 *
 *   - Signing in again. One click, no password on a live Accounts session, and
 *     it re-reads. The Dashboard offers exactly this, labelled for what it does.
 *   - A subscription event on the Accounts webhook outbox, which pushes the
 *     news rather than waiting to be asked. That is the right long-term answer
 *     and it needs Arena's service client provisioned and the outbox signature
 *     scheme, neither of which exists yet.
 *
 * What is read is one bit: the `entitlements[]` row for the Arena product and
 * its `active` flag. Arena sells nothing per item any more, so there is no
 * purchase ledger to reconcile — the flag is written on the account and every
 * paid cosmetic is unlocked or hidden by it at read time.
 */

// entitlementsSyncTimeout bounds the read. A sign-in must not hang on the
// commerce service, so this is short enough to be invisible next to the token
// exchange that just happened and long enough for a cold Worker.
const entitlementsSyncTimeout = 10 * time.Second

// syncEntitlementsFromAccounts records, on the account that just signed in,
// whether Accounts reports an active Arena subscription right now, using the
// access token from the sign-in that just completed.
//
// Best effort by design, and the return value is only for tests and logs: a
// commerce service that is down must not stop somebody signing in to look at
// their bots. Nothing here is on the path to a session.
func (h *CustomerOIDCHandler) syncEntitlementsFromAccounts(ctx context.Context, accountID, accessToken string) (*db.SubscriptionSyncChange, error) {
	if h == nil || h.entitlements == nil {
		return nil, nil
	}
	if accountID == "" || accessToken == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, entitlementsSyncTimeout)
	defer cancel()
	snapshot, err := h.entitlements.Fetch(ctx, accessToken)
	if err != nil {
		/*
		 * A rejected token is worth its own line. It means the scope was not
		 * granted, or the client is not provisioned for entitlements yet —
		 * which is not a fault to page anybody about, but is exactly what an
		 * operator will want to see when they ask why nobody's subscription
		 * is arriving. Either way the previous answer stands: a read that
		 * failed is not a read that said "no".
		 */
		if errors.Is(err, accounts.ErrUnauthorized) {
			slog.Info("accounts entitlements not readable for this client", "account_id", accountID)
			return nil, err
		}
		slog.Warn("accounts entitlements read failed", "account_id", accountID, "error", err)
		return nil, err
	}
	active := snapshot.ArenaSubscriptionActive()
	if h.authority == nil {
		return nil, errors.New("accounts entitlements: no identity authority to record against")
	}
	change, err := h.authority.SetSubscription(ctx, accountID, active, time.Now().UTC())
	if err != nil {
		slog.Warn("accounts subscription sync failed", "account_id", accountID, "error", err)
		return nil, err
	}
	if change.Changed {
		slog.Info("accounts subscription applied", "account_id", accountID, "active", active, "bots", len(change.BotIDs))
		/*
		 * The flag has flipped, so every paid look on this account's bots
		 * just appeared or disappeared at the next database read. The
		 * engine caches presentation per connected bot, so the ones online
		 * are told now rather than at their next equip or reconnect.
		 */
		if h.onSubscriptionSynced != nil && len(change.BotIDs) > 0 {
			h.onSubscriptionSynced(ctx, change.BotIDs)
		}
	}
	return change, nil
}

// accountsShopURL is where a customer goes to subscribe: the Angel Accounts
// portal, from ARENA_ACCOUNTS_SHOP_URL. Empty when the operator has not set
// one, in which case the Dashboard says what is missing without a link.
func accountsShopURL() string {
	return strings.TrimSpace(config.C.AccountsShopURL)
}
