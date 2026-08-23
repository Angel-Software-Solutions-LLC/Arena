package db

import (
	"context"
	"errors"
	"testing"
)

// accountsGrantTestAccount makes a verified account the way a sign-in does.
func accountsGrantTestAccount(t *testing.T, ctx context.Context, subject string) *CustomerAccount {
	t.Helper()
	account, err := UpsertVerifiedCustomerAccount(ctx, "", "https://accounts.angel-serv.com", subject, "Player "+subject)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount(%s): %v", subject, err)
	}
	return account
}

func accountsGrantPackItems(t *testing.T, ctx context.Context, packID string) []string {
	t.Helper()
	rows, err := Pool.Query(ctx,
		`SELECT item_id FROM cosmetic_pack_items WHERE pack_id = $1 ORDER BY sort_order, item_id`, packID)
	if err != nil {
		t.Fatalf("read pack items for %s: %v", packID, err)
	}
	defer rows.Close()
	items := []string{}
	for rows.Next() {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			t.Fatalf("scan pack item: %v", err)
		}
		items = append(items, itemID)
	}
	if len(items) == 0 {
		t.Fatalf("pack %s seeded with no items; the fixture this test relies on is gone", packID)
	}
	return items
}

func accountsGrantLicenseStatuses(t *testing.T, ctx context.Context, grantID string) map[string]string {
	t.Helper()
	rows, err := Pool.Query(ctx, `
		SELECT gl.item_id, l.status
		FROM customer_accounts_grant_licenses gl
		JOIN cosmetic_licenses l ON l.id = gl.license_id
		WHERE gl.grant_id = $1`, grantID)
	if err != nil {
		t.Fatalf("read grant licenses: %v", err)
	}
	defer rows.Close()
	statuses := map[string]string{}
	for rows.Next() {
		var itemID, status string
		if err := rows.Scan(&itemID, &status); err != nil {
			t.Fatalf("scan grant license: %v", err)
		}
		statuses[itemID] = status
	}
	return statuses
}

// TestPostgresAccountsPurchasesMaterializeOnceAndWithdrawExactly is the whole
// of the ownership contract Arena implements against Support#188, exercised
// through the real call path against a real database.
//
// Accounts owns the licence; Arena owns only which bot wears it. So each
// assertion below is about Arena faithfully reflecting somebody else's answer:
// grant what is granted, grant it exactly once, withdraw precisely what a
// revocation covers and nothing beside it, and never invent or destroy
// ownership on its own initiative.
func TestPostgresAccountsPurchasesMaterializeOnceAndWithdrawExactly(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	account := accountsGrantTestAccount(t, ctx, "owner-subject")
	const packID = "arena-set-003-ember-vanguard-pack"
	const otherPackID = "arena-set-004-glacier-circuit-pack"
	packItems := accountsGrantPackItems(t, ctx, packID)

	/* ---------------------------------------------- a purchase becomes licences */

	result, err := SyncAccountsPurchases(ctx, account.ID, []AccountsPurchaseGrant{
		{GrantID: "pur_ember", SKU: packID},
	})
	if err != nil {
		t.Fatalf("SyncAccountsPurchases first: %v", err)
	}
	if result.GrantsMaterialized != 1 || result.LicensesGranted != len(packItems) {
		t.Fatalf("first sync = %+v, want 1 grant and %d licenses", result, len(packItems))
	}
	licenses, err := ListCustomerCosmeticLicenses(ctx, account.ID)
	if err != nil {
		t.Fatalf("ListCustomerCosmeticLicenses: %v", err)
	}
	if len(licenses) != len(packItems) {
		t.Fatalf("licenses on the account = %d, want %d", len(licenses), len(packItems))
	}
	for _, license := range licenses {
		if license.Source != AccountsGrantSource {
			t.Fatalf("license %s source = %q, want %q", license.ID, license.Source, AccountsGrantSource)
		}
		// The grant id has to be recoverable from the licence, because that
		// reference is the entire join between what Accounts owns and what
		// Arena owns.
		if license.ExternalRef == nil || *license.ExternalRef == "" {
			t.Fatalf("license %s carries no grant reference", license.ID)
		}
	}

	/* ------------------------------------ reading the same answer twice is inert */

	repeat, err := SyncAccountsPurchases(ctx, account.ID, []AccountsPurchaseGrant{
		{GrantID: "pur_ember", SKU: packID},
	})
	if err != nil {
		t.Fatalf("SyncAccountsPurchases repeat: %v", err)
	}
	if repeat.GrantsMaterialized != 0 || repeat.LicensesGranted != 0 {
		t.Fatalf("repeat sync = %+v, want nothing granted twice", repeat)
	}

	/*
	 * A pack that gains an item after the purchase does not retroactively
	 * enlarge it. This is the property that makes a purchase a snapshot rather
	 * than a subscription, and it is the one an idempotency key alone would
	 * not give: without the grant ledger, a re-read would expand the pack
	 * again and hand over the new item for free.
	 */
	addedItem := accountsGrantPackItems(t, ctx, otherPackID)[0]
	if _, err := Pool.Exec(ctx,
		`INSERT INTO cosmetic_pack_items (pack_id, item_id, sort_order) VALUES ($1, $2, 99)`,
		packID, addedItem); err != nil {
		t.Fatalf("widen the pack: %v", err)
	}
	afterWidening, err := SyncAccountsPurchases(ctx, account.ID, []AccountsPurchaseGrant{
		{GrantID: "pur_ember", SKU: packID},
	})
	if err != nil {
		t.Fatalf("SyncAccountsPurchases after widening: %v", err)
	}
	if afterWidening.LicensesGranted != 0 {
		t.Fatalf("a pack that grew handed out %d extra licenses to an old purchase", afterWidening.LicensesGranted)
	}

	/* ------------------------------ a second purchase, so revocation can be aimed */

	second, err := SyncAccountsPurchases(ctx, account.ID, []AccountsPurchaseGrant{
		{GrantID: "pur_glacier", SKU: otherPackID},
	})
	if err != nil {
		t.Fatalf("SyncAccountsPurchases second: %v", err)
	}
	if second.GrantsMaterialized != 1 {
		t.Fatalf("second sync = %+v, want the other pack materialised", second)
	}

	/* --------------------------------------------- silence is not a revocation */

	quiet, err := SyncAccountsPurchases(ctx, account.ID, []AccountsPurchaseGrant{
		{GrantID: "pur_glacier", SKU: otherPackID},
	})
	if err != nil {
		t.Fatalf("SyncAccountsPurchases quiet: %v", err)
	}
	if quiet.LicensesRevoked != 0 {
		t.Fatalf("a snapshot that simply stopped mentioning a grant revoked %d licenses", quiet.LicensesRevoked)
	}
	for item, status := range accountsGrantLicenseStatuses(t, ctx, "pur_ember") {
		if status != "active" {
			t.Fatalf("unmentioned grant lost item %s (status %q)", item, status)
		}
	}

	/* ----------------------------------- an explicit revocation, and only that */

	revoked, err := SyncAccountsPurchases(ctx, account.ID, []AccountsPurchaseGrant{
		{GrantID: "pur_ember", SKU: packID, Revoked: true},
		{GrantID: "pur_glacier", SKU: otherPackID},
	})
	if err != nil {
		t.Fatalf("SyncAccountsPurchases revoke: %v", err)
	}
	if revoked.GrantsRevoked != 1 || revoked.LicensesRevoked != len(packItems) {
		t.Fatalf("revoke sync = %+v, want 1 grant and %d licenses withdrawn", revoked, len(packItems))
	}
	for item, status := range accountsGrantLicenseStatuses(t, ctx, "pur_ember") {
		if status != "revoked" {
			t.Fatalf("revoked grant kept item %s active (status %q)", item, status)
		}
	}
	for item, status := range accountsGrantLicenseStatuses(t, ctx, "pur_glacier") {
		if status != "active" {
			t.Fatalf("revoking one grant took item %s from another (status %q)", item, status)
		}
	}

	// And a revoked grant is not re-granted by a snapshot that lists it again.
	replay, err := SyncAccountsPurchases(ctx, account.ID, []AccountsPurchaseGrant{
		{GrantID: "pur_ember", SKU: packID},
	})
	if err != nil {
		t.Fatalf("SyncAccountsPurchases replay: %v", err)
	}
	if replay.LicensesGranted != 0 {
		t.Fatalf("a withdrawn grant came back with %d licenses", replay.LicensesGranted)
	}
}

// TestPostgresAccountsPurchasesUnknownSKUsAndAccountConflicts covers the two
// ways the other service can say something Arena cannot act on.
func TestPostgresAccountsPurchasesUnknownSKUsAndAccountConflicts(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := accountsGrantTestAccount(t, ctx, "unknown-sku-subject")
	const packID = "arena-set-005-storm-herald-pack"
	packItems := accountsGrantPackItems(t, ctx, packID)

	/*
	 * A pack this Arena does not have must not sink the purchases beside it.
	 * Catalogues drift — a pack retired here, or added on the Accounts side
	 * first — and refusing the whole read over one unknown name would strand
	 * everything else the same person bought.
	 */
	result, err := SyncAccountsPurchases(ctx, account.ID, []AccountsPurchaseGrant{
		{GrantID: "pur_ghost", SKU: "arena-set-999-not-a-pack"},
		{GrantID: "pur_storm", SKU: packID},
	})
	if err != nil {
		t.Fatalf("SyncAccountsPurchases with an unknown SKU: %v", err)
	}
	if len(result.UnknownSKUs) != 1 || result.UnknownSKUs[0] != "arena-set-999-not-a-pack" {
		t.Fatalf("unknown SKUs = %v, want the one that is missing reported", result.UnknownSKUs)
	}
	if result.LicensesGranted != len(packItems) {
		t.Fatalf("granted %d licenses, want the known pack fulfilled anyway (%d)", result.LicensesGranted, len(packItems))
	}

	/*
	 * A stable identifier that stops being stable is two services disagreeing,
	 * not a race. Granting under either reading would be a guess about whose
	 * purchase it is, so nothing is granted and the caller is told.
	 */
	other := accountsGrantTestAccount(t, ctx, "second-subject")
	if _, err := SyncAccountsPurchases(ctx, other.ID, []AccountsPurchaseGrant{
		{GrantID: "pur_storm", SKU: packID},
	}); !errors.Is(err, ErrAccountsGrantConflict) {
		t.Fatalf("re-pointing a grant at another account = %v, want ErrAccountsGrantConflict", err)
	}
	if _, err := SyncAccountsPurchases(ctx, account.ID, []AccountsPurchaseGrant{
		{GrantID: "pur_storm", SKU: "arena-set-006-terra-forge-pack"},
	}); !errors.Is(err, ErrAccountsGrantConflict) {
		t.Fatalf("re-pointing a grant at another pack = %v, want ErrAccountsGrantConflict", err)
	}
	// Neither disagreement left a licence behind.
	if statuses := accountsGrantLicenseStatuses(t, ctx, "pur_storm"); len(statuses) != len(packItems) {
		t.Fatalf("grant licenses after conflicts = %d, want the original %d", len(statuses), len(packItems))
	}
	grants, err := ListAccountsGrantIDs(ctx, other.ID)
	if err != nil {
		t.Fatalf("ListAccountsGrantIDs: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("the second account holds %v after a refused conflict", grants)
	}
}
