package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

func TestPostgresCustomerCosmeticsAccountOwnershipAndExclusiveAssignment(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	botOne := createCustomerCosmeticsTestBot(t, ctx, "one")
	botTwo := createCustomerCosmeticsTestBot(t, ctx, "two")
	botOther := createCustomerCosmeticsTestBot(t, ctx, "other")

	// Fulfillment may arrive before first login. The verified identity claims
	// exactly that pending email account; a different identity cannot reuse it.
	firstLicense, created, err := GrantCosmeticLicense(ctx, "Owner@Example.com", "skin-neon-grid", "stripe", "checkout-1")
	if err != nil || !created {
		t.Fatalf("GrantCosmeticLicense first = (%+v, %v, %v)", firstLicense, created, err)
	}
	pendingAccountID := *firstLicense.AccountID
	account, err := UpsertVerifiedCustomerAccount(ctx, "owner@example.com", "https://id.example", "owner-subject", "Owner")
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	if account.ID != pendingAccountID || account.EmailVerifiedAt == nil {
		t.Fatalf("verified account = %+v, pending account ID = %s", account, pendingAccountID)
	}
	/*
	 * A second identity presenting the same address must not end up holding
	 * this account. It no longer ends up holding an *error* either, and that
	 * change is deliberate rather than incidental.
	 *
	 * The old expectation was ErrCustomerIdentityConflict, and it was produced
	 * by the UNIQUE index on the address: the insert carried one, the row
	 * above already had it, and the unique violation surfaced as a conflict.
	 * Linking now empties that column — that is the entire point of the change
	 * — so by the time this call is made there is no stored address left for
	 * anything to collide with. A guarantee that was a side effect of holding
	 * the data cannot outlive the data.
	 *
	 * What matters is unchanged and is what is asserted instead: the intruder
	 * does not get this account. Claiming an existing row still requires that
	 * row to be unlinked, so the owner's bots, licenses and handle stay with
	 * the owner, and the newcomer gets an empty account of its own. Accounts
	 * is the authority on one-verified-address-per-identity now, which is
	 * where that rule belongs.
	 */
	intruder, err := UpsertVerifiedCustomerAccount(ctx, "owner@example.com", "https://id.example", "attacker-subject", "Other")
	if err != nil {
		t.Fatalf("second identity bind error = %v, want a separate account", err)
	}
	if intruder.ID == account.ID {
		t.Fatalf("second identity took over the owner's account %s", account.ID)
	}
	if intruder.Email != "" {
		t.Fatalf("second identity stored an address: %q", intruder.Email)
	}

	/*
	 * Fulfilment is keyed by account from here on, and these three assertions
	 * are the reason the account-keyed seam had to exist.
	 *
	 * They used to be written against the address. That worked because the
	 * address was a durable identifier: a late Stripe webhook naming it found
	 * the same row every time. Linking empties the column, so the address
	 * stops being able to find this account at all — see the test below, which
	 * pins that consequence rather than leaving it to be discovered by a
	 * webhook in production.
	 */
	idempotent, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "stripe", "checkout-1")
	if err != nil || created || idempotent.ID != firstLicense.ID {
		t.Fatalf("idempotent grant = (%+v, %v, %v)", idempotent, created, err)
	}
	if _, _, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "weapon-solar-flare", "stripe", "checkout-1"); !errors.Is(err, ErrCosmeticLicenseGrantConflict) {
		t.Fatalf("conflicting external reference error = %v", err)
	}
	if _, _, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "weapon-solar-flare", "stripe", ""); !errors.Is(err, ErrCosmeticLicenseReferenceRequired) {
		t.Fatalf("provider grant without reference error = %v, want ErrCosmeticLicenseReferenceRequired", err)
	}

	linkedBotOne, err := LinkBotToCustomerAccount(ctx, account.ID, botOne.ID)
	if err != nil {
		t.Fatalf("link bot one: %v", err)
	}
	if linkedBotOne.AvatarColor != botOne.AvatarColor || linkedBotOne.DefaultWeapon != botOne.DefaultWeapon {
		t.Fatalf("linked bot one preview metadata = %+v, want color=%q weapon=%q", linkedBotOne, botOne.AvatarColor, botOne.DefaultWeapon)
	}
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, botTwo.ID); err != nil {
		t.Fatalf("link bot two: %v", err)
	}
	linkedBots, err := ListAccountBots(ctx, account.ID)
	if err != nil {
		t.Fatalf("ListAccountBots: %v", err)
	}
	if len(linkedBots) != 2 || linkedBots[0].AvatarColor != botOne.AvatarColor || linkedBots[0].DefaultWeapon != botOne.DefaultWeapon {
		t.Fatalf("linked bot inventory preview metadata = %+v", linkedBots)
	}
	change, err := AssignCosmeticLicense(ctx, account.ID, firstLicense.ID, &botOne.ID)
	if err != nil || change.CurrentBotID == nil || *change.CurrentBotID != botOne.ID {
		t.Fatalf("assign first license = (%+v, %v)", change, err)
	}
	if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, botOne.ID, firstLicense.ID); err != nil {
		t.Fatalf("equip exact first license: %v", err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, firstLicense.ID, &botOne.ID); err != nil {
		t.Fatalf("repeat same assignment: %v", err)
	}
	equippedAfterIdempotentAssign, err := GetEquippedCosmetics(ctx, botOne.ID)
	if err != nil || equippedAfterIdempotentAssign[CosmeticSlotBotSkin] != "neon_grid" {
		t.Fatalf("same assignment removed exact loadout: (%v, %v)", equippedAfterIdempotentAssign, err)
	}

	// Each purchase creates a stable, independently assignable copy.
	secondLicense, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "")
	if err != nil || !created || secondLicense.ID == firstLicense.ID {
		t.Fatalf("second copy = (%+v, %v, %v)", secondLicense, created, err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, secondLicense.ID, &botTwo.ID); err != nil {
		t.Fatalf("assign second copy: %v", err)
	}
	if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, botOne.ID, secondLicense.ID); !errors.Is(err, ErrCustomerBotNotLinked) {
		t.Fatalf("equip license on wrong bot error = %v", err)
	}

	// PostgreSQL itself enforces one assignment per license.
	_, err = Pool.Exec(ctx, `
		INSERT INTO cosmetic_license_assignments (license_id, account_id, bot_id)
		VALUES ($1, $2, $3)`, firstLicense.ID, account.ID, botTwo.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("duplicate license assignment error = %v, want unique violation", err)
	}

	otherAccount, err := UpsertVerifiedCustomerAccount(ctx, "other@example.com", "https://id.example", "other-subject", "Other")
	if err != nil {
		t.Fatalf("create other account: %v", err)
	}
	if _, err := LinkBotToCustomerAccount(ctx, otherAccount.ID, botOther.ID); err != nil {
		t.Fatalf("link other bot: %v", err)
	}
	thirdLicense, _, err := GrantCosmeticLicense(ctx, "owner@example.com", "weapon-solar-flare", "manual", "copy-3")
	if err != nil {
		t.Fatalf("grant third license: %v", err)
	}
	_, err = Pool.Exec(ctx, `
		INSERT INTO cosmetic_license_assignments (license_id, account_id, bot_id)
		VALUES ($1, $2, $3)`, thirdLicense.ID, otherAccount.ID, botOther.ID)
	pgErr = nil
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("cross-account assignment error = %v, want FK violation", err)
	}
	if _, err := Pool.Exec(ctx, `UPDATE cosmetic_licenses SET status = 'refunded' WHERE id = $1`, thirdLicense.ID); err != nil {
		t.Fatalf("mark exact copy refunded: %v", err)
	}
	if _, revoked, err := RevokeCosmeticLicense(ctx, thirdLicense.ID); err != nil || revoked {
		t.Fatalf("revoke refunded copy = (%v, %v), want unchanged", revoked, err)
	}
	refunded, err := getCosmeticLicense(ctx, thirdLicense.ID)
	if err != nil || refunded.Status != "refunded" {
		t.Fatalf("terminal status after revoke = (%+v, %v), want refunded", refunded, err)
	}

	// A revoked key keeps ownership intact but cannot receive a new assignment
	// or equip an already-assigned license. Unassign remains available.
	if err := DeactivateAPIKey(ctx, botTwo.APIKeyID); err != nil {
		t.Fatalf("DeactivateAPIKey: %v", err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, firstLicense.ID, &botTwo.ID); !errors.Is(err, ErrCustomerBotKeyInactive) {
		t.Fatalf("assignment to inactive bot error = %v", err)
	}
	if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, botTwo.ID, secondLicense.ID); !errors.Is(err, ErrCustomerBotKeyInactive) {
		t.Fatalf("equip on inactive bot error = %v", err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, secondLicense.ID, nil); err != nil {
		t.Fatalf("unassign from inactive bot: %v", err)
	}

	assignment, revoked, err := RevokeCosmeticLicense(ctx, firstLicense.ID)
	if err != nil || !revoked || assignment.PreviousBotID == nil || *assignment.PreviousBotID != botOne.ID {
		t.Fatalf("revoke first license = (%+v, %v, %v)", assignment, revoked, err)
	}
	if _, revokedAgain, err := RevokeCosmeticLicense(ctx, firstLicense.ID); err != nil || revokedAgain {
		t.Fatalf("idempotent revoke = (%v, %v)", revokedAgain, err)
	}
	equipped, err := GetEquippedCosmetics(ctx, botOne.ID)
	if err != nil || equipped[CosmeticSlotBotSkin] != "standard" {
		t.Fatalf("post-revoke equipped cosmetics = (%v, %v)", equipped, err)
	}

	for index, preservedStatus := range []string{"refunded", "chargeback"} {
		license, _, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "attachment-orbital-halo", "manual",
			fmt.Sprintf("preserve-status-%d", index))
		if err != nil {
			t.Fatalf("grant %s license: %v", preservedStatus, err)
		}
		if _, err := Pool.Exec(ctx, `UPDATE cosmetic_licenses SET status = $2 WHERE id = $1`, license.ID, preservedStatus); err != nil {
			t.Fatalf("set %s status: %v", preservedStatus, err)
		}
		change, revoked, err := RevokeCosmeticLicense(ctx, license.ID)
		if err != nil || revoked || change.License.Status != preservedStatus {
			t.Fatalf("revoke preserved status %s = (%+v, %v, %v)", preservedStatus, change, revoked, err)
		}
	}
}

func TestPostgresPlatformChangeOrderingDoesNotDeadlockLegacyClaimAndRevoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(useFreshPostgresSchema(t), 15*time.Second)
	defer cancel()
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	bot := createCustomerCosmeticsTestBot(t, ctx, "change-lock-order")
	if created, err := GrantCosmeticEntitlement(
		ctx,
		bot.ID,
		"skin-neon-grid",
		"manual",
		"change-lock-order-license",
	); err != nil || !created {
		t.Fatalf("grant legacy entitlement = (%v, %v)", created, err)
	}
	var licenseID string
	if err := Pool.QueryRow(ctx, `
		SELECT id FROM cosmetic_licenses
		WHERE legacy_bot_id = $1 AND cosmetic_id = 'skin-neon-grid'`, bot.ID).Scan(&licenseID); err != nil {
		t.Fatalf("load legacy license: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"change-lock-order@example.com",
		"https://id.example",
		"change-lock-order",
		"Change Lock Order",
	)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	const barrierClassID = 72104
	const barrierObjectID = 11
	if _, err := Pool.Exec(ctx, `
		CREATE FUNCTION test_block_legacy_revoke() RETURNS TRIGGER
		LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.status = 'active' AND NEW.status = 'revoked' THEN
				PERFORM pg_advisory_xact_lock(72104, 11);
			END IF;
			RETURN NEW;
		END
		$$;
		CREATE TRIGGER test_block_legacy_revoke
		BEFORE UPDATE OF status ON cosmetic_licenses
		FOR EACH ROW EXECUTE FUNCTION test_block_legacy_revoke()`); err != nil {
		t.Fatalf("install revoke barrier: %v", err)
	}
	releaseBarrier := holdPostgresTestAdvisoryBarrier(t, ctx, barrierClassID, barrierObjectID)

	type revokeResult struct {
		revoked bool
		err     error
	}
	revokeDone := make(chan revokeResult, 1)
	go func() {
		_, revoked, err := RevokeCosmeticLicense(ctx, licenseID)
		revokeDone <- revokeResult{revoked: revoked, err: err}
	}()

	waitForPostgresTestCondition(t, ctx, "legacy revoke barrier", `
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
	waitForPostgresTestCondition(t, ctx, "legacy claim row lock", `
		SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%FROM cosmetic_licenses%'
			  AND query LIKE '%legacy_bot_id%'
		)`)

	releaseBarrier()

	var revoked revokeResult
	select {
	case revoked = <-revokeDone:
	case <-ctx.Done():
		t.Fatalf("legacy revoke did not finish: %v", ctx.Err())
	}
	var linkErr error
	select {
	case linkErr = <-linkDone:
	case <-ctx.Done():
		t.Fatalf("legacy claim did not finish: %v", ctx.Err())
	}
	if revoked.err != nil || !revoked.revoked || linkErr != nil {
		t.Fatalf("concurrent legacy revoke/link = (revoked %v, revoke error %v, link error %v)", revoked.revoked, revoked.err, linkErr)
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

func TestPostgresExactPR69CosmeticsSchemaUpgradeAndLegacyRevoke(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	// Recreate the exact cosmetics-related shape that PR #69 shipped: no
	// customer/account tables and no license/account columns on the loadout.
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
		VALUES ('old-bot', 'skin-neon-grid', 'stripe', 'old-order-line-1');
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
	if err := EnsureCosmeticSubscriptionsSchema(ctx); err != nil {
		t.Fatalf("install subscription schema for W1b.4: %v", err)
	}
	if err := EnsureCosmeticAdminMembershipsSchema(ctx); err != nil {
		t.Fatalf("install membership schema for W1b.4: %v", err)
	}
	// W1b.4 lands after the core bot timestamp migration. The fixture above is
	// intentionally the older PR #69 cosmetics shape, so add only the
	// intervening non-cosmetics columns before exercising the platform cutover.
	if _, err := Pool.Exec(ctx, `
		ALTER TABLE bots ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
		ALTER TABLE bots ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`); err != nil {
		t.Fatalf("install intervening bot timestamps: %v", err)
	}
	if err := EnsurePlatformAuthoritySchema(ctx); err != nil {
		t.Fatalf("complete W1b.4 authority upgrade: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO bot_cosmetic_loadout (bot_id, slot, cosmetic_id)
		VALUES ('old-bot', 'trail', 'trail-standard')`); err != nil {
		t.Fatalf("upgraded loadout constraint rejected trail slot: %v", err)
	}
	var migratedTrailItem string
	if err := Pool.QueryRow(ctx, `
		SELECT cosmetic_id FROM bot_cosmetic_loadout
		WHERE bot_id = 'old-bot' AND slot = 'trail'`).Scan(&migratedTrailItem); err != nil {
		t.Fatalf("load upgraded trail slot: %v", err)
	}
	if migratedTrailItem != "trail-standard" {
		t.Fatalf("upgraded trail slot item = %q, want trail-standard", migratedTrailItem)
	}

	var licenseID string
	if err := Pool.QueryRow(ctx, `
		SELECT id FROM cosmetic_licenses
		WHERE legacy_bot_id = 'old-bot' AND external_reference = 'old-order-line-1'`).Scan(&licenseID); err != nil {
		t.Fatalf("load upgraded legacy license: %v", err)
	}
	var migratedLoadoutLicense *string
	if err := Pool.QueryRow(ctx, `
		SELECT license_id FROM bot_cosmetic_loadout WHERE bot_id = 'old-bot' AND slot = 'bot_skin'`).Scan(&migratedLoadoutLicense); err != nil {
		t.Fatalf("load upgraded loadout: %v", err)
	}
	if migratedLoadoutLicense == nil || *migratedLoadoutLicense != licenseID {
		t.Fatalf("upgraded loadout license = %v, want %s", migratedLoadoutLicense, licenseID)
	}
	if _, err := Pool.Exec(ctx, `
		UPDATE cosmetic_licenses SET status = 'chargeback', assigned_bot_id = NULL WHERE id = $1`, licenseID); err != nil {
		t.Fatalf("mark terminal legacy license: %v", err)
	}
	if _, err := GrantCosmeticEntitlement(ctx, "old-bot", "skin-neon-grid", "manual", ""); err != nil {
		t.Fatalf("replay legacy grant: %v", err)
	}
	var terminalStatus string
	var terminalAssignment *string
	if err := Pool.QueryRow(ctx, `SELECT status, assigned_bot_id FROM cosmetic_licenses WHERE id = $1`, licenseID).
		Scan(&terminalStatus, &terminalAssignment); err != nil {
		t.Fatal(err)
	}
	if terminalStatus != "chargeback" || terminalAssignment != nil {
		t.Fatalf("legacy replay changed terminal license: status=%q assignment=%v", terminalStatus, terminalAssignment)
	}
	// A separate active legacy generation exercises account recovery. The
	// terminal copy above must remain terminal; W1b.4 deliberately forbids
	// restoring it to active for test setup or runtime recovery.
	if created, err := GrantCosmeticEntitlement(ctx, "old-bot", "weapon-solar-flare", "stripe", "old-order-line-2"); err != nil || !created {
		t.Fatalf("create active legacy recovery generation = (%v, %v)", created, err)
	}
	if _, err := Pool.Exec(ctx, `DELETE FROM api_keys WHERE id = 'old-key'`); err != nil {
		t.Fatalf("delete lost legacy key: %v", err)
	}
	recovered, claimed, err := GrantCosmeticLicense(ctx, "recovered@example.com", "weapon-solar-flare", "stripe", "old-order-line-2")
	if err != nil || !claimed || recovered.ID == licenseID || recovered.AccountID == nil || recovered.LegacyBotID != nil {
		t.Fatalf("recover legacy purchase by email/reference = (%+v, %v, %v)", recovered, claimed, err)
	}

	if _, revoked, err := RevokeCosmeticLicense(ctx, recovered.ID); err != nil || !revoked {
		t.Fatalf("revoke upgraded legacy license = (%v, %v)", revoked, err)
	}
	var remaining int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM bot_cosmetic_loadout WHERE license_id = $1`, recovered.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("legacy paid loadout rows after revoke = %d, want 0", remaining)
	}
}

func TestPostgresLegacyBotEntitlementMigratesAndSurvivesBotDeletion(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	bot := createCustomerCosmeticsTestBot(t, ctx, "legacy")

	// Recreate the exact pre-account PR #69 storage shape, then rerun startup
	// schema repair to materialize the durable legacy license.
	if _, err := Pool.Exec(ctx, `
		INSERT INTO cosmetic_entitlements (bot_id, cosmetic_id, source, external_reference)
		VALUES ($1, 'skin-neon-grid', 'legacy-test', 'legacy-order')`, bot.ID); err != nil {
		t.Fatalf("insert legacy entitlement: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO bot_cosmetic_loadout (bot_id, slot, cosmetic_id)
		VALUES ($1, 'bot_skin', 'skin-neon-grid')`, bot.ID); err != nil {
		t.Fatalf("insert legacy loadout: %v", err)
	}
	if err := EnsureCosmeticsSchema(ctx); err != nil {
		t.Fatalf("EnsureCosmeticsSchema legacy migration: %v", err)
	}

	var licenseID string
	var owner, legacyBot, assignedBot *string
	if err := Pool.QueryRow(ctx, `
		SELECT id, account_id, legacy_bot_id, assigned_bot_id
		FROM cosmetic_licenses WHERE external_reference = 'legacy-order'`).
		Scan(&licenseID, &owner, &legacyBot, &assignedBot); err != nil {
		t.Fatalf("load migrated legacy license: %v", err)
	}
	if owner != nil || legacyBot == nil || *legacyBot != bot.ID || assignedBot == nil || *assignedBot != bot.ID {
		t.Fatalf("migrated legacy state owner=%v legacy=%v assigned=%v", owner, legacyBot, assignedBot)
	}

	account, err := UpsertVerifiedCustomerAccount(ctx, "legacy@example.com", "https://id.example", "legacy-subject", "Legacy Owner")
	if err != nil {
		t.Fatalf("create legacy owner: %v", err)
	}
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("claim legacy bot: %v", err)
	}
	claimed, err := getCosmeticLicense(ctx, licenseID)
	if err != nil || claimed.AccountID == nil || *claimed.AccountID != account.ID || claimed.LegacyBotID != nil ||
		claimed.AssignedBotID == nil || *claimed.AssignedBotID != bot.ID || !claimed.Equipped {
		t.Fatalf("claimed legacy license = (%+v, %v)", claimed, err)
	}

	// Even destructive stale-key cleanup can remove the bot/link/assignment but
	// never the purchased account license.
	if _, err := Pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, bot.APIKeyID); err != nil {
		t.Fatalf("delete stale API key: %v", err)
	}
	claimed, err = getCosmeticLicense(ctx, licenseID)
	if err != nil || claimed.AccountID == nil || *claimed.AccountID != account.ID || claimed.AssignedBotID != nil {
		t.Fatalf("license after bot deletion = (%+v, %v)", claimed, err)
	}

	// If stale cleanup deleted the bot before it could be linked, replaying the
	// trusted fulfillment reference claims the exact orphaned legacy copy for
	// the purchase email instead of minting a duplicate.
	orphanBot := createCustomerCosmeticsTestBot(t, ctx, "legacy-orphan")
	if _, err := Pool.Exec(ctx, `
		INSERT INTO cosmetic_entitlements (bot_id, cosmetic_id, source, external_reference)
		VALUES ($1, 'skin-neon-grid', 'legacy-test', 'legacy-orphan-order')`, orphanBot.ID); err != nil {
		t.Fatalf("insert orphan legacy entitlement: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO bot_cosmetic_loadout (bot_id, slot, cosmetic_id)
		VALUES ($1, 'bot_skin', 'skin-neon-grid')`, orphanBot.ID); err != nil {
		t.Fatalf("insert orphan legacy loadout: %v", err)
	}
	if err := EnsureCosmeticsSchema(ctx); err != nil {
		t.Fatalf("migrate orphan legacy entitlement: %v", err)
	}
	var orphanLicenseID string
	if err := Pool.QueryRow(ctx, `
		SELECT id FROM cosmetic_licenses
		WHERE source = 'legacy-test' AND external_reference = 'legacy-orphan-order'`).Scan(&orphanLicenseID); err != nil {
		t.Fatalf("load orphan legacy license: %v", err)
	}
	if _, err := Pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, orphanBot.APIKeyID); err != nil {
		t.Fatalf("delete orphan bot key: %v", err)
	}
	recovered, newlyClaimed, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "legacy-test", "legacy-orphan-order")
	if err != nil || !newlyClaimed || recovered.ID != orphanLicenseID ||
		recovered.AccountID == nil || *recovered.AccountID != account.ID || recovered.LegacyBotID != nil || recovered.AssignedBotID != nil {
		t.Fatalf("recover orphan legacy license = (%+v, %v, %v), want ID %s", recovered, newlyClaimed, err, orphanLicenseID)
	}
	replayed, newlyClaimed, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "legacy-test", "legacy-orphan-order")
	if err != nil || newlyClaimed || replayed.ID != orphanLicenseID {
		t.Fatalf("idempotent orphan recovery = (%+v, %v, %v)", replayed, newlyClaimed, err)
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
		  AND conname = 'bot_cosmetic_loadout_assignment_fk'`).Scan(&constraintCount); err != nil {
		t.Fatal(err)
	}
	if constraintCount != 1 {
		t.Fatalf("assignment FK count = %d, want 1", constraintCount)
	}
}

func TestPostgresAccountRowIsFirstAssignmentLock(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	bot := createCustomerCosmeticsTestBot(t, ctx, "lock-order")
	account, err := UpsertVerifiedCustomerAccount(ctx, "locks@example.com", "https://id.example", "locks-subject", "Locks")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatal(err)
	}
	license, _, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "lock-order")
	if err != nil {
		t.Fatal(err)
	}

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
		_, err := AssignCosmeticLicense(context.Background(), account.ID, license.ID, &bot.ID)
		result <- err
	}()
	waitForBlockedCustomerStatement(t, ctx, "customer_accounts")

	probe, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Exec(ctx, `SELECT 1 FROM cosmetic_licenses WHERE id = $1 FOR UPDATE NOWAIT`, license.ID); err != nil {
		probe.Rollback(ctx)
		t.Fatalf("assignment locked license before account row: %v", err)
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
			t.Fatalf("assignment after account unlock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("assignment did not finish after account unlock")
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
