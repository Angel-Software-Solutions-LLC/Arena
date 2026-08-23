package db

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPostgresCosmeticCatalogAdministrationAndPublicProjection(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	category := CosmeticCategory{ID: "event", Name: "Event", Description: "Event cosmetics", IsActive: true, SortOrder: 50}
	if _, err := UpsertCosmeticCategory(ctx, category, "integration-admin"); err != nil {
		t.Fatalf("UpsertCosmeticCategory: %v", err)
	}
	item := CosmeticItem{
		ID: "attachment-event-crown", Name: "Event Crown", Description: "Presentation only.",
		CategoryID: category.ID, Slot: CosmeticSlotAttachment, AssetKey: "arena_set_902_event_crown", Rarity: "rare",
		PriceCents: 99, Currency: "USD", IsPurchasable: true, IsActive: true, SortOrder: 10,
	}
	if _, err := UpsertCosmeticCatalogItem(ctx, item, "integration-admin"); err != nil {
		t.Fatalf("UpsertCosmeticCatalogItem: %v", err)
	}
	pack := CosmeticPack{
		ID: "event-pack", CategoryID: category.ID, Name: "Event Pack", Description: "One event cosmetic.",
		PriceCents: CosmeticPackPriceCents, Currency: "USD", IsPurchasable: true, IsActive: true, SortOrder: 50,
		ItemIDs: []string{item.ID},
	}
	if _, err := UpsertCosmeticPack(ctx, pack, "integration-admin"); err != nil {
		t.Fatalf("UpsertCosmeticPack: %v", err)
	}

	publicCatalog, err := GetPublicCosmeticCatalog(ctx)
	if err != nil {
		t.Fatalf("GetPublicCosmeticCatalog: %v", err)
	}
	gotPack := cosmeticPackByID(publicCatalog.Packs, pack.ID)
	if gotPack == nil || len(gotPack.Items) != 1 || gotPack.Items[0].ID != item.ID {
		t.Fatalf("public pack = %+v, want embedded item %q", gotPack, item.ID)
	}

	pack.Description = "Updated event cosmetic description."
	if _, err := UpsertCosmeticPack(ctx, pack, "integration-admin"); err != nil {
		t.Fatalf("update pack metadata: %v", err)
	}
	audit, err := ListCosmeticCatalogAudit(ctx, 20)
	if err != nil {
		t.Fatalf("ListCosmeticCatalogAudit: %v", err)
	}
	if len(audit) < 4 || audit[0].Actor != "integration-admin" || audit[0].EntityID != pack.ID || len(audit[0].Before) == 0 || len(audit[0].After) == 0 {
		t.Fatalf("catalog audit did not capture update before/after: %+v", audit)
	}

	category.IsActive = false
	if _, err := UpsertCosmeticCategory(ctx, category, "integration-admin"); err != nil {
		t.Fatalf("deactivate category: %v", err)
	}
	publicCatalog, err = GetPublicCosmeticCatalog(ctx)
	if err != nil {
		t.Fatalf("GetPublicCosmeticCatalog after deactivate: %v", err)
	}
	if cosmeticPackByID(publicCatalog.Packs, pack.ID) != nil || cosmeticItemByID(publicCatalog.Items, item.ID) != nil {
		t.Fatal("public catalog exposed entries in an inactive category")
	}
	adminCatalog, err := GetAdminCosmeticCatalog(ctx)
	if err != nil {
		t.Fatalf("GetAdminCosmeticCatalog: %v", err)
	}
	if cosmeticPackByID(adminCatalog.Packs, pack.ID) == nil || cosmeticItemByID(adminCatalog.Items, item.ID) == nil {
		t.Fatal("admin catalog omitted inactive entries")
	}
}

func TestPostgresInactiveCosmeticCategorySuspendsGrantEquipAndRendering(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	category := CosmeticCategory{ID: "retired-collection", Name: "Retired Collection", IsActive: true, SortOrder: 500}
	if _, err := UpsertCosmeticCategory(ctx, category, "integration-admin"); err != nil {
		t.Fatalf("create category: %v", err)
	}
	item := CosmeticItem{
		ID: "retired-attachment", Name: "Retired Attachment", CategoryID: category.ID,
		Slot: CosmeticSlotAttachment, AssetKey: "arena_set_906_retired_attachment", Rarity: "rare",
		PriceCents: 99, Currency: "USD", IsActive: true, SortOrder: 10,
	}
	if _, err := UpsertCosmeticCatalogItem(ctx, item, "integration-admin"); err != nil {
		t.Fatalf("create item: %v", err)
	}

	bot := createCustomerCosmeticsTestBot(t, ctx, "inactive-category")
	legacyBot := createCustomerCosmeticsTestBot(t, ctx, "inactive-category-legacy")
	if created, err := GrantCosmeticEntitlement(ctx, legacyBot.ID, item.ID, "manual", "inactive-legacy-existing"); err != nil || !created {
		t.Fatalf("grant active legacy entitlement = (%v, %v), want created", created, err)
	}
	license, created, err := GrantCosmeticLicense(ctx, "inactive-owner@example.com", item.ID, "manual", "inactive-license")
	if err != nil || !created {
		t.Fatalf("grant active category item = (%+v, %v, %v)", license, created, err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "inactive-owner@example.com", "https://id.example", "inactive-owner", "Inactive Owner")
	if err != nil {
		t.Fatalf("verify owner: %v", err)
	}
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("link bot: %v", err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, license.ID, &bot.ID); err != nil {
		t.Fatalf("assign license: %v", err)
	}
	if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, bot.ID, license.ID); err != nil {
		t.Fatalf("equip active category item: %v", err)
	}
	equipped, err := GetEquippedCosmetics(ctx, bot.ID)
	if err != nil || equipped[CosmeticSlotAttachment] != item.AssetKey {
		t.Fatalf("active equipped assets = (%v, %v), want %q", equipped, err, item.AssetKey)
	}
	batch, err := GetEquippedCosmeticsForBots(ctx, []string{bot.ID, legacyBot.ID, bot.ID})
	if err != nil || len(batch) != 2 || batch[bot.ID][CosmeticSlotAttachment] != item.AssetKey ||
		batch[legacyBot.ID][CosmeticSlotAttachment] != "none" {
		t.Fatalf("batched equipped assets = (%v, %v)", batch, err)
	}

	category.IsActive = false
	if _, err := UpsertCosmeticCategory(ctx, category, "integration-admin"); err != nil {
		t.Fatalf("deactivate category: %v", err)
	}
	licenses, err := ListCustomerCosmeticLicenses(ctx, account.ID)
	if err != nil || len(licenses) != 1 {
		t.Fatalf("durable licenses after category deactivate = (%+v, %v)", licenses, err)
	}
	if licenses[0].Item.IsActive {
		t.Fatal("license item remained effectively active while its category was inactive")
	}
	botItems, err := ListBotCosmetics(ctx, bot.ID)
	if err != nil {
		t.Fatalf("ListBotCosmetics inactive category: %v", err)
	}
	for _, got := range botItems {
		if got.ID == item.ID {
			t.Fatalf("bot inventory exposed item %q from an inactive category", item.ID)
		}
	}
	equipped, err = GetEquippedCosmetics(ctx, bot.ID)
	if err != nil || equipped[CosmeticSlotAttachment] != "none" {
		t.Fatalf("inactive equipped assets = (%v, %v), want attachment fallback", equipped, err)
	}
	if _, err := EquipCosmetic(ctx, bot.ID, item.Slot, item.ID); !errors.Is(err, ErrCosmeticInactive) {
		t.Fatalf("bot-key equip inactive category error = %v, want ErrCosmeticInactive", err)
	}
	if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, bot.ID, license.ID); !errors.Is(err, ErrCosmeticInactive) {
		t.Fatalf("account equip inactive category error = %v, want ErrCosmeticInactive", err)
	}
	replayed, replayCreated, err := GrantCosmeticLicenseToAccount(ctx, account.ID, item.ID, "manual", "inactive-license")
	if err != nil || replayCreated || replayed.ID != license.ID {
		t.Fatalf("inactive category idempotent license replay = (%+v, %v, %v), want existing license", replayed, replayCreated, err)
	}
	if created, err := GrantCosmeticEntitlement(ctx, legacyBot.ID, item.ID, "manual", "inactive-legacy-existing"); err != nil || created {
		t.Fatalf("inactive category idempotent legacy replay = (%v, %v), want existing entitlement", created, err)
	}
	if _, _, err := GrantCosmeticLicense(ctx, "other-owner@example.com", item.ID, "manual", "inactive-second-license"); !errors.Is(err, ErrCosmeticInactive) {
		t.Fatalf("account grant inactive category error = %v, want ErrCosmeticInactive", err)
	}
	if _, err := GrantCosmeticEntitlement(ctx, bot.ID, item.ID, "manual", "inactive-legacy-license"); !errors.Is(err, ErrCosmeticInactive) {
		t.Fatalf("legacy grant inactive category error = %v, want ErrCosmeticInactive", err)
	}

	category.IsActive = true
	if _, err := UpsertCosmeticCategory(ctx, category, "integration-admin"); err != nil {
		t.Fatalf("reactivate category: %v", err)
	}
	equipped, err = GetEquippedCosmetics(ctx, bot.ID)
	if err != nil || equipped[CosmeticSlotAttachment] != item.AssetKey {
		t.Fatalf("reactivated equipped assets = (%v, %v), want %q", equipped, err, item.AssetKey)
	}
}

func TestPostgresCosmeticPackUpdatesAreAtomicUnderConcurrency(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	packA := CosmeticPack{
		ID: "concurrent-pack", CategoryID: "starter-packs", Name: "Concurrent Pack A",
		PriceCents: CosmeticPackPriceCents, Currency: "USD", IsPurchasable: true, IsActive: true, SortOrder: 90,
		ItemIDs: []string{"skin-neon-grid"},
	}
	packB := packA
	packB.Name = "Concurrent Pack B"
	packB.ItemIDs = []string{"weapon-void-edge"}

	results := make(chan error, 2)
	go func() { _, err := UpsertCosmeticPack(context.Background(), packA, "writer-a"); results <- err }()
	go func() { _, err := UpsertCosmeticPack(context.Background(), packB, "writer-b"); results <- err }()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent UpsertCosmeticPack: %v", err)
		}
	}

	catalog, err := GetAdminCosmeticCatalog(ctx)
	if err != nil {
		t.Fatalf("GetAdminCosmeticCatalog: %v", err)
	}
	got := cosmeticPackByID(catalog.Packs, packA.ID)
	if got == nil {
		t.Fatal("concurrent pack missing")
	}
	isA := got.Name == packA.Name && got.PriceCents == packA.PriceCents && len(got.ItemIDs) == 1 && got.ItemIDs[0] == packA.ItemIDs[0]
	isB := got.Name == packB.Name && got.PriceCents == packB.PriceCents && len(got.ItemIDs) == 1 && got.ItemIDs[0] == packB.ItemIDs[0]
	if !isA && !isB {
		t.Fatalf("concurrent pack contains torn metadata/membership: %+v", got)
	}
}

func TestPostgresCosmeticCatalogRejectsBrokenReferencesAndImmutableRenderIdentity(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	invalidPack := CosmeticPack{
		ID: "broken-pack", CategoryID: "starter-packs", Name: "Broken Pack",
		PriceCents: CosmeticPackPriceCents, Currency: "USD", IsPurchasable: true, IsActive: true,
		ItemIDs: []string{"missing-item"},
	}
	if _, err := UpsertCosmeticPack(ctx, invalidPack, "integration-admin"); !errors.Is(err, ErrCosmeticCatalogInvalid) {
		t.Fatalf("missing pack item error = %v, want ErrCosmeticCatalogInvalid", err)
	}
	trailInSet := CosmeticPack{
		ID: "trail-in-set", CategoryID: "starter-packs", Name: "Trail Disguised As Set",
		PriceCents: CosmeticPackPriceCents, Currency: "USD", IsPurchasable: true, IsActive: true,
		ItemIDs: []string{"trail-ember-sparks"},
	}
	if _, err := UpsertCosmeticPack(ctx, trailInSet, "integration-admin"); !errors.Is(err, ErrCosmeticCatalogInvalid) {
		t.Fatalf("trail in non-trail product error = %v, want ErrCosmeticCatalogInvalid", err)
	}
	nonTrailInTrail := CosmeticPack{
		ID: "nontrail-in-trail", CategoryID: CosmeticTrailCategoryID, Name: "Non-Trail Disguised As Trail",
		PriceCents: CosmeticTrailPriceCents, Currency: "USD", IsPurchasable: true, IsActive: true,
		ItemIDs: []string{"skin-neon-grid"},
	}
	if _, err := UpsertCosmeticPack(ctx, nonTrailInTrail, "integration-admin"); !errors.Is(err, ErrCosmeticCatalogInvalid) {
		t.Fatalf("non-trail in trail product error = %v, want ErrCosmeticCatalogInvalid", err)
	}

	item := DefaultCosmeticCatalog()[0]
	item.AssetKey = "arena_set_904_changed_identity"
	if _, err := UpsertCosmeticCatalogItem(ctx, item, "integration-admin"); !errors.Is(err, ErrCosmeticCatalogConflict) {
		t.Fatalf("render identity change error = %v, want ErrCosmeticCatalogConflict", err)
	}

	if deleted, err := DeleteCosmeticCategory(ctx, "chassis", "integration-admin"); deleted || !errors.Is(err, ErrCosmeticCatalogConflict) {
		t.Fatalf("referenced category delete = (%v, %v), want conflict", deleted, err)
	}

	chassis := CosmeticCategory{ID: "chassis", Name: "Chassis", IsActive: false, SortOrder: 10}
	if _, err := UpsertCosmeticCategory(ctx, chassis, "integration-admin"); !errors.Is(err, ErrCosmeticCatalogConflict) {
		t.Fatalf("deactivating the free chassis fallback category error = %v, want conflict", err)
	}
}

func TestPostgresCosmeticStarterPackAdminEditsSurviveSchemaRepair(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	pack := starterCosmeticPacks[0]
	pack.ItemIDs = []string{"skin-neon-grid"}
	if _, err := UpsertCosmeticPack(ctx, pack, "integration-admin"); err != nil {
		t.Fatalf("edit starter pack: %v", err)
	}
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("schema repair after starter pack edit: %v", err)
	}
	catalog, err := GetAdminCosmeticCatalog(ctx)
	if err != nil {
		t.Fatalf("GetAdminCosmeticCatalog: %v", err)
	}
	got := cosmeticPackByID(catalog.Packs, pack.ID)
	if got == nil || len(got.ItemIDs) != 1 || got.ItemIDs[0] != "skin-neon-grid" {
		t.Fatalf("schema repair overwrote admin starter pack membership: %+v", got)
	}
}

func TestPostgresCosmeticPackPriceMigrationPreservesOrderSnapshot(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "price-migration@example.com", "https://id.example", "price-migration", "Price Migration")
	if err != nil {
		t.Fatalf("create price migration account: %v", err)
	}
	order, err := CreateCosmeticOrder(ctx, account.ID, "neon-signal-pack", 1)
	if err != nil || order.UnitPriceCents != CosmeticPackPriceCents {
		t.Fatalf("create pre-migration order snapshot = (%+v, %v)", order, err)
	}
	// Simulate rows written by a pre-fixed-price release. Current order creation
	// correctly rejects this catalog price, while schema repair must preserve
	// historical order snapshots already in the ledger.
	if _, err := Pool.Exec(ctx, `UPDATE cosmetic_packs SET price_cents = 699 WHERE id = 'neon-signal-pack'`); err != nil {
		t.Fatalf("stage legacy catalog price: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		UPDATE cosmetic_orders SET unit_price_cents = 699, expected_subtotal_cents = 699 WHERE id = $1`, order.ID); err != nil {
		t.Fatalf("stage legacy order price: %v", err)
	}
	if err := EnsureCosmeticsSchema(ctx); err != nil {
		t.Fatalf("EnsureCosmeticsSchema price migration: %v", err)
	}
	var packPrice, orderPrice int64
	if err := Pool.QueryRow(ctx, `
		SELECT
			(SELECT price_cents FROM cosmetic_packs WHERE id = 'neon-signal-pack'),
			(SELECT unit_price_cents FROM cosmetic_orders WHERE id = $1)`, order.ID).Scan(&packPrice, &orderPrice); err != nil {
		t.Fatalf("read migrated catalog and order prices: %v", err)
	}
	if packPrice != CosmeticPackPriceCents || orderPrice != 699 {
		t.Fatalf("post-migration pack/order prices = %d/%d, want 199/699", packPrice, orderPrice)
	}
}

func TestPostgresBuiltinCatalogEntriesAreEditableButNotDeletable(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	catalog, err := GetAdminCosmeticCatalog(ctx)
	if err != nil {
		t.Fatalf("GetAdminCosmeticCatalog: %v", err)
	}
	if item := cosmeticItemByID(catalog.Items, "skin-neon-grid"); item == nil || !item.IsBuiltin {
		t.Fatalf("seed item built-in state = %+v, want true", item)
	}
	pack := cosmeticPackByID(catalog.Packs, "neon-signal-pack")
	if pack == nil || !pack.IsBuiltin {
		t.Fatalf("seed pack built-in state = %+v, want true", pack)
	}

	pack.Description = "Operator-edited built-in description."
	updated, err := UpsertCosmeticPack(ctx, *pack, "integration-admin")
	if err != nil {
		t.Fatalf("edit built-in pack: %v", err)
	}
	if !updated.IsBuiltin {
		t.Fatal("editing a built-in pack cleared its built-in state")
	}
	if deleted, err := DeleteCosmeticPack(ctx, pack.ID, "integration-admin"); deleted ||
		!errors.Is(err, ErrCosmeticCatalogConflict) || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("delete built-in pack = (%v, %v), want clear catalog conflict", deleted, err)
	}
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("schema repair after rejected delete: %v", err)
	}
	catalog, err = GetAdminCosmeticCatalog(ctx)
	if err != nil {
		t.Fatalf("GetAdminCosmeticCatalog after repair: %v", err)
	}
	if got := cosmeticPackByID(catalog.Packs, pack.ID); got == nil || got.Description != pack.Description || got.PriceCents != CosmeticPackPriceCents || !got.IsBuiltin {
		t.Fatalf("built-in pack after repair = %+v, want edited metadata, fixed price, and built-in state", got)
	}

	category := CosmeticCategory{ID: "operator-collection", Name: "Operator Collection", IsActive: true}
	createdCategory, err := UpsertCosmeticCategory(ctx, category, "integration-admin")
	if err != nil || createdCategory.IsBuiltin {
		t.Fatalf("admin category = (%+v, %v), want non-built-in", createdCategory, err)
	}
	item := CosmeticItem{
		ID: "operator-attachment", Name: "Operator Attachment", CategoryID: category.ID,
		Slot: CosmeticSlotAttachment, AssetKey: "arena_set_905_operator_attachment", Rarity: "rare",
		Currency: "USD", IsFree: true, IsActive: true,
	}
	createdItem, err := UpsertCosmeticCatalogItem(ctx, item, "integration-admin")
	if err != nil || createdItem.IsBuiltin {
		t.Fatalf("admin item = (%+v, %v), want non-built-in", createdItem, err)
	}
	operatorPack := CosmeticPack{
		ID: "operator-pack", CategoryID: category.ID, Name: "Operator Pack",
		PriceCents: CosmeticPackPriceCents, Currency: "USD", IsPurchasable: true, IsActive: true,
		ItemIDs: []string{item.ID},
	}
	createdPack, err := UpsertCosmeticPack(ctx, operatorPack, "integration-admin")
	if err != nil || createdPack.IsBuiltin {
		t.Fatalf("admin pack = (%+v, %v), want non-built-in", createdPack, err)
	}
	if deleted, err := DeleteCosmeticPack(ctx, operatorPack.ID, "integration-admin"); err != nil || !deleted {
		t.Fatalf("delete admin pack = (%v, %v), want success", deleted, err)
	}
	if deleted, err := DeleteCosmeticCatalogItem(ctx, item.ID, "integration-admin"); err != nil || !deleted {
		t.Fatalf("delete admin item = (%v, %v), want success", deleted, err)
	}
	if deleted, err := DeleteCosmeticCategory(ctx, category.ID, "integration-admin"); err != nil || !deleted {
		t.Fatalf("delete admin category = (%v, %v), want success", deleted, err)
	}
}

func TestPostgresConcurrentFallbackChangesCannotRemoveLastFreeSlotItem(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	second := CosmeticItem{
		ID: "skin-second-free", Name: "Second Free Chassis", CategoryID: "chassis",
		Slot: CosmeticSlotBotSkin, AssetKey: "arena_set_903_second_free", Rarity: "common",
		Currency: "USD", IsFree: true, IsActive: true, SortOrder: 99,
	}
	if _, err := UpsertCosmeticCatalogItem(ctx, second, "integration-admin"); err != nil {
		t.Fatalf("create second free fallback: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		CREATE FUNCTION delay_cosmetic_fallback_update() RETURNS trigger AS $$
		BEGIN
			IF OLD.slot = 'bot_skin' AND OLD.is_active = true AND NEW.is_active = false THEN
				PERFORM pg_sleep(0.2);
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`); err != nil {
		t.Fatalf("install fallback concurrency function: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		CREATE TRIGGER delay_cosmetic_fallback_update
			BEFORE UPDATE ON cosmetic_items
			FOR EACH ROW EXECUTE FUNCTION delay_cosmetic_fallback_update()`); err != nil {
		t.Fatalf("install fallback concurrency trigger: %v", err)
	}

	first := DefaultCosmeticCatalog()[0]
	first.IsActive = false
	second.IsActive = false
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, item := range []CosmeticItem{first, second} {
		item := item
		go func() {
			<-start
			_, err := UpsertCosmeticCatalogItem(context.Background(), item, "concurrent-admin")
			results <- err
		}()
	}
	close(start)
	conflicts := 0
	for range 2 {
		err := <-results
		if errors.Is(err, ErrCosmeticCatalogConflict) {
			conflicts++
		} else if err != nil {
			t.Fatalf("concurrent fallback update: %v", err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("fallback conflicts = %d, want exactly one rejected update", conflicts)
	}
	var activeFree int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM cosmetic_items item
		JOIN cosmetic_categories category ON category.id = item.category_id
		WHERE item.slot = 'bot_skin' AND item.is_active = true AND item.is_free = true
		  AND category.is_active = true`).Scan(&activeFree); err != nil {
		t.Fatalf("count active free chassis: %v", err)
	}
	if activeFree != 1 {
		t.Fatalf("active free chassis = %d, want 1", activeFree)
	}
}

func cosmeticPackByID(packs []CosmeticPack, id string) *CosmeticPack {
	for index := range packs {
		if packs[index].ID == id {
			return &packs[index]
		}
	}
	return nil
}

func cosmeticItemByID(items []CosmeticItem, id string) *CosmeticItem {
	for index := range items {
		if items[index].ID == id {
			return &items[index]
		}
	}
	return nil
}
