package db

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrCosmeticAdminMembershipInvalid = errors.New("invalid cosmetic membership")
	ErrCosmeticAdminMembershipActive  = errors.New("an active cosmetic membership already exists for this account")
	ErrCosmeticAdminMembershipLicense = errors.New("membership-issued cosmetics must be revoked with the membership")
)

const maxCosmeticAdminMembershipDuration = 5 * 365 * 24 * time.Hour

type CosmeticAdminMembership struct {
	ID           string     `json:"id"`
	AccountID    string     `json:"account_id"`
	Email        string     `json:"email,omitempty"`
	Status       string     `json:"status"`
	Note         string     `json:"note"`
	GrantedBy    string     `json:"granted_by"`
	GrantedAt    time.Time  `json:"granted_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedBy    *string    `json:"revoked_by,omitempty"`
	RevokeReason *string    `json:"revoke_reason,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LicenseCount int        `json:"license_count"`
}

type CosmeticAdminAccess struct {
	Email       string                    `json:"email"`
	Account     CustomerAccount           `json:"account"`
	Licenses    []CosmeticLicense         `json:"licenses"`
	Memberships []CosmeticAdminMembership `json:"memberships"`
}

func EnsureCosmeticAdminMembershipsSchema(ctx context.Context) error {
	if Pool == nil {
		return nil
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("EnsureCosmeticAdminMembershipsSchema begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('arena_cosmetic_admin_memberships_schema'))`); err != nil {
		return fmt.Errorf("EnsureCosmeticAdminMembershipsSchema lock: %w", err)
	}
	statements := []string{
		`DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid = 'cosmetic_licenses'::regclass
				  AND conname = 'cosmetic_licenses_status_check'
				  AND POSITION('expired' IN pg_get_constraintdef(oid)) > 0
			) THEN
				IF EXISTS (
					SELECT 1 FROM pg_constraint
					WHERE conrelid = 'cosmetic_licenses'::regclass
					  AND conname = 'cosmetic_licenses_status_check'
				) THEN
					ALTER TABLE cosmetic_licenses DROP CONSTRAINT cosmetic_licenses_status_check;
				END IF;
				ALTER TABLE cosmetic_licenses ADD CONSTRAINT cosmetic_licenses_status_check
					CHECK (status IN ('active', 'refunded', 'revoked', 'chargeback', 'expired')) NOT VALID;
			END IF;
		END
		$$`,
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid = 'cosmetic_licenses'::regclass
				  AND conname = 'cosmetic_licenses_status_check'
				  AND convalidated = false
			) THEN
				ALTER TABLE cosmetic_licenses VALIDATE CONSTRAINT cosmetic_licenses_status_check;
			END IF;
		END
		$$`,
		`CREATE TABLE IF NOT EXISTS cosmetic_admin_memberships (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL REFERENCES customer_accounts(id) ON DELETE RESTRICT,
			status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked', 'expired')),
			note TEXT NOT NULL DEFAULT '',
			granted_by TEXT NOT NULL,
			granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at TIMESTAMPTZ NOT NULL,
			revoked_by TEXT,
			revoke_reason TEXT,
			revoked_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CHECK (expires_at > granted_at)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cosmetic_admin_memberships_account
			ON cosmetic_admin_memberships (account_id, granted_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_cosmetic_admin_memberships_expiry
			ON cosmetic_admin_memberships (expires_at, id) WHERE status = 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cosmetic_admin_memberships_one_active
			ON cosmetic_admin_memberships (account_id) WHERE status = 'active'`,
		`CREATE TABLE IF NOT EXISTS cosmetic_admin_membership_licenses (
			membership_id TEXT NOT NULL REFERENCES cosmetic_admin_memberships(id) ON DELETE RESTRICT,
			item_id TEXT NOT NULL REFERENCES cosmetic_items(id) ON DELETE RESTRICT,
			license_id TEXT NOT NULL UNIQUE REFERENCES cosmetic_licenses(id) ON DELETE RESTRICT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (membership_id, item_id)
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("EnsureCosmeticAdminMembershipsSchema exec: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("EnsureCosmeticAdminMembershipsSchema commit: %w", err)
	}
	return nil
}

func scanCosmeticAdminMembership(row rowScanner) (*CosmeticAdminMembership, error) {
	var membership CosmeticAdminMembership
	// The joined address is null for every linked account, which is the
	// normal case now rather than an anomaly. It reads as empty, the same way
	// it does on the account itself.
	var email *string
	if err := row.Scan(
		&membership.ID, &membership.AccountID, &email, &membership.Status,
		&membership.Note, &membership.GrantedBy, &membership.GrantedAt, &membership.ExpiresAt,
		&membership.RevokedBy, &membership.RevokeReason, &membership.RevokedAt,
		&membership.UpdatedAt, &membership.LicenseCount,
	); err != nil {
		return nil, err
	}
	if email != nil {
		membership.Email = *email
	}
	return &membership, nil
}

func cosmeticAdminMembershipSelect() string {
	return `SELECT m.id, m.account_id, a.email,
		CASE WHEN m.status = 'active' AND m.expires_at <= NOW() THEN 'expired' ELSE m.status END,
		m.note, m.granted_by,
		m.granted_at, m.expires_at, m.revoked_by, m.revoke_reason, m.revoked_at,
		m.updated_at, (SELECT COUNT(*) FROM cosmetic_admin_membership_licenses ml WHERE ml.membership_id = m.id)
		FROM cosmetic_admin_memberships m
		JOIN customer_accounts a ON a.id = m.account_id`
}

func getCosmeticAdminMembership(ctx context.Context, membershipID string) (*CosmeticAdminMembership, error) {
	membership, err := scanCosmeticAdminMembership(Pool.QueryRow(ctx,
		cosmeticAdminMembershipSelect()+` WHERE m.id = $1`, membershipID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getCosmeticAdminMembership: %w", err)
	}
	return membership, nil
}

// CreateCosmeticAdminMembership comps a membership to whoever owns an address.
//
// Thin, for the same reason GrantCosmeticLicense is: an account linked to an
// Accounts identity has no address to be found by. It resolves and delegates.
func CreateCosmeticAdminMembership(ctx context.Context, rawEmail string, expiresAt time.Time, note, actor string) (*CosmeticAdminMembership, int, error) {
	if Pool == nil {
		return nil, 0, ErrNoDatabase
	}
	account, err := GetOrCreateCustomerAccountByEmail(ctx, rawEmail)
	if err != nil {
		return nil, 0, err
	}
	return CreateCosmeticAdminMembershipForAccount(ctx, account.ID, expiresAt, note, actor)
}

// CreateCosmeticAdminMembershipForAccount comps a membership to an account.
//
// The form an administrator will actually reach for once the shop is driven by
// Accounts identities: they are looking at a customer, not at an address.
func CreateCosmeticAdminMembershipForAccount(ctx context.Context, accountID string, expiresAt time.Time, note, actor string) (*CosmeticAdminMembership, int, error) {
	if Pool == nil {
		return nil, 0, ErrNoDatabase
	}
	note = strings.TrimSpace(note)
	actor = strings.TrimSpace(actor)
	expiresAt = expiresAt.UTC()
	now := time.Now().UTC()
	if actor == "" || len(actor) > 200 || len(note) > 500 || !expiresAt.After(now) || expiresAt.After(now.Add(maxCosmeticAdminMembershipDuration)) {
		return nil, 0, ErrCosmeticAdminMembershipInvalid
	}
	account, err := GetCustomerAccount(ctx, strings.TrimSpace(accountID))
	if err != nil {
		return nil, 0, err
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("CreateCosmeticAdminMembership begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := lockCustomerAccount(ctx, tx, account.ID, false); err != nil {
		return nil, 0, err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM cosmetic_admin_memberships WHERE account_id = $1 AND status = 'active'
	)`, account.ID).Scan(&active); err != nil {
		return nil, 0, fmt.Errorf("CreateCosmeticAdminMembership active check: %w", err)
	}
	if active {
		return nil, 0, ErrCosmeticAdminMembershipActive
	}
	membershipID := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		INSERT INTO cosmetic_admin_memberships
			(id, account_id, note, granted_by, granted_at, expires_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), $5, NOW())`,
		membershipID, account.ID, note, actor, expiresAt); err != nil {
		return nil, 0, fmt.Errorf("CreateCosmeticAdminMembership insert: %w", err)
	}
	created, err := syncCosmeticAdminMembershipLicenses(ctx, tx, membershipID, account.ID)
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, fmt.Errorf("CreateCosmeticAdminMembership commit: %w", err)
	}
	membership, err := getCosmeticAdminMembership(ctx, membershipID)
	return membership, created, err
}

func syncCosmeticAdminMembershipLicenses(ctx context.Context, tx pgx.Tx, membershipID, accountID string) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT i.id AS item_id
		FROM cosmetic_pack_items pi
		JOIN cosmetic_packs p ON p.id = pi.pack_id
		JOIN cosmetic_categories pc ON pc.id = p.category_id
		JOIN cosmetic_items i ON i.id = pi.item_id
		JOIN cosmetic_categories ic ON ic.id = i.category_id
		JOIN cosmetic_admin_memberships m ON m.id = $1
		LEFT JOIN cosmetic_admin_membership_licenses ml
		  ON ml.membership_id = $1 AND ml.item_id = i.id
		WHERE m.account_id = $2 AND m.status = 'active' AND m.expires_at > NOW()
		  AND p.is_active = true AND p.is_purchasable = true AND p.is_free = false
		  AND pc.is_active = true AND i.is_active = true AND ic.is_active = true
		  AND ml.item_id IS NULL
		ORDER BY i.id`, membershipID, accountID)
	if err != nil {
		return 0, fmt.Errorf("syncCosmeticAdminMembershipLicenses candidates: %w", err)
	}
	itemIDs := make([]string, 0)
	for rows.Next() {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("syncCosmeticAdminMembershipLicenses scan: %w", err)
		}
		itemIDs = append(itemIDs, itemID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("syncCosmeticAdminMembershipLicenses rows: %w", err)
	}
	rows.Close()
	inputs := make([]cosmeticLicenseCreate, 0, len(itemIDs))
	licenseIDs := make([]string, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		licenseID := uuid.NewString()
		externalReference := "admin-membership:" + membershipID + ":item:" + itemID
		inputs = append(inputs, cosmeticLicenseCreate{
			LicenseID: licenseID, AccountID: &accountID, CosmeticID: itemID,
			Source: "admin_membership", Reason: "admin_membership_granted", ExternalReference: externalReference,
		})
		licenseIDs = append(licenseIDs, licenseID)
	}
	inserted, err := createCosmeticLicensesTx(ctx, tx, inputs)
	if err != nil {
		return 0, fmt.Errorf("syncCosmeticAdminMembershipLicenses licenses: %w", err)
	}
	mappedItemIDs := make([]string, 0, len(itemIDs))
	mappedLicenseIDs := make([]string, 0, len(itemIDs))
	for index, licenseID := range licenseIDs {
		if inserted[licenseID] {
			mappedItemIDs = append(mappedItemIDs, itemIDs[index])
			mappedLicenseIDs = append(mappedLicenseIDs, licenseID)
		}
	}
	if len(mappedLicenseIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO cosmetic_admin_membership_licenses (
				membership_id, item_id, license_id, created_at
			)
			SELECT $1, item_id, license_id, NOW()
			FROM UNNEST($2::TEXT[], $3::TEXT[]) AS mappings(item_id, license_id)`,
			membershipID, mappedItemIDs, mappedLicenseIDs); err != nil {
			return 0, fmt.Errorf("syncCosmeticAdminMembershipLicenses mappings: %w", err)
		}
	}
	return len(mappedLicenseIDs), nil
}

func SyncCustomerCosmeticAdminMembershipLicenses(ctx context.Context, accountID string) (int, error) {
	if Pool == nil {
		return 0, ErrNoDatabase
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("SyncCustomerCosmeticAdminMembershipLicenses begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := lockCustomerAccount(ctx, tx, accountID, false); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id FROM cosmetic_admin_memberships
		WHERE account_id = $1 AND status = 'active' AND expires_at > NOW()
		ORDER BY granted_at, id`, accountID)
	if err != nil {
		return 0, fmt.Errorf("SyncCustomerCosmeticAdminMembershipLicenses memberships: %w", err)
	}
	ids := make([]string, 0, 1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	created := 0
	for _, membershipID := range ids {
		count, err := syncCosmeticAdminMembershipLicenses(ctx, tx, membershipID, accountID)
		if err != nil {
			return 0, err
		}
		created += count
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("SyncCustomerCosmeticAdminMembershipLicenses commit: %w", err)
	}
	return created, nil
}

// GetActiveCosmeticAdminMembership returns the account's active,
// unexpired admin-granted membership, or nil if it has none. The unique
// partial index on (account_id) WHERE status = 'active' guarantees at most
// one row matches.
func GetActiveCosmeticAdminMembership(ctx context.Context, accountID string) (*CosmeticAdminMembership, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	membership, err := scanCosmeticAdminMembership(Pool.QueryRow(ctx,
		cosmeticAdminMembershipSelect()+`
		WHERE m.account_id = $1 AND m.status = 'active' AND m.expires_at > NOW()`, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetActiveCosmeticAdminMembership: %w", err)
	}
	return membership, nil
}

// GetCosmeticAdminAccessByEmail looks a customer up by address.
//
// It finds only accounts that still hold one — that is, accounts nobody has
// signed into since the cutover. For anything else an administrator has the
// account id in front of them; see GetCosmeticAdminAccessByAccount. Returning
// an empty result rather than an error is the behaviour this always had for an
// address nobody recognises, and a linked account is now exactly that.
func GetCosmeticAdminAccessByEmail(ctx context.Context, rawEmail string) (*CosmeticAdminAccess, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	email, err := NormalizeCustomerEmail(rawEmail)
	if err != nil {
		return nil, err
	}
	var accountID string
	lookupErr := Pool.QueryRow(ctx, `SELECT id FROM customer_accounts WHERE email = $1`, email).Scan(&accountID)
	if errors.Is(lookupErr, pgx.ErrNoRows) {
		return &CosmeticAdminAccess{Email: email, Licenses: []CosmeticLicense{}, Memberships: []CosmeticAdminMembership{}}, nil
	}
	if lookupErr != nil {
		return nil, fmt.Errorf("GetCosmeticAdminAccessByEmail account: %w", lookupErr)
	}
	access, err := GetCosmeticAdminAccessByAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	access.Email = email
	return access, nil
}

// GetCosmeticAdminAccessByAccount looks a customer up by the identifier that
// still exists after the cutover.
func GetCosmeticAdminAccessByAccount(ctx context.Context, accountID string) (*CosmeticAdminAccess, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	accountID = strings.TrimSpace(accountID)
	access := &CosmeticAdminAccess{Licenses: []CosmeticLicense{}, Memberships: []CosmeticAdminMembership{}}
	if accountID == "" {
		return access, nil
	}
	account, err := GetCustomerAccount(ctx, accountID)
	if errors.Is(err, ErrCustomerAccountNotFound) {
		return access, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetCosmeticAdminAccessByAccount account: %w", err)
	}
	access.Email = account.Email
	access.Account = *account
	if _, err := SyncCustomerCosmeticAdminMembershipLicenses(ctx, account.ID); err != nil {
		return nil, err
	}
	access.Licenses, err = ListCustomerCosmeticLicenses(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	rows, err := Pool.Query(ctx, cosmeticAdminMembershipSelect()+`
		WHERE m.account_id = $1 ORDER BY m.granted_at DESC, m.id DESC`, account.ID)
	if err != nil {
		return nil, fmt.Errorf("GetCosmeticAdminAccessByAccount memberships: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		membership, err := scanCosmeticAdminMembership(rows)
		if err != nil {
			return nil, fmt.Errorf("GetCosmeticAdminAccessByAccount membership scan: %w", err)
		}
		access.Memberships = append(access.Memberships, *membership)
	}
	return access, rows.Err()
}

func RevokeCosmeticAdminMembership(ctx context.Context, membershipID, actor, reason string) (*CosmeticAdminMembership, []string, bool, error) {
	actor = strings.TrimSpace(actor)
	reason = strings.TrimSpace(reason)
	if strings.TrimSpace(membershipID) == "" || actor == "" || len(actor) > 200 || len(reason) > 500 {
		return nil, nil, false, ErrCosmeticAdminMembershipInvalid
	}
	membership, affected, changed, err := transitionCosmeticAdminMembership(ctx, membershipID, "revoked", time.Time{}, actor, reason)
	return membership, affected, changed, err
}

func ExpireCosmeticAdminMemberships(ctx context.Context, now time.Time, limit int) (int, []string, error) {
	if Pool == nil {
		return 0, nil, ErrNoDatabase
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := Pool.Query(ctx, `
		SELECT id FROM cosmetic_admin_memberships
		WHERE status = 'active' AND expires_at <= $1
		ORDER BY expires_at, id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return 0, nil, fmt.Errorf("ExpireCosmeticAdminMemberships candidates: %w", err)
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, nil, err
	}
	rows.Close()
	affectedSet := make(map[string]struct{})
	changedCount := 0
	for _, membershipID := range ids {
		_, affected, changed, err := transitionCosmeticAdminMembership(ctx, membershipID, "expired", now.UTC(), "system-expiry", "membership reached its expiry")
		if err != nil {
			return changedCount, sortedBotIDs(affectedSet), err
		}
		if changed {
			changedCount++
		}
		for _, botID := range affected {
			affectedSet[botID] = struct{}{}
		}
	}
	return changedCount, sortedBotIDs(affectedSet), nil
}

// ExpireCustomerCosmeticAdminMemberships sweeps one customer's expired
// memberships, found by address.
//
// Resolves and delegates, like its siblings. An address that belongs to nobody
// — including one whose account has since been linked and emptied — sweeps
// nothing rather than failing, which is the behaviour it always had for an
// unknown address.
func ExpireCustomerCosmeticAdminMemberships(ctx context.Context, rawEmail string, now time.Time) (int, []string, error) {
	if Pool == nil {
		return 0, nil, ErrNoDatabase
	}
	email, err := NormalizeCustomerEmail(rawEmail)
	if err != nil {
		return 0, nil, err
	}
	var accountID string
	if err := Pool.QueryRow(ctx, `SELECT id FROM customer_accounts WHERE email = $1`, email).Scan(&accountID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, []string{}, nil
		}
		return 0, nil, fmt.Errorf("ExpireCustomerCosmeticAdminMemberships account: %w", err)
	}
	return ExpireAccountCosmeticAdminMemberships(ctx, accountID, now)
}

// ExpireAccountCosmeticAdminMemberships sweeps one account's expired
// memberships.
func ExpireAccountCosmeticAdminMemberships(ctx context.Context, accountID string, now time.Time) (int, []string, error) {
	if Pool == nil {
		return 0, nil, ErrNoDatabase
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return 0, []string{}, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := Pool.Query(ctx, `
		SELECT id FROM cosmetic_admin_memberships
		WHERE account_id = $1 AND status = 'active' AND expires_at <= $2
		ORDER BY expires_at, id`, accountID, now.UTC())
	if err != nil {
		return 0, nil, fmt.Errorf("ExpireCustomerCosmeticAdminMemberships candidates: %w", err)
	}
	ids := make([]string, 0, 1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, nil, err
	}
	rows.Close()
	affectedSet := make(map[string]struct{})
	changedCount := 0
	for _, membershipID := range ids {
		_, affected, changed, err := transitionCosmeticAdminMembership(ctx, membershipID, "expired", now.UTC(), "system-expiry", "membership reached its expiry")
		if err != nil {
			return changedCount, sortedBotIDs(affectedSet), err
		}
		if changed {
			changedCount++
		}
		for _, botID := range affected {
			affectedSet[botID] = struct{}{}
		}
	}
	return changedCount, sortedBotIDs(affectedSet), nil
}

func sortedBotIDs(botIDs map[string]struct{}) []string {
	affected := make([]string, 0, len(botIDs))
	for botID := range botIDs {
		affected = append(affected, botID)
	}
	sort.Strings(affected)
	return affected
}

func transitionCosmeticAdminMembership(ctx context.Context, membershipID, targetStatus string, expiryCutoff time.Time, actor, reason string) (*CosmeticAdminMembership, []string, bool, error) {
	if Pool == nil {
		return nil, nil, false, ErrNoDatabase
	}
	var accountID string
	if err := Pool.QueryRow(ctx, `SELECT account_id FROM cosmetic_admin_memberships WHERE id = $1`, membershipID).Scan(&accountID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, false, nil
		}
		return nil, nil, false, fmt.Errorf("transitionCosmeticAdminMembership owner: %w", err)
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := lockCustomerAccount(ctx, tx, accountID, false); err != nil {
		return nil, nil, false, err
	}
	var status string
	var expiresAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT status, expires_at FROM cosmetic_admin_memberships
		WHERE id = $1 AND account_id = $2 FOR UPDATE`, membershipID, accountID).Scan(&status, &expiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	if status != "active" || (targetStatus == "expired" && expiresAt.After(expiryCutoff)) {
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, false, err
		}
		membership, err := getCosmeticAdminMembership(ctx, membershipID)
		return membership, nil, false, err
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT a.bot_id
		FROM cosmetic_admin_membership_licenses ml
		JOIN cosmetic_license_assignments a ON a.license_id = ml.license_id
		WHERE ml.membership_id = $1 ORDER BY a.bot_id`, membershipID)
	if err != nil {
		return nil, nil, false, err
	}
	affected := make([]string, 0)
	for rows.Next() {
		var botID string
		if err := rows.Scan(&botID); err != nil {
			rows.Close()
			return nil, nil, false, err
		}
		affected = append(affected, botID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, false, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `
		SELECT licenses.id
		FROM cosmetic_admin_membership_licenses AS membership_licenses
		JOIN cosmetic_licenses AS licenses ON licenses.id = membership_licenses.license_id
		WHERE membership_licenses.membership_id = $1
		ORDER BY licenses.id
		FOR UPDATE OF licenses`, membershipID)
	if err != nil {
		return nil, nil, false, err
	}
	licenseIDs := make([]string, 0)
	for rows.Next() {
		var licenseID string
		if err := rows.Scan(&licenseID); err != nil {
			rows.Close()
			return nil, nil, false, err
		}
		licenseIDs = append(licenseIDs, licenseID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, false, err
	}
	rows.Close()
	lifecycleReason := "admin_membership_" + targetStatus
	if _, err := applyCosmeticLicenseTerminalBatchTx(
		ctx, tx, licenseIDs, targetStatus, "admin_membership", lifecycleReason, membershipID,
	); err != nil {
		return nil, nil, false, err
	}
	if targetStatus == "revoked" {
		_, err = tx.Exec(ctx, `
			UPDATE cosmetic_admin_memberships
			SET status = 'revoked', revoked_by = $2, revoke_reason = NULLIF($3, ''),
				revoked_at = NOW(), updated_at = NOW()
			WHERE id = $1`, membershipID, actor, reason)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE cosmetic_admin_memberships SET status = 'expired', updated_at = NOW() WHERE id = $1`, membershipID)
	}
	if err != nil {
		return nil, nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, false, err
	}
	membership, err := getCosmeticAdminMembership(ctx, membershipID)
	return membership, affected, true, err
}

func cosmeticLicenseBlockedByAdminMembership(ctx context.Context, tx pgx.Tx, licenseID string) (bool, error) {
	var blocked bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1
		FROM cosmetic_admin_membership_licenses ml
		JOIN cosmetic_admin_memberships m ON m.id = ml.membership_id
		WHERE ml.license_id = $1 AND (m.status <> 'active' OR m.expires_at <= NOW())
	)`, licenseID).Scan(&blocked)
	return blocked, err
}
