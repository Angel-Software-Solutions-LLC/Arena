package db

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrCustomerEmailInvalid      = errors.New("invalid customer email")
	ErrCustomerAccountNotFound   = errors.New("customer account not found")
	ErrCustomerAccountUnverified = errors.New("customer email is not verified")
	ErrCustomerIdentityConflict  = errors.New("verified identity is already bound to another account")
	ErrCustomerBotAlreadyLinked  = errors.New("bot is already linked to another account")
	ErrCustomerBotNotLinked      = errors.New("bot is not linked to this account")
	ErrCustomerBotKeyInactive    = errors.New("bot API key is inactive")
	// ErrSubscriptionRequired is the one commerce error left: a paid cosmetic
	// was asked for by an account whose Arena subscription is not active.
	ErrSubscriptionRequired = errors.New("an active Arena subscription is required for this cosmetic")
)

/*
 * botMayWearCosmeticSQL is the single rule for whether a bot may wear one
 * catalog item, written once and joined everywhere a paid cosmetic is read,
 * equipped or rendered. `l` is the bot (a row with `bot_id`), `i` the item.
 *
 *   - a free item is always available;
 *   - the admin demo-loadout tool grants bot-scoped complimentary items in
 *     `cosmetic_entitlements`, and those are honoured for that bot;
 *   - everything else is included with the Arena subscription: the bot must
 *     be linked to a customer account whose `subscription_active` flag is on.
 *
 * The flag is read at query time rather than copied into the loadout, so a
 * subscription that lapses hides every paid look at the next read with no
 * row to clean up, and one that resumes brings the saved loadout straight
 * back. There is no per-item ownership anywhere in Arena any more — Accounts
 * owns the subscription, and Arena owns which bot wears what.
 */
const botMayWearCosmeticSQL = `(
	i.is_free = true
	OR EXISTS (
		SELECT 1 FROM cosmetic_entitlements e
		WHERE e.bot_id = l.bot_id AND e.cosmetic_id = i.id
	)
	OR EXISTS (
		SELECT 1 FROM account_bot_links abl
		JOIN customer_accounts ca ON ca.id = abl.account_id
		WHERE abl.bot_id = l.bot_id AND ca.subscription_active = true
	)
)`

// CustomerAccount is the durable owner of an Arena subscription and of the
// bots that wear it. API keys are intentionally absent: keys prove control
// of a bot, but account ownership survives key loss, revocation, and
// replacement.
type CustomerAccount struct {
	ID string `json:"id"`
	// Empty for every account linked to an Accounts identity, which after the
	// cutover is all of them. Kept on the struct rather than deleted because
	// the cutover is sequenced: a legacy row still carries one until its owner
	// next signs in, and the straggler report reads exactly this field.
	Email           string     `json:"email,omitempty"`
	DisplayName     string     `json:"display_name"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	// SubscriptionActive is Arena's cache of the one commerce fact it acts
	// on: Accounts reported an ACTIVE entitlement for the Arena product at
	// this account's last sign-in. SubscriptionSyncedAt is when that was
	// last read; nil for an account that has never been synced.
	SubscriptionActive   bool       `json:"subscription_active"`
	SubscriptionSyncedAt *time.Time `json:"subscription_synced_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type AccountBot struct {
	BotID         string    `json:"bot_id"`
	Name          string    `json:"name"`
	AvatarColor   string    `json:"avatar_color"`
	DefaultWeapon string    `json:"default_weapon"`
	KeyPrefix     string    `json:"key_prefix"`
	KeyIsActive   bool      `json:"key_is_active"`
	LinkedAt      time.Time `json:"linked_at"`
}

// CustomerSubscription is what the Dashboard is told about the account's
// Arena subscription. It is a projection of the two account columns, kept as
// its own object so the wire shape can grow without renaming account fields.
type CustomerSubscription struct {
	Active   bool       `json:"active"`
	SyncedAt *time.Time `json:"synced_at,omitempty"`
	// URL is where the subscription is bought and managed. Filled in by the
	// API layer from configuration; the database knows nothing about it.
	URL string `json:"url,omitempty"`
}

// CustomerCosmeticsInventory is the Dashboard's view: the account, its bots,
// whether the subscription is active, every active catalog item (all of them
// are included with the subscription), and what each linked bot currently
// wears. There are no licences to list; with a subscription, everything is
// unlocked for every linked bot.
type CustomerCosmeticsInventory struct {
	Account      CustomerAccount      `json:"account"`
	Bots         []AccountBot         `json:"bots"`
	Subscription CustomerSubscription `json:"subscription"`
	Items        []CosmeticItem       `json:"items"`
	// Loadouts is bot id -> slot -> cosmetic id, for the cosmetics that bot
	// actually renders right now (a paid look saved while subscribed is
	// omitted while the subscription is lapsed, exactly as the spectator
	// stream omits it).
	Loadouts map[string]map[string]string `json:"loadouts"`
}

// SubscriptionSyncChange reports what one sync did, so the caller can refresh
// the live visuals of the bots it affected.
type SubscriptionSyncChange struct {
	AccountID string
	Active    bool
	Changed   bool
	BotIDs    []string
}

// NormalizeCustomerEmail produces the canonical form of an address, used only
// while pre-cutover accounts are still being matched by the address they
// signed up with.
func NormalizeCustomerEmail(raw string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" || len(normalized) > 254 {
		return "", ErrCustomerEmailInvalid
	}
	parsed, err := mail.ParseAddress(normalized)
	if err != nil || strings.ToLower(parsed.Address) != normalized || !strings.Contains(normalized, "@") {
		return "", ErrCustomerEmailInvalid
	}
	return normalized, nil
}

func scanCustomerAccount(row pgx.Row) (*CustomerAccount, error) {
	var account CustomerAccount
	// The address column is nullable now, and for a linked account it is
	// null. An absent address is not an error and not a special case for
	// every caller to handle — it reads as the empty string, which is what
	// "we do not hold one" has always looked like everywhere above this.
	var email *string
	err := row.Scan(&account.ID, &email, &account.DisplayName, &account.EmailVerifiedAt,
		&account.SubscriptionActive, &account.SubscriptionSyncedAt,
		&account.CreatedAt, &account.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if email != nil {
		account.Email = *email
	}
	return &account, nil
}

func customerAccountSelect() string {
	return `SELECT id, email, display_name, email_verified_at, subscription_active, subscription_synced_at,
	               created_at, updated_at FROM customer_accounts`
}

// lockCustomerAccount is the first lock taken by every account-scoped
// mutation. Serialising on this row gives equip, link, unlink, subscription
// sync and identity binding one shared lock order before they touch loadouts
// or bot links.
func lockCustomerAccount(ctx context.Context, tx pgx.Tx, accountID string, requireVerified bool) (*CustomerAccount, error) {
	account, err := scanCustomerAccount(tx.QueryRow(ctx,
		customerAccountSelect()+` WHERE id = $1 FOR UPDATE`, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCustomerAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lockCustomerAccount: %w", err)
	}
	if requireVerified && account.EmailVerifiedAt == nil {
		return nil, ErrCustomerAccountUnverified
	}
	return account, nil
}

// UpsertVerifiedCustomerAccount binds an Accounts identity to an Arena account
// without keeping the person's email address.
//
// The owner's instruction is the whole design: *"I don't want to have anyone's
// email addresses stored anymore, but instead switch it to the Accounts
// login."* Accounts is the identity provider now, so an address here would be a
// second copy of something we do not own, cannot keep current, and have no
// remaining use for — every question Arena used to answer with it is now
// answered by the `(oidc_issuer, oidc_subject)` pair.
//
// `linkEmail` is therefore an *input only*, and never reaches a column. It
// exists for one job: adopting an account that was created before this change,
// whose only identifier is the address it signed up with. The address is
// matched, the row is claimed, and the column is emptied in the same
// transaction — so an account is de-emailed by the very act of being linked,
// and a second sign-in finds it by subject and never looks at an address again.
// Pass an empty string once the cutover is finished and no legacy rows remain.
//
// `email_verified_at` is deliberately *kept* and refreshed. Despite the name it
// is the identity-verified marker the rest of the server reads — `keys.go`
// refuses bot API-key management without it — and the fact being recorded is
// still true, and now attested by Accounts rather than by Arena's own mail. The
// column's name outlived its meaning; renaming it is a separate change, because
// three readers and a session cache would move with it.
func UpsertVerifiedCustomerAccount(ctx context.Context, linkEmail, issuer, subject, displayName string) (*CustomerAccount, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	// Empty is allowed and expected: an identity that needs no legacy row
	// adopted supplies no address at all. Anything non-empty must still be a
	// real address, because it is about to be matched against stored ones.
	var email *string
	if strings.TrimSpace(linkEmail) != "" {
		normalized, err := NormalizeCustomerEmail(linkEmail)
		if err != nil {
			return nil, err
		}
		email = &normalized
	}
	var err error
	issuer = strings.TrimSpace(issuer)
	subject = strings.TrimSpace(subject)
	displayName = strings.TrimSpace(displayName)
	if issuer == "" || subject == "" || len(issuer) > 1024 || len(subject) > 512 || len(displayName) > 200 {
		return nil, ErrCustomerIdentityConflict
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("UpsertVerifiedCustomerAccount begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// The identity lookup is read-only. Once a candidate ID is known, the
	// account row itself is always the first row locked in this transaction.
	var accountID string
	err = tx.QueryRow(ctx, `
		SELECT id FROM customer_accounts
		WHERE oidc_issuer = $1 AND oidc_subject = $2`, issuer, subject).Scan(&accountID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("UpsertVerifiedCustomerAccount identity lookup: %w", err)
	}
	if err == nil {
		if _, err = lockCustomerAccount(ctx, tx, accountID, false); err == nil {
			// Every sign-in clears the address, not just the first. That makes
			// the purge self-healing: a row linked before this change still
			// carries what it stored then, and is emptied the next time its
			// owner appears, without a migration having to find it.
			_, err = tx.Exec(ctx, `
				UPDATE customer_accounts
				SET email = NULL, display_name = $2, email_verified_at = NOW(), updated_at = NOW()
				WHERE id = $1 AND oidc_issuer = $3 AND oidc_subject = $4`,
				accountID, displayName, issuer, subject)
		}
	} else {
		// Only an as-yet-unbound legacy account may be claimed by address, and
		// only while an address is still being supplied. With linking finished
		// this lookup does not run at all and the branch below simply creates
		// an account that never had one.
		if email == nil {
			err = pgx.ErrNoRows
		} else {
			err = tx.QueryRow(ctx, `
				SELECT id FROM customer_accounts
				WHERE email = $1 AND oidc_issuer IS NULL AND oidc_subject IS NULL
				FOR UPDATE`, *email).Scan(&accountID)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// A concurrent callback for this same identity may have completed
			// while the pending-email row lock was waiting. Recheck the stable
			// identity before attempting a unique email insert.
			var racedAccountID string
			raceErr := tx.QueryRow(ctx, `
				SELECT id FROM customer_accounts
				WHERE oidc_issuer = $1 AND oidc_subject = $2`, issuer, subject).Scan(&racedAccountID)
			if raceErr == nil {
				accountID = racedAccountID
				if _, err = lockCustomerAccount(ctx, tx, accountID, false); err == nil {
					_, err = tx.Exec(ctx, `
						UPDATE customer_accounts
						SET email = NULL, display_name = $2, email_verified_at = NOW(), updated_at = NOW()
						WHERE id = $1 AND oidc_issuer = $3 AND oidc_subject = $4`,
						accountID, displayName, issuer, subject)
				}
			} else if errors.Is(raceErr, pgx.ErrNoRows) {
				/*
				 * One identity per address, for as long as an address is held.
				 *
				 * This used to be enforced by the UNIQUE index alone: the
				 * insert below carried the address, a row already holding it
				 * raised 23505, and that surfaced as an identity conflict. The
				 * insert no longer carries one, so the index no longer fires
				 * and the check has to be made in the open.
				 *
				 * It is not a formality. Without it a second Accounts subject
				 * presenting a verified address that some *already linked*
				 * account still carries would quietly be given an account of
				 * its own, and the collision nobody looked at would be the
				 * first sign that two identities claim one mailbox. Takeover
				 * was never possible — claiming an existing row requires it to
				 * be unlinked — but silence is the wrong answer to this.
				 *
				 * Once a row is purged it holds no address, matches nothing
				 * here, and stops being able to collide at all.
				 */
				if email != nil {
					var conflicting string
					clashErr := tx.QueryRow(ctx, `
						SELECT id FROM customer_accounts WHERE email = $1`, *email).Scan(&conflicting)
					if clashErr == nil {
						return nil, ErrCustomerIdentityConflict
					}
					if !errors.Is(clashErr, pgx.ErrNoRows) {
						return nil, fmt.Errorf("UpsertVerifiedCustomerAccount address check: %w", clashErr)
					}
				}
				accountID = uuid.NewString()
				// A brand-new account is created without an address at all.
				// There is nothing to purge later because nothing is written.
				_, err = tx.Exec(ctx, `
					INSERT INTO customer_accounts
						(id, email, display_name, email_verified_at, oidc_issuer, oidc_subject, created_at, updated_at)
					VALUES ($1, NULL, $2, NOW(), $3, $4, NOW(), NOW())`,
					accountID, displayName, issuer, subject)
			} else {
				err = raceErr
			}
		} else if err == nil {
			// The adoption. Claiming the row and emptying its address are one
			// statement in one transaction, so there is no window in which an
			// account is linked and still carries what it was matched by.
			_, err = tx.Exec(ctx, `
				UPDATE customer_accounts
				SET email = NULL, display_name = $2, email_verified_at = NOW(),
				    oidc_issuer = $3, oidc_subject = $4, updated_at = NOW()
				WHERE id = $1 AND oidc_issuer IS NULL AND oidc_subject IS NULL`,
				accountID, displayName, issuer, subject)
		}
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrCustomerIdentityConflict
		}
		return nil, fmt.Errorf("UpsertVerifiedCustomerAccount bind: %w", err)
	}

	account, err := scanCustomerAccount(tx.QueryRow(ctx, customerAccountSelect()+` WHERE id = $1`, accountID))
	if err != nil {
		return nil, fmt.Errorf("UpsertVerifiedCustomerAccount load: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("UpsertVerifiedCustomerAccount commit: %w", err)
	}
	return account, nil
}

func GetCustomerAccount(ctx context.Context, accountID string) (*CustomerAccount, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	account, err := scanCustomerAccount(Pool.QueryRow(ctx, customerAccountSelect()+` WHERE id = $1`, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCustomerAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetCustomerAccount: %w", err)
	}
	return account, nil
}

/*
 * SetCustomerSubscription records what Accounts said at this sign-in.
 *
 * It is the whole of the "entitlement sync" now. Nothing is materialised per
 * item; the flag is joined at read time by botMayWearCosmeticSQL, so
 * flipping it is what unlocks or hides every paid cosmetic on every bot this
 * account has linked. The affected bot ids are returned so the caller can
 * refresh the engine's presentation cache for the ones that are connected.
 *
 * Both directions are written, deliberately. A lapsed subscription is a fact
 * Accounts reported, and the previous flag is a cache of an older report;
 * keeping it would be Arena deciding to disagree with the authority.
 */
func SetCustomerSubscription(ctx context.Context, accountID string, active bool, syncedAt time.Time) (*SubscriptionSyncChange, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, ErrCustomerAccountNotFound
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("SetCustomerSubscription begin: %w", err)
	}
	defer tx.Rollback(ctx)
	account, err := lockCustomerAccount(ctx, tx, accountID, false)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE customer_accounts
		SET subscription_active = $2, subscription_synced_at = $3, updated_at = NOW()
		WHERE id = $1`, accountID, active, syncedAt.UTC()); err != nil {
		return nil, fmt.Errorf("SetCustomerSubscription update: %w", err)
	}
	change := &SubscriptionSyncChange{AccountID: accountID, Active: active, Changed: account.SubscriptionActive != active}
	if change.Changed {
		rows, err := tx.Query(ctx, `SELECT bot_id FROM account_bot_links WHERE account_id = $1 ORDER BY bot_id`, accountID)
		if err != nil {
			return nil, fmt.Errorf("SetCustomerSubscription bots: %w", err)
		}
		for rows.Next() {
			var botID string
			if err := rows.Scan(&botID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("SetCustomerSubscription bot scan: %w", err)
			}
			change.BotIDs = append(change.BotIDs, botID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("SetCustomerSubscription bot rows: %w", err)
		}
		rows.Close()
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("SetCustomerSubscription commit: %w", err)
	}
	return change, nil
}

func ListAccountBots(ctx context.Context, accountID string) ([]AccountBot, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx, `
		SELECT b.id, b.name, b.avatar_color, b.default_weapon,
		       k.key_prefix, k.is_active, l.linked_at
		FROM account_bot_links l
		JOIN bots b ON b.id = l.bot_id
		JOIN api_keys k ON k.id = b.api_key_id
		WHERE l.account_id = $1
		ORDER BY l.linked_at, b.id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("ListAccountBots: %w", err)
	}
	defer rows.Close()
	bots := make([]AccountBot, 0)
	for rows.Next() {
		var bot AccountBot
		if err := rows.Scan(&bot.BotID, &bot.Name, &bot.AvatarColor, &bot.DefaultWeapon,
			&bot.KeyPrefix, &bot.KeyIsActive, &bot.LinkedAt); err != nil {
			return nil, fmt.Errorf("ListAccountBots scan: %w", err)
		}
		bots = append(bots, bot)
	}
	return bots, rows.Err()
}

// ListAccountBotLoadouts reports what each of the account's linked bots
// renders right now: bot id -> slot -> cosmetic id, through the same rule the
// spectator stream uses, so the Dashboard never shows a look the arena does
// not.
func ListAccountBotLoadouts(ctx context.Context, accountID string) (map[string]map[string]string, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx, `
		SELECT l.bot_id, l.slot, l.cosmetic_id
		FROM bot_cosmetic_loadout l
		JOIN account_bot_links abl ON abl.bot_id = l.bot_id AND abl.account_id = $1
		JOIN cosmetic_items i ON i.id = l.cosmetic_id AND i.slot = l.slot
		JOIN cosmetic_categories c ON c.id = i.category_id
		WHERE i.is_active = true AND c.is_active = true AND `+botMayWearCosmeticSQL+`
		ORDER BY l.bot_id, l.slot`, accountID)
	if err != nil {
		return nil, fmt.Errorf("ListAccountBotLoadouts: %w", err)
	}
	defer rows.Close()
	loadouts := make(map[string]map[string]string)
	for rows.Next() {
		var botID, slot, cosmeticID string
		if err := rows.Scan(&botID, &slot, &cosmeticID); err != nil {
			return nil, fmt.Errorf("ListAccountBotLoadouts scan: %w", err)
		}
		if loadouts[botID] == nil {
			loadouts[botID] = make(map[string]string)
		}
		loadouts[botID][slot] = cosmeticID
	}
	return loadouts, rows.Err()
}

func ClaimArenaAgentWithControlProof(
	ctx context.Context,
	accountID, controlProof string,
) (*AccountBot, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	if len(controlProof) < 12 || len(controlProof) > 256 {
		return nil, ErrPlatformControlProofRejected
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("ClaimArenaAgentWithControlProof begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := lockCustomerAccount(ctx, tx, accountID, true); err != nil {
		return nil, err
	}
	// Match the exact-command and native-registration lock order. The legacy
	// facade intentionally adopts the locked current revision instead of
	// inventing a client revision or idempotency key.
	if _, err := lockPlatformAccountCapacityTx(ctx, tx, accountID); err != nil {
		return nil, err
	}

	bot, apiKeyID, agentStatus, err := loadPlatformAgentControlProofTx(ctx, tx, controlProof)
	if err != nil {
		return nil, err
	}
	if agentStatus == "retired" {
		return nil, ErrPlatformAgentInactive
	}

	linked, _, err := linkBotToCustomerAccountTx(ctx, tx, accountID, bot, apiKeyID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("ClaimArenaAgentWithControlProof commit: %w", err)
	}
	return linked, nil
}

// linkBotToCustomerAccountTx is the single private link-state core. Callers
// must lock the verified customer account and, for existing agents, verify and
// lock the control credential before entering it.
func linkBotToCustomerAccountTx(
	ctx context.Context,
	tx pgx.Tx,
	accountID string,
	bot AccountBot,
	apiKeyID string,
) (*AccountBot, bool, error) {
	botID := bot.BotID

	var owningAccountID string
	ownershipErr := tx.QueryRow(ctx, `
		SELECT account_id FROM account_api_keys WHERE api_key_id = $1 FOR UPDATE`, apiKeyID).Scan(&owningAccountID)
	if ownershipErr == nil && owningAccountID != accountID {
		return nil, false, ErrCustomerAPIKeyAlreadyOwned
	}
	if ownershipErr != nil && !errors.Is(ownershipErr, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("LinkBotToCustomerAccount key ownership: %w", ownershipErr)
	}
	if errors.Is(ownershipErr, pgx.ErrNoRows) {
		activeCount, totalCount, err := accountAPIKeyCapacity(ctx, tx, accountID)
		if err != nil {
			return nil, false, err
		}
		if activeCount >= MaxActiveAccountAPIKeys {
			return nil, false, ErrCustomerAPIKeyLimit
		}
		if totalCount >= MaxAccountAPIKeyHistory {
			return nil, false, ErrCustomerAPIKeyHistoryLimit
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO account_api_keys (account_id, api_key_id, linked_at)
			VALUES ($1, $2, NOW())`, accountID, apiKeyID); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return nil, false, ErrCustomerAPIKeyAlreadyOwned
			}
			return nil, false, fmt.Errorf("LinkBotToCustomerAccount key ownership insert: %w", err)
		}
	}

	var existingAccountID string
	err := tx.QueryRow(ctx, `SELECT account_id FROM account_bot_links WHERE bot_id = $1 FOR UPDATE`, botID).Scan(&existingAccountID)
	if err == nil && existingAccountID != accountID {
		return nil, false, ErrCustomerBotAlreadyLinked
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("LinkBotToCustomerAccount existing link: %w", err)
	}
	linkCreated := errors.Is(err, pgx.ErrNoRows)
	if linkCreated {
		if err := enforcePlatformAgentCapacityTx(ctx, tx, accountID); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO account_bot_links (account_id, bot_id, linked_at)
			VALUES ($1, $2, NOW())`, accountID, botID); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return nil, false, ErrCustomerBotAlreadyLinked
			}
			return nil, false, fmt.Errorf("LinkBotToCustomerAccount insert: %w", err)
		}
	}
	if linkCreated {
		if err := appendPlatformAgentLinkEventTx(ctx, tx, accountID, botID, "linked", "arena_account_link", time.Now()); err != nil {
			return nil, false, fmt.Errorf("LinkBotToCustomerAccount platform link: %w", err)
		}
	}
	if err := tx.QueryRow(ctx, `SELECT linked_at FROM account_bot_links WHERE account_id = $1 AND bot_id = $2`,
		accountID, botID).Scan(&bot.LinkedAt); err != nil {
		return nil, false, fmt.Errorf("LinkBotToCustomerAccount linked_at: %w", err)
	}
	return &bot, linkCreated, nil
}

func UnlinkBotFromCustomerAccount(ctx context.Context, accountID, botID string) (bool, error) {
	if Pool == nil {
		return false, ErrNoDatabase
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("UnlinkBotFromCustomerAccount begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := lockCustomerAccount(ctx, tx, accountID, true); err != nil {
		return false, err
	}
	if _, err := unlinkBotFromCustomerAccountTx(ctx, tx, accountID, botID, "arena_account_unlink"); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("UnlinkBotFromCustomerAccount commit: %w", err)
	}
	return true, nil
}

func unlinkBotFromCustomerAccountTx(ctx context.Context, tx pgx.Tx, accountID, botID, reason string) (*PlatformAgentLinkResult, error) {
	var linkedAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT linked_at FROM account_bot_links WHERE account_id = $1 AND bot_id = $2 FOR UPDATE`,
		accountID, botID).Scan(&linkedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCustomerBotNotLinked
		}
		return nil, fmt.Errorf("unlink bot from customer account link: %w", err)
	}
	/*
	 * The subscription travelled with the link, so the paid loadout goes
	 * with it. Free items and admin demo grants belong to the bot itself and
	 * stay. Deleting rather than relying on the read-time rule alone keeps
	 * the loadout table honest: a bot nobody subscribes for has no paid rows
	 * waiting to reappear the moment somebody else links it.
	 */
	if _, err := tx.Exec(ctx, `
		DELETE FROM bot_cosmetic_loadout l
		USING cosmetic_items i
		WHERE l.bot_id = $1 AND i.id = l.cosmetic_id AND i.is_free = false
		  AND NOT EXISTS (
		    SELECT 1 FROM cosmetic_entitlements e
		    WHERE e.bot_id = l.bot_id AND e.cosmetic_id = i.id
		  )`, botID); err != nil {
		return nil, fmt.Errorf("unlink bot from customer account loadout: %w", err)
	}
	_, err := tx.Exec(ctx, `DELETE FROM account_bot_links WHERE account_id = $1 AND bot_id = $2`, accountID, botID)
	if err != nil {
		return nil, fmt.Errorf("unlink bot from customer account delete: %w", err)
	}
	if err := appendPlatformAgentLinkEventTx(ctx, tx, accountID, botID, "unlinked", reason, time.Now()); err != nil {
		return nil, fmt.Errorf("unlink bot from customer account platform link: %w", err)
	}
	result := &PlatformAgentLinkResult{
		AccountID: accountID,
		AgentID:   botID,
		Status:    "unlinked",
		LinkedAt:  linkedAt,
	}
	if err := tx.QueryRow(ctx, `
		SELECT revision, occurred_at
		FROM platform_agent_link_events
		WHERE account_id = $1 AND agent_id = $2
		ORDER BY event_id DESC
		LIMIT 1`, accountID, botID).Scan(&result.Revision, &result.UpdatedAt); err != nil {
		return nil, fmt.Errorf("unlink bot from customer account load result: %w", err)
	}
	result.UnlinkedAt = &result.UpdatedAt
	return result, nil
}

func IsBotLinkedToCustomerAccount(ctx context.Context, accountID, botID string) (bool, error) {
	if Pool == nil {
		return false, ErrNoDatabase
	}
	var linked bool
	if err := Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM account_bot_links WHERE account_id = $1 AND bot_id = $2)`,
		accountID, botID).Scan(&linked); err != nil {
		return false, fmt.Errorf("IsBotLinkedToCustomerAccount: %w", err)
	}
	return linked, nil
}

func GetCustomerCosmeticsInventory(ctx context.Context, accountID string) (*CustomerCosmeticsInventory, error) {
	account, err := GetCustomerAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	bots, err := ListAccountBots(ctx, accountID)
	if err != nil {
		return nil, err
	}
	items, err := ListCosmeticCatalog(ctx)
	if err != nil {
		return nil, err
	}
	loadouts, err := ListAccountBotLoadouts(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return &CustomerCosmeticsInventory{
		Account: *account,
		Bots:    bots,
		Subscription: CustomerSubscription{
			Active: account.SubscriptionActive, SyncedAt: account.SubscriptionSyncedAt,
		},
		Items:    items,
		Loadouts: loadouts,
	}, nil
}

/*
 * EquipCustomerCosmetic is the Dashboard's equip: this linked bot wears this
 * catalog item in this slot from now on.
 *
 * A free item needs only the link. A paid item needs the account's Arena
 * subscription to be active — that is the one and only ownership check, and
 * it is made against the account row locked at the top of the transaction so
 * a sync landing at the same moment cannot be half-seen. The admin demo grant
 * is not consulted here: it is a bot-scoped complimentary path for the demo
 * fleet, and the Dashboard equips what the subscription includes.
 */
func EquipCustomerCosmetic(ctx context.Context, accountID, botID, slot, cosmeticID string) (*CosmeticItem, error) {
	slot = strings.TrimSpace(strings.ToLower(slot))
	if !IsValidCosmeticSlot(slot) {
		return nil, ErrInvalidCosmeticSlot
	}
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("EquipCustomerCosmetic begin: %w", err)
	}
	defer tx.Rollback(ctx)
	account, err := lockCustomerAccount(ctx, tx, accountID, true)
	if err != nil {
		return nil, err
	}
	var keyIsActive bool
	if err := tx.QueryRow(ctx, `
		SELECT k.is_active
		FROM account_bot_links l
		JOIN bots b ON b.id = l.bot_id
		JOIN api_keys k ON k.id = b.api_key_id
		WHERE l.account_id = $1 AND l.bot_id = $2
		FOR SHARE OF l, k`, accountID, botID).Scan(&keyIsActive); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCustomerBotNotLinked
		}
		return nil, fmt.Errorf("EquipCustomerCosmetic bot link: %w", err)
	}
	if !keyIsActive {
		return nil, ErrCustomerBotKeyInactive
	}
	item, err := lockActiveCosmeticItemTx(ctx, tx, cosmeticID)
	if err != nil {
		return nil, err
	}
	if item.Slot != slot {
		return nil, ErrCosmeticSlotMismatch
	}
	if !item.IsFree && !account.SubscriptionActive {
		return nil, ErrSubscriptionRequired
	}
	if err := upsertBotCosmeticLoadoutTx(ctx, tx, botID, slot, cosmeticID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("EquipCustomerCosmetic commit: %w", err)
	}
	return item, nil
}
