package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func createCommerceTestAccount(t *testing.T, ctx context.Context, suffix string) *CustomerAccount {
	t.Helper()
	email := fmt.Sprintf("commerce-%s@example.com", suffix)
	account, err := UpsertVerifiedCustomerAccount(ctx, email, "https://commerce-id.example", "subject-"+suffix, "Commerce "+suffix)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount(%s): %v", suffix, err)
	}
	return account
}

func commerceEventHash(character string) string {
	return strings.Repeat(character, 64)
}

func paidCommerceEvent(order *CosmeticOrder, eventID, hash string, amount int64) CosmeticPaymentEventInput {
	return CosmeticPaymentEventInput{
		Provider: "stripe", EventID: eventID, EventType: CosmeticStripeCheckoutCompleted, PayloadHash: hash,
		OrderID: order.ID, AccountID: order.AccountID, CheckoutSessionID: order.CheckoutSessionID,
		PaymentIntentID: "pi_" + order.ID, Currency: order.Currency, Paid: true, AmountReceivedCents: amount,
	}
}

func createAttachedCommerceOrder(t *testing.T, ctx context.Context, account *CustomerAccount, quantity int, suffix string) *CosmeticOrder {
	t.Helper()
	order, err := CreateCosmeticOrder(ctx, account.ID, "neon-signal-pack", quantity)
	if err != nil {
		t.Fatalf("CreateCosmeticOrder(%s): %v", suffix, err)
	}
	order, err = AttachCosmeticOrderCheckout(ctx, account.ID, order.ID, "cs_"+suffix)
	if err != nil {
		t.Fatalf("AttachCosmeticOrderCheckout(%s): %v", suffix, err)
	}
	return order
}

func fulfillCommerceOrder(t *testing.T, ctx context.Context, order *CosmeticOrder, amount int64, suffix string) *CosmeticOrder {
	t.Helper()
	result, err := ProcessCosmeticPaymentEvent(ctx, paidCommerceEvent(order, "evt_paid_"+suffix, commerceEventHash("a"), amount))
	if err != nil {
		t.Fatalf("ProcessCosmeticPaymentEvent paid(%s): %v", suffix, err)
	}
	if !result.Applied || result.Duplicate || result.LicensesCreated != len(order.Items)*order.Quantity {
		t.Fatalf("paid result(%s) = %+v", suffix, result)
	}
	return result.Order
}

func countOrderLicenses(t *testing.T, ctx context.Context, orderID string) int {
	t.Helper()
	var count int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM cosmetic_order_licenses WHERE order_id = $1`, orderID).Scan(&count); err != nil {
		t.Fatalf("count order licenses: %v", err)
	}
	return count
}

func TestPostgresCosmeticOrderSchemaUpgradeAcceptsTrailSnapshots(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		ALTER TABLE cosmetic_order_items DROP CONSTRAINT cosmetic_order_items_slot_check;
		ALTER TABLE cosmetic_order_items ADD CONSTRAINT cosmetic_order_items_slot_check
			CHECK (slot IN ('bot_skin','weapon_skin','attachment'))`); err != nil {
		t.Fatalf("restore legacy order-item slot constraint: %v", err)
	}
	if err := EnsureCosmeticOrdersSchema(ctx); err != nil {
		t.Fatalf("upgrade legacy order-item slot constraint: %v", err)
	}

	account := createCommerceTestAccount(t, ctx, "trail-schema-upgrade")
	order, err := CreateCosmeticOrder(ctx, account.ID, "trail-ember-sparks-pack", 1)
	if err != nil {
		t.Fatalf("CreateCosmeticOrder after trail schema upgrade: %v", err)
	}
	if len(order.Items) != 1 || order.Items[0].Slot != CosmeticSlotTrail || order.UnitPriceCents != CosmeticTrailPriceCents {
		t.Fatalf("trail order after schema upgrade = %+v", order)
	}
}

func TestPostgresCosmeticOrderSnapshotsServerPriceAndFulfillsQuantityExactlyOnce(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	if err := EnsureCosmeticOrdersSchema(ctx); err != nil {
		t.Fatalf("EnsureCosmeticOrdersSchema second run: %v", err)
	}

	account := createCommerceTestAccount(t, ctx, "snapshot")
	other := createCommerceTestAccount(t, ctx, "snapshot-other")
	order, err := CreateCosmeticOrder(ctx, account.ID, "neon-signal-pack", 2)
	if err != nil {
		t.Fatalf("CreateCosmeticOrder: %v", err)
	}
	if order.AccountEmail != account.Email || order.PackDescription == "" || order.UnitPriceCents != CosmeticPackPriceCents ||
		order.ExpectedSubtotalCents != 398 || order.Quantity != 2 || len(order.Items) != 3 {
		t.Fatalf("server order snapshot = %+v", order)
	}
	for position, item := range order.Items {
		if item.Position != position || item.ID == "" || item.Name == "" || item.AssetKey == "" || !IsValidCosmeticSlot(item.Slot) {
			t.Fatalf("invalid order item snapshot at %d: %+v", position, item)
		}
	}

	pack := cosmeticPackByID(DefaultCosmeticCatalogData().Packs, "neon-signal-pack")
	pack.Description = "Changed after the order snapshot."
	if _, err := UpsertCosmeticPack(ctx, *pack, "price-editor"); err != nil {
		t.Fatalf("change live pack metadata after snapshot: %v", err)
	}
	order, err = AttachCosmeticOrderCheckout(ctx, account.ID, order.ID, "cs_snapshot")
	if err != nil {
		t.Fatalf("AttachCosmeticOrderCheckout: %v", err)
	}
	attachedAgain, err := AttachCosmeticOrderCheckout(ctx, account.ID, order.ID, "cs_snapshot")
	if err != nil || attachedAgain.CheckoutSessionID != order.CheckoutSessionID {
		t.Fatalf("idempotent checkout attach = (%+v, %v)", attachedAgain, err)
	}
	ownedOrder, err := GetCustomerCosmeticOrder(ctx, account.ID, order.ID)
	if err != nil || ownedOrder.ID != order.ID || ownedOrder.CheckoutSessionID != "cs_snapshot" {
		t.Fatalf("account-owned order lookup = (%+v, %v)", ownedOrder, err)
	}
	if _, err := GetCustomerCosmeticOrder(ctx, other.ID, order.ID); !errors.Is(err, ErrCosmeticOrderNotFound) {
		t.Fatalf("cross-account order lookup error = %v, want not found", err)
	}

	paidInput := paidCommerceEvent(order, "evt_snapshot_paid", commerceEventHash("b"), 418)
	result, err := ProcessCosmeticPaymentEvent(ctx, paidInput)
	if err != nil {
		t.Fatalf("ProcessCosmeticPaymentEvent: %v", err)
	}
	if !result.Applied || result.Duplicate || result.LicensesCreated != 6 || result.Order.Status != CosmeticOrderStatusPaid ||
		result.Order.AmountReceivedCents != 418 || result.Order.ExpectedSubtotalCents != 398 {
		t.Fatalf("paid result = %+v", result)
	}
	if got := countOrderLicenses(t, ctx, order.ID); got != 6 {
		t.Fatalf("order licenses = %d, want 6", got)
	}
	var licenses, references int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT l.external_reference)
		FROM cosmetic_order_licenses ol
		JOIN cosmetic_licenses l ON l.id = ol.license_id
		WHERE ol.order_id = $1 AND l.source = 'stripe'`, order.ID).Scan(&licenses, &references); err != nil {
		t.Fatalf("inspect fulfilled licenses: %v", err)
	}
	if licenses != 6 || references != 6 {
		t.Fatalf("fulfilled license/reference counts = %d/%d, want 6/6", licenses, references)
	}

	duplicate, err := ProcessCosmeticPaymentEvent(ctx, paidInput)
	if err != nil || !duplicate.Duplicate || duplicate.Applied || duplicate.LicensesCreated != 0 {
		t.Fatalf("duplicate paid event = (%+v, %v)", duplicate, err)
	}
	if got := countOrderLicenses(t, ctx, order.ID); got != 6 {
		t.Fatalf("licenses after duplicate = %d, want 6", got)
	}

	intentMismatch := paidInput
	intentMismatch.EventID = "evt_snapshot_wrong_intent"
	intentMismatch.PayloadHash = commerceEventHash("c")
	intentMismatch.PaymentIntentID = "pi_wrong"
	if _, err := ProcessCosmeticPaymentEvent(ctx, intentMismatch); !errors.Is(err, ErrCosmeticOrderMismatch) {
		t.Fatalf("payment intent mismatch error = %v", err)
	}

	customerOrders, err := ListCustomerCosmeticOrders(ctx, account.ID, 20)
	if err != nil || len(customerOrders) != 1 || customerOrders[0].UnitPriceCents != CosmeticPackPriceCents || customerOrders[0].FulfilledLicenseCount != 6 {
		t.Fatalf("customer orders = (%+v, %v)", customerOrders, err)
	}
	isolated, err := ListCustomerCosmeticOrders(ctx, other.ID, 20)
	if err != nil || len(isolated) != 0 {
		t.Fatalf("other customer orders = (%+v, %v), want none", isolated, err)
	}
	adminOrders, err := ListAdminCosmeticOrders(ctx, account.Email, CosmeticOrderStatusPaid, 20)
	if err != nil || len(adminOrders) != 1 || adminOrders[0].ID != order.ID {
		t.Fatalf("admin order search = (%+v, %v)", adminOrders, err)
	}

	conflictingPayload := paidInput
	conflictingPayload.PayloadHash = commerceEventHash("d")
	if _, err := ProcessCosmeticPaymentEvent(ctx, conflictingPayload); !errors.Is(err, ErrCosmeticPaymentEventConflict) {
		t.Fatalf("same event id with another payload error = %v", err)
	}
}

func TestPostgresCosmeticCheckoutReservationPinsPresentationAcrossAmbiguousRetries(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createCommerceTestAccount(t, ctx, "checkout-reservation")

	first, created, err := ReserveCosmeticOrderCheckout(ctx, account.ID, "neon-signal-pack", 1, CosmeticCheckoutPresentationEmbedded)
	if err != nil || !created || first.CheckoutPresentation != CosmeticCheckoutPresentationEmbedded {
		t.Fatalf("first reservation = (%+v, %v, %v)", first, created, err)
	}
	if _, err := MarkCosmeticOrderCheckoutFailed(ctx, account.ID, first.ID, "ambiguous provider timeout"); err != nil {
		t.Fatalf("mark ambiguous checkout failure: %v", err)
	}
	retried, created, err := ReserveCosmeticOrderCheckout(ctx, account.ID, "neon-signal-pack", 1, CosmeticCheckoutPresentationHosted)
	if err != nil || created || retried.ID != first.ID || retried.CheckoutPresentation != CosmeticCheckoutPresentationEmbedded ||
		retried.Status != CosmeticOrderStatusPaymentFailed {
		t.Fatalf("ambiguous retry reservation = (%+v, %v, %v)", retried, created, err)
	}
	attached, err := AttachCosmeticOrderCheckout(ctx, account.ID, first.ID, "cs_reserved_retry")
	if err != nil {
		t.Fatalf("attach replayed checkout: %v", err)
	}
	resumed, created, err := ReserveCosmeticOrderCheckout(ctx, account.ID, "neon-signal-pack", 1, CosmeticCheckoutPresentationHosted)
	if err != nil || created || resumed.ID != first.ID || resumed.CheckoutSessionID != attached.CheckoutSessionID ||
		resumed.CheckoutPresentation != CosmeticCheckoutPresentationEmbedded {
		t.Fatalf("attached reservation replay = (%+v, %v, %v)", resumed, created, err)
	}
}

func TestPostgresTrailOrderChargesNinetyNineCentsAndFulfillsOneMovableCopy(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createCommerceTestAccount(t, ctx, "trail-order")
	order, err := CreateCosmeticOrder(ctx, account.ID, "trail-ember-sparks-pack", 1)
	if err != nil {
		t.Fatalf("CreateCosmeticOrder trail: %v", err)
	}
	if order.UnitPriceCents != CosmeticTrailPriceCents || order.ExpectedSubtotalCents != CosmeticTrailPriceCents ||
		order.Quantity != 1 || len(order.Items) != 1 || order.Items[0].ID != "trail-ember-sparks" ||
		order.Items[0].Slot != CosmeticSlotTrail || order.Items[0].AssetKey != "ember_sparks" {
		t.Fatalf("trail order snapshot = %+v", order)
	}
	order, err = AttachCosmeticOrderCheckout(ctx, account.ID, order.ID, "cs_trail_order")
	if err != nil {
		t.Fatalf("AttachCosmeticOrderCheckout trail: %v", err)
	}
	paid := fulfillCommerceOrder(t, ctx, order, CosmeticTrailPriceCents, "trail-order")
	if paid.Status != CosmeticOrderStatusPaid || paid.AmountReceivedCents != CosmeticTrailPriceCents ||
		paid.ExpectedSubtotalCents != CosmeticTrailPriceCents || paid.FulfilledLicenseCount != 1 {
		t.Fatalf("paid trail order = %+v", paid)
	}

	var licenseID string
	if err := Pool.QueryRow(ctx, `
		SELECT l.id
		FROM cosmetic_order_licenses ol
		JOIN cosmetic_licenses l ON l.id = ol.license_id
		WHERE ol.order_id = $1 AND l.cosmetic_id = 'trail-ember-sparks' AND l.status = 'active'`, order.ID).
		Scan(&licenseID); err != nil {
		t.Fatalf("load fulfilled trail license: %v", err)
	}
	firstBot := createCustomerCosmeticsTestBot(t, ctx, "trail-order-first")
	secondBot := createCustomerCosmeticsTestBot(t, ctx, "trail-order-second")
	for _, botID := range []string{firstBot.ID, secondBot.ID} {
		if _, err := LinkBotToCustomerAccount(ctx, account.ID, botID); err != nil {
			t.Fatalf("link trail bot %s: %v", botID, err)
		}
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, licenseID, &firstBot.ID); err != nil {
		t.Fatalf("assign trail copy to first bot: %v", err)
	}
	if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, firstBot.ID, licenseID); err != nil {
		t.Fatalf("equip trail copy on first bot: %v", err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, licenseID, &secondBot.ID); err != nil {
		t.Fatalf("move exact trail copy to second bot: %v", err)
	}
	firstLoadout, err := GetEquippedCosmetics(ctx, firstBot.ID)
	if err != nil || firstLoadout[CosmeticSlotTrail] != "standard" {
		t.Fatalf("first bot trail after move = (%q, %v), want standard", firstLoadout[CosmeticSlotTrail], err)
	}
	if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, secondBot.ID, licenseID); err != nil {
		t.Fatalf("equip moved trail copy on second bot: %v", err)
	}
	secondLoadout, err := GetEquippedCosmetics(ctx, secondBot.ID)
	if err != nil || secondLoadout[CosmeticSlotTrail] != "ember_sparks" {
		t.Fatalf("second bot trail after move = (%q, %v), want ember_sparks", secondLoadout[CosmeticSlotTrail], err)
	}
	var assignments int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM cosmetic_license_assignments WHERE license_id = $1`, licenseID).Scan(&assignments); err != nil || assignments != 1 {
		t.Fatalf("trail exact-copy assignments = (%d, %v), want one", assignments, err)
	}
}

func TestPostgresCosmeticPaidEventRejectsMismatchedPurchaseClaims(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createCommerceTestAccount(t, ctx, "mismatch")
	other := createCommerceTestAccount(t, ctx, "mismatch-other")
	order := createAttachedCommerceOrder(t, ctx, account, 1, "mismatch")
	base := paidCommerceEvent(order, "evt_mismatch", commerceEventHash("1"), order.ExpectedSubtotalCents)

	tests := []struct {
		name   string
		mutate func(*CosmeticPaymentEventInput)
	}{
		{name: "amount", mutate: func(input *CosmeticPaymentEventInput) { input.AmountReceivedCents-- }},
		{name: "currency", mutate: func(input *CosmeticPaymentEventInput) { input.Currency = "EUR" }},
		{name: "account", mutate: func(input *CosmeticPaymentEventInput) { input.AccountID = other.ID }},
		{name: "session", mutate: func(input *CosmeticPaymentEventInput) { input.CheckoutSessionID = "cs_wrong" }},
		{name: "not-paid", mutate: func(input *CosmeticPaymentEventInput) {
			input.EventType = CosmeticStripeCheckoutAsyncPaymentSucceeded
			input.Paid = false
		}},
	}
	for index, test := range tests {
		input := base
		input.EventID = fmt.Sprintf("evt_mismatch_%d", index)
		input.PayloadHash = strings.Repeat(fmt.Sprintf("%x", index+2), 64)
		test.mutate(&input)
		if _, err := ProcessCosmeticPaymentEvent(ctx, input); !errors.Is(err, ErrCosmeticOrderMismatch) {
			t.Errorf("%s mismatch error = %v, want ErrCosmeticOrderMismatch", test.name, err)
		}
	}
	if got := countOrderLicenses(t, ctx, order.ID); got != 0 {
		t.Fatalf("mismatched events granted %d licenses", got)
	}
}

func TestPostgresCosmeticPaidEventConcurrentDuplicateCreatesOneLicenseSet(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createCommerceTestAccount(t, ctx, "concurrent")
	order := createAttachedCommerceOrder(t, ctx, account, 1, "concurrent")
	input := paidCommerceEvent(order, "evt_concurrent", commerceEventHash("e"), order.ExpectedSubtotalCents)

	type outcome struct {
		result *CosmeticPaymentEventResult
		err    error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			result, err := ProcessCosmeticPaymentEvent(context.Background(), input)
			results <- outcome{result: result, err: err}
		}()
	}
	close(start)
	applied, duplicate := 0, 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent paid event: %v", result.err)
		}
		if result.result.Applied {
			applied++
		}
		if result.result.Duplicate {
			duplicate++
		}
	}
	if applied != 1 || duplicate != 1 || countOrderLicenses(t, ctx, order.ID) != 3 {
		t.Fatalf("concurrent results applied=%d duplicate=%d licenses=%d", applied, duplicate, countOrderLicenses(t, ctx, order.ID))
	}
}

func TestPostgresCosmeticRefundLifecycleRevokesOnlyMappedCopies(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createCommerceTestAccount(t, ctx, "refund")
	order := createAttachedCommerceOrder(t, ctx, account, 2, "refund")
	order = fulfillCommerceOrder(t, ctx, order, 418, "refund")

	var mappedLicenseID, mappedItemID string
	if err := Pool.QueryRow(ctx, `
		SELECT ol.license_id, ol.item_id FROM cosmetic_order_licenses ol
		WHERE ol.order_id = $1 ORDER BY ol.copy_index, ol.item_position LIMIT 1`, order.ID).
		Scan(&mappedLicenseID, &mappedItemID); err != nil {
		t.Fatalf("load mapped license: %v", err)
	}
	unrelated, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, mappedItemID, "manual", "refund-unrelated")
	if err != nil || !created {
		t.Fatalf("grant unrelated license = (%+v, %v, %v)", unrelated, created, err)
	}
	bot := createCustomerCosmeticsTestBot(t, ctx, "refund")
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("link refund bot: %v", err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, mappedLicenseID, &bot.ID); err != nil {
		t.Fatalf("assign mapped license: %v", err)
	}
	if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, bot.ID, mappedLicenseID); err != nil {
		t.Fatalf("equip mapped license: %v", err)
	}

	partial := CosmeticPaymentEventInput{
		Provider: "stripe", EventID: "evt_refund_partial", EventType: CosmeticStripeRefundCreated, PayloadHash: commerceEventHash("f"),
		PaymentIntentID: order.PaymentIntentID, Currency: "USD",
		RefundID: "re_partial", RefundStatus: CosmeticRefundStatusSucceeded, RefundAmountCents: 50,
	}
	result, err := ProcessCosmeticPaymentEvent(ctx, partial)
	if err != nil || result.Order.Status != CosmeticOrderStatusRefundReview || result.Order.AmountRefundedCents != 50 {
		t.Fatalf("partial refund = (%+v, %v)", result, err)
	}
	var activeMapped int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM cosmetic_order_licenses ol JOIN cosmetic_licenses l ON l.id = ol.license_id
		WHERE ol.order_id = $1 AND l.status = 'active'`, order.ID).Scan(&activeMapped); err != nil || activeMapped != 6 {
		t.Fatalf("active mapped after partial = (%d, %v), want 6", activeMapped, err)
	}

	failed := partial
	failed.EventID = "evt_refund_failed"
	failed.EventType = CosmeticStripeRefundFailed
	failed.PayloadHash = commerceEventHash("0")
	failed.RefundID = "re_failed"
	failed.RefundStatus = CosmeticRefundStatusFailed
	failed.RefundAmountCents = 100
	result, err = ProcessCosmeticPaymentEvent(ctx, failed)
	if err != nil || result.Order.AmountRefundedCents != 50 || result.Order.Status != CosmeticOrderStatusRefundReview {
		t.Fatalf("failed refund recompute = (%+v, %v)", result, err)
	}
	wrongMetadata := partial
	wrongMetadata.EventID = "evt_refund_wrong_metadata"
	wrongMetadata.PayloadHash = commerceEventHash("e")
	wrongMetadata.OrderID = "not-the-paid-order"
	wrongMetadata.RefundID = "re_wrong_metadata"
	if _, err := ProcessCosmeticPaymentEvent(ctx, wrongMetadata); !errors.Is(err, ErrCosmeticOrderMismatch) {
		t.Fatalf("wrong refund metadata error = %v", err)
	}

	full := partial
	full.EventID = "evt_refund_full"
	full.EventType = CosmeticStripeRefundUpdated
	full.PayloadHash = commerceEventHash("9")
	full.RefundID = "re_full"
	full.RefundAmountCents = 368
	result, err = ProcessCosmeticPaymentEvent(ctx, full)
	if err != nil || result.Order.Status != CosmeticOrderStatusRefunded || result.Order.AmountRefundedCents != 418 {
		t.Fatalf("full refund = (%+v, %v)", result, err)
	}
	var refundEventTypes int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT event_type) FROM cosmetic_payment_events
		WHERE order_id = $1 AND event_type IN ('refund.created','refund.updated','refund.failed')`, order.ID).
		Scan(&refundEventTypes); err != nil || refundEventTypes != 3 {
		t.Fatalf("persisted refund event types = (%d, %v), want 3", refundEventTypes, err)
	}
	var refundedMapped, assignments, loadouts int
	if err := Pool.QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM cosmetic_order_licenses ol JOIN cosmetic_licenses l ON l.id = ol.license_id WHERE ol.order_id = $1 AND l.status = 'refunded'),
		  (SELECT COUNT(*) FROM cosmetic_license_assignments a JOIN cosmetic_order_licenses ol ON ol.license_id = a.license_id WHERE ol.order_id = $1),
		  (SELECT COUNT(*) FROM bot_cosmetic_loadout l JOIN cosmetic_order_licenses ol ON ol.license_id = l.license_id WHERE ol.order_id = $1)`, order.ID).
		Scan(&refundedMapped, &assignments, &loadouts); err != nil {
		t.Fatalf("inspect full refund cleanup: %v", err)
	}
	if refundedMapped != 6 || assignments != 0 || loadouts != 0 {
		t.Fatalf("full refund cleanup refunded=%d assignments=%d loadouts=%d", refundedMapped, assignments, loadouts)
	}
	unrelatedAfter, err := getCosmeticLicense(ctx, unrelated.ID)
	if err != nil || unrelatedAfter.Status != "active" {
		t.Fatalf("unrelated license after refund = (%+v, %v)", unrelatedAfter, err)
	}

	duplicate, err := ProcessCosmeticPaymentEvent(ctx, full)
	if err != nil || !duplicate.Duplicate || duplicate.Applied {
		t.Fatalf("duplicate full refund = (%+v, %v)", duplicate, err)
	}
	delayedPaid := paidCommerceEvent(order, "evt_paid_after_refund", commerceEventHash("8"), 418)
	if _, err := ProcessCosmeticPaymentEvent(ctx, delayedPaid); !errors.Is(err, ErrCosmeticOrderTerminal) {
		t.Fatalf("paid after refund error = %v, want terminal", err)
	}
	if countOrderLicenses(t, ctx, order.ID) != 6 {
		t.Fatal("paid-after-refund created additional licenses")
	}
}

func TestPostgresCosmeticChargeRefundUsesCumulativeAmountWithoutDoubleCounting(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createCommerceTestAccount(t, ctx, "charge-refund")
	order := fulfillCommerceOrder(t, ctx, createAttachedCommerceOrder(t, ctx, account, 2, "charge-refund"), 398, "charge-refund")

	individual := CosmeticPaymentEventInput{
		Provider: "stripe", EventID: "evt_individual_refund", EventType: CosmeticStripeRefundUpdated, PayloadHash: commerceEventHash("7"),
		PaymentIntentID: order.PaymentIntentID, Currency: "USD",
		RefundID: "re_individual", RefundStatus: CosmeticRefundStatusSucceeded, RefundAmountCents: 50,
	}
	if _, err := ProcessCosmeticPaymentEvent(ctx, individual); err != nil {
		t.Fatalf("individual refund: %v", err)
	}
	charge := CosmeticPaymentEventInput{
		Provider: "stripe", EventID: "evt_charge_refund_50", EventType: CosmeticStripeChargeRefunded, PayloadHash: commerceEventHash("6"),
		PaymentIntentID: order.PaymentIntentID, Currency: "USD", CumulativeRefundedCents: 50,
	}
	result, err := ProcessCosmeticPaymentEvent(ctx, charge)
	if err != nil || result.Order.AmountRefundedCents != 50 {
		t.Fatalf("overlapping cumulative refund = (%+v, %v), want 50", result, err)
	}
	charge.EventID = "evt_charge_refund_full"
	charge.PayloadHash = commerceEventHash("5")
	charge.CumulativeRefundedCents = 398
	result, err = ProcessCosmeticPaymentEvent(ctx, charge)
	if err != nil || result.Order.AmountRefundedCents != 398 || result.Order.Status != CosmeticOrderStatusRefunded {
		t.Fatalf("cumulative full refund = (%+v, %v)", result, err)
	}
}

func TestPostgresCosmeticRefundTerminalStateDoesNotRegressOnOutOfOrderEvent(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createCommerceTestAccount(t, ctx, "refund-out-of-order")
	order := fulfillCommerceOrder(t, ctx, createAttachedCommerceOrder(t, ctx, account, 1, "refund-out-of-order"), CosmeticPackPriceCents, "refund-out-of-order")

	succeeded := CosmeticPaymentEventInput{
		Provider: "stripe", EventID: "evt_refund_succeeded_first", EventType: CosmeticStripeRefundUpdated, PayloadHash: commerceEventHash("2"),
		PaymentIntentID: order.PaymentIntentID, Currency: "USD",
		RefundID: "re_out_of_order", RefundStatus: CosmeticRefundStatusSucceeded, RefundAmountCents: 50,
	}
	result, err := ProcessCosmeticPaymentEvent(ctx, succeeded)
	if err != nil || result.Order.Status != CosmeticOrderStatusRefundReview || result.Order.AmountRefundedCents != 50 {
		t.Fatalf("succeeded refund = (%+v, %v)", result, err)
	}

	latePending := succeeded
	latePending.EventID = "evt_refund_pending_late"
	latePending.EventType = CosmeticStripeRefundCreated
	latePending.PayloadHash = commerceEventHash("3")
	latePending.RefundStatus = CosmeticRefundStatusPending
	result, err = ProcessCosmeticPaymentEvent(ctx, latePending)
	if err != nil || result.Order.Status != CosmeticOrderStatusRefundReview || result.Order.AmountRefundedCents != 50 {
		t.Fatalf("late pending refund = (%+v, %v)", result, err)
	}

	var storedStatus string
	if err := Pool.QueryRow(ctx, `
		SELECT status FROM cosmetic_order_refunds
		WHERE provider = $1 AND refund_id = $2`, "stripe", succeeded.RefundID).Scan(&storedStatus); err != nil {
		t.Fatalf("load refund state: %v", err)
	}
	if storedStatus != CosmeticRefundStatusSucceeded {
		t.Fatalf("stored refund status = %q, want succeeded", storedStatus)
	}
	if got := countOrderLicenses(t, ctx, order.ID); got != 3 {
		t.Fatalf("out-of-order partial refund changed licenses = %d, want 3", got)
	}
}

func TestPostgresCosmeticDisputeIsTerminalAndRevokesMappedLicenses(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createCommerceTestAccount(t, ctx, "dispute")
	order := fulfillCommerceOrder(t, ctx, createAttachedCommerceOrder(t, ctx, account, 1, "dispute"), CosmeticPackPriceCents, "dispute")
	dispute := CosmeticPaymentEventInput{
		Provider: "stripe", EventID: "evt_dispute", EventType: CosmeticStripeDisputeCreated, PayloadHash: commerceEventHash("4"),
		PaymentIntentID: order.PaymentIntentID, Currency: "USD",
	}
	result, err := ProcessCosmeticPaymentEvent(ctx, dispute)
	if err != nil || result.Order.Status != CosmeticOrderStatusDisputed {
		t.Fatalf("dispute result = (%+v, %v)", result, err)
	}
	var chargebacks int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM cosmetic_order_licenses ol JOIN cosmetic_licenses l ON l.id = ol.license_id
		WHERE ol.order_id = $1 AND l.status = 'chargeback'`, order.ID).Scan(&chargebacks); err != nil || chargebacks != 3 {
		t.Fatalf("chargeback licenses = (%d, %v), want 3", chargebacks, err)
	}
	delayedPaid := paidCommerceEvent(order, "evt_paid_after_dispute", commerceEventHash("3"), CosmeticPackPriceCents)
	if _, err := ProcessCosmeticPaymentEvent(ctx, delayedPaid); !errors.Is(err, ErrCosmeticOrderTerminal) {
		t.Fatalf("paid after dispute error = %v", err)
	}
}

func TestPostgresCosmeticCheckoutFailureAndExpiryDoNotDowngradePaidOrders(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createCommerceTestAccount(t, ctx, "failure")
	delayedOrder := createAttachedCommerceOrder(t, ctx, account, 1, "delayed")
	delayed := paidCommerceEvent(delayedOrder, "evt_delayed_completion", commerceEventHash("d"), 0)
	delayed.Paid = false
	delayed.PaymentIntentID = ""
	result, err := ProcessCosmeticPaymentEvent(ctx, delayed)
	if err != nil || !result.Applied || result.Order.Status != CosmeticOrderStatusProcessing || result.LicensesCreated != 0 {
		t.Fatalf("delayed-method completion = (%+v, %v)", result, err)
	}
	if got := countOrderLicenses(t, ctx, delayedOrder.ID); got != 0 {
		t.Fatalf("unpaid delayed completion granted %d licenses", got)
	}
	delayedSuccess := paidCommerceEvent(result.Order, "evt_delayed_success", commerceEventHash("c"), delayedOrder.ExpectedSubtotalCents)
	delayedSuccess.EventType = CosmeticStripeCheckoutAsyncPaymentSucceeded
	result, err = ProcessCosmeticPaymentEvent(ctx, delayedSuccess)
	if err != nil || result.Order.Status != CosmeticOrderStatusPaid || result.LicensesCreated != 3 {
		t.Fatalf("delayed-method async success = (%+v, %v)", result, err)
	}

	failedOrder := createAttachedCommerceOrder(t, ctx, account, 1, "failure")
	failedOrder, err = MarkCosmeticOrderCheckoutFailed(ctx, account.ID, failedOrder.ID, "checkout creation failed")
	if err != nil || failedOrder.Status != CosmeticOrderStatusPaymentFailed || failedOrder.LastError == "" {
		t.Fatalf("mark checkout failure = (%+v, %v)", failedOrder, err)
	}

	expiredOrder := createAttachedCommerceOrder(t, ctx, account, 1, "expired")
	expired := CosmeticPaymentEventInput{
		Provider: "stripe", EventID: "evt_expired", EventType: CosmeticStripeCheckoutExpired, PayloadHash: commerceEventHash("2"),
		OrderID: expiredOrder.ID, AccountID: account.ID, CheckoutSessionID: expiredOrder.CheckoutSessionID, Currency: "USD",
	}
	result, err = ProcessCosmeticPaymentEvent(ctx, expired)
	if err != nil || result.Order.Status != CosmeticOrderStatusExpired {
		t.Fatalf("expired order = (%+v, %v)", result, err)
	}

	paidOrder := fulfillCommerceOrder(t, ctx, createAttachedCommerceOrder(t, ctx, account, 1, "paid-stable"), CosmeticPackPriceCents, "paid-stable")
	lateFailure := CosmeticPaymentEventInput{
		Provider: "stripe", EventID: "evt_late_failure", EventType: CosmeticStripeCheckoutAsyncPaymentFailed, PayloadHash: commerceEventHash("1"),
		OrderID: paidOrder.ID, AccountID: account.ID, CheckoutSessionID: paidOrder.CheckoutSessionID,
		PaymentIntentID: paidOrder.PaymentIntentID, Currency: "USD", FailureMessage: "late failure",
	}
	result, err = ProcessCosmeticPaymentEvent(ctx, lateFailure)
	if err != nil || result.Order.Status != CosmeticOrderStatusPaid || result.Applied {
		t.Fatalf("late failure downgraded paid order = (%+v, %v)", result, err)
	}
}

func createProcessingCommerceOrder(t *testing.T, ctx context.Context, suffix string) (*CustomerAccount, *CosmeticOrder) {
	t.Helper()
	account := createCommerceTestAccount(t, ctx, suffix)
	order := createAttachedCommerceOrder(t, ctx, account, 1, suffix)
	delayed := paidCommerceEvent(order, "evt_processing_"+suffix, commerceEventHash("b"), 0)
	delayed.Paid = false
	result, err := ProcessCosmeticPaymentEvent(ctx, delayed)
	if err != nil || result.Order.Status != CosmeticOrderStatusProcessing || result.Order.PaymentIntentID == "" {
		t.Fatalf("create processing order(%s) = (%+v, %v)", suffix, result, err)
	}
	return account, result.Order
}

func assertRetryableReversalWasNotClaimed(t *testing.T, ctx context.Context, eventID string) {
	t.Helper()
	var count int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM cosmetic_payment_events WHERE provider = 'stripe' AND event_id = $1`, eventID).Scan(&count); err != nil {
		t.Fatalf("inspect retryable reversal claim: %v", err)
	}
	if count != 0 {
		t.Fatalf("retryable reversal %s left %d durable event claims", eventID, count)
	}
}

func TestPostgresCosmeticRefundBeforeAsyncSuccessRemainsRetryable(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	_, order := createProcessingCommerceOrder(t, ctx, "refund-before-paid")
	refund := CosmeticPaymentEventInput{
		Provider: "stripe", EventID: "evt_refund_before_paid", EventType: CosmeticStripeRefundCreated,
		PayloadHash: commerceEventHash("a"), PaymentIntentID: order.PaymentIntentID, Currency: order.Currency,
		RefundID: "re_before_paid", RefundStatus: CosmeticRefundStatusSucceeded,
		RefundAmountCents: order.ExpectedSubtotalCents,
	}
	if _, err := ProcessCosmeticPaymentEvent(ctx, refund); !errors.Is(err, ErrCosmeticPaymentEventRetryable) {
		t.Fatalf("pre-payment refund error = %v, want retryable", err)
	}
	assertRetryableReversalWasNotClaimed(t, ctx, refund.EventID)
	if got := countOrderLicenses(t, ctx, order.ID); got != 0 {
		t.Fatalf("pre-payment refund path granted %d licenses", got)
	}

	paid := paidCommerceEvent(order, "evt_async_paid_after_refund", commerceEventHash("c"), order.ExpectedSubtotalCents)
	paid.EventType = CosmeticStripeCheckoutAsyncPaymentSucceeded
	paidResult, err := ProcessCosmeticPaymentEvent(ctx, paid)
	if err != nil || paidResult.LicensesCreated != len(order.Items) {
		t.Fatalf("async success after early refund = (%+v, %v)", paidResult, err)
	}
	result, err := ProcessCosmeticPaymentEvent(ctx, refund)
	if err != nil || result.Order.Status != CosmeticOrderStatusRefunded || result.Order.AmountRefundedCents != order.ExpectedSubtotalCents {
		t.Fatalf("retried early refund = (%+v, %v)", result, err)
	}
	var refunded int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM cosmetic_order_licenses ol JOIN cosmetic_licenses l ON l.id = ol.license_id
		WHERE ol.order_id = $1 AND l.status = 'refunded'`, order.ID).Scan(&refunded); err != nil || refunded != len(order.Items) {
		t.Fatalf("retried early refund licenses = (%d, %v), want %d", refunded, err, len(order.Items))
	}
	duplicate, err := ProcessCosmeticPaymentEvent(ctx, refund)
	if err != nil || !duplicate.Duplicate || duplicate.Applied {
		t.Fatalf("duplicate retried refund = (%+v, %v)", duplicate, err)
	}
}

func TestPostgresCosmeticDisputeBeforeAsyncSuccessRemainsRetryable(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	_, order := createProcessingCommerceOrder(t, ctx, "dispute-before-paid")
	dispute := CosmeticPaymentEventInput{
		Provider: "stripe", EventID: "evt_dispute_before_paid", EventType: CosmeticStripeDisputeCreated,
		PayloadHash: commerceEventHash("d"), PaymentIntentID: order.PaymentIntentID, Currency: order.Currency,
	}
	if _, err := ProcessCosmeticPaymentEvent(ctx, dispute); !errors.Is(err, ErrCosmeticPaymentEventRetryable) {
		t.Fatalf("pre-payment dispute error = %v, want retryable", err)
	}
	assertRetryableReversalWasNotClaimed(t, ctx, dispute.EventID)
	if got := countOrderLicenses(t, ctx, order.ID); got != 0 {
		t.Fatalf("pre-payment dispute path granted %d licenses", got)
	}

	paid := paidCommerceEvent(order, "evt_async_paid_after_dispute", commerceEventHash("e"), order.ExpectedSubtotalCents)
	paid.EventType = CosmeticStripeCheckoutAsyncPaymentSucceeded
	paidResult, err := ProcessCosmeticPaymentEvent(ctx, paid)
	if err != nil || paidResult.LicensesCreated != len(order.Items) {
		t.Fatalf("async success after early dispute = (%+v, %v)", paidResult, err)
	}
	result, err := ProcessCosmeticPaymentEvent(ctx, dispute)
	if err != nil || result.Order.Status != CosmeticOrderStatusDisputed {
		t.Fatalf("retried early dispute = (%+v, %v)", result, err)
	}
	var chargebacks int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM cosmetic_order_licenses ol JOIN cosmetic_licenses l ON l.id = ol.license_id
		WHERE ol.order_id = $1 AND l.status = 'chargeback'`, order.ID).Scan(&chargebacks); err != nil || chargebacks != len(order.Items) {
		t.Fatalf("retried early dispute licenses = (%d, %v), want %d", chargebacks, err, len(order.Items))
	}
	duplicate, err := ProcessCosmeticPaymentEvent(ctx, dispute)
	if err != nil || !duplicate.Duplicate || duplicate.Applied {
		t.Fatalf("duplicate retried dispute = (%+v, %v)", duplicate, err)
	}
}

func TestPostgresCosmeticOrderRejectsInvalidQuantityAndUnavailablePack(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account := createCommerceTestAccount(t, ctx, "availability")
	for _, quantity := range []int{0, 11} {
		if _, err := CreateCosmeticOrder(ctx, account.ID, "neon-signal-pack", quantity); !errors.Is(err, ErrCosmeticOrderQuantity) {
			t.Errorf("quantity %d error = %v", quantity, err)
		}
	}
	if _, err := Pool.Exec(ctx, `UPDATE cosmetic_packs SET currency = 'JPY' WHERE id = 'neon-signal-pack'`); err != nil {
		t.Fatalf("simulate legacy non-USD pack: %v", err)
	}
	if _, err := CreateCosmeticOrder(ctx, account.ID, "neon-signal-pack", 1); !errors.Is(err, ErrCosmeticOrderPackUnavailable) {
		t.Fatalf("legacy non-USD pack order error = %v", err)
	}
	if _, err := Pool.Exec(ctx, `UPDATE cosmetic_packs SET currency = 'USD' WHERE id = 'neon-signal-pack'`); err != nil {
		t.Fatalf("restore launch currency: %v", err)
	}
	if _, err := Pool.Exec(ctx, `UPDATE cosmetic_packs SET price_cents = 299 WHERE id = 'neon-signal-pack'`); err != nil {
		t.Fatalf("simulate stale non-$1.99 pack: %v", err)
	}
	if _, err := CreateCosmeticOrder(ctx, account.ID, "neon-signal-pack", 1); !errors.Is(err, ErrCosmeticOrderPackUnavailable) {
		t.Fatalf("stale non-$1.99 pack order error = %v", err)
	}
	if _, err := Pool.Exec(ctx, `UPDATE cosmetic_packs SET price_cents = $1 WHERE id = 'neon-signal-pack'`, CosmeticPackPriceCents); err != nil {
		t.Fatalf("restore launch price: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO cosmetic_pack_items (pack_id, item_id, sort_order)
		VALUES ('neon-signal-pack', 'trail-ember-sparks', 9990)`); err != nil {
		t.Fatalf("simulate trail hidden in set: %v", err)
	}
	if _, err := CreateCosmeticOrder(ctx, account.ID, "neon-signal-pack", 1); !errors.Is(err, ErrCosmeticOrderPackUnavailable) {
		t.Fatalf("trail hidden in non-trail order error = %v", err)
	}
	if _, err := Pool.Exec(ctx, `DELETE FROM cosmetic_pack_items WHERE pack_id = 'neon-signal-pack' AND item_id = 'trail-ember-sparks'`); err != nil {
		t.Fatalf("restore set membership: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO cosmetic_pack_items (pack_id, item_id, sort_order)
		VALUES ('trail-ember-sparks-pack', 'skin-neon-grid', 9990)`); err != nil {
		t.Fatalf("simulate non-trail hidden in trail product: %v", err)
	}
	if _, err := CreateCosmeticOrder(ctx, account.ID, "trail-ember-sparks-pack", 1); !errors.Is(err, ErrCosmeticOrderPackUnavailable) {
		t.Fatalf("non-trail hidden in trail order error = %v", err)
	}
	pack := cosmeticPackByID(DefaultCosmeticCatalogData().Packs, "neon-signal-pack")
	pack.IsActive = false
	if _, err := UpsertCosmeticPack(ctx, *pack, "availability-admin"); err != nil {
		t.Fatalf("deactivate pack: %v", err)
	}
	if _, err := CreateCosmeticOrder(ctx, account.ID, pack.ID, 1); !errors.Is(err, ErrCosmeticOrderPackUnavailable) {
		t.Fatalf("inactive pack order error = %v", err)
	}
}
