package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func createAdminMembershipTestAccount(t *testing.T, ctx context.Context, email, subject string) *CustomerAccount {
	t.Helper()
	account, err := UpsertVerifiedCustomerAccount(ctx, email, "https://id.example", subject, "Admin Grant Recipient")
	if err != nil {
		t.Fatalf("create membership account: %v", err)
	}
	return account
}

func TestPostgresAdminMembershipCreatesOneLicensePerCurrentPurchasablePackItem(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createAdminMembershipTestAccount(t, ctx, "Member@Example.com", "admin-membership-current")
	expiresAt := time.Now().UTC().Add(30 * 24 * time.Hour)

	membership, licensesCreated, err := CreateCosmeticAdminMembershipForAccount(
		ctx, account.ID, expiresAt, "Community event prize", "admin-token",
	)
	if err != nil {
		t.Fatalf("CreateCosmeticAdminMembership: %v", err)
	}
	if membership == nil || membership.ID == "" || licensesCreated != launchSubscriptionCosmeticCount {
		t.Fatalf("membership = %+v, licenses created = %d, want %d", membership, licensesCreated, launchSubscriptionCosmeticCount)
	}

	var mappings, distinctItems, activeLicenses int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT ml.item_id),
		       COUNT(*) FILTER (WHERE l.status = 'active' AND l.account_id = $2)
		FROM cosmetic_admin_membership_licenses ml
		JOIN cosmetic_licenses l ON l.id = ml.license_id
		WHERE ml.membership_id = $1`, membership.ID, account.ID).
		Scan(&mappings, &distinctItems, &activeLicenses); err != nil {
		t.Fatalf("inspect membership licenses: %v", err)
	}
	if mappings != launchSubscriptionCosmeticCount || distinctItems != launchSubscriptionCosmeticCount || activeLicenses != launchSubscriptionCosmeticCount {
		t.Fatalf("membership license counts = mappings:%d distinct:%d active:%d, want %d each",
			mappings, distinctItems, activeLicenses, launchSubscriptionCosmeticCount)
	}
	// The audit row is joined to the account, and a linked account holds no
	// address — so what is checked is that the membership points at the right
	// *account*, which is the identifier the audit trail actually turns on.
	var accountID, note, actor, status string
	var storedExpiry time.Time
	if err := Pool.QueryRow(ctx, `
		SELECT a.id, m.note, m.granted_by, m.status, m.expires_at
		FROM cosmetic_admin_memberships m
		JOIN customer_accounts a ON a.id = m.account_id
		WHERE m.id = $1`, membership.ID).Scan(&accountID, &note, &actor, &status, &storedExpiry); err != nil {
		t.Fatalf("inspect membership audit metadata: %v", err)
	}
	if accountID != account.ID || note != "Community event prize" || actor != "admin-token" ||
		status != "active" || storedExpiry.Before(expiresAt.Add(-time.Microsecond)) || storedExpiry.After(expiresAt.Add(time.Microsecond)) {
		t.Fatalf("membership metadata = %q %q %q %q %v", accountID, note, actor, status, storedExpiry)
	}
}

func TestPostgresAdminMembershipSyncAddsFuturePackItemsExactlyOnce(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createAdminMembershipTestAccount(t, ctx, "future-member@example.com", "admin-membership-future")
	membership, _, err := CreateCosmeticAdminMembershipForAccount(
		ctx, account.ID, time.Now().UTC().Add(60*24*time.Hour), "Future sets included", "admin-session",
	)
	if err != nil {
		t.Fatalf("CreateCosmeticAdminMembership: %v", err)
	}

	item := CosmeticItem{
		ID: "skin-future-admin-set", Name: "Future Admin Skin", Description: "Future membership item.",
		CategoryID: "chassis", Slot: CosmeticSlotBotSkin, AssetKey: "arena_set_101_future_admin",
		Rarity: "rare", Currency: "USD", IsActive: true, SortOrder: 1010,
	}
	if _, err := UpsertCosmeticCatalogItem(ctx, item, "integration-admin"); err != nil {
		t.Fatalf("create future membership item: %v", err)
	}
	pack := CosmeticPack{
		ID: "future-admin-set", CategoryID: "starter-packs", Name: "Future Admin Set",
		PriceCents: CosmeticPackPriceCents, Currency: "USD", IsPurchasable: true, IsActive: true,
		SortOrder: 1010, ItemIDs: []string{item.ID},
	}
	if _, err := UpsertCosmeticPack(ctx, pack, "integration-admin"); err != nil {
		t.Fatalf("create future membership pack: %v", err)
	}

	created, err := SyncCustomerCosmeticAdminMembershipLicenses(ctx, account.ID)
	if err != nil || created != 1 {
		t.Fatalf("first future sync = (%d, %v), want (1, nil)", created, err)
	}
	created, err = SyncCustomerCosmeticAdminMembershipLicenses(ctx, account.ID)
	if err != nil || created != 0 {
		t.Fatalf("idempotent future sync = (%d, %v), want (0, nil)", created, err)
	}
	var mappings int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM cosmetic_admin_membership_licenses WHERE membership_id = $1`, membership.ID).
		Scan(&mappings); err != nil || mappings != launchSubscriptionCosmeticCount+1 {
		t.Fatalf("membership mapping count = (%d, %v), want %d", mappings, err, launchSubscriptionCosmeticCount+1)
	}
}

func TestPostgresAdminCosmeticAccessLookupByEmail(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createAdminMembershipTestAccount(t, ctx, "access-member@example.com", "admin-access")
	if _, _, err := CreateCosmeticAdminMembershipForAccount(
		ctx, account.ID, time.Now().UTC().Add(7*24*time.Hour), "Seven day grant", "admin-session",
	); err != nil {
		t.Fatalf("CreateCosmeticAdminMembership: %v", err)
	}
	if _, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "admin-manual-copy"); err != nil || !created {
		t.Fatalf("manual comparison grant = (%v, %v)", created, err)
	}

	/*
	 * By account, which is how an administrator reaches a linked customer.
	 * The address lookup below is kept and still exercised, but it can only
	 * find someone who has not signed in since the cutover — this account has
	 * been linked, so its address column is empty and nothing can match it.
	 */
	access, err := GetCosmeticAdminAccessByAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetCosmeticAdminAccessByAccount: %v", err)
	}
	if access == nil || access.Account.ID != account.ID || len(access.Memberships) != 1 || len(access.Licenses) != launchSubscriptionCosmeticCount+1 {
		t.Fatalf("admin access lookup = %+v, want the account, one membership, %d licenses", access, launchSubscriptionCosmeticCount+1)
	}

	// The address route, on an account that still has one: a pending
	// fulfilment row created by a purchase before its buyer ever signed in.
	pending, err := GetOrCreateCustomerAccountByEmail(ctx, "not-yet-linked@example.com")
	if err != nil {
		t.Fatalf("pending account: %v", err)
	}
	byEmail, err := GetCosmeticAdminAccessByEmail(ctx, " NOT-YET-LINKED@EXAMPLE.COM ")
	if err != nil {
		t.Fatalf("GetCosmeticAdminAccessByEmail: %v", err)
	}
	if byEmail == nil || byEmail.Account.ID != pending.ID || byEmail.Email != "not-yet-linked@example.com" {
		t.Fatalf("address lookup = %+v, want the normalized pending account", byEmail)
	}

	// And on a linked account, it finds nobody rather than erroring.
	linkedByEmail, err := GetCosmeticAdminAccessByEmail(ctx, "access-member@example.com")
	if err != nil {
		t.Fatalf("GetCosmeticAdminAccessByEmail on a linked account: %v", err)
	}
	if linkedByEmail.Account.ID != "" {
		t.Fatalf("a linked account was still reachable by address: %+v", linkedByEmail)
	}
}

// TestPostgresCustomerInventoryReflectsActiveAdminMembership is a
// regression test for a Dashboard bug: an admin-granted "All Access"
// membership materialized per-item licenses correctly, but the customer
// inventory response never surfaced the membership itself, so the
// Dashboard's All Access status kept showing "Available" instead of
// "Access active".
func TestPostgresCustomerInventoryReflectsActiveAdminMembership(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createAdminMembershipTestAccount(t, ctx, "granted-member@example.com", "admin-inventory-membership")
	expiresAt := time.Now().UTC().Add(14 * 24 * time.Hour)
	membership, _, err := CreateCosmeticAdminMembershipForAccount(
		ctx, account.ID, expiresAt, "Support ticket #42", "admin-token",
	)
	if err != nil {
		t.Fatalf("CreateCosmeticAdminMembership: %v", err)
	}

	found, err := GetActiveCosmeticAdminMembership(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetActiveCosmeticAdminMembership: %v", err)
	}
	if found == nil || found.ID != membership.ID || found.Status != "active" {
		t.Fatalf("GetActiveCosmeticAdminMembership = %+v, want the granted membership", found)
	}

	inventory, err := GetCustomerCosmeticsInventory(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetCustomerCosmeticsInventory: %v", err)
	}
	if inventory.Membership == nil || inventory.Membership.ID != membership.ID {
		t.Fatalf("inventory.Membership = %+v, want it populated from the admin grant", inventory.Membership)
	}
	if len(inventory.Licenses) != launchSubscriptionCosmeticCount {
		t.Fatalf("inventory.Licenses = %d, want %d from the membership grant", len(inventory.Licenses), launchSubscriptionCosmeticCount)
	}

	if _, _, _, err := RevokeCosmeticAdminMembership(ctx, membership.ID, "admin-token", "test cleanup"); err != nil {
		t.Fatalf("RevokeCosmeticAdminMembership: %v", err)
	}
	afterRevoke, err := GetActiveCosmeticAdminMembership(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetActiveCosmeticAdminMembership after revoke: %v", err)
	}
	if afterRevoke != nil {
		t.Fatalf("GetActiveCosmeticAdminMembership after revoke = %+v, want nil", afterRevoke)
	}
}

func TestPostgresAdminMembershipExpiryAndRevokeRemoveOnlyMappedUse(t *testing.T) {
	tests := []struct {
		name       string
		transition func(context.Context, string, time.Time) (int, []string, error)
		wantStatus string
	}{
		{
			name: "expiry",
			transition: func(ctx context.Context, _ string, expiresAt time.Time) (int, []string, error) {
				return ExpireCosmeticAdminMemberships(ctx, expiresAt.Add(time.Second), 100)
			},
			wantStatus: "expired",
		},
		{
			name: "revocation",
			transition: func(ctx context.Context, membershipID string, _ time.Time) (int, []string, error) {
				_, affected, changed, err := RevokeCosmeticAdminMembership(ctx, membershipID, "admin-token", "Prize rescinded")
				if err != nil || !changed {
					return 0, affected, err
				}
				return 1, affected, nil
			},
			wantStatus: "revoked",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := useFreshPostgresSchema(t)
			if err := EnsureCoreSchema(ctx); err != nil {
				t.Fatalf("EnsureCoreSchema: %v", err)
			}
			account := createAdminMembershipTestAccount(t, ctx, test.name+"@example.com", "admin-"+test.name)
			expiresAt := time.Now().UTC().Add(time.Hour)
			membership, _, err := CreateCosmeticAdminMembershipForAccount(ctx, account.ID, expiresAt, test.name, "admin-token")
			if err != nil {
				t.Fatalf("CreateCosmeticAdminMembership: %v", err)
			}
			manual, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "preserve-"+test.name)
			if err != nil || !created {
				t.Fatalf("manual license = (%+v, %v, %v)", manual, created, err)
			}
			bot := createCustomerCosmeticsTestBot(t, ctx, "admin-membership-"+test.name)
			if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
				t.Fatalf("link bot: %v", err)
			}
			var membershipLicenseID string
			if err := Pool.QueryRow(ctx, `
				SELECT ml.license_id
				FROM cosmetic_admin_membership_licenses ml
				WHERE ml.membership_id = $1 AND ml.item_id = 'skin-neon-grid'`, membership.ID).
				Scan(&membershipLicenseID); err != nil {
				t.Fatalf("load membership license: %v", err)
			}
			if _, err := AssignCosmeticLicense(ctx, account.ID, membershipLicenseID, &bot.ID); err != nil {
				t.Fatalf("assign membership license: %v", err)
			}
			if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, bot.ID, membershipLicenseID); err != nil {
				t.Fatalf("equip membership license: %v", err)
			}

			changed, affected, err := test.transition(ctx, membership.ID, expiresAt)
			if err != nil || changed != 1 || len(affected) != 1 || affected[0] != bot.ID {
				t.Fatalf("transition = changed:%d affected:%v err:%v", changed, affected, err)
			}
			var membershipStatus, membershipLicenseStatus, manualStatus string
			var assignments, loadouts int
			if err := Pool.QueryRow(ctx, `
				SELECT m.status, ml.status, manual.status,
				       (SELECT COUNT(*) FROM cosmetic_license_assignments WHERE license_id = $2),
				       (SELECT COUNT(*) FROM bot_cosmetic_loadout WHERE license_id = $2)
				FROM cosmetic_admin_memberships m
				JOIN cosmetic_licenses ml ON ml.id = $2
				JOIN cosmetic_licenses manual ON manual.id = $3
				WHERE m.id = $1`, membership.ID, membershipLicenseID, manual.ID).
				Scan(&membershipStatus, &membershipLicenseStatus, &manualStatus, &assignments, &loadouts); err != nil {
				t.Fatalf("inspect transition: %v", err)
			}
			if membershipStatus != test.wantStatus || membershipLicenseStatus != test.wantStatus ||
				manualStatus != "active" || assignments != 0 || loadouts != 0 {
				t.Fatalf("post-transition membership=%q license=%q manual=%q assignments=%d loadouts=%d",
					membershipStatus, membershipLicenseStatus, manualStatus, assignments, loadouts)
			}
		})
	}
}

func TestPostgresAdminMembershipRejectsPastExpiry(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createAdminMembershipTestAccount(t, ctx, "past-member@example.com", "admin-membership-past")
	if _, _, err := CreateCosmeticAdminMembershipForAccount(
		ctx, account.ID, time.Now().UTC().Add(-time.Minute), "Already expired", "admin-token",
	); err == nil {
		t.Fatal("past membership expiry was accepted")
	}
}

func TestPostgresAdminMembershipExpiryUsesExactTimestampBoundary(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createAdminMembershipTestAccount(t, ctx, "boundary-member@example.com", "admin-membership-boundary")
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	membership, _, err := CreateCosmeticAdminMembershipForAccount(ctx, account.ID, expiresAt, "Boundary", "admin-token")
	if err != nil {
		t.Fatalf("CreateCosmeticAdminMembership: %v", err)
	}
	changed, _, err := ExpireCosmeticAdminMemberships(ctx, expiresAt.Add(-time.Microsecond), 100)
	if err != nil || changed != 0 {
		t.Fatalf("pre-boundary expiry = (%d, %v), want (0, nil)", changed, err)
	}
	changed, _, err = ExpireCosmeticAdminMemberships(ctx, expiresAt, 100)
	if err != nil || changed != 1 {
		t.Fatalf("exact-boundary expiry = (%d, %v), want (1, nil)", changed, err)
	}
	loaded, err := getCosmeticAdminMembership(ctx, membership.ID)
	if err != nil || loaded.Status != "expired" {
		t.Fatalf("expired membership = (%+v, %v)", loaded, err)
	}
}

func TestPostgresMembershipLicenseRequiresMembershipLevelRevocation(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createAdminMembershipTestAccount(t, ctx, "membership-revoke@example.com", "admin-membership-license-revoke")
	membership, _, err := CreateCosmeticAdminMembershipForAccount(
		ctx, account.ID, time.Now().UTC().Add(24*time.Hour), "Membership copy", "admin-token",
	)
	if err != nil {
		t.Fatalf("CreateCosmeticAdminMembership: %v", err)
	}
	var licenseID string
	if err := Pool.QueryRow(ctx, `
		SELECT license_id FROM cosmetic_admin_membership_licenses
		WHERE membership_id = $1 ORDER BY item_id LIMIT 1`, membership.ID).Scan(&licenseID); err != nil {
		t.Fatalf("load membership license: %v", err)
	}
	if _, _, err := RevokeCosmeticLicense(ctx, licenseID); !errors.Is(err, ErrCosmeticAdminMembershipLicense) {
		t.Fatalf("direct membership license revoke error = %v", err)
	}
	var status string
	if err := Pool.QueryRow(ctx, `SELECT status FROM cosmetic_licenses WHERE id = $1`, licenseID).Scan(&status); err != nil || status != "active" {
		t.Fatalf("membership license status = (%q, %v), want active", status, err)
	}
}

func TestPostgresElapsedMembershipFailsClosedBeforeExpiryWorker(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createAdminMembershipTestAccount(t, ctx, "elapsed-member@example.com", "admin-membership-elapsed")
	membership, _, err := CreateCosmeticAdminMembershipForAccount(
		ctx, account.ID, time.Now().UTC().Add(time.Hour), "Elapsed", "admin-token",
	)
	if err != nil {
		t.Fatalf("CreateCosmeticAdminMembership: %v", err)
	}
	bot := createCustomerCosmeticsTestBot(t, ctx, "admin-membership-elapsed")
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("LinkBotToCustomerAccount: %v", err)
	}
	var licenseID string
	if err := Pool.QueryRow(ctx, `
		SELECT license_id FROM cosmetic_admin_membership_licenses
		WHERE membership_id = $1 AND item_id = 'skin-neon-grid'`, membership.ID).Scan(&licenseID); err != nil {
		t.Fatalf("load membership license: %v", err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, licenseID, &bot.ID); err != nil {
		t.Fatalf("AssignCosmeticLicense: %v", err)
	}
	if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, bot.ID, licenseID); err != nil {
		t.Fatalf("EquipCustomerCosmeticLicense: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		UPDATE cosmetic_admin_memberships
		SET granted_at = NOW() - INTERVAL '1 hour', expires_at = NOW() - INTERVAL '1 second'
		WHERE id = $1`, membership.ID); err != nil {
		t.Fatalf("elapse membership: %v", err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, licenseID, &bot.ID); !errors.Is(err, ErrCosmeticInactive) {
		t.Fatalf("elapsed assignment error = %v", err)
	}
	equipped, err := GetEquippedCosmetics(ctx, bot.ID)
	if err != nil {
		t.Fatalf("GetEquippedCosmetics: %v", err)
	}
	if equipped[CosmeticSlotBotSkin] != "standard" {
		t.Fatalf("elapsed equipped skin = %q, want standard", equipped[CosmeticSlotBotSkin])
	}
}

func TestPostgresCustomerScopedExpiryAllowsImmediateReplacement(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createAdminMembershipTestAccount(t, ctx, "replacement-member@example.com", "admin-membership-replacement")
	first, _, err := CreateCosmeticAdminMembershipForAccount(
		ctx, account.ID, time.Now().UTC().Add(time.Hour), "First", "admin-token",
	)
	if err != nil {
		t.Fatalf("CreateCosmeticAdminMembership(first): %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		UPDATE cosmetic_admin_memberships
		SET granted_at = NOW() - INTERVAL '1 hour', expires_at = NOW() - INTERVAL '1 second'
		WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("elapse first membership: %v", err)
	}
	changed, _, err := ExpireAccountCosmeticAdminMemberships(ctx, account.ID, time.Now().UTC())
	if err != nil || changed != 1 {
		t.Fatalf("ExpireCustomerCosmeticAdminMemberships = (%d, %v)", changed, err)
	}
	second, _, err := CreateCosmeticAdminMembershipForAccount(
		ctx, account.ID, time.Now().UTC().Add(48*time.Hour), "Replacement", "admin-token",
	)
	if err != nil || second == nil || second.ID == first.ID {
		t.Fatalf("replacement membership = (%+v, %v)", second, err)
	}
}

func TestPostgresAdminMembershipSchemaUpgradeIsIdempotent(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		ALTER TABLE cosmetic_licenses DROP CONSTRAINT cosmetic_licenses_status_check;
		ALTER TABLE cosmetic_licenses ADD CONSTRAINT cosmetic_licenses_status_check
			CHECK (status IN ('active', 'refunded', 'revoked', 'chargeback'))`); err != nil {
		t.Fatalf("restore legacy license status constraint: %v", err)
	}
	if err := EnsureCosmeticAdminMembershipsSchema(ctx); err != nil {
		t.Fatalf("first EnsureCosmeticAdminMembershipsSchema: %v", err)
	}
	if err := EnsureCosmeticAdminMembershipsSchema(ctx); err != nil {
		t.Fatalf("second EnsureCosmeticAdminMembershipsSchema: %v", err)
	}
	var definition string
	var validated bool
	if err := Pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid), convalidated
		FROM pg_constraint
		WHERE conrelid = 'cosmetic_licenses'::regclass
		  AND conname = 'cosmetic_licenses_status_check'`).Scan(&definition, &validated); err != nil {
		t.Fatalf("inspect license status constraint: %v", err)
	}
	if !validated || !strings.Contains(definition, "expired") {
		t.Fatalf("license status constraint = %q validated=%v", definition, validated)
	}
}
