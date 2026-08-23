package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"arena-server/internal/accounts"
	"arena-server/internal/db"
)

/*
 * Reading what somebody owns, at the one moment Arena is allowed to.
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
 * What it costs is freshness. Somebody who buys a pack in the Accounts app and
 * comes straight back does not see it until the next read. Two things close
 * that gap, and the second is not built here:
 *
 *   - Signing in again. One click, no password on a live Accounts session, and
 *     it re-reads. The shop offers exactly this, labelled for what it does.
 *   - `purchase.granted` on the Accounts webhook outbox, which pushes the news
 *     rather than waiting to be asked. That is the right long-term answer and
 *     it needs Arena's service client provisioned and the outbox signature
 *     scheme, neither of which exists yet. Flagged in the pull request rather
 *     than guessed at here: a signature verifier written against an imagined
 *     scheme is worse than no verifier, because it looks like one.
 */

// entitlementsSyncTimeout bounds the read. A sign-in must not hang on the
// commerce service, so this is short enough to be invisible next to the token
// exchange that just happened and long enough for a cold Worker.
const entitlementsSyncTimeout = 10 * time.Second

// syncEntitlementsFromAccounts reconciles one account against the Accounts
// entitlements endpoint, using the access token from the sign-in that just
// completed.
//
// Best effort by design, and the return value is only for tests and logs: a
// commerce service that is down must not stop somebody signing in to look at
// their bots. Nothing here is on the path to a session.
func (h *CustomerOIDCHandler) syncEntitlementsFromAccounts(ctx context.Context, accountID, accessToken string) (*db.AccountsPurchaseSyncResult, error) {
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
		 * which is the state this ships in, and is not a fault to page anybody
		 * about, but is exactly what an operator will want to see when they
		 * ask why nobody's purchases are arriving.
		 */
		if errors.Is(err, accounts.ErrUnauthorized) {
			slog.Info("accounts entitlements not readable for this client", "account_id", accountID)
			return nil, err
		}
		slog.Warn("accounts entitlements read failed", "account_id", accountID, "error", err)
		return nil, err
	}
	grants := purchaseGrants(snapshot)
	if len(grants) == 0 {
		return &db.AccountsPurchaseSyncResult{UnknownSKUs: []string{}}, nil
	}
	result, err := db.SyncAccountsPurchases(ctx, accountID, grants)
	if err != nil {
		slog.Warn("accounts entitlements sync failed", "account_id", accountID, "error", err)
		return nil, err
	}
	if result.GrantsMaterialized > 0 || result.GrantsRevoked > 0 {
		slog.Info("accounts entitlements applied",
			"account_id", accountID,
			"grants_materialized", result.GrantsMaterialized,
			"grants_revoked", result.GrantsRevoked,
			"licenses_granted", result.LicensesGranted,
			"licenses_revoked", result.LicensesRevoked)
	}
	if len(result.UnknownSKUs) > 0 {
		/*
		 * A purchase naming a pack this Arena does not have. Reported at warn
		 * because it is somebody who paid and did not get the thing, which is
		 * the failure that matters most here — and it is the signal that the
		 * two catalogues have drifted.
		 */
		slog.Warn("accounts entitlements named packs this arena does not sell",
			"account_id", accountID, "skus", result.UnknownSKUs)
	}
	return result, nil
}

// purchaseGrants reduces a snapshot to what the reconciler acts on.
//
// Every purchase is carried, revoked ones included — the reconciler needs to
// be told about a withdrawal to act on it, and dropping revoked rows here
// would make a revocation indistinguishable from a purchase that simply
// stopped being mentioned, which is the one thing it must never do anything
// about.
func purchaseGrants(snapshot *accounts.Snapshot) []db.AccountsPurchaseGrant {
	if snapshot == nil {
		return nil
	}
	grants := make([]db.AccountsPurchaseGrant, 0, len(snapshot.Purchases))
	for _, purchase := range snapshot.Purchases {
		if purchase.ID == "" || purchase.SKU == "" {
			continue
		}
		grants = append(grants, db.AccountsPurchaseGrant{
			GrantID: purchase.ID,
			SKU:     purchase.SKU,
			Revoked: !purchase.Active(),
		})
	}
	return grants
}
