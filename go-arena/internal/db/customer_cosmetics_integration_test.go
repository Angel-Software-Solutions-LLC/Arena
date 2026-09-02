package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func createCustomerCosmeticsTestBot(t *testing.T, ctx context.Context, suffix string) *Bot {
	t.Helper()
	now := time.Now().UTC()
	bot := &Bot{
		ID:              "customer-bot-" + suffix,
		APIKeyID:        "customer-key-" + suffix,
		Name:            "Customer Bot " + suffix,
		AvatarColor:     "#123456",
		DefaultWeapon:   "sword",
		DefaultStats:    JSONBStats{"hp": 5, "speed": 5, "attack": 5, "defense": 5},
		DefaultFallback: "aggressive",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := CreateAPIKeyAndBot(ctx, bot.APIKeyID, "hash-"+suffix, "arena_customer_"+suffix, "127.0.0.1", bot); err != nil {
		t.Fatalf("CreateAPIKeyAndBot(%s): %v", suffix, err)
	}
	return bot
}

func holdPostgresTestAdvisoryBarrier(t *testing.T, ctx context.Context, classID, objectID int) func() {
	t.Helper()
	barrier, err := Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire advisory barrier connection: %v", err)
	}
	if _, err := barrier.Exec(ctx, `SELECT pg_advisory_lock($1, $2)`, classID, objectID); err != nil {
		barrier.Release()
		t.Fatalf("hold advisory barrier: %v", err)
	}
	held := true
	release := func() {
		if !held {
			return
		}
		if _, err := barrier.Exec(context.Background(), `SELECT pg_advisory_unlock($1, $2)`, classID, objectID); err != nil {
			t.Errorf("release advisory barrier: %v", err)
		}
		held = false
	}
	t.Cleanup(func() {
		release()
		barrier.Release()
	})
	return release
}

func waitForPostgresTestCondition(t *testing.T, ctx context.Context, description string, query string, args ...any) {
	t.Helper()
	for {
		var ready bool
		if err := Pool.QueryRow(ctx, query, args...).Scan(&ready); err != nil {
			t.Fatalf("wait for %s: %v", description, err)
		}
		if ready {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s: %v", description, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func linkSubscriptionTestBot(t *testing.T, ctx context.Context, account *CustomerAccount, bot *Bot) {
	t.Helper()
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("link %s: %v", bot.ID, err)
	}
}

func countLoadoutRows(t *testing.T, ctx context.Context, botID string) int {
	t.Helper()
	var rows int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM bot_cosmetic_loadout WHERE bot_id = $1`, botID).Scan(&rows); err != nil {
		t.Fatalf("count loadout rows for %s: %v", botID, err)
	}
	return rows
}

/*
 * TestPostgresArenaSubscriptionUnlocksEveryCosmeticForLinkedBots is the
 * commerce model in one place: there is no per-item ownership in Arena.
 *
 * A linked account whose Arena subscription is active may put any paid
 * cosmetic on any of its bots, the same item on several at once. One without
 * sees every paid item locked and every free one open. The subscription flag
 * flipping is what turns the saved looks on and off — nothing is copied per
 * item and nothing has to be cleaned up when it lapses.
 */
func TestPostgresArenaSubscriptionUnlocksEveryCosmeticForLinkedBots(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	botOne := createCustomerCosmeticsTestBot(t, ctx, "one")
	botTwo := createCustomerCosmeticsTestBot(t, ctx, "two")
	stranger := createCustomerCosmeticsTestBot(t, ctx, "stranger")
	account, err := UpsertVerifiedCustomerAccount(ctx, "", "https://id.example", "owner-subject", "Owner")
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	if account.SubscriptionActive || account.SubscriptionSyncedAt != nil {
		t.Fatalf("a new account is subscribed before any sync: %+v", account)
	}
	linkSubscriptionTestBot(t, ctx, account, botOne)
	linkSubscriptionTestBot(t, ctx, account, botTwo)

	// Not subscribed: paid is locked, free is not, and the bot-key path
	// agrees with the Dashboard path.
	if _, err := EquipCustomerCosmetic(ctx, account.ID, botOne.ID, CosmeticSlotBotSkin, "skin-neon-grid"); !errors.Is(err, ErrSubscriptionRequired) {
		t.Fatalf("paid equip without a subscription error = %v, want ErrSubscriptionRequired", err)
	}
	if _, err := EquipCustomerCosmetic(ctx, account.ID, botOne.ID, CosmeticSlotAttachment, "attachment-signal-antenna"); err != nil {
		t.Fatalf("free equip without a subscription: %v", err)
	}
	if _, err := EquipCosmetic(ctx, botOne.ID, CosmeticSlotBotSkin, "skin-neon-grid"); !errors.Is(err, ErrCosmeticNotOwned) {
		t.Fatalf("bot-key paid equip without a subscription error = %v, want ErrCosmeticNotOwned", err)
	}
	locked, err := ListBotCosmetics(ctx, botOne.ID)
	if err != nil {
		t.Fatalf("ListBotCosmetics locked: %v", err)
	}
	paid := 0
	for _, item := range locked {
		if item.IsFree != item.Owned {
			t.Fatalf("unsubscribed inventory item %s free=%v owned=%v, want owned only when free", item.ID, item.IsFree, item.Owned)
		}
		if !item.IsFree {
			paid++
		}
	}
	if paid < 300 {
		t.Fatalf("catalog exposes %d paid items to the unsubscribed bot, want the whole launch catalog listed as locked", paid)
	}

	// Accounts reports an active Arena entitlement at sign-in.
	syncedAt := time.Now().UTC().Truncate(time.Microsecond)
	change, err := SetCustomerSubscription(ctx, account.ID, true, syncedAt)
	if err != nil || !change.Changed || !change.Active || len(change.BotIDs) != 2 ||
		change.BotIDs[0] != botOne.ID || change.BotIDs[1] != botTwo.ID {
		t.Fatalf("subscribe = (%+v, %v), want a change naming both linked bots", change, err)
	}
	account, err = GetCustomerAccount(ctx, account.ID)
	if err != nil || !account.SubscriptionActive || account.SubscriptionSyncedAt == nil || !account.SubscriptionSyncedAt.Equal(syncedAt) {
		t.Fatalf("account after subscribe = (%+v, %v)", account, err)
	}
	if _, err := EquipCustomerCosmetic(ctx, account.ID, botOne.ID, CosmeticSlotBotSkin, "skin-neon-grid"); err != nil {
		t.Fatalf("subscribed equip on bot one: %v", err)
	}
	// The same paid item on a second bot at the same time: there is no copy
	// to move between them any more.
	if _, err := EquipCustomerCosmetic(ctx, account.ID, botTwo.ID, CosmeticSlotBotSkin, "skin-neon-grid"); err != nil {
		t.Fatalf("subscribed equip of the same item on bot two: %v", err)
	}
	if _, err := EquipCustomerCosmetic(ctx, account.ID, botTwo.ID, CosmeticSlotTrail, "trail-ember-sparks"); err != nil {
		t.Fatalf("subscribed trail equip on bot two: %v", err)
	}
	if _, err := EquipCosmetic(ctx, botOne.ID, CosmeticSlotWeaponSkin, "weapon-solar-flare"); err != nil {
		t.Fatalf("bot-key paid equip with a subscription: %v", err)
	}
	equipped, err := GetEquippedCosmeticsForBots(ctx, []string{botOne.ID, botTwo.ID})
	if err != nil {
		t.Fatalf("GetEquippedCosmeticsForBots subscribed: %v", err)
	}
	if equipped[botOne.ID][CosmeticSlotBotSkin] != "neon_grid" || equipped[botOne.ID][CosmeticSlotWeaponSkin] != "solar_flare" ||
		equipped[botOne.ID][CosmeticSlotAttachment] != "signal_antenna" || equipped[botOne.ID][CosmeticSlotTrail] != "standard" ||
		equipped[botTwo.ID][CosmeticSlotBotSkin] != "neon_grid" || equipped[botTwo.ID][CosmeticSlotTrail] != "ember_sparks" {
		t.Fatalf("subscribed loadouts = %v", equipped)
	}
	unlocked, err := ListBotCosmetics(ctx, botOne.ID)
	if err != nil {
		t.Fatalf("ListBotCosmetics unlocked: %v", err)
	}
	for _, item := range unlocked {
		if !item.Owned {
			t.Fatalf("subscribed inventory still locks %s", item.ID)
		}
	}
	inventory, err := GetCustomerCosmeticsInventory(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetCustomerCosmeticsInventory: %v", err)
	}
	catalog, err := ListCosmeticCatalog(ctx)
	if err != nil {
		t.Fatalf("ListCosmeticCatalog: %v", err)
	}
	if !inventory.Subscription.Active || inventory.Subscription.SyncedAt == nil || len(inventory.Items) != len(catalog) || len(inventory.Bots) != 2 ||
		inventory.Loadouts[botOne.ID][CosmeticSlotBotSkin] != "skin-neon-grid" || inventory.Loadouts[botTwo.ID][CosmeticSlotTrail] != "trail-ember-sparks" {
		t.Fatalf("subscribed inventory = subscription %+v items %d bots %d loadouts %v",
			inventory.Subscription, len(inventory.Items), len(inventory.Bots), inventory.Loadouts)
	}

	// The subscription is the account's, and reaches only the bots it links.
	if _, err := EquipCosmetic(ctx, stranger.ID, CosmeticSlotBotSkin, "skin-neon-grid"); !errors.Is(err, ErrCosmeticNotOwned) {
		t.Fatalf("unlinked bot paid equip error = %v, want ErrCosmeticNotOwned", err)
	}
	if _, err := EquipCustomerCosmetic(ctx, account.ID, stranger.ID, CosmeticSlotBotSkin, "skin-neon-grid"); !errors.Is(err, ErrCustomerBotNotLinked) {
		t.Fatalf("equip on an unlinked bot error = %v, want ErrCustomerBotNotLinked", err)
	}
	if _, err := EquipCustomerCosmetic(ctx, account.ID, botOne.ID, CosmeticSlotWeaponSkin, "skin-neon-grid"); !errors.Is(err, ErrCosmeticSlotMismatch) {
		t.Fatalf("wrong-slot equip error = %v, want ErrCosmeticSlotMismatch", err)
	}
	if _, err := EquipCustomerCosmetic(ctx, account.ID, botOne.ID, CosmeticSlotBotSkin, "skin-does-not-exist"); !errors.Is(err, ErrCosmeticNotFound) {
		t.Fatalf("unknown item equip error = %v, want ErrCosmeticNotFound", err)
	}

	// The subscription lapses. Nothing is deleted; the paid looks simply
	// stop resolving, on both bots, and the free one stays.
	change, err = SetCustomerSubscription(ctx, account.ID, false, syncedAt.Add(time.Hour))
	if err != nil || !change.Changed || change.Active || len(change.BotIDs) != 2 {
		t.Fatalf("lapse = (%+v, %v), want a change naming both linked bots", change, err)
	}
	equipped, err = GetEquippedCosmeticsForBots(ctx, []string{botOne.ID, botTwo.ID})
	if err != nil {
		t.Fatalf("GetEquippedCosmeticsForBots lapsed: %v", err)
	}
	if equipped[botOne.ID][CosmeticSlotBotSkin] != "standard" || equipped[botOne.ID][CosmeticSlotWeaponSkin] != "standard" ||
		equipped[botOne.ID][CosmeticSlotAttachment] != "signal_antenna" ||
		equipped[botTwo.ID][CosmeticSlotBotSkin] != "standard" || equipped[botTwo.ID][CosmeticSlotTrail] != "standard" {
		t.Fatalf("lapsed loadouts = %v, want every paid look hidden and the free one kept", equipped)
	}
	if rows := countLoadoutRows(t, ctx, botOne.ID); rows != 3 {
		t.Fatalf("lapse deleted loadout rows: %d, want 3 retained", rows)
	}
	inventory, err = GetCustomerCosmeticsInventory(ctx, account.ID)
	if err != nil || inventory.Subscription.Active || len(inventory.Loadouts[botOne.ID]) != 1 || inventory.Loadouts[botOne.ID][CosmeticSlotAttachment] != "attachment-signal-antenna" {
		t.Fatalf("lapsed inventory = (%+v, %v)", inventory, err)
	}
	if _, err := EquipCustomerCosmetic(ctx, account.ID, botOne.ID, CosmeticSlotBotSkin, "skin-neon-grid"); !errors.Is(err, ErrSubscriptionRequired) {
		t.Fatalf("paid equip after lapse error = %v, want ErrSubscriptionRequired", err)
	}
	change, err = SetCustomerSubscription(ctx, account.ID, false, syncedAt.Add(2*time.Hour))
	if err != nil || change.Changed || len(change.BotIDs) != 0 {
		t.Fatalf("repeat lapse = (%+v, %v), want no change", change, err)
	}

	// It resumes: the saved looks come straight back.
	if _, err := SetCustomerSubscription(ctx, account.ID, true, syncedAt.Add(3*time.Hour)); err != nil {
		t.Fatalf("resubscribe: %v", err)
	}
	restored, err := GetEquippedCosmetics(ctx, botOne.ID)
	if err != nil || restored[CosmeticSlotBotSkin] != "neon_grid" || restored[CosmeticSlotWeaponSkin] != "solar_flare" {
		t.Fatalf("restored loadout = (%v, %v)", restored, err)
	}

	// A revoked key cannot receive an equip; unlinking removes the paid
	// loadout and keeps the free one.
	if err := DeactivateAPIKey(ctx, botTwo.APIKeyID); err != nil {
		t.Fatalf("DeactivateAPIKey: %v", err)
	}
	if _, err := EquipCustomerCosmetic(ctx, account.ID, botTwo.ID, CosmeticSlotBotSkin, "skin-neon-grid"); !errors.Is(err, ErrCustomerBotKeyInactive) {
		t.Fatalf("equip on an inactive key error = %v, want ErrCustomerBotKeyInactive", err)
	}
	if _, err := UnlinkBotFromCustomerAccount(ctx, account.ID, botOne.ID); err != nil {
		t.Fatalf("unlink bot one: %v", err)
	}
	if rows := countLoadoutRows(t, ctx, botOne.ID); rows != 1 {
		t.Fatalf("loadout rows after unlink = %d, want only the free attachment", rows)
	}
	unlinked, err := GetEquippedCosmetics(ctx, botOne.ID)
	if err != nil || unlinked[CosmeticSlotBotSkin] != "standard" || unlinked[CosmeticSlotAttachment] != "signal_antenna" {
		t.Fatalf("unlinked loadout = (%v, %v)", unlinked, err)
	}
	if _, err := UnlinkBotFromCustomerAccount(ctx, account.ID, botOne.ID); !errors.Is(err, ErrCustomerBotNotLinked) {
		t.Fatalf("repeat unlink error = %v, want ErrCustomerBotNotLinked", err)
	}
}

func TestPostgresPlatformChangeOrderingDoesNotDeadlockAgentLinkAndProfileTransition(t *testing.T) {
	ctx, cancel := context.WithTimeout(useFreshPostgresSchema(t), 15*time.Second)
	defer cancel()
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	bot := createCustomerCosmeticsTestBot(t, ctx, "profile-lock-order")
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"profile-lock-order@example.com",
		"https://id.example",
		"profile-lock-order",
		"Profile Lock Order",
	)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	var agentRevision int64
	if err := Pool.QueryRow(ctx, `SELECT revision FROM platform_agents WHERE agent_id = $1`, bot.ID).Scan(&agentRevision); err != nil {
		t.Fatalf("load platform agent revision: %v", err)
	}

	const barrierClassID = 72104
	const barrierObjectID = 12
	if _, err := Pool.Exec(ctx, `
		CREATE FUNCTION test_block_profile_transition() RETURNS TRIGGER
		LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.status = 'active' AND NEW.status = 'suspended' THEN
				PERFORM pg_advisory_xact_lock(72104, 12);
			END IF;
			RETURN NEW;
		END
		$$;
		CREATE TRIGGER test_block_profile_transition
		BEFORE UPDATE OF status ON platform_game_profiles
		FOR EACH ROW EXECUTE FUNCTION test_block_profile_transition()`); err != nil {
		t.Fatalf("install profile barrier: %v", err)
	}
	releaseBarrier := holdPostgresTestAdvisoryBarrier(t, ctx, barrierClassID, barrierObjectID)

	type profileResult struct {
		result *PlatformProfileTransitionResult
		err    error
	}
	profileDone := make(chan profileResult, 1)
	go func() {
		result, err := TransitionPlatformProfile(ctx, PlatformProfileTransition{
			AgentID:          bot.ID,
			Game:             "arena",
			Status:           "suspended",
			ExpectedRevision: agentRevision,
			IdempotencyKey:   "profile-link-lock-order",
		})
		profileDone <- profileResult{result: result, err: err}
	}()

	waitForPostgresTestCondition(t, ctx, "profile transition barrier", `
		SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory'
			  AND classid = $1
			  AND objid = $2
			  AND NOT granted
		)`, barrierClassID, barrierObjectID)

	linkDone := make(chan error, 1)
	go func() {
		_, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID)
		linkDone <- err
	}()
	waitForPostgresTestCondition(t, ctx, "platform agent row lock", `
		SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%SELECT revision FROM platform_agents%'
		)`)

	releaseBarrier()

	var transitioned profileResult
	select {
	case transitioned = <-profileDone:
	case <-ctx.Done():
		t.Fatalf("profile transition did not finish: %v", ctx.Err())
	}
	var linkErr error
	select {
	case linkErr = <-linkDone:
	case <-ctx.Done():
		t.Fatalf("agent link did not finish: %v", ctx.Err())
	}
	if transitioned.err != nil || transitioned.result == nil || transitioned.result.Status != "suspended" || linkErr != nil {
		t.Fatalf("concurrent profile transition/link = (result %+v, transition error %v, link error %v)", transitioned.result, transitioned.err, linkErr)
	}
}

func TestPostgresPlatformChangeOrderingDoesNotDeadlockRestartReconciliationAndProfileTransition(t *testing.T) {
	ctx, cancel := context.WithTimeout(useFreshPostgresSchema(t), 20*time.Second)
	defer cancel()
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	bot := createCustomerCosmeticsTestBot(t, ctx, "restart-lock-order")
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"restart-lock-order@example.com",
		"https://id.example",
		"restart-lock-order",
		"Restart Lock Order",
	)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO account_api_keys (account_id, api_key_id, linked_at)
		VALUES ($1, $2, NOW())`, account.ID, bot.APIKeyID); err != nil {
		t.Fatalf("seed unreconciled API key ownership: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO account_bot_links (account_id, bot_id, linked_at)
		VALUES ($1, $2, NOW())`, account.ID, bot.ID); err != nil {
		t.Fatalf("seed unreconciled account link: %v", err)
	}
	var agentRevision int64
	if err := Pool.QueryRow(ctx, `SELECT revision FROM platform_agents WHERE agent_id = $1`, bot.ID).Scan(&agentRevision); err != nil {
		t.Fatalf("load platform agent revision: %v", err)
	}

	const barrierClassID = 72104
	const barrierObjectID = 13
	if _, err := Pool.Exec(ctx, `
		CREATE FUNCTION test_block_restart_profile_transition() RETURNS TRIGGER
		LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.status = 'active' AND NEW.status = 'suspended' THEN
				PERFORM pg_advisory_xact_lock(72104, 13);
			END IF;
			RETURN NEW;
		END
		$$;
		CREATE TRIGGER test_block_restart_profile_transition
		BEFORE UPDATE OF status ON platform_game_profiles
		FOR EACH ROW EXECUTE FUNCTION test_block_restart_profile_transition()`); err != nil {
		t.Fatalf("install restart profile barrier: %v", err)
	}
	releaseBarrier := holdPostgresTestAdvisoryBarrier(t, ctx, barrierClassID, barrierObjectID)

	type profileResult struct {
		result *PlatformProfileTransitionResult
		err    error
	}
	profileDone := make(chan profileResult, 1)
	go func() {
		result, err := TransitionPlatformProfile(ctx, PlatformProfileTransition{
			AgentID:          bot.ID,
			Game:             "arena",
			Status:           "suspended",
			ExpectedRevision: agentRevision,
			IdempotencyKey:   "restart-profile-lock-order",
		})
		profileDone <- profileResult{result: result, err: err}
	}()
	waitForPostgresTestCondition(t, ctx, "restart profile transition barrier", `
		SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory'
			  AND classid = $1
			  AND objid = $2
			  AND NOT granted
		)`, barrierClassID, barrierObjectID)

	restartDone := make(chan error, 1)
	go func() {
		restartDone <- EnsurePlatformAuthoritySchema(ctx)
	}()
	waitForPostgresTestCondition(t, ctx, "restart reconciliation agent lock", `
		SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%platform_agents%'
			  AND (query LIKE '%FOR UPDATE%' OR query LIKE '%LOCK TABLE%')
		)`)

	releaseBarrier()

	var transitioned profileResult
	select {
	case transitioned = <-profileDone:
	case <-ctx.Done():
		t.Fatalf("profile transition did not finish: %v", ctx.Err())
	}
	var restartErr error
	select {
	case restartErr = <-restartDone:
	case <-ctx.Done():
		t.Fatalf("platform authority restart did not finish: %v", ctx.Err())
	}
	if transitioned.err != nil || transitioned.result == nil || transitioned.result.Status != "suspended" || restartErr != nil {
		t.Fatalf("concurrent profile transition/restart = (result %+v, transition error %v, restart error %v)", transitioned.result, transitioned.err, restartErr)
	}
	var linkEvents int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform_agent_link_events
		WHERE account_id = $1 AND agent_id = $2 AND status = 'linked'`, account.ID, bot.ID).Scan(&linkEvents); err != nil {
		t.Fatalf("count reconciled link events: %v", err)
	}
	if linkEvents != 1 {
		t.Fatalf("reconciled link events = %d, want 1", linkEvents)
	}
}

func TestPostgresPlatformAgentLinkReconciliationDoesNotOverwriteConcurrentUnlink(t *testing.T) {
	ctx, cancel := context.WithTimeout(useFreshPostgresSchema(t), 15*time.Second)
	defer cancel()
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	bot := createCustomerCosmeticsTestBot(t, ctx, "reconcile-unlink")
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"reconcile-unlink@example.com",
		"https://id.example",
		"reconcile-unlink",
		"Reconcile Unlink",
	)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO account_api_keys (account_id, api_key_id, linked_at)
		VALUES ($1, $2, NOW())`, account.ID, bot.APIKeyID); err != nil {
		t.Fatalf("seed unreconciled API key ownership: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO account_bot_links (account_id, bot_id, linked_at)
		VALUES ($1, $2, NOW())`, account.ID, bot.ID); err != nil {
		t.Fatalf("seed unreconciled account link: %v", err)
	}

	reconcileTx, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reconciliation transaction: %v", err)
	}
	defer reconcileTx.Rollback(ctx)
	repairs, err := loadPlatformAgentLinkRepairsTx(ctx, reconcileTx)
	if err != nil {
		t.Fatalf("discover reconciliation repairs: %v", err)
	}
	if len(repairs) != 1 || repairs[0].accountID != account.ID ||
		repairs[0].agentID != bot.ID || repairs[0].status != "linked" {
		t.Fatalf("discovered repairs = %+v, want one linked repair", repairs)
	}

	unlinked, err := UnlinkBotFromCustomerAccount(ctx, account.ID, bot.ID)
	if err != nil || !unlinked {
		t.Fatalf("concurrent unlink = (%v, %v), want success", unlinked, err)
	}
	if err := lockPlatformAgentLinkRepairsTx(ctx, reconcileTx, repairs); err != nil {
		t.Fatalf("lock stale reconciliation repairs: %v", err)
	}
	if err := applyPlatformAgentLinkRepairsTx(ctx, reconcileTx, repairs); err != nil {
		t.Fatalf("apply stale reconciliation repairs: %v", err)
	}
	if err := reconcileTx.Commit(ctx); err != nil {
		t.Fatalf("commit reconciliation transaction: %v", err)
	}

	var authoritativeLinks int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM account_bot_links
		WHERE account_id = $1 AND bot_id = $2`, account.ID, bot.ID).Scan(&authoritativeLinks); err != nil {
		t.Fatalf("count authoritative links: %v", err)
	}
	if authoritativeLinks != 0 {
		t.Fatalf("authoritative links = %d, want 0", authoritativeLinks)
	}
	var latestStatus string
	if err := Pool.QueryRow(ctx, `
		SELECT status FROM platform_agent_link_events
		WHERE agent_id = $1
		ORDER BY event_id DESC
		LIMIT 1`, bot.ID).Scan(&latestStatus); err != nil {
		t.Fatalf("load latest durable link status: %v", err)
	}
	if latestStatus != "unlinked" {
		t.Fatalf("latest durable link status = %q, want unlinked", latestStatus)
	}
	var linkedEvents int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform_agent_link_events
		WHERE account_id = $1 AND agent_id = $2 AND status = 'linked'`,
		account.ID, bot.ID).Scan(&linkedEvents); err != nil {
		t.Fatalf("count stale linked events: %v", err)
	}
	if linkedEvents != 0 {
		t.Fatalf("stale linked events = %d, want 0", linkedEvents)
	}
	var latestChange string
	if err := Pool.QueryRow(ctx, `
		SELECT transition FROM platform_changes
		WHERE subject_kind = 'agent_link' AND subject_id = $1
		ORDER BY change_id DESC
		LIMIT 1`, bot.ID).Scan(&latestChange); err != nil {
		t.Fatalf("load latest link change: %v", err)
	}
	if latestChange != "unlinked" {
		t.Fatalf("latest link change = %q, want unlinked", latestChange)
	}
}

func TestPostgresPlatformAgentLinkReconciliationDoesNotOverwriteConcurrentRelink(t *testing.T) {
	ctx, cancel := context.WithTimeout(useFreshPostgresSchema(t), 15*time.Second)
	defer cancel()
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	bot := createCustomerCosmeticsTestBot(t, ctx, "reconcile-relink")
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"reconcile-relink@example.com",
		"https://id.example",
		"reconcile-relink",
		"Reconcile Relink",
	)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("create durable account link: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		DELETE FROM account_bot_links
		WHERE account_id = $1 AND bot_id = $2`, account.ID, bot.ID); err != nil {
		t.Fatalf("remove authoritative link without durable event: %v", err)
	}

	reconcileTx, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reconciliation transaction: %v", err)
	}
	defer reconcileTx.Rollback(ctx)
	repairs, err := loadPlatformAgentLinkRepairsTx(ctx, reconcileTx)
	if err != nil {
		t.Fatalf("discover reconciliation repairs: %v", err)
	}
	if len(repairs) != 1 || repairs[0].accountID != account.ID ||
		repairs[0].agentID != bot.ID || repairs[0].status != "unlinked" {
		t.Fatalf("discovered repairs = %+v, want one unlinked repair", repairs)
	}

	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("concurrent relink: %v", err)
	}
	if err := lockPlatformAgentLinkRepairsTx(ctx, reconcileTx, repairs); err != nil {
		t.Fatalf("lock stale reconciliation repairs: %v", err)
	}
	if err := applyPlatformAgentLinkRepairsTx(ctx, reconcileTx, repairs); err != nil {
		t.Fatalf("apply stale reconciliation repairs: %v", err)
	}
	if err := reconcileTx.Commit(ctx); err != nil {
		t.Fatalf("commit reconciliation transaction: %v", err)
	}

	var authoritativeLinks int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM account_bot_links
		WHERE account_id = $1 AND bot_id = $2`, account.ID, bot.ID).Scan(&authoritativeLinks); err != nil {
		t.Fatalf("count authoritative links: %v", err)
	}
	if authoritativeLinks != 1 {
		t.Fatalf("authoritative links = %d, want 1", authoritativeLinks)
	}
	var latestStatus string
	if err := Pool.QueryRow(ctx, `
		SELECT status FROM platform_agent_link_events
		WHERE agent_id = $1
		ORDER BY event_id DESC
		LIMIT 1`, bot.ID).Scan(&latestStatus); err != nil {
		t.Fatalf("load latest durable link status: %v", err)
	}
	if latestStatus != "linked" {
		t.Fatalf("latest durable link status = %q, want linked", latestStatus)
	}
}

func TestPostgresPlatformAgentLinkTransferReconciliationPreservesConcurrentCurrentAccountChange(t *testing.T) {
	for _, relink := range []bool{false, true} {
		name := "unlink"
		if relink {
			name = "unlink_and_relink"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(useFreshPostgresSchema(t), 20*time.Second)
			defer cancel()
			if err := EnsureCoreSchema(ctx); err != nil {
				t.Fatalf("EnsureCoreSchema: %v", err)
			}

			bot := createCustomerCosmeticsTestBot(t, ctx, "transfer-"+name)
			priorAccount, err := UpsertVerifiedCustomerAccount(
				ctx,
				"transfer-prior-"+name+"@example.com",
				"https://id.example",
				"transfer-prior-"+name,
				"Transfer Prior "+name,
			)
			if err != nil {
				t.Fatalf("create prior account: %v", err)
			}
			currentAccount, err := UpsertVerifiedCustomerAccount(
				ctx,
				"transfer-current-"+name+"@example.com",
				"https://id.example",
				"transfer-current-"+name,
				"Transfer Current "+name,
			)
			if err != nil {
				t.Fatalf("create current account: %v", err)
			}
			if _, err := LinkBotToCustomerAccount(ctx, priorAccount.ID, bot.ID); err != nil {
				t.Fatalf("create prior durable link: %v", err)
			}
			if _, err := Pool.Exec(ctx, `
				UPDATE account_api_keys SET account_id = $2
				WHERE account_id = $1 AND api_key_id = $3`,
				priorAccount.ID, currentAccount.ID, bot.APIKeyID); err != nil {
				t.Fatalf("transfer compatibility key ownership: %v", err)
			}
			if _, err := Pool.Exec(ctx, `
				UPDATE account_bot_links SET account_id = $2
				WHERE account_id = $1 AND bot_id = $3`,
				priorAccount.ID, currentAccount.ID, bot.ID); err != nil {
				t.Fatalf("transfer compatibility bot link: %v", err)
			}

			reconcileTx, err := Pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin reconciliation transaction: %v", err)
			}
			defer reconcileTx.Rollback(ctx)
			repairs, err := loadPlatformAgentLinkRepairsTx(ctx, reconcileTx)
			if err != nil {
				t.Fatalf("discover transfer repairs: %v", err)
			}
			if len(repairs) != 2 ||
				repairs[0].accountID != priorAccount.ID ||
				repairs[0].agentID != bot.ID ||
				repairs[0].status != "unlinked" ||
				repairs[0].expectedEventID <= 0 ||
				repairs[1].accountID != currentAccount.ID ||
				repairs[1].agentID != bot.ID ||
				repairs[1].status != "linked" {
				t.Fatalf("discovered transfer repairs = %+v, want prior unlink then current link", repairs)
			}

			unlinked, err := UnlinkBotFromCustomerAccount(ctx, currentAccount.ID, bot.ID)
			if err != nil || !unlinked {
				t.Fatalf("concurrent current-account unlink = (%v, %v), want success", unlinked, err)
			}
			if relink {
				if _, err := LinkBotToCustomerAccount(ctx, currentAccount.ID, bot.ID); err != nil {
					t.Fatalf("concurrent current-account relink: %v", err)
				}
			}
			if err := lockPlatformAgentLinkRepairsTx(ctx, reconcileTx, repairs); err != nil {
				t.Fatalf("lock transfer repairs: %v", err)
			}
			if err := applyPlatformAgentLinkRepairsTx(ctx, reconcileTx, repairs); err != nil {
				t.Fatalf("apply transfer repairs: %v", err)
			}
			if err := reconcileTx.Commit(ctx); err != nil {
				t.Fatalf("commit transfer reconciliation: %v", err)
			}

			var priorLatestStatus string
			if err := Pool.QueryRow(ctx, `
				SELECT status FROM platform_agent_link_events
				WHERE account_id = $1 AND agent_id = $2
				ORDER BY event_id DESC
				LIMIT 1`, priorAccount.ID, bot.ID).Scan(&priorLatestStatus); err != nil {
				t.Fatalf("load prior-account terminal history: %v", err)
			}
			if priorLatestStatus != "unlinked" {
				t.Fatalf("prior-account latest status = %q, want unlinked", priorLatestStatus)
			}

			wantGlobalAccount := priorAccount.ID
			wantGlobalStatus := "unlinked"
			wantAuthoritativeLinks := 0
			if relink {
				wantGlobalAccount = currentAccount.ID
				wantGlobalStatus = "linked"
				wantAuthoritativeLinks = 1
			}
			var globalAccount, globalStatus string
			if err := Pool.QueryRow(ctx, `
				SELECT account_id, status FROM platform_agent_link_events
				WHERE agent_id = $1
				ORDER BY event_id DESC
				LIMIT 1`, bot.ID).Scan(&globalAccount, &globalStatus); err != nil {
				t.Fatalf("load global terminal link: %v", err)
			}
			if globalAccount != wantGlobalAccount || globalStatus != wantGlobalStatus {
				t.Fatalf("global terminal link = (%q, %q), want (%q, %q)",
					globalAccount, globalStatus, wantGlobalAccount, wantGlobalStatus)
			}
			var authoritativeLinks int
			if err := Pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM account_bot_links
				WHERE bot_id = $1`, bot.ID).Scan(&authoritativeLinks); err != nil {
				t.Fatalf("count authoritative links: %v", err)
			}
			if authoritativeLinks != wantAuthoritativeLinks {
				t.Fatalf("authoritative links = %d, want %d", authoritativeLinks, wantAuthoritativeLinks)
			}

			var eventCountBeforeRepeat int
			if err := Pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM platform_agent_link_events
				WHERE agent_id = $1`, bot.ID).Scan(&eventCountBeforeRepeat); err != nil {
				t.Fatalf("count events before repeat reconciliation: %v", err)
			}
			if err := EnsurePlatformAuthoritySchema(ctx); err != nil {
				t.Fatalf("repeat reconciliation: %v", err)
			}
			var eventCountAfterRepeat int
			if err := Pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM platform_agent_link_events
				WHERE agent_id = $1`, bot.ID).Scan(&eventCountAfterRepeat); err != nil {
				t.Fatalf("count events after repeat reconciliation: %v", err)
			}
			if eventCountAfterRepeat != eventCountBeforeRepeat {
				t.Fatalf("repeat reconciliation event count = %d, want %d",
					eventCountAfterRepeat, eventCountBeforeRepeat)
			}
		})
	}
}

func TestPostgresPlatformAgentLinkReconciliationRestartsForNewCurrentAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(useFreshPostgresSchema(t), 20*time.Second)
	defer cancel()
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	bot := createCustomerCosmeticsTestBot(t, ctx, "reconcile-restart")
	priorAccount, err := UpsertVerifiedCustomerAccount(
		ctx,
		"reconcile-restart-prior@example.com",
		"https://id.example",
		"reconcile-restart-prior",
		"Reconcile Restart Prior",
	)
	if err != nil {
		t.Fatalf("create prior account: %v", err)
	}
	currentAccount, err := UpsertVerifiedCustomerAccount(
		ctx,
		"reconcile-restart-current@example.com",
		"https://id.example",
		"reconcile-restart-current",
		"Reconcile Restart Current",
	)
	if err != nil {
		t.Fatalf("create current account: %v", err)
	}
	if _, err := LinkBotToCustomerAccount(ctx, priorAccount.ID, bot.ID); err != nil {
		t.Fatalf("create prior durable link: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		DELETE FROM account_bot_links
		WHERE account_id = $1 AND bot_id = $2`, priorAccount.ID, bot.ID); err != nil {
		t.Fatalf("remove prior authoritative link: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		UPDATE account_api_keys SET account_id = $2
		WHERE account_id = $1 AND api_key_id = $3`,
		priorAccount.ID, currentAccount.ID, bot.APIKeyID); err != nil {
		t.Fatalf("transfer compatibility key ownership: %v", err)
	}

	reconcileTx, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reconciliation transaction: %v", err)
	}
	defer reconcileTx.Rollback(ctx)
	repairs, err := loadPlatformAgentLinkRepairsTx(ctx, reconcileTx)
	if err != nil {
		t.Fatalf("discover stale prior-account repair: %v", err)
	}
	if len(repairs) != 1 ||
		repairs[0].accountID != priorAccount.ID ||
		repairs[0].agentID != bot.ID ||
		repairs[0].status != "unlinked" {
		t.Fatalf("discovered repairs = %+v, want only prior-account unlink", repairs)
	}

	if _, err := LinkBotToCustomerAccount(ctx, currentAccount.ID, bot.ID); err != nil {
		t.Fatalf("concurrent new-account link: %v", err)
	}
	if err := lockPlatformAgentLinkRepairsTx(ctx, reconcileTx, repairs); err != nil {
		t.Fatalf("lock stale repair set: %v", err)
	}
	if err := applyPlatformAgentLinkRepairsTx(ctx, reconcileTx, repairs); !errors.Is(
		err,
		errPlatformAgentLinkReconciliationRetry,
	) {
		t.Fatalf("stale repair apply error = %v, want reconciliation retry", err)
	}
	if err := reconcileTx.Rollback(ctx); err != nil {
		t.Fatalf("roll back stale repair attempt: %v", err)
	}
	if err := reconcilePlatformAgentLinks(ctx); err != nil {
		t.Fatalf("fresh reconciliation attempt: %v", err)
	}

	var priorLatestStatus string
	if err := Pool.QueryRow(ctx, `
		SELECT status FROM platform_agent_link_events
		WHERE account_id = $1 AND agent_id = $2
		ORDER BY event_id DESC
		LIMIT 1`, priorAccount.ID, bot.ID).Scan(&priorLatestStatus); err != nil {
		t.Fatalf("load prior-account terminal history: %v", err)
	}
	if priorLatestStatus != "unlinked" {
		t.Fatalf("prior-account latest status = %q, want unlinked", priorLatestStatus)
	}
	var globalAccount, globalStatus string
	if err := Pool.QueryRow(ctx, `
		SELECT account_id, status FROM platform_agent_link_events
		WHERE agent_id = $1
		ORDER BY event_id DESC
		LIMIT 1`, bot.ID).Scan(&globalAccount, &globalStatus); err != nil {
		t.Fatalf("load global terminal link: %v", err)
	}
	if globalAccount != currentAccount.ID || globalStatus != "linked" {
		t.Fatalf("global terminal link = (%q, %q), want (%q, linked)",
			globalAccount, globalStatus, currentAccount.ID)
	}

	var eventCountBeforeRepeat int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform_agent_link_events
		WHERE agent_id = $1`, bot.ID).Scan(&eventCountBeforeRepeat); err != nil {
		t.Fatalf("count events before repeat reconciliation: %v", err)
	}
	if err := reconcilePlatformAgentLinks(ctx); err != nil {
		t.Fatalf("repeat reconciliation: %v", err)
	}
	var eventCountAfterRepeat int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform_agent_link_events
		WHERE agent_id = $1`, bot.ID).Scan(&eventCountAfterRepeat); err != nil {
		t.Fatalf("count events after repeat reconciliation: %v", err)
	}
	if eventCountAfterRepeat != eventCountBeforeRepeat {
		t.Fatalf("repeat reconciliation event count = %d, want %d",
			eventCountAfterRepeat, eventCountBeforeRepeat)
	}
}

// TestPostgresPreAccountSchemaUpgradesAdditively recreates the exact
// cosmetics shape PR #69 shipped — bot-scoped entitlements, no customer
// tables — and checks the current schema repair carries it forward without
// losing the bot's look or refusing to run twice.
func TestPostgresPreAccountSchemaUpgradesAdditively(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	_, err := Pool.Exec(ctx, `
		CREATE TABLE api_keys (id TEXT PRIMARY KEY);
		CREATE TABLE bots (
			id TEXT PRIMARY KEY,
			api_key_id TEXT NOT NULL UNIQUE REFERENCES api_keys(id) ON DELETE CASCADE,
			name TEXT NOT NULL DEFAULT 'Unnamed Bot'
		);
		CREATE TABLE cosmetic_items (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			slot TEXT NOT NULL CHECK (slot IN ('bot_skin', 'weapon_skin', 'attachment')),
			asset_key TEXT NOT NULL,
			rarity TEXT NOT NULL DEFAULT 'common',
			price_cents INT NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
			currency TEXT NOT NULL DEFAULT 'USD',
			is_free BOOLEAN NOT NULL DEFAULT false,
			is_purchasable BOOLEAN NOT NULL DEFAULT false,
			is_active BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (slot, asset_key)
		);
		CREATE TABLE cosmetic_entitlements (
			bot_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
			cosmetic_id TEXT NOT NULL REFERENCES cosmetic_items(id) ON DELETE CASCADE,
			source TEXT NOT NULL DEFAULT 'manual',
			external_reference TEXT,
			granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (bot_id, cosmetic_id)
		);
		CREATE UNIQUE INDEX idx_cosmetic_entitlements_external
			ON cosmetic_entitlements (source, external_reference)
			WHERE external_reference IS NOT NULL AND external_reference <> '';
		CREATE TABLE bot_cosmetic_loadout (
			bot_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
			slot TEXT NOT NULL CHECK (slot IN ('bot_skin', 'weapon_skin', 'attachment')),
			cosmetic_id TEXT NOT NULL REFERENCES cosmetic_items(id) ON DELETE CASCADE,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (bot_id, slot)
		);
		INSERT INTO api_keys (id) VALUES ('old-key');
		INSERT INTO bots (id, api_key_id) VALUES ('old-bot', 'old-key');
		INSERT INTO cosmetic_items
			(id, name, description, slot, asset_key, rarity, price_cents, currency, is_free, is_purchasable, is_active)
		VALUES ('skin-neon-grid', 'Neon Grid', '', 'bot_skin', 'neon_grid', 'rare', 499, 'USD', false, false, true);
		INSERT INTO cosmetic_entitlements (bot_id, cosmetic_id, source, external_reference)
		VALUES ('old-bot', 'skin-neon-grid', 'demo', 'old-demo-grant');
		INSERT INTO bot_cosmetic_loadout (bot_id, slot, cosmetic_id)
		VALUES ('old-bot', 'bot_skin', 'skin-neon-grid')`)
	if err != nil {
		t.Fatalf("create PR #69 schema: %v", err)
	}

	if err := EnsureCosmeticsSchema(ctx); err != nil {
		t.Fatalf("upgrade PR #69 schema: %v", err)
	}
	if err := EnsureCosmeticsSchema(ctx); err != nil {
		t.Fatalf("repeat upgraded schema: %v", err)
	}
	var subscriptionColumns int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'customer_accounts'
		  AND column_name IN ('subscription_active', 'subscription_synced_at')`).Scan(&subscriptionColumns); err != nil {
		t.Fatalf("inspect subscription columns: %v", err)
	}
	if subscriptionColumns != 2 {
		t.Fatalf("customer_accounts subscription columns = %d, want both added by the upgrade", subscriptionColumns)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO bot_cosmetic_loadout (bot_id, slot, cosmetic_id)
		VALUES ('old-bot', 'trail', 'trail-standard')`); err != nil {
		t.Fatalf("upgraded loadout constraint rejected trail slot: %v", err)
	}
	// The bot-scoped grant is honoured as it always was: the demo fleet's
	// complimentary looks survive the upgrade.
	equipped, err := GetEquippedCosmetics(ctx, "old-bot")
	if err != nil || equipped[CosmeticSlotBotSkin] != "neon_grid" || equipped[CosmeticSlotTrail] != "standard" {
		t.Fatalf("upgraded demo loadout = (%v, %v)", equipped, err)
	}
	if created, err := GrantCosmeticEntitlement(ctx, "old-bot", "skin-neon-grid", "demo", "old-demo-grant"); err != nil || created {
		t.Fatalf("replay of the old grant = (%v, %v), want idempotent", created, err)
	}
}

/*
 * TestPostgresLicenceEraLoadoutColumnsStayNullAndHarmless is the migration
 * story for a database that ran the retired per-item licence model.
 *
 * Such a database still has `license_id` and `account_id` on
 * bot_cosmetic_loadout, a foreign key to cosmetic_licenses and the composite
 * assignment foreign key. Nothing drops them — migrations here are additive
 * — so every write the new code makes must satisfy them, which it does by
 * leaving both columns null.
 */
func TestPostgresLicenceEraLoadoutColumnsStayNullAndHarmless(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		CREATE TABLE cosmetic_licenses (
			id TEXT PRIMARY KEY,
			account_id TEXT REFERENCES customer_accounts(id) ON DELETE RESTRICT,
			cosmetic_id TEXT NOT NULL REFERENCES cosmetic_items(id) ON DELETE RESTRICT,
			status TEXT NOT NULL DEFAULT 'active',
			UNIQUE (id, account_id)
		);
		CREATE TABLE cosmetic_license_assignments (
			license_id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			bot_id TEXT NOT NULL,
			UNIQUE (license_id, account_id, bot_id),
			FOREIGN KEY (license_id, account_id) REFERENCES cosmetic_licenses(id, account_id) ON DELETE CASCADE,
			FOREIGN KEY (account_id, bot_id) REFERENCES account_bot_links(account_id, bot_id) ON DELETE CASCADE
		);
		ALTER TABLE bot_cosmetic_loadout
			ADD COLUMN license_id TEXT REFERENCES cosmetic_licenses(id) ON DELETE CASCADE,
			ADD COLUMN account_id TEXT REFERENCES customer_accounts(id) ON DELETE RESTRICT,
			ADD CONSTRAINT bot_cosmetic_loadout_assignment_fk
				FOREIGN KEY (license_id, account_id, bot_id)
				REFERENCES cosmetic_license_assignments(license_id, account_id, bot_id) ON DELETE CASCADE;
		CREATE UNIQUE INDEX idx_bot_cosmetic_loadout_license
			ON bot_cosmetic_loadout (license_id) WHERE license_id IS NOT NULL`); err != nil {
		t.Fatalf("recreate licence-era loadout columns: %v", err)
	}
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("repeat EnsureCoreSchema over licence-era columns: %v", err)
	}

	bot := createCustomerCosmeticsTestBot(t, ctx, "licence-era")
	account, err := UpsertVerifiedCustomerAccount(ctx, "", "https://id.example", "licence-era", "Licence Era")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	linkSubscriptionTestBot(t, ctx, account, bot)
	if _, err := SetCustomerSubscription(ctx, account.ID, true, time.Now()); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := EquipCustomerCosmetic(ctx, account.ID, bot.ID, CosmeticSlotBotSkin, "skin-neon-grid"); err != nil {
		t.Fatalf("account equip over licence-era columns: %v", err)
	}
	if _, err := EquipCosmetic(ctx, bot.ID, CosmeticSlotWeaponSkin, "weapon-solar-flare"); err != nil {
		t.Fatalf("bot-key equip over licence-era columns: %v", err)
	}
	var nullRows int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM bot_cosmetic_loadout
		WHERE bot_id = $1 AND license_id IS NULL AND account_id IS NULL`, bot.ID).Scan(&nullRows); err != nil {
		t.Fatalf("inspect loadout columns: %v", err)
	}
	if nullRows != 2 {
		t.Fatalf("loadout rows with null licence columns = %d, want both equips", nullRows)
	}
	equipped, err := GetEquippedCosmetics(ctx, bot.ID)
	if err != nil || equipped[CosmeticSlotBotSkin] != "neon_grid" || equipped[CosmeticSlotWeaponSkin] != "solar_flare" {
		t.Fatalf("loadout over licence-era columns = (%v, %v)", equipped, err)
	}
}

func TestPostgresConcurrentCosmeticsSchemaRepairIsSerialized(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	const workers = 8
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			results <- EnsureCosmeticsSchema(context.Background())
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-results; err != nil {
			t.Fatalf("concurrent EnsureCosmeticsSchema: %v", err)
		}
	}
	var constraintCount int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_constraint
		WHERE conrelid = 'bot_cosmetic_loadout'::regclass
		  AND conname = 'bot_cosmetic_loadout_slot_check'`).Scan(&constraintCount); err != nil {
		t.Fatal(err)
	}
	if constraintCount != 1 {
		t.Fatalf("slot check constraint count = %d, want 1", constraintCount)
	}
}

// TestPostgresAccountRowIsFirstEquipLock pins the lock order every
// account-scoped mutation shares: the account row first, then the bot link.
// A subscription sync landing at the same moment therefore serialises with
// the equip that reads the flag, instead of racing it.
func TestPostgresAccountRowIsFirstEquipLock(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	bot := createCustomerCosmeticsTestBot(t, ctx, "lock-order")
	account, err := UpsertVerifiedCustomerAccount(ctx, "", "https://id.example", "locks-subject", "Locks")
	if err != nil {
		t.Fatal(err)
	}
	linkSubscriptionTestBot(t, ctx, account, bot)

	locker, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback(context.Background())
	if _, err := locker.Exec(ctx, `SELECT 1 FROM customer_accounts WHERE id = $1 FOR UPDATE`, account.ID); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := EquipCustomerCosmetic(context.Background(), account.ID, bot.ID, CosmeticSlotAttachment, "attachment-signal-antenna")
		result <- err
	}()
	waitForBlockedCustomerStatement(t, ctx, "customer_accounts")

	probe, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Exec(ctx, `SELECT 1 FROM account_bot_links WHERE bot_id = $1 FOR UPDATE NOWAIT`, bot.ID); err != nil {
		probe.Rollback(ctx)
		t.Fatalf("equip locked the bot link before the account row: %v", err)
	}
	if err := probe.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := locker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("equip after account unlock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("equip did not finish after account unlock")
	}
}

func waitForBlockedCustomerStatement(t *testing.T, ctx context.Context, queryFragment string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		pattern := fmt.Sprintf("%%%s%%", queryFragment)
		if err := Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE pid <> pg_backend_pid()
				  AND wait_event_type = 'Lock'
				  AND query LIKE $1
			)`, pattern).Scan(&waiting); err != nil {
			t.Fatalf("inspect blocked customer statement: %v", err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for blocked statement containing %q", queryFragment)
}
