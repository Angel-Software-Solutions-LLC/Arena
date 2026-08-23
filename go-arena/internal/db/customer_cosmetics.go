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
	ErrCustomerEmailInvalid             = errors.New("invalid customer email")
	ErrCustomerAccountNotFound          = errors.New("customer account not found")
	ErrCustomerAccountUnverified        = errors.New("customer email is not verified")
	ErrCustomerIdentityConflict         = errors.New("verified identity is already bound to another account")
	ErrCustomerBotAlreadyLinked         = errors.New("bot is already linked to another account")
	ErrCustomerBotNotLinked             = errors.New("bot is not linked to this account")
	ErrCustomerBotKeyInactive           = errors.New("bot API key is inactive")
	ErrCosmeticLicenseNotFound          = errors.New("cosmetic license not found")
	ErrCosmeticLicenseNotOwned          = errors.New("cosmetic license is not owned by this account")
	ErrCosmeticLicenseReferenceRequired = errors.New("external reference is required for provider fulfillment")
	ErrCosmeticLicenseGrantConflict     = errors.New("external reference already granted a different cosmetic license")
	ErrCosmeticLicenseAlreadyAssigned   = errors.New("cosmetic license is already assigned to another agent")
)

// CustomerAccount is the durable owner of purchased cosmetics. API keys are
// intentionally absent: keys prove control of a bot, but account ownership
// survives key loss, revocation, and replacement.
type CustomerAccount struct {
	ID string `json:"id"`
	// Empty for every account linked to an Accounts identity, which after the
	// cutover is all of them. Kept on the struct rather than deleted because
	// the cutover is sequenced: a legacy row still carries one until its owner
	// next signs in, and the straggler report reads exactly this field.
	Email           string     `json:"email,omitempty"`
	DisplayName     string     `json:"display_name"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
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

// CosmeticLicense represents one independently assignable copy. Buying the
// same catalog item twice creates two rows with different IDs.
type CosmeticLicense struct {
	ID              string       `json:"id"`
	AccountID       *string      `json:"account_id,omitempty"`
	LegacyBotID     *string      `json:"legacy_bot_id,omitempty"`
	CosmeticID      string       `json:"cosmetic_id"`
	AssignedBotID   *string      `json:"assigned_bot_id,omitempty"`
	AssignedBotName *string      `json:"assigned_bot_name,omitempty"`
	Equipped        bool         `json:"equipped"`
	Status          string       `json:"status"`
	Source          string       `json:"source"`
	ExternalRef     *string      `json:"external_reference,omitempty"`
	Revision        int64        `json:"revision"`
	GrantedAt       time.Time    `json:"granted_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	Item            CosmeticItem `json:"item"`
}

type CustomerCosmeticsInventory struct {
	Account           CustomerAccount           `json:"account"`
	Bots              []AccountBot              `json:"bots"`
	Licenses          []CosmeticLicense         `json:"licenses"`
	Subscription      *CosmeticSubscription     `json:"subscription"`
	SubscriptionOffer CosmeticSubscriptionOffer `json:"subscription_offer"`
	// Membership is the account's active admin-granted "All Access" grant, if
	// any. It is a distinct entitlement from Subscription (Stripe-driven) --
	// both can grant the same catalog access, but only Subscription implies a
	// recurring charge. The Dashboard's All Access status must treat either as
	// "access active" or a staff-granted membership silently looks inactive.
	Membership *CosmeticAdminMembership `json:"membership,omitempty"`
}

type CosmeticAssignmentChange struct {
	License       CosmeticLicense `json:"license"`
	PreviousBotID *string         `json:"previous_bot_id,omitempty"`
	CurrentBotID  *string         `json:"current_bot_id,omitempty"`
}

// NormalizeCustomerEmail produces the canonical ownership key used by both
// payment fulfillment and verified OIDC login.
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
	return `SELECT id, email, display_name, email_verified_at, created_at, updated_at FROM customer_accounts`
}

// lockCustomerAccount is the first lock taken by every account-scoped
// mutation. Serialising on this row gives assign, equip, link, unlink, grant,
// and identity binding one shared lock order before they touch licenses or bot
// links.
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

// GetOrCreateCustomerAccountByEmail creates a pending account for fulfillment.
// It does not mark the email verified; only a verified OIDC claim does that.
func GetOrCreateCustomerAccountByEmail(ctx context.Context, rawEmail string) (*CustomerAccount, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	email, err := NormalizeCustomerEmail(rawEmail)
	if err != nil {
		return nil, err
	}
	account, err := scanCustomerAccount(Pool.QueryRow(ctx, `
		INSERT INTO customer_accounts (id, email, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (email) DO UPDATE SET updated_at = customer_accounts.updated_at
		RETURNING id, email, display_name, email_verified_at, created_at, updated_at`,
		uuid.NewString(), email))
	if err != nil {
		return nil, fmt.Errorf("GetOrCreateCustomerAccountByEmail: %w", err)
	}
	return account, nil
}

// UpsertVerifiedCustomerAccount binds a verified OIDC identity to the durable
// email account. It first follows a stable issuer+subject binding. Email
// fallback is deliberately restricted to an identity-null pending account so
// a second identity can never take over an already-bound email owner.
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
	// Claim every pre-account license for this bot. The account assignment is
	// represented by a composite-FK row, so a future handler bug cannot point a
	// license at a bot belonging to another account.
	legacyRows, err := tx.Query(ctx, `
		SELECT id, assigned_bot_id, status FROM cosmetic_licenses
		WHERE legacy_bot_id = $1 AND account_id IS NULL
		ORDER BY id FOR UPDATE`, botID)
	if err != nil {
		return nil, false, fmt.Errorf("LinkBotToCustomerAccount legacy locks: %w", err)
	}
	type legacyClaim struct {
		licenseID string
		assigned  *string
		status    string
	}
	claims := make([]legacyClaim, 0)
	for legacyRows.Next() {
		var claim legacyClaim
		if err := legacyRows.Scan(&claim.licenseID, &claim.assigned, &claim.status); err != nil {
			legacyRows.Close()
			return nil, false, fmt.Errorf("LinkBotToCustomerAccount legacy scan: %w", err)
		}
		claims = append(claims, claim)
	}
	if err := legacyRows.Err(); err != nil {
		legacyRows.Close()
		return nil, false, fmt.Errorf("LinkBotToCustomerAccount legacy rows: %w", err)
	}
	legacyRows.Close()
	for _, claim := range claims {
		var preserveAgentID *string
		if claim.status == "active" && claim.assigned != nil && *claim.assigned == botID {
			preserveAgentID = &botID
		}
		if _, err := claimCosmeticLicenseTx(
			ctx, tx, claim.licenseID, accountID, preserveAgentID, "arena_agent_link_claim",
		); err != nil {
			return nil, false, fmt.Errorf("LinkBotToCustomerAccount claim legacy: %w", err)
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
	// The account row above serializes all same-account mutations. License locks
	// then protect against admin revocation while the assignment is removed.
	rows, err := tx.Query(ctx, `
		SELECT cl.id FROM cosmetic_licenses cl
		JOIN cosmetic_license_assignments cla ON cla.license_id = cl.id
		WHERE cla.account_id = $1 AND cla.bot_id = $2
		ORDER BY cl.id FOR UPDATE OF cl, cla`, accountID, botID)
	if err != nil {
		return nil, fmt.Errorf("unlink bot from customer account license locks: %w", err)
	}
	licenseIDs := make([]string, 0)
	for rows.Next() {
		var licenseID string
		if err := rows.Scan(&licenseID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("unlink bot from customer account license scan: %w", err)
		}
		licenseIDs = append(licenseIDs, licenseID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("unlink bot from customer account license rows: %w", err)
	}
	rows.Close()
	for _, licenseID := range licenseIDs {
		license, _, err := lockPlatformCosmeticLicenseTx(ctx, tx, licenseID)
		if err != nil {
			return nil, err
		}
		if _, err := applyCosmeticLicenseAssignmentTx(
			ctx, tx, license, nil, "unassigned", "arena", "arena_agent_unlink",
		); err != nil {
			return nil, err
		}
	}
	_, err = tx.Exec(ctx, `DELETE FROM account_bot_links WHERE account_id = $1 AND bot_id = $2`, accountID, botID)
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

const cosmeticLicenseSelect = `
	SELECT cl.id, cl.account_id, cl.legacy_bot_id, cl.cosmetic_id,
	       COALESCE(cla.bot_id, cl.assigned_bot_id), b.name,
	       EXISTS (SELECT 1 FROM bot_cosmetic_loadout l WHERE l.license_id = cl.id) AS equipped,
	       cl.status, cl.source, cl.external_reference, cl.revision,
	       cl.granted_at, cl.updated_at,
	       i.id, i.name, i.description, i.slot, i.asset_key, i.rarity,
	       i.price_cents, i.currency, i.is_free, i.is_purchasable,
	       (i.is_active AND c.is_active), i.is_builtin
	FROM cosmetic_licenses cl
	JOIN cosmetic_items i ON i.id = cl.cosmetic_id
	JOIN cosmetic_categories c ON c.id = i.category_id
	LEFT JOIN cosmetic_license_assignments cla ON cla.license_id = cl.id
	LEFT JOIN bots b ON b.id = COALESCE(cla.bot_id, cl.assigned_bot_id)`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCosmeticLicense(row rowScanner) (*CosmeticLicense, error) {
	var license CosmeticLicense
	err := row.Scan(&license.ID, &license.AccountID, &license.LegacyBotID, &license.CosmeticID,
		&license.AssignedBotID, &license.AssignedBotName, &license.Equipped,
		&license.Status, &license.Source, &license.ExternalRef, &license.Revision,
		&license.GrantedAt, &license.UpdatedAt,
		&license.Item.ID, &license.Item.Name, &license.Item.Description, &license.Item.Slot,
		&license.Item.AssetKey, &license.Item.Rarity, &license.Item.PriceCents,
		&license.Item.Currency, &license.Item.IsFree, &license.Item.IsPurchasable, &license.Item.IsActive, &license.Item.IsBuiltin)
	if err != nil {
		return nil, err
	}
	return &license, nil
}

func getCosmeticLicense(ctx context.Context, licenseID string) (*CosmeticLicense, error) {
	license, err := scanCosmeticLicense(Pool.QueryRow(ctx, cosmeticLicenseSelect+` WHERE cl.id = $1`, licenseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCosmeticLicenseNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getCosmeticLicense: %w", err)
	}
	return license, nil
}

func ListCustomerCosmeticLicenses(ctx context.Context, accountID string) ([]CosmeticLicense, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx, cosmeticLicenseSelect+`
		WHERE cl.account_id = $1
		ORDER BY cl.granted_at, cl.id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("ListCustomerCosmeticLicenses: %w", err)
	}
	defer rows.Close()
	licenses := make([]CosmeticLicense, 0)
	for rows.Next() {
		license, err := scanCosmeticLicense(rows)
		if err != nil {
			return nil, fmt.Errorf("ListCustomerCosmeticLicenses scan: %w", err)
		}
		licenses = append(licenses, *license)
	}
	return licenses, rows.Err()
}

func GetCustomerCosmeticsInventory(ctx context.Context, accountID string) (*CustomerCosmeticsInventory, error) {
	// Subscription access is materialized as ordinary per-item licenses. This
	// idempotent sync makes newly published sets available before the Dashboard
	// renders inventory, without weakening the existing assignment constraints.
	if _, err := SyncCustomerCosmeticSubscriptionLicenses(ctx, accountID); err != nil {
		return nil, err
	}
	if _, err := SyncCustomerCosmeticAdminMembershipLicenses(ctx, accountID); err != nil {
		return nil, err
	}
	account, err := GetCustomerAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	bots, err := ListAccountBots(ctx, accountID)
	if err != nil {
		return nil, err
	}
	licenses, err := ListCustomerCosmeticLicenses(ctx, accountID)
	if err != nil {
		return nil, err
	}
	subscription, err := GetCustomerCosmeticSubscription(ctx, accountID)
	if err != nil {
		return nil, err
	}
	membership, err := GetActiveCosmeticAdminMembership(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return &CustomerCosmeticsInventory{
		Account: *account, Bots: bots, Licenses: licenses, Subscription: subscription, Membership: membership,
	}, nil
}

// GrantCosmeticLicense is the email-owned payment/manual fulfillment seam.
// An external reference is idempotent; without one, each call intentionally
// creates another independently assignable copy.
// GrantCosmeticLicense fulfils against an email address, for the payment path
// that only knows one.
//
// Retained, and deliberately thin. It resolves the address to an account and
// hands over; every line of the actual fulfilment now lives in
// GrantCosmeticLicenseToAccount, because an account that has been linked to an
// Accounts identity no longer *has* an address to be found by.
//
// This entry point is for the legacy Stripe seam, where a person pays before
// signing in and the receipt is the only identifier that exists. Once
// purchasing moves to the Accounts app the buyer is known by id at the moment
// of purchase, and nothing needs to call this.
func GrantCosmeticLicense(ctx context.Context, rawEmail, cosmeticID, source, externalReference string) (*CosmeticLicense, bool, error) {
	if Pool == nil {
		return nil, false, ErrNoDatabase
	}
	account, err := GetOrCreateCustomerAccountByEmail(ctx, rawEmail)
	if err != nil {
		return nil, false, err
	}
	return GrantCosmeticLicenseToAccount(ctx, account.ID, cosmeticID, source, externalReference)
}

// GrantCosmeticLicenseToAccount is the fulfilment seam, keyed by the thing
// that actually identifies a customer now.
//
// Splitting this out is what makes "stop storing email addresses" survive
// contact with the shop. Fulfilment never needed an address — it needed an
// account, and the address was only ever how one was found. An account id is
// how one is found now, from a signed-in session or from an Accounts purchase
// that already knows who bought.
func GrantCosmeticLicenseToAccount(ctx context.Context, accountID, cosmeticID, source, externalReference string) (*CosmeticLicense, bool, error) {
	if Pool == nil {
		return nil, false, ErrNoDatabase
	}
	account, err := GetCustomerAccount(ctx, strings.TrimSpace(accountID))
	if err != nil {
		return nil, false, err
	}
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		source = "manual"
	}
	externalReference = strings.TrimSpace(externalReference)
	if source != "manual" && externalReference == "" {
		return nil, false, ErrCosmeticLicenseReferenceRequired
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("GrantCosmeticLicense begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := lockCustomerAccount(ctx, tx, account.ID, false); err != nil {
		return nil, false, err
	}
	if externalReference != "" {
		var existingID, existingCosmetic string
		var existingAccount *string
		err := tx.QueryRow(ctx, `
			SELECT id, account_id, cosmetic_id FROM cosmetic_licenses
			WHERE source = $1 AND external_reference = $2
			FOR UPDATE`, source, externalReference).Scan(&existingID, &existingAccount, &existingCosmetic)
		if err == nil {
			if existingCosmetic != cosmeticID {
				return nil, false, ErrCosmeticLicenseGrantConflict
			}
			if existingAccount == nil {
				// PR #69 licenses may outlive a deleted bot/key with only their
				// payment/manual reference as recovery evidence. Email fulfillment
				// claims that exact copy without reactivating its status or creating
				// a duplicate. Any legacy loadout is removed because the copy is now
				// account-owned and intentionally unassigned.
				if _, err := claimCosmeticLicenseTx(
					ctx, tx, existingID, account.ID, nil, "arena_fulfillment_claim",
				); err != nil {
					return nil, false, fmt.Errorf("GrantCosmeticLicense claim legacy: %w", err)
				}
				if err := tx.Commit(ctx); err != nil {
					return nil, false, fmt.Errorf("GrantCosmeticLicense claim legacy commit: %w", err)
				}
				license, loadErr := getCosmeticLicense(ctx, existingID)
				return license, true, loadErr
			}
			if *existingAccount == account.ID {
				if err := tx.Commit(ctx); err != nil {
					return nil, false, fmt.Errorf("GrantCosmeticLicense idempotent commit: %w", err)
				}
				license, loadErr := getCosmeticLicense(ctx, existingID)
				return license, false, loadErr
			}
			return nil, false, ErrCosmeticLicenseGrantConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, fmt.Errorf("GrantCosmeticLicense idempotency: %w", err)
		}
	}
	var itemActive, categoryActive bool
	if err := tx.QueryRow(ctx, `
		SELECT i.is_active, c.is_active
		FROM cosmetic_items i
		JOIN cosmetic_categories c ON c.id = i.category_id
		WHERE i.id = $1
		FOR SHARE OF i, c`, cosmeticID).Scan(&itemActive, &categoryActive); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrCosmeticNotFound
		}
		return nil, false, fmt.Errorf("GrantCosmeticLicense item: %w", err)
	}
	if !itemActive || !categoryActive {
		return nil, false, ErrCosmeticInactive
	}

	licenseID := uuid.NewString()
	_, err = createCosmeticLicenseTx(ctx, tx, cosmeticLicenseCreate{
		LicenseID: licenseID, AccountID: &account.ID, CosmeticID: cosmeticID,
		Source: source, Reason: "arena_license_grant", ExternalReference: externalReference,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, false, ErrCosmeticLicenseGrantConflict
		}
		return nil, false, fmt.Errorf("GrantCosmeticLicense insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("GrantCosmeticLicense commit: %w", err)
	}
	license, err := getCosmeticLicense(ctx, licenseID)
	return license, true, err
}

func AssignCosmeticLicense(ctx context.Context, accountID, licenseID string, botID *string) (*CosmeticAssignmentChange, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	var normalizedBotID *string
	if botID != nil && strings.TrimSpace(*botID) != "" {
		value := strings.TrimSpace(*botID)
		normalizedBotID = &value
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("AssignCosmeticLicense begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := lockCustomerAccount(ctx, tx, accountID, true); err != nil {
		return nil, err
	}
	var owner *string
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT account_id, status FROM cosmetic_licenses WHERE id = $1 FOR UPDATE`, licenseID).
		Scan(&owner, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCosmeticLicenseNotFound
		}
		return nil, fmt.Errorf("AssignCosmeticLicense lock: %w", err)
	}
	if owner == nil || *owner != accountID {
		return nil, ErrCosmeticLicenseNotOwned
	}
	if blocked, err := cosmeticLicenseBlockedByAdminMembership(ctx, tx, licenseID); err != nil {
		return nil, fmt.Errorf("AssignCosmeticLicense membership: %w", err)
	} else if blocked {
		return nil, ErrCosmeticInactive
	}
	var previous *string
	err = tx.QueryRow(ctx, `
		SELECT bot_id FROM cosmetic_license_assignments WHERE license_id = $1 FOR UPDATE`, licenseID).Scan(&previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("AssignCosmeticLicense current assignment: %w", err)
	}
	if normalizedBotID != nil {
		if status != "active" {
			return nil, ErrCosmeticInactive
		}
		var keyIsActive bool
		if err := tx.QueryRow(ctx, `
			SELECT k.is_active
			FROM account_bot_links l
			JOIN bots b ON b.id = l.bot_id
			JOIN api_keys k ON k.id = b.api_key_id
			WHERE l.account_id = $1 AND l.bot_id = $2
			FOR SHARE OF l, k`, accountID, *normalizedBotID).Scan(&keyIsActive); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrCustomerBotNotLinked
			}
			return nil, fmt.Errorf("AssignCosmeticLicense bot link: %w", err)
		}
		if !keyIsActive {
			return nil, ErrCustomerBotKeyInactive
		}
	}
	platformLicense, _, err := lockPlatformCosmeticLicenseTx(ctx, tx, licenseID)
	if err != nil {
		return nil, err
	}
	transition := "assigned"
	reason := "arena_compatibility_assignment"
	if normalizedBotID == nil {
		transition = "unassigned"
		reason = "arena_compatibility_unassignment"
	}
	if _, err := applyCosmeticLicenseAssignmentTx(ctx, tx, platformLicense, normalizedBotID, transition, "arena", reason); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("AssignCosmeticLicense commit: %w", err)
	}
	license, err := getCosmeticLicense(ctx, licenseID)
	if err != nil {
		return nil, err
	}
	return &CosmeticAssignmentChange{License: *license, PreviousBotID: previous, CurrentBotID: normalizedBotID}, nil
}

// EquipCustomerCosmeticLicense equips one exact assigned license. Assignment
// and equip are deliberately separate: moving a license never overwrites the
// destination bot's current slot.
func EquipCustomerCosmeticLicense(ctx context.Context, accountID, botID, licenseID string) (*CosmeticLicense, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("EquipCustomerCosmeticLicense begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := lockCustomerAccount(ctx, tx, accountID, true); err != nil {
		return nil, err
	}
	var owner *string
	var cosmeticID, slot, status string
	var itemActive, categoryActive bool
	if err := tx.QueryRow(ctx, `
		SELECT cl.account_id, cl.cosmetic_id, i.slot, cl.status, i.is_active, c.is_active
		FROM cosmetic_licenses cl
		JOIN cosmetic_items i ON i.id = cl.cosmetic_id
		JOIN cosmetic_categories c ON c.id = i.category_id
		WHERE cl.id = $1
		FOR UPDATE OF cl
		FOR SHARE OF i, c`, licenseID).Scan(&owner, &cosmeticID, &slot, &status, &itemActive, &categoryActive); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCosmeticLicenseNotFound
		}
		return nil, fmt.Errorf("EquipCustomerCosmeticLicense license: %w", err)
	}
	if owner == nil || *owner != accountID {
		return nil, ErrCosmeticLicenseNotOwned
	}
	if blocked, err := cosmeticLicenseBlockedByAdminMembership(ctx, tx, licenseID); err != nil {
		return nil, fmt.Errorf("EquipCustomerCosmeticLicense membership: %w", err)
	} else if blocked {
		return nil, ErrCosmeticInactive
	}
	if status != "active" {
		return nil, ErrCosmeticInactive
	}
	if !itemActive || !categoryActive {
		return nil, ErrCosmeticInactive
	}
	var keyIsActive bool
	if err := tx.QueryRow(ctx, `
		SELECT k.is_active
		FROM cosmetic_license_assignments a
		JOIN bots b ON b.id = a.bot_id
		JOIN api_keys k ON k.id = b.api_key_id
		WHERE a.license_id = $1 AND a.account_id = $2 AND a.bot_id = $3
		FOR SHARE OF a, k`, licenseID, accountID, botID).Scan(&keyIsActive); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCustomerBotNotLinked
		}
		return nil, fmt.Errorf("EquipCustomerCosmeticLicense assignment: %w", err)
	}
	if !keyIsActive {
		return nil, ErrCustomerBotKeyInactive
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_cosmetic_loadout
			(bot_id, slot, cosmetic_id, license_id, account_id, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (bot_id, slot) DO UPDATE
		SET cosmetic_id = EXCLUDED.cosmetic_id, license_id = EXCLUDED.license_id,
		    account_id = EXCLUDED.account_id, updated_at = NOW()`,
		botID, slot, cosmeticID, licenseID, accountID); err != nil {
		return nil, fmt.Errorf("EquipCustomerCosmeticLicense upsert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("EquipCustomerCosmeticLicense commit: %w", err)
	}
	return getCosmeticLicense(ctx, licenseID)
}

func RevokeCosmeticLicense(ctx context.Context, licenseID string) (*CosmeticAssignmentChange, bool, error) {
	if Pool == nil {
		return nil, false, ErrNoDatabase
	}
	var membershipID string
	err := Pool.QueryRow(ctx, `
		SELECT membership_id FROM cosmetic_admin_membership_licenses WHERE license_id = $1`, licenseID).
		Scan(&membershipID)
	if err == nil {
		return nil, false, ErrCosmeticAdminMembershipLicense
	}
	var pgErr *pgconn.PgError
	membershipSchemaMissing := errors.As(err, &pgErr) && pgErr.Code == "42P01"
	if !errors.Is(err, pgx.ErrNoRows) && !membershipSchemaMissing {
		return nil, false, fmt.Errorf("RevokeCosmeticLicense membership: %w", err)
	}
	// Admin revocation does not receive an account ID. Read the current owner,
	// then lock that account before locking the license. If a legacy claim races
	// this read, retry so an account-owned mutation never skips the account lock.
	for attempt := 0; attempt < 3; attempt++ {
		var observedOwner *string
		if err := Pool.QueryRow(ctx, `SELECT account_id FROM cosmetic_licenses WHERE id = $1`, licenseID).
			Scan(&observedOwner); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("RevokeCosmeticLicense owner: %w", err)
		}

		tx, err := Pool.Begin(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("RevokeCosmeticLicense begin: %w", err)
		}
		if observedOwner != nil {
			if _, err := lockCustomerAccount(ctx, tx, *observedOwner, false); err != nil {
				tx.Rollback(ctx)
				return nil, false, err
			}
		}

		var actualOwner, legacyAssigned *string
		var status string
		if err := tx.QueryRow(ctx, `
			SELECT account_id, assigned_bot_id, status
			FROM cosmetic_licenses WHERE id = $1 FOR UPDATE`, licenseID).
			Scan(&actualOwner, &legacyAssigned, &status); err != nil {
			tx.Rollback(ctx)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("RevokeCosmeticLicense lock: %w", err)
		}
		ownerChanged := (observedOwner == nil) != (actualOwner == nil) ||
			(observedOwner != nil && actualOwner != nil && *observedOwner != *actualOwner)
		if ownerChanged {
			tx.Rollback(ctx)
			continue
		}

		var previous *string
		err = tx.QueryRow(ctx, `
			SELECT bot_id FROM cosmetic_license_assignments
			WHERE license_id = $1 FOR UPDATE`, licenseID).Scan(&previous)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			tx.Rollback(ctx)
			return nil, false, fmt.Errorf("RevokeCosmeticLicense assignment: %w", err)
		}
		if previous == nil {
			previous = legacyAssigned
		}
		platformLicense, _, err := lockPlatformCosmeticLicenseTx(ctx, tx, licenseID)
		if err != nil {
			tx.Rollback(ctx)
			return nil, false, err
		}
		transitioned, err := applyCosmeticLicenseTerminalTransitionTx(
			ctx, tx, platformLicense, "revoked", "arena_admin", "arena_admin_revoke", "",
		)
		if err != nil {
			tx.Rollback(ctx)
			return nil, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("RevokeCosmeticLicense commit: %w", err)
		}
		license, err := getCosmeticLicense(ctx, licenseID)
		if err != nil {
			return nil, false, err
		}
		return &CosmeticAssignmentChange{License: *license, PreviousBotID: previous},
			status == "active" && transitioned, nil
	}
	return nil, false, fmt.Errorf("RevokeCosmeticLicense: account ownership changed repeatedly")
}
