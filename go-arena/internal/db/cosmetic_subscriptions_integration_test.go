package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func seedLegacyOwnedAPIKeys(t *testing.T, ctx context.Context, accountID, suffix string, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		keyID := fmt.Sprintf("legacy-subscription-key-%s-%d", suffix, index)
		if _, err := Pool.Exec(ctx, `
			INSERT INTO api_keys (id, key_hash, key_prefix, created_at, is_active)
			VALUES ($1, $2, $3, NOW(), true)`, keyID, "legacy-test-hash", "legacy_"+keyID); err != nil {
			t.Fatalf("seed legacy API key %d: %v", index, err)
		}
		if _, err := Pool.Exec(ctx, `
			INSERT INTO account_api_keys (account_id, api_key_id, linked_at)
			VALUES ($1, $2, NOW())`, accountID, keyID); err != nil {
			t.Fatalf("seed legacy API key ownership %d: %v", index, err)
		}
	}
}

func subscriptionEventHash(char string) string {
	return strings.Repeat(char, 64)
}

var launchSubscriptionCosmeticCount = countLaunchSubscriptionCosmetics(DefaultCosmeticCatalogData())

// Mirror the production candidate query instead of hard-coding a launch
// catalog total. Adding an active purchasable pack should expand All Access
// without requiring unrelated integration-test constants to be updated.
func countLaunchSubscriptionCosmetics(catalog CosmeticCatalog) int {
	activeCategories := make(map[string]bool, len(catalog.Categories))
	for _, category := range catalog.Categories {
		activeCategories[category.ID] = category.IsActive
	}
	items := make(map[string]CosmeticItem, len(catalog.Items))
	for _, item := range catalog.Items {
		items[item.ID] = item
	}
	eligible := make(map[string]bool)
	for _, pack := range catalog.Packs {
		if !pack.IsActive || !pack.IsPurchasable || pack.IsFree || !activeCategories[pack.CategoryID] {
			continue
		}
		for _, itemID := range pack.ItemIDs {
			item, ok := items[itemID]
			if ok && item.IsActive && activeCategories[item.CategoryID] {
				eligible[itemID] = true
			}
		}
	}
	return len(eligible)
}

func TestPostgresCosmeticSubscriptionLifecycleAndFutureSetSync(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "subscriber@example.com", "https://id.example", "subscriber", "Subscriber")
	if err != nil {
		t.Fatalf("create subscriber: %v", err)
	}
	subscription, err := CreateCosmeticSubscription(ctx, account.ID)
	if err != nil {
		t.Fatalf("CreateCosmeticSubscription: %v", err)
	}
	if subscription.PriceCents != 1999 || subscription.Currency != "USD" || subscription.Interval != "month" {
		t.Fatalf("subscription price snapshot = %+v", subscription)
	}
	if _, err := AttachCosmeticSubscriptionCheckout(ctx, account.ID, subscription.ID, "cs_subscription"); err != nil {
		t.Fatalf("AttachCosmeticSubscriptionCheckout: %v", err)
	}

	started := time.Now().UTC().Truncate(time.Second)
	periodEnd := started.Add(30 * 24 * time.Hour)
	active := CosmeticSubscriptionEventInput{
		Provider: "stripe", EventID: "evt_subscription_active", EventType: CosmeticStripeSubscriptionCreated,
		PayloadHash: subscriptionEventHash("a"), SubscriptionID: subscription.ID, AccountID: account.ID,
		CheckoutSessionID: "cs_subscription", ProviderSubscriptionID: "sub_provider", CustomerID: "cus_provider",
		Status: CosmeticSubscriptionStatusActive, CurrentPeriodEnd: &periodEnd, ProviderCreatedAt: started,
	}
	result, err := ProcessCosmeticSubscriptionEvent(ctx, active)
	if err != nil {
		t.Fatalf("ProcessCosmeticSubscriptionEvent(active): %v", err)
	}
	if !result.Applied || result.Duplicate || result.LicensesCreated != launchSubscriptionCosmeticCount || result.Subscription == nil || !result.Subscription.HasAccess {
		t.Fatalf("active subscription result = %+v", result)
	}
	duplicate, err := ProcessCosmeticSubscriptionEvent(ctx, active)
	if err != nil || !duplicate.Duplicate || duplicate.LicensesCreated != 0 {
		t.Fatalf("duplicate active event = (%+v, %v)", duplicate, err)
	}

	futureItem := CosmeticItem{
		ID: "skin-future-subscription-set", Name: "Future Subscription Skin", Description: "Future set item.",
		CategoryID: "chassis", Slot: CosmeticSlotBotSkin, AssetKey: "arena_set_101_future_subscription",
		Rarity: "rare", Currency: "USD", IsActive: true, SortOrder: 1010,
	}
	if _, err := UpsertCosmeticCatalogItem(ctx, futureItem, "subscription-test"); err != nil {
		t.Fatalf("create future item: %v", err)
	}
	futurePack := CosmeticPack{
		ID: "future-subscription-set", CategoryID: "starter-packs", Name: "Future Subscription Set",
		PriceCents: CosmeticPackPriceCents, Currency: "USD", IsPurchasable: true, IsActive: true,
		SortOrder: 1010, ItemIDs: []string{futureItem.ID},
	}
	if _, err := UpsertCosmeticPack(ctx, futurePack, "subscription-test"); err != nil {
		t.Fatalf("create future set: %v", err)
	}
	created, err := SyncCustomerCosmeticSubscriptionLicenses(ctx, account.ID)
	if err != nil || created != 1 {
		t.Fatalf("future subscription sync = (%d, %v), want (1, nil)", created, err)
	}

	manual, manualCreated, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "manual-preserved-copy")
	if err != nil || !manualCreated {
		t.Fatalf("create manual comparison license = (%+v, %v, %v)", manual, manualCreated, err)
	}
	bot := createCustomerCosmeticsTestBot(t, ctx, "subscription-cancel")
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("link cancellation bot: %v", err)
	}
	var subscriptionLicenseID string
	if err := Pool.QueryRow(ctx, `
		SELECT sl.license_id
		FROM cosmetic_subscription_licenses sl
		JOIN cosmetic_licenses l ON l.id = sl.license_id
		WHERE sl.subscription_id = $1 AND l.cosmetic_id = 'skin-neon-grid'`, subscription.ID).Scan(&subscriptionLicenseID); err != nil {
		t.Fatalf("load subscription license: %v", err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, subscriptionLicenseID, &bot.ID); err != nil {
		t.Fatalf("assign subscription license: %v", err)
	}
	if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, bot.ID, subscriptionLicenseID); err != nil {
		t.Fatalf("equip subscription license: %v", err)
	}

	canceledAt := started.Add(time.Hour)
	canceled := active
	canceled.EventID = "evt_subscription_canceled"
	canceled.EventType = CosmeticStripeSubscriptionDeleted
	canceled.PayloadHash = subscriptionEventHash("b")
	canceled.Status = CosmeticSubscriptionStatusCanceled
	canceled.Terminal = true
	canceled.ProviderCreatedAt = canceledAt
	result, err = ProcessCosmeticSubscriptionEvent(ctx, canceled)
	if err != nil {
		t.Fatalf("ProcessCosmeticSubscriptionEvent(canceled): %v", err)
	}
	if !result.Applied || !result.Subscription.Terminal || result.Subscription.HasAccess || result.LicensesRevoked != launchSubscriptionCosmeticCount+1 {
		t.Fatalf("canceled subscription result = %+v", result)
	}

	var mappedActive, mappedAssignments, mappedLoadouts int
	if err := Pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM cosmetic_subscription_licenses sl JOIN cosmetic_licenses l ON l.id = sl.license_id
			 WHERE sl.subscription_id = $1 AND l.status = 'active'),
			(SELECT COUNT(*) FROM cosmetic_subscription_licenses sl JOIN cosmetic_license_assignments a ON a.license_id = sl.license_id
			 WHERE sl.subscription_id = $1),
			(SELECT COUNT(*) FROM cosmetic_subscription_licenses sl JOIN bot_cosmetic_loadout l ON l.license_id = sl.license_id
			 WHERE sl.subscription_id = $1)`, subscription.ID).Scan(&mappedActive, &mappedAssignments, &mappedLoadouts); err != nil {
		t.Fatalf("inspect canceled subscription licenses: %v", err)
	}
	if mappedActive != 0 || mappedAssignments != 0 || mappedLoadouts != 0 {
		t.Fatalf("cancellation left active=%d assignments=%d loadouts=%d", mappedActive, mappedAssignments, mappedLoadouts)
	}
	manualAfter, err := getCosmeticLicense(ctx, manual.ID)
	if err != nil || manualAfter.Status != "active" {
		t.Fatalf("manual license after cancellation = (%+v, %v)", manualAfter, err)
	}

	stale := active
	stale.EventID = "evt_subscription_stale_active"
	stale.PayloadHash = subscriptionEventHash("c")
	stale.ProviderCreatedAt = started.Add(30 * time.Minute)
	staleResult, err := ProcessCosmeticSubscriptionEvent(ctx, stale)
	if err != nil || staleResult.Applied || staleResult.Subscription.HasAccess || !staleResult.Subscription.Terminal {
		t.Fatalf("stale active event after cancellation = (%+v, %v)", staleResult, err)
	}

	replacement, reservationCreated, err := ReserveCosmeticSubscriptionCheckout(ctx, account.ID, CosmeticCheckoutPresentationHosted)
	if err != nil || !reservationCreated || replacement.ID == subscription.ID ||
		replacement.CheckoutPresentation != CosmeticCheckoutPresentationHosted || replacement.CustomerID != "cus_provider" || replacement.CanManage {
		t.Fatalf("replacement subscription reservation = (%+v, %v, %v)", replacement, reservationCreated, err)
	}
	replayed, reservationCreated, err := ReserveCosmeticSubscriptionCheckout(ctx, account.ID, CosmeticCheckoutPresentationEmbedded)
	if err != nil || reservationCreated || replayed.ID != replacement.ID ||
		replayed.CheckoutPresentation != CosmeticCheckoutPresentationHosted || replayed.CustomerID != "cus_provider" {
		t.Fatalf("replacement subscription replay = (%+v, %v, %v)", replayed, reservationCreated, err)
	}
}

func TestPostgresCosmeticSubscriptionSyncNeverReactivatesTerminalLicense(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "terminal-subscriber@example.com", "https://id.example", "terminal-subscriber", "Terminal Subscriber")
	if err != nil {
		t.Fatalf("create subscriber: %v", err)
	}
	subscription, err := CreateCosmeticSubscription(ctx, account.ID)
	if err != nil {
		t.Fatalf("CreateCosmeticSubscription: %v", err)
	}
	if _, err := AttachCosmeticSubscriptionCheckout(ctx, account.ID, subscription.ID, "cs_terminal_subscription"); err != nil {
		t.Fatalf("AttachCosmeticSubscriptionCheckout: %v", err)
	}

	started := time.Now().UTC().Truncate(time.Second)
	periodEnd := started.Add(30 * 24 * time.Hour)
	active := CosmeticSubscriptionEventInput{
		Provider: "stripe", EventID: "evt_terminal_subscription_active", EventType: CosmeticStripeSubscriptionCreated,
		PayloadHash: subscriptionEventHash("f"), SubscriptionID: subscription.ID, AccountID: account.ID,
		CheckoutSessionID: "cs_terminal_subscription", ProviderSubscriptionID: "sub_terminal_provider", CustomerID: "cus_terminal_provider",
		Status: CosmeticSubscriptionStatusActive, CurrentPeriodEnd: &periodEnd, ProviderCreatedAt: started,
	}
	if result, err := ProcessCosmeticSubscriptionEvent(ctx, active); err != nil || !result.Applied || result.LicensesCreated == 0 {
		t.Fatalf("activate subscription = (%+v, %v)", result, err)
	}

	var licenseID string
	if err := Pool.QueryRow(ctx, `
		SELECT sl.license_id
		FROM cosmetic_subscription_licenses sl
		JOIN cosmetic_licenses l ON l.id = sl.license_id
		WHERE sl.subscription_id = $1 AND l.cosmetic_id = 'skin-neon-grid'`, subscription.ID).Scan(&licenseID); err != nil {
		t.Fatalf("load subscription license: %v", err)
	}
	if _, err := Pool.Exec(ctx, `UPDATE cosmetic_licenses SET status = 'refunded' WHERE id = $1`, licenseID); err != nil {
		t.Fatalf("mark subscription license refunded: %v", err)
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin subscription reconciliation: %v", err)
	}
	defer tx.Rollback(ctx)
	_, reactivated, err := syncCosmeticSubscriptionLicenses(ctx, tx, subscription.ID, account.ID)
	if err != nil {
		t.Fatalf("syncCosmeticSubscriptionLicenses: %v", err)
	}
	if reactivated != 0 {
		t.Fatalf("terminal subscription licenses reactivated = %d, want 0", reactivated)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit subscription reconciliation: %v", err)
	}
	var status string
	if err := Pool.QueryRow(ctx, `SELECT status FROM cosmetic_licenses WHERE id = $1`, licenseID).Scan(&status); err != nil {
		t.Fatalf("load synchronized license: %v", err)
	}
	if status != "refunded" {
		t.Fatalf("terminal subscription license status = %q, want refunded", status)
	}
}

func TestPostgresCosmeticSubscriptionAutomaticallyAddsNewlyPublishedTrail(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	const packID = "trail-bounty-flare-pack"
	const itemID = "trail-bounty-flare"
	if _, err := Pool.Exec(ctx, `UPDATE cosmetic_packs SET is_active = false WHERE id = $1`, packID); err != nil {
		t.Fatalf("stage unpublished trail: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "future-trail@example.com", "https://id.example", "future-trail", "Future Trail")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	subscription, err := CreateCosmeticSubscription(ctx, account.ID)
	if err != nil {
		t.Fatalf("CreateCosmeticSubscription: %v", err)
	}
	started := time.Now().UTC().Truncate(time.Second)
	active, err := ProcessCosmeticSubscriptionEvent(ctx, CosmeticSubscriptionEventInput{
		Provider: "stripe", EventID: "evt_future_trail_active", EventType: CosmeticStripeSubscriptionCreated,
		PayloadHash: subscriptionEventHash("9"), SubscriptionID: subscription.ID, AccountID: account.ID,
		ProviderSubscriptionID: "sub_future_trail", CustomerID: "cus_future_trail",
		Status: CosmeticSubscriptionStatusActive, ProviderCreatedAt: started,
	})
	if err != nil || active.LicensesCreated != launchSubscriptionCosmeticCount-1 || !active.Subscription.HasAccess {
		t.Fatalf("activate without unpublished trail = (%+v, %v)", active, err)
	}

	if _, err := Pool.Exec(ctx, `UPDATE cosmetic_packs SET is_active = true WHERE id = $1`, packID); err != nil {
		t.Fatalf("publish future trail: %v", err)
	}
	created, err := SyncCustomerCosmeticSubscriptionLicenses(ctx, account.ID)
	if err != nil || created != 1 {
		t.Fatalf("future trail sync = (%d, %v), want (1, nil)", created, err)
	}
	created, err = SyncCustomerCosmeticSubscriptionLicenses(ctx, account.ID)
	if err != nil || created != 0 {
		t.Fatalf("idempotent future trail sync = (%d, %v), want (0, nil)", created, err)
	}
	var mapped int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM cosmetic_subscription_licenses sl
		JOIN cosmetic_licenses l ON l.id = sl.license_id
		WHERE sl.subscription_id = $1 AND l.cosmetic_id = $2 AND l.status = 'active'`, subscription.ID, itemID).
		Scan(&mapped); err != nil || mapped != 1 {
		t.Fatalf("future trail mapping = (%d, %v), want one active copy", mapped, err)
	}
}

func TestPostgresCosmeticSubscriptionCancelAtPeriodEndRetainsAccess(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "period-end@example.com", "https://id.example", "period-end", "Period End")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	subscription, err := CreateCosmeticSubscription(ctx, account.ID)
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	periodEnd := now.Add(7 * 24 * time.Hour)
	result, err := ProcessCosmeticSubscriptionEvent(ctx, CosmeticSubscriptionEventInput{
		Provider: "stripe", EventID: "evt_period_end", EventType: CosmeticStripeSubscriptionUpdated,
		PayloadHash: subscriptionEventHash("d"), SubscriptionID: subscription.ID, AccountID: account.ID,
		ProviderSubscriptionID: "sub_period_end", CustomerID: "cus_period_end",
		Status: CosmeticSubscriptionStatusActive, CancelAtPeriodEnd: true, CurrentPeriodEnd: &periodEnd,
		ProviderCreatedAt: now,
	})
	if err != nil || result.Subscription == nil || !result.Subscription.HasAccess || !result.Subscription.CancelAtPeriodEnd ||
		result.LicensesCreated != launchSubscriptionCosmeticCount || result.LicensesRevoked != 0 {
		t.Fatalf("cancel-at-period-end active result = (%+v, %v)", result, err)
	}
}

func TestPostgresCosmeticSubscriptionIncompleteExpiredIsTerminal(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "incomplete-expired@example.com", "https://id.example", "incomplete-expired", "Incomplete Expired")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	subscription, err := CreateCosmeticSubscription(ctx, account.ID)
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	active, err := ProcessCosmeticSubscriptionEvent(ctx, CosmeticSubscriptionEventInput{
		Provider: "stripe", EventID: "evt_incomplete_expired_active", EventType: CosmeticStripeSubscriptionUpdated,
		PayloadHash: subscriptionEventHash("e"), SubscriptionID: subscription.ID, AccountID: account.ID,
		ProviderSubscriptionID: "sub_incomplete_expired", CustomerID: "cus_incomplete_expired",
		Status: CosmeticSubscriptionStatusActive, ProviderCreatedAt: now,
	})
	if err != nil || active.LicensesCreated != launchSubscriptionCosmeticCount || !active.Subscription.HasAccess {
		t.Fatalf("activate subscription = (%+v, %v)", active, err)
	}

	expired, err := ProcessCosmeticSubscriptionEvent(ctx, CosmeticSubscriptionEventInput{
		Provider: "stripe", EventID: "evt_incomplete_expired_terminal", EventType: CosmeticStripeSubscriptionUpdated,
		PayloadHash: subscriptionEventHash("f"), SubscriptionID: subscription.ID, AccountID: account.ID,
		ProviderSubscriptionID: "sub_incomplete_expired", CustomerID: "cus_incomplete_expired",
		Status: "incomplete_expired", ProviderCreatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("process incomplete_expired: %v", err)
	}
	if expired.Subscription.Status != CosmeticSubscriptionStatusExpired || !expired.Subscription.Terminal ||
		expired.Subscription.HasAccess || expired.Subscription.CanManage || expired.LicensesRevoked != launchSubscriptionCosmeticCount {
		t.Fatalf("incomplete_expired result = %+v", expired)
	}
}

func TestPostgresCosmeticSubscriptionAuthoritativeBillingMismatchRevokesAndCanRecover(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "billing-mismatch@example.com", "https://id.example", "billing-mismatch", "Billing Mismatch")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	subscription, err := CreateCosmeticSubscription(ctx, account.ID)
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	providerEventCreated := base.Add(10 * time.Minute)
	firstObserved := base.Add(time.Second)
	active, err := ProcessCosmeticSubscriptionEvent(ctx, CosmeticSubscriptionEventInput{
		Provider: "stripe", EventID: "evt_billing_valid", EventType: CosmeticStripeSubscriptionUpdated,
		PayloadHash: subscriptionEventHash("1"), SubscriptionID: subscription.ID, AccountID: account.ID,
		ProviderSubscriptionID: "sub_billing_mismatch", CustomerID: "cus_billing_mismatch",
		Status: CosmeticSubscriptionStatusActive, ProviderCreatedAt: providerEventCreated,
		ProviderStateObservedAt: firstObserved,
	})
	if err != nil || !active.Subscription.HasAccess || active.LicensesCreated != launchSubscriptionCosmeticCount {
		t.Fatalf("initial authoritative active = (%+v, %v)", active, err)
	}
	initialRows, err := Pool.Query(ctx, `
		SELECT item_id, license_id, generation
		FROM cosmetic_subscription_licenses
		WHERE subscription_id = $1
		ORDER BY item_id`, subscription.ID)
	if err != nil {
		t.Fatalf("load initial generations: %v", err)
	}
	initialItemIDs := make([]string, 0)
	initialLicenseIDs := make([]string, 0)
	for initialRows.Next() {
		var itemID, licenseID string
		var generation int64
		if err := initialRows.Scan(&itemID, &licenseID, &generation); err != nil {
			initialRows.Close()
			t.Fatalf("scan initial generation: %v", err)
		}
		if generation != 1 {
			initialRows.Close()
			t.Fatalf("initial generation for %s = %d, want 1", itemID, generation)
		}
		initialItemIDs = append(initialItemIDs, itemID)
		initialLicenseIDs = append(initialLicenseIDs, licenseID)
	}
	if err := initialRows.Err(); err != nil {
		initialRows.Close()
		t.Fatalf("initial generation rows: %v", err)
	}
	initialRows.Close()

	// The signed event is older, but its provider observation is newer. The
	// authoritative billing mismatch must apply and revoke access.
	secondObserved := firstObserved.Add(time.Second)
	mismatch, err := ProcessCosmeticSubscriptionEvent(ctx, CosmeticSubscriptionEventInput{
		Provider: "stripe", EventID: "evt_billing_mismatch", EventType: CosmeticStripeSubscriptionUpdated,
		PayloadHash: subscriptionEventHash("2"), SubscriptionID: subscription.ID, AccountID: account.ID,
		ProviderSubscriptionID: "sub_billing_mismatch", CustomerID: "cus_billing_mismatch",
		Status: CosmeticSubscriptionStatusBillingMismatch, ProviderCreatedAt: base,
		ProviderStateObservedAt: secondObserved,
	})
	if err != nil || !mismatch.Applied || mismatch.Subscription.Status != CosmeticSubscriptionStatusBillingMismatch ||
		mismatch.Subscription.HasAccess || !mismatch.Subscription.CanManage || mismatch.LicensesRevoked != launchSubscriptionCosmeticCount {
		t.Fatalf("authoritative billing mismatch = (%+v, %v)", mismatch, err)
	}

	recoveredObserved := secondObserved.Add(time.Second)
	recovered, err := ProcessCosmeticSubscriptionEvent(ctx, CosmeticSubscriptionEventInput{
		Provider: "stripe", EventID: "evt_billing_recovered", EventType: CosmeticStripeSubscriptionUpdated,
		PayloadHash: subscriptionEventHash("3"), SubscriptionID: subscription.ID, AccountID: account.ID,
		ProviderSubscriptionID: "sub_billing_mismatch", CustomerID: "cus_billing_mismatch",
		Status: CosmeticSubscriptionStatusActive, ProviderCreatedAt: base.Add(time.Second),
		ProviderStateObservedAt: recoveredObserved,
	})
	if err != nil || !recovered.Applied || !recovered.Subscription.HasAccess || !recovered.Subscription.CanManage {
		t.Fatalf("authoritative billing recovery = (%+v, %v)", recovered, err)
	}
	var activeLicenses, replacedLicenses, preservedTerminalLicenses int
	if err := Pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE current.status = 'active' AND sl.generation = 2),
			COUNT(*) FILTER (WHERE sl.license_id <> initial.license_id),
			COUNT(*) FILTER (WHERE original.status = 'revoked')
		FROM cosmetic_subscription_licenses AS sl
		JOIN cosmetic_licenses AS current ON current.id = sl.license_id
		JOIN (SELECT * FROM UNNEST($2::TEXT[], $3::TEXT[]) AS seeded(item_id, license_id)) AS initial
		  ON initial.item_id = sl.item_id
		JOIN cosmetic_licenses AS original ON original.id = initial.license_id
		WHERE sl.subscription_id = $1`, subscription.ID, initialItemIDs, initialLicenseIDs).Scan(
		&activeLicenses, &replacedLicenses, &preservedTerminalLicenses,
	); err != nil {
		t.Fatalf("count recovered licenses: %v", err)
	}
	if activeLicenses != launchSubscriptionCosmeticCount || replacedLicenses != launchSubscriptionCosmeticCount || preservedTerminalLicenses != launchSubscriptionCosmeticCount {
		t.Fatalf("recovered generations = active %d replaced %d terminal %d, want %d each", activeLicenses, replacedLicenses, preservedTerminalLicenses, launchSubscriptionCosmeticCount)
	}

	// A slower request whose observation began earlier cannot reverse the newer
	// state even when both signed events share the same second.
	stale, err := ProcessCosmeticSubscriptionEvent(ctx, CosmeticSubscriptionEventInput{
		Provider: "stripe", EventID: "evt_billing_same_second_stale", EventType: CosmeticStripeSubscriptionUpdated,
		PayloadHash: subscriptionEventHash("4"), SubscriptionID: subscription.ID, AccountID: account.ID,
		ProviderSubscriptionID: "sub_billing_mismatch", CustomerID: "cus_billing_mismatch",
		Status: CosmeticSubscriptionStatusPastDue, ProviderCreatedAt: base.Add(time.Second),
		ProviderStateObservedAt: secondObserved,
	})
	if err != nil || stale.Applied || !stale.Subscription.HasAccess || stale.Subscription.Status != CosmeticSubscriptionStatusActive {
		t.Fatalf("stale same-second observation = (%+v, %v)", stale, err)
	}
}

func TestPostgresCosmeticSubscriptionCheckoutCanResumeAndReplaceOnlyAfterExpiry(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "resume-subscription@example.com", "https://id.example", "resume-subscription", "Resume Subscription")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	first, err := CreateCosmeticSubscription(ctx, account.ID)
	if err != nil {
		t.Fatalf("create first subscription: %v", err)
	}
	first, err = AttachCosmeticSubscriptionCheckout(ctx, account.ID, first.ID, "cs_resume_pending")
	if err != nil {
		t.Fatalf("attach pending checkout: %v", err)
	}
	resumed, err := CreateCosmeticSubscription(ctx, account.ID)
	if err != nil || resumed.ID != first.ID || resumed.Status != CosmeticSubscriptionStatusCheckoutPending {
		t.Fatalf("resume pending subscription = (%+v, %v), want %s", resumed, err, first.ID)
	}

	const concurrent = 8
	results := make(chan string, concurrent)
	errorsSeen := make(chan error, concurrent)
	var wait sync.WaitGroup
	for range concurrent {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, createErr := CreateCosmeticSubscription(context.Background(), account.ID)
			if createErr != nil {
				errorsSeen <- createErr
				return
			}
			results <- got.ID
		}()
	}
	wait.Wait()
	close(results)
	close(errorsSeen)
	for createErr := range errorsSeen {
		t.Errorf("concurrent resume error: %v", createErr)
	}
	for id := range results {
		if id != first.ID {
			t.Errorf("concurrent resume ID = %s, want %s", id, first.ID)
		}
	}

	expired, err := ExpireCosmeticSubscriptionCheckout(ctx, account.ID, first.ID, first.CheckoutSessionID)
	if err != nil || expired.Status != CosmeticSubscriptionStatusExpired || !expired.Terminal {
		t.Fatalf("expire provider-confirmed checkout = (%+v, %v)", expired, err)
	}
	replayed, err := ExpireCosmeticSubscriptionCheckout(ctx, account.ID, first.ID, first.CheckoutSessionID)
	if err != nil || replayed.ID != first.ID || replayed.Status != CosmeticSubscriptionStatusExpired {
		t.Fatalf("idempotent checkout expiry = (%+v, %v)", replayed, err)
	}
	replacement, err := CreateCosmeticSubscription(ctx, account.ID)
	if err != nil || replacement.ID == first.ID || replacement.Status != CosmeticSubscriptionStatusCreated {
		t.Fatalf("replacement subscription = (%+v, %v)", replacement, err)
	}
}

func TestPostgresCosmeticSubscriptionRejectsOnlyLegacyAPIKeyOverflow(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	atLimit, err := UpsertVerifiedCustomerAccount(ctx, "subscription-five@example.com", "https://id.example", "subscription-five", "Five Keys")
	if err != nil {
		t.Fatalf("create five-key account: %v", err)
	}
	seedLegacyOwnedAPIKeys(t, ctx, atLimit.ID, "five", CosmeticSubscriptionMaxAPIKeys)
	if _, err := CreateCosmeticSubscription(ctx, atLimit.ID); err != nil {
		t.Fatalf("exactly five active keys should be allowed to subscribe: %v", err)
	}

	overLimit, err := UpsertVerifiedCustomerAccount(ctx, "subscription-six@example.com", "https://id.example", "subscription-six", "Six Keys")
	if err != nil {
		t.Fatalf("create six-key account: %v", err)
	}
	seedLegacyOwnedAPIKeys(t, ctx, overLimit.ID, "six", CosmeticSubscriptionMaxAPIKeys+1)
	if _, err := CreateCosmeticSubscription(ctx, overLimit.ID); !errors.Is(err, ErrCustomerAPIKeyLimit) {
		t.Fatalf("six active keys subscription error = %v, want %v", err, ErrCustomerAPIKeyLimit)
	}
	var subscriptions int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM cosmetic_subscriptions WHERE account_id = $1`, overLimit.ID).Scan(&subscriptions); err != nil {
		t.Fatalf("count rejected subscriptions: %v", err)
	}
	if subscriptions != 0 {
		t.Fatalf("rejected overflow account has %d subscription rows, want none", subscriptions)
	}
}

func TestCosmeticSubscriptionDatabaseFunctionsRequirePool(t *testing.T) {
	original := Pool
	Pool = nil
	t.Cleanup(func() { Pool = original })
	ctx := context.Background()
	if _, err := CreateCosmeticSubscription(ctx, "account"); err != ErrNoDatabase {
		t.Fatalf("CreateCosmeticSubscription nil pool error = %v", err)
	}
	if _, err := SyncCustomerCosmeticSubscriptionLicenses(ctx, "account"); err != ErrNoDatabase {
		t.Fatalf("SyncCustomerCosmeticSubscriptionLicenses nil pool error = %v", err)
	}
}
