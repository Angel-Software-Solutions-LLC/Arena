package db

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

/*
 * What somebody owns, once Accounts is the one that knows.
 *
 * Arena keeps no purchase records of its own any more. What it keeps is a
 * materialisation of somebody else's answer: for each grant the Accounts
 * entitlements endpoint reports, the per-item licences Arena's own equip and
 * assign machinery needs a row for. Every one of those rows is traceable back
 * to the grant that justifies it, and none of them outlives it.
 *
 * This is the same shape the subscription path already uses — a mapping table
 * beside the licences, so a re-read grants nothing twice and a withdrawal can
 * find exactly what it granted. It is not a new pattern; it is the third
 * instance of an existing one (cosmetic_order_licenses,
 * cosmetic_subscription_licenses, and now these).
 *
 * The mapping table is also what makes a purchase a snapshot rather than a
 * subscription. A pack that gains an item next year does not retroactively
 * appear in a wallet that bought it last year: a grant already materialised is
 * left alone, exactly as a paid Stripe order is.
 */

// AccountsGrantSource is the licence source for anything Accounts granted.
// Distinct from "stripe" and "stripe_subscription" because where a licence
// came from decides who may withdraw it, and nothing in Arena may withdraw one
// of these on its own initiative.
const AccountsGrantSource = "accounts"

// ErrAccountsGrantConflict is returned when a grant id turns up naming a
// different account or a different pack than the one it was materialised for.
//
// That is not a race to resolve — it is two services disagreeing about what a
// stable identifier means, and the honest response is to grant nothing and
// say so rather than to pick a winner.
var ErrAccountsGrantConflict = errors.New("accounts grant conflicts with an existing materialisation")

// AccountsPurchaseGrant is one row of the contract's `purchases[]`, reduced to
// the three things Arena acts on: which grant, which pack, and whether it
// still confers ownership.
type AccountsPurchaseGrant struct {
	GrantID string
	SKU     string
	Revoked bool
}

// AccountsPurchaseSyncResult is what one reconciliation did.
//
// UnknownSKUs is reported rather than treated as an error. A pack Arena does
// not have is a catalogue that has moved on — a pack retired here, or one
// added on the Accounts side first — and refusing the whole sync over one
// would strand every other purchase the same person made.
type AccountsPurchaseSyncResult struct {
	GrantsMaterialized int
	GrantsRevoked      int
	LicensesGranted    int
	LicensesRevoked    int
	UnknownSKUs        []string
}

// EnsureCustomerEntitlementsSchema creates the grant ledger.
//
// Two tables for the same reason the order and subscription paths have two:
// one row per grant records that it was seen and settled, and the licence
// mapping records precisely what that settlement produced. A single table
// keyed by licence could not tell "this grant covered no items" from "this
// grant was never read".
func EnsureCustomerEntitlementsSchema(ctx context.Context) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("EnsureCustomerEntitlementsSchema begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(2026082301::BIGINT)`); err != nil {
		return fmt.Errorf("EnsureCustomerEntitlementsSchema migration lock: %w", err)
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS customer_accounts_grants (
			grant_id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL REFERENCES customer_accounts(id) ON DELETE CASCADE,
			sku TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked')),
			materialized_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			revoked_at TIMESTAMPTZ,
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_customer_accounts_grants_account
			ON customer_accounts_grants (account_id, status)`,
		`CREATE TABLE IF NOT EXISTS customer_accounts_grant_licenses (
			grant_id TEXT NOT NULL REFERENCES customer_accounts_grants(grant_id) ON DELETE RESTRICT,
			item_id TEXT NOT NULL REFERENCES cosmetic_items(id) ON DELETE RESTRICT,
			license_id TEXT NOT NULL UNIQUE REFERENCES cosmetic_licenses(id) ON DELETE RESTRICT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (grant_id, item_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_customer_accounts_grant_licenses_item
			ON customer_accounts_grant_licenses (item_id, grant_id)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("EnsureCustomerEntitlementsSchema exec: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("EnsureCustomerEntitlementsSchema commit: %w", err)
	}
	return nil
}

// SyncAccountsPurchases brings one account's licences into line with what
// Accounts says that person owns.
//
// Deliberately *not* a full reconciliation. It grants what is newly present
// and withdraws what is explicitly marked revoked, and it does nothing at all
// about a grant that has simply stopped being mentioned. A snapshot that
// arrived truncated, or scoped to the wrong client, or from a half-deployed
// version of the endpoint, would otherwise strip a paying customer's wallet
// on a single bad read. Absence is not a statement; `status` is, and only
// `status` is acted on.
func SyncAccountsPurchases(ctx context.Context, accountID string, grants []AccountsPurchaseGrant) (*AccountsPurchaseSyncResult, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, ErrCustomerAccountNotFound
	}
	result := &AccountsPurchaseSyncResult{UnknownSKUs: []string{}}
	if len(grants) == 0 {
		return result, nil
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("SyncAccountsPurchases begin: %w", err)
	}
	defer tx.Rollback(ctx)
	account, err := lockCustomerAccount(ctx, tx, accountID, false)
	if err != nil {
		return nil, err
	}

	unknown := map[string]struct{}{}
	for _, grant := range grants {
		grantID := strings.TrimSpace(grant.GrantID)
		sku := strings.TrimSpace(grant.SKU)
		if grantID == "" || sku == "" {
			continue
		}
		existing, err := lockAccountsGrantTx(ctx, tx, grantID)
		if err != nil {
			return nil, err
		}
		if existing != nil && (existing.accountID != account.ID || existing.sku != sku) {
			return nil, ErrAccountsGrantConflict
		}
		if grant.Revoked {
			revokedLicenses, revokedGrant, err := revokeAccountsGrantTx(ctx, tx, grantID, existing)
			if err != nil {
				return nil, err
			}
			result.LicensesRevoked += revokedLicenses
			if revokedGrant {
				result.GrantsRevoked++
			}
			continue
		}
		if existing != nil {
			// Already settled. Touch it so an operator can see the grant is
			// still being reported, and leave the licences exactly as they
			// were — a purchase is a snapshot of a pack, not a subscription
			// to it.
			if _, err := tx.Exec(ctx,
				`UPDATE customer_accounts_grants SET last_seen_at = NOW() WHERE grant_id = $1`, grantID); err != nil {
				return nil, fmt.Errorf("SyncAccountsPurchases touch: %w", err)
			}
			continue
		}
		items, err := packItemsForGrantTx(ctx, tx, sku)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			unknown[sku] = struct{}{}
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO customer_accounts_grants (grant_id, account_id, sku, status, materialized_at, last_seen_at)
			VALUES ($1, $2, $3, 'active', NOW(), NOW())`, grantID, account.ID, sku); err != nil {
			return nil, fmt.Errorf("SyncAccountsPurchases grant: %w", err)
		}
		granted, err := materializeAccountsGrantLicensesTx(ctx, tx, account.ID, grantID, items)
		if err != nil {
			return nil, err
		}
		result.GrantsMaterialized++
		result.LicensesGranted += granted
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("SyncAccountsPurchases commit: %w", err)
	}
	for sku := range unknown {
		result.UnknownSKUs = append(result.UnknownSKUs, sku)
	}
	sort.Strings(result.UnknownSKUs)
	return result, nil
}

type accountsGrantRow struct {
	accountID string
	sku       string
	status    string
}

func lockAccountsGrantTx(ctx context.Context, tx pgx.Tx, grantID string) (*accountsGrantRow, error) {
	var row accountsGrantRow
	err := tx.QueryRow(ctx,
		`SELECT account_id, sku, status FROM customer_accounts_grants WHERE grant_id = $1 FOR UPDATE`, grantID).
		Scan(&row.accountID, &row.sku, &row.status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("SyncAccountsPurchases lock grant: %w", err)
	}
	return &row, nil
}

// packItemsForGrantTx resolves a SKU to the items a purchase of it confers.
//
// The SKU is Arena's own pack id, carried across the catalogue sync unchanged,
// which is what makes this a lookup rather than a mapping table that would
// have to be kept in step with 142 rows on the other side.
//
// Inactive items are included deliberately. What was bought was bought; an
// item retired from sale afterwards is still owned, and the inventory join
// already reports it as inactive so nothing equips it by accident.
func packItemsForGrantTx(ctx context.Context, tx pgx.Tx, sku string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT pi.item_id
		FROM cosmetic_pack_items pi
		JOIN cosmetic_packs p ON p.id = pi.pack_id
		WHERE p.id = $1
		ORDER BY pi.sort_order, pi.item_id`, sku)
	if err != nil {
		return nil, fmt.Errorf("SyncAccountsPurchases pack items: %w", err)
	}
	defer rows.Close()
	items := make([]string, 0, 8)
	for rows.Next() {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			return nil, fmt.Errorf("SyncAccountsPurchases pack item scan: %w", err)
		}
		items = append(items, itemID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("SyncAccountsPurchases pack item rows: %w", err)
	}
	return items, nil
}

func materializeAccountsGrantLicensesTx(ctx context.Context, tx pgx.Tx, accountID, grantID string, items []string) (int, error) {
	inputs := make([]cosmeticLicenseCreate, 0, len(items))
	licenseIDs := make([]string, 0, len(items))
	itemIDs := make([]string, 0, len(items))
	for _, itemID := range items {
		licenseID := uuid.NewString()
		inputs = append(inputs, cosmeticLicenseCreate{
			LicenseID:  licenseID,
			AccountID:  &accountID,
			CosmeticID: itemID,
			Source:     AccountsGrantSource,
			Reason:     "accounts_grant",
			// The grant id, on every licence it produced. This is the
			// reference the ownership boundary is drawn on: Accounts owns the
			// grant, Arena owns which bot wears it, and this is the only
			// string that joins the two.
			ExternalReference: fmt.Sprintf("accounts-grant:%s:item:%s", grantID, itemID),
		})
		licenseIDs = append(licenseIDs, licenseID)
		itemIDs = append(itemIDs, itemID)
	}
	inserted, err := createCosmeticLicensesTx(ctx, tx, inputs)
	if err != nil {
		return 0, fmt.Errorf("SyncAccountsPurchases licenses: %w", err)
	}
	mappedItemIDs := make([]string, 0, len(itemIDs))
	mappedLicenseIDs := make([]string, 0, len(itemIDs))
	for index, licenseID := range licenseIDs {
		if inserted[licenseID] {
			mappedItemIDs = append(mappedItemIDs, itemIDs[index])
			mappedLicenseIDs = append(mappedLicenseIDs, licenseID)
		}
	}
	if len(mappedLicenseIDs) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO customer_accounts_grant_licenses (grant_id, item_id, license_id, created_at)
		SELECT $1, item_id, license_id, NOW()
		FROM UNNEST($2::TEXT[], $3::TEXT[]) AS mappings(item_id, license_id)
		ON CONFLICT (grant_id, item_id) DO NOTHING`,
		grantID, mappedItemIDs, mappedLicenseIDs); err != nil {
		return 0, fmt.Errorf("SyncAccountsPurchases mappings: %w", err)
	}
	return len(mappedLicenseIDs), nil
}

// revokeAccountsGrantTx withdraws everything one grant produced.
//
// A grant that was never materialised here is recorded as revoked all the
// same, so a later snapshot that still lists it — or lists it as active again
// — cannot quietly re-grant what has been taken away.
func revokeAccountsGrantTx(ctx context.Context, tx pgx.Tx, grantID string, existing *accountsGrantRow) (int, bool, error) {
	if existing == nil {
		return 0, false, nil
	}
	if existing.status == "revoked" {
		return 0, false, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT l.id
		FROM customer_accounts_grant_licenses gl
		JOIN cosmetic_licenses l ON l.id = gl.license_id
		WHERE gl.grant_id = $1
		ORDER BY l.id
		FOR UPDATE OF l`, grantID)
	if err != nil {
		return 0, false, fmt.Errorf("SyncAccountsPurchases revoke lock: %w", err)
	}
	licenseIDs := make([]string, 0)
	for rows.Next() {
		var licenseID string
		if err := rows.Scan(&licenseID); err != nil {
			rows.Close()
			return 0, false, fmt.Errorf("SyncAccountsPurchases revoke scan: %w", err)
		}
		licenseIDs = append(licenseIDs, licenseID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, false, fmt.Errorf("SyncAccountsPurchases revoke rows: %w", err)
	}
	rows.Close()
	changed, err := applyCosmeticLicenseTerminalBatchTx(
		ctx, tx, licenseIDs, "revoked", AccountsGrantSource, "accounts_grant_revoked", grantID,
	)
	if err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE customer_accounts_grants
		SET status = 'revoked', revoked_at = NOW(), last_seen_at = NOW()
		WHERE grant_id = $1`, grantID); err != nil {
		return 0, false, fmt.Errorf("SyncAccountsPurchases revoke grant: %w", err)
	}
	return changed, true, nil
}

// ListAccountsGrantIDs returns the grant ids currently held by an account,
// active first. The dashboard reports these so a person can see the reference
// that ties a licence here to a purchase over there — the one string support
// needs when the two disagree.
func ListAccountsGrantIDs(ctx context.Context, accountID string) ([]string, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx, `
		SELECT grant_id FROM customer_accounts_grants
		WHERE account_id = $1 AND status = 'active'
		ORDER BY materialized_at, grant_id`, strings.TrimSpace(accountID))
	if err != nil {
		return nil, fmt.Errorf("ListAccountsGrantIDs: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ListAccountsGrantIDs scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
