package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPostgresPlatformLicenseTransitionIsRevisionedIdempotentAndTerminal(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-transition@example.com", "https://id.example", "license-transition", "License Transition")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	bot := createCustomerCosmeticsTestBot(t, ctx, "license-transition")
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("link bot: %v", err)
	}
	license, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "license-transition-copy")
	if err != nil || !created {
		t.Fatalf("grant license = (%+v, %v, %v)", license, created, err)
	}
	if license.Revision != 1 {
		t.Fatalf("new license revision = %d, want 1", license.Revision)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, license.ID, &bot.ID); err != nil {
		t.Fatalf("assign license: %v", err)
	}
	if _, err := EquipCustomerCosmeticLicense(ctx, account.ID, bot.ID, license.ID); err != nil {
		t.Fatalf("equip license: %v", err)
	}
	var assignedRevision int64
	if err := Pool.QueryRow(ctx, `SELECT revision FROM cosmetic_licenses WHERE id = $1`, license.ID).Scan(&assignedRevision); err != nil {
		t.Fatalf("load assigned revision: %v", err)
	}

	command := PlatformLicenseTransitionCommand{
		LicenseID:         license.ID,
		TargetStatus:      "refunded",
		ExpectedRevision:  assignedRevision,
		Reason:            "order_refund",
		ProviderReference: "refund-123",
		IdempotencyKey:    "refund-license-once",
	}
	first, err := TransitionPlatformLicense(ctx, command)
	if err != nil {
		t.Fatalf("TransitionPlatformLicense: %v", err)
	}
	if first.LicenseID != license.ID || first.AccountID != account.ID || first.Status != "refunded" ||
		first.AssignedAgentID != nil || first.Revision != assignedRevision+1 {
		t.Fatalf("transition result = %+v", first)
	}
	if first.CreatedAt.Location() != time.UTC || first.UpdatedAt.Location() != time.UTC {
		t.Fatalf("transition timestamps are not canonical UTC: created=%v updated=%v", first.CreatedAt.Location(), first.UpdatedAt.Location())
	}
	var assignments, loadouts, history, changes, assignmentChanges int
	var previousAgentID, previousStatus *string
	var currentStatus string
	var historySource string
	if err := Pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM cosmetic_license_assignments WHERE license_id = $1),
			(SELECT COUNT(*) FROM bot_cosmetic_loadout WHERE license_id = $1),
			(SELECT COUNT(*) FROM platform_license_lifecycle_events WHERE license_id = $1 AND transition = 'refunded' AND revision = $2),
			(SELECT COUNT(*) FROM platform_changes WHERE subject_kind = 'license' AND subject_id = $1 AND transition = 'refunded' AND revision = $2),
			(SELECT COUNT(*) FROM platform_changes WHERE subject_kind = 'license_assignment' AND subject_id = $1 AND transition = 'unassigned' AND revision = $2),
			(SELECT previous_agent_id FROM platform_license_lifecycle_events WHERE license_id = $1 AND revision = $2),
			(SELECT source FROM platform_license_lifecycle_events WHERE license_id = $1 AND revision = $2),
			(SELECT previous_status FROM platform_license_lifecycle_events WHERE license_id = $1 AND revision = $2),
			(SELECT current_status FROM platform_license_lifecycle_events WHERE license_id = $1 AND revision = $2)`,
		license.ID, first.Revision).Scan(&assignments, &loadouts, &history, &changes, &assignmentChanges, &previousAgentID, &historySource, &previousStatus, &currentStatus); err != nil {
		t.Fatalf("inspect terminal transition: %v", err)
	}
	if assignments != 0 || loadouts != 0 || history != 1 || changes != 1 || assignmentChanges != 1 || previousAgentID == nil || *previousAgentID != bot.ID || historySource != "platform" || previousStatus == nil || *previousStatus != "active" || currentStatus != "refunded" {
		t.Fatalf("terminal transition left assignments=%d loadouts=%d history=%d changes=%d assignment changes=%d previous=%v source=%q status=%v->%q", assignments, loadouts, history, changes, assignmentChanges, previousAgentID, historySource, previousStatus, currentStatus)
	}

	replayed, err := TransitionPlatformLicense(ctx, command)
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("exact replay = (%+v, %v), want %+v", replayed, err, first)
	}
	conflictingReplay := command
	conflictingReplay.TargetStatus = "chargeback"
	if _, err := TransitionPlatformLicense(ctx, conflictingReplay); !errors.Is(err, ErrPlatformIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v, want %v", err, ErrPlatformIdempotencyConflict)
	}
	stale := command
	stale.IdempotencyKey = "stale-license-transition"
	if _, err := TransitionPlatformLicense(ctx, stale); !errors.Is(err, ErrPlatformRevisionConflict) {
		t.Fatalf("stale revision error = %v, want %v", err, ErrPlatformRevisionConflict)
	}
	differentTerminal := command
	differentTerminal.TargetStatus = "chargeback"
	differentTerminal.ExpectedRevision = first.Revision
	differentTerminal.IdempotencyKey = "different-terminal-transition"
	stronger, err := TransitionPlatformLicense(ctx, differentTerminal)
	if err != nil || stronger.Status != "chargeback" || stronger.Revision != first.Revision+1 {
		t.Fatalf("stronger terminal transition = (%+v, %v)", stronger, err)
	}
	replayedAfterStronger, err := TransitionPlatformLicense(ctx, command)
	if err != nil || !reflect.DeepEqual(replayedAfterStronger, first) {
		t.Fatalf("original replay after stronger state = (%+v, %v), want %+v", replayedAfterStronger, err, first)
	}
}

func TestPostgresPlatformLicenseAssignmentAndUnassignmentAreExactCommands(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-assignment@example.com", "https://id.example", "license-assignment", "License Assignment")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	bot := createCustomerCosmeticsTestBot(t, ctx, "exact-license-assignment")
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("link bot: %v", err)
	}
	license, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "exact-license-assignment-copy")
	if err != nil || !created {
		t.Fatalf("grant license = (%+v, %v, %v)", license, created, err)
	}

	assign := PlatformLicenseAssignmentCommand{
		LicenseID: license.ID, AgentID: bot.ID, ExpectedRevision: license.Revision,
		IdempotencyKey: "assign-license-once",
	}
	assigned, err := AssignPlatformLicense(ctx, assign)
	if err != nil {
		t.Fatalf("AssignPlatformLicense: %v", err)
	}
	if assigned.AssignedAgentID == nil || *assigned.AssignedAgentID != bot.ID || assigned.Revision != license.Revision+1 {
		t.Fatalf("assigned license = %+v", assigned)
	}
	if assigned.CreatedAt.Location() != time.UTC || assigned.UpdatedAt.Location() != time.UTC {
		t.Fatalf("assignment timestamps are not canonical UTC: created=%v updated=%v", assigned.CreatedAt.Location(), assigned.UpdatedAt.Location())
	}
	replayed, err := AssignPlatformLicense(ctx, assign)
	if err != nil || !reflect.DeepEqual(replayed, assigned) {
		t.Fatalf("assignment replay = (%+v, %v), want %+v", replayed, err, assigned)
	}
	conflicting := assign
	conflicting.AgentID = "another-agent"
	if _, err := AssignPlatformLicense(ctx, conflicting); !errors.Is(err, ErrPlatformIdempotencyConflict) {
		t.Fatalf("conflicting assignment replay error = %v, want %v", err, ErrPlatformIdempotencyConflict)
	}

	unassign := PlatformLicenseUnassignmentCommand{
		LicenseID: license.ID, ExpectedRevision: assigned.Revision,
		Reason: "customer_unassigned", IdempotencyKey: "unassign-license-once",
	}
	unassigned, err := UnassignPlatformLicense(ctx, unassign)
	if err != nil {
		t.Fatalf("UnassignPlatformLicense: %v", err)
	}
	if unassigned.AssignedAgentID != nil || unassigned.Revision != assigned.Revision+1 {
		t.Fatalf("unassigned license = %+v", unassigned)
	}
	var history, assignedChange, unassignedChange int
	if err := Pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM platform_license_lifecycle_events WHERE license_id = $1 AND transition IN ('assigned', 'unassigned')),
			(SELECT COUNT(*) FROM platform_changes WHERE subject_kind = 'license_assignment' AND subject_id = $1 AND transition = 'assigned' AND revision = $2),
			(SELECT COUNT(*) FROM platform_changes WHERE subject_kind = 'license_assignment' AND subject_id = $1 AND transition = 'unassigned' AND revision = $3)`,
		license.ID, assigned.Revision, unassigned.Revision).Scan(&history, &assignedChange, &unassignedChange); err != nil {
		t.Fatalf("inspect assignment history: %v", err)
	}
	if history != 2 || assignedChange != 1 || unassignedChange != 1 {
		t.Fatalf("assignment history=%d assigned change=%d unassigned change=%d", history, assignedChange, unassignedChange)
	}
	var assignedPrevious, unassignedCurrent *string
	var assignedCurrent, unassignedPrevious string
	if err := Pool.QueryRow(ctx, `
		SELECT assigned.previous_agent_id, assigned.current_agent_id,
		       unassigned.previous_agent_id, unassigned.current_agent_id
		FROM platform_license_lifecycle_events AS assigned
		JOIN platform_license_lifecycle_events AS unassigned ON unassigned.license_id = assigned.license_id
		WHERE assigned.license_id = $1 AND assigned.transition = 'assigned'
		  AND unassigned.transition = 'unassigned'`, license.ID).Scan(
		&assignedPrevious, &assignedCurrent, &unassignedPrevious, &unassignedCurrent,
	); err != nil {
		t.Fatalf("inspect assignment endpoints: %v", err)
	}
	if assignedPrevious != nil || assignedCurrent != bot.ID || unassignedPrevious != bot.ID || unassignedCurrent != nil {
		t.Fatalf("assignment endpoints = previous %v current %q; unassignment previous %q current %v", assignedPrevious, assignedCurrent, unassignedPrevious, unassignedCurrent)
	}
	firstPage, nextCursor, err := ListPlatformLicenseLifecycleEvents(ctx, license.ID, 0, 2)
	if err != nil || len(firstPage) != 2 || nextCursor == 0 || firstPage[0].Transition != "created" || firstPage[0].Source != "manual" || firstPage[0].Reason != "arena_license_grant" || firstPage[1].Transition != "assigned" || firstPage[1].Source != "platform" {
		t.Fatalf("first lifecycle page = (%+v, %d, %v)", firstPage, nextCursor, err)
	}
	secondPage, finalCursor, err := ListPlatformLicenseLifecycleEvents(ctx, license.ID, nextCursor, 2)
	if err != nil || len(secondPage) != 1 || finalCursor != 0 || secondPage[0].Transition != "unassigned" {
		t.Fatalf("second lifecycle page = (%+v, %d, %v)", secondPage, finalCursor, err)
	}
	stale := assign
	stale.IdempotencyKey = "stale-assignment-command"
	if _, err := AssignPlatformLicense(ctx, stale); !errors.Is(err, ErrPlatformRevisionConflict) {
		t.Fatalf("stale assignment error = %v, want %v", err, ErrPlatformRevisionConflict)
	}
}

func TestPostgresPlatformLicenseConcurrentTerminalTransitionsCommitOneWinner(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-race@example.com", "https://id.example", "license-race", "License Race")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	license, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "license-race-copy")
	if err != nil || !created {
		t.Fatalf("grant license = (%+v, %v, %v)", license, created, err)
	}
	commands := []PlatformLicenseTransitionCommand{
		{LicenseID: license.ID, TargetStatus: "refunded", ExpectedRevision: license.Revision, Reason: "concurrent_refund", IdempotencyKey: "concurrent-refund-command"},
		{LicenseID: license.ID, TargetStatus: "chargeback", ExpectedRevision: license.Revision, Reason: "concurrent_chargeback", IdempotencyKey: "concurrent-chargeback-command"},
	}
	results := make([]*PlatformCosmeticLicense, len(commands))
	errorsSeen := make([]error, len(commands))
	var wait sync.WaitGroup
	for index := range commands {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errorsSeen[index] = TransitionPlatformLicense(context.Background(), commands[index])
		}(index)
	}
	wait.Wait()
	winners := 0
	for index := range results {
		if errorsSeen[index] == nil {
			winners++
			continue
		}
		if !errors.Is(errorsSeen[index], ErrPlatformRevisionConflict) {
			t.Fatalf("transition %d error = %v, want revision conflict", index, errorsSeen[index])
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent terminal winners = %d, want 1; results=%+v errors=%v", winners, results, errorsSeen)
	}
	var status string
	var revision int64
	var terminalEvents int
	if err := Pool.QueryRow(ctx, `
		SELECT licenses.status, licenses.revision,
		       (SELECT COUNT(*) FROM platform_license_lifecycle_events
		        WHERE license_id = licenses.id AND transition IN ('refunded', 'chargeback'))
		FROM cosmetic_licenses AS licenses WHERE licenses.id = $1`, license.ID).Scan(&status, &revision, &terminalEvents); err != nil {
		t.Fatalf("inspect concurrent transition: %v", err)
	}
	if (status != "refunded" && status != "chargeback") || revision != license.Revision+1 || terminalEvents != 1 {
		t.Fatalf("concurrent terminal state = (%q, %d, %d events)", status, revision, terminalEvents)
	}
}

func TestPostgresPlatformLicenseEveryTerminalStatusIsAbsorbing(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-terminal-states@example.com", "https://id.example", "license-terminal-states", "License Terminal States")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	statuses := []string{"refunded", "revoked", "chargeback", "expired"}
	for _, status := range statuses {
		status := status
		t.Run(status, func(t *testing.T) {
			reference := "terminal-state-" + status
			license, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", reference)
			if err != nil || !created {
				t.Fatalf("grant license = (%+v, %v, %v)", license, created, err)
			}
			transitioned, err := TransitionPlatformLicense(ctx, PlatformLicenseTransitionCommand{
				LicenseID: license.ID, TargetStatus: status, ExpectedRevision: license.Revision,
				Reason: "terminal_state_test", IdempotencyKey: "terminal-state-" + status,
			})
			if err != nil || transitioned.Status != status {
				t.Fatalf("transition %s = (%+v, %v)", status, transitioned, err)
			}
			replayed, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", reference)
			if err != nil || created || replayed.Status != status || replayed.Revision != transitioned.Revision {
				t.Fatalf("grant replay after %s = (%+v, %v, %v)", status, replayed, created, err)
			}
		})
	}
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("restart terminal state schema: %v", err)
	}
	for _, status := range statuses {
		var storedStatus string
		if err := Pool.QueryRow(ctx, `SELECT status FROM cosmetic_licenses WHERE external_reference = $1`, "terminal-state-"+status).Scan(&storedStatus); err != nil {
			t.Fatalf("load restarted %s license: %v", status, err)
		}
		if storedStatus != status {
			t.Fatalf("restarted terminal status = %q, want %q", storedStatus, status)
		}
	}
}

func TestPostgresPlatformLicenseHistoryIsBoundedAndIndexCompatible(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-history@example.com", "https://id.example", "license-history", "License History")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO cosmetic_licenses (
			id, account_id, cosmetic_id, status, source, external_reference, revision, granted_at, updated_at
		)
		SELECT 'irrelevant-license-' || series::TEXT, $1, 'skin-neon-grid', 'active',
		       'manual', 'irrelevant-history-' || series::TEXT, 1, NOW(), NOW()
		FROM generate_series(1, 500) AS series`, account.ID); err != nil {
		t.Fatalf("seed irrelevant licenses: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO platform_license_lifecycle_events (
			license_id, account_id, status, transition, revision, reason, occurred_at
		)
		SELECT 'irrelevant-license-' || series::TEXT, $1, 'active', 'created', 1,
		       'large_history_fixture', NOW()
		FROM generate_series(1, 500) AS series`, account.ID); err != nil {
		t.Fatalf("seed irrelevant license history: %v", err)
	}
	license, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "bounded-history-target")
	if err != nil || !created {
		t.Fatalf("grant target license = (%+v, %v, %v)", license, created, err)
	}
	if _, err := Pool.Exec(ctx, `ANALYZE platform_license_lifecycle_events`); err != nil {
		t.Fatalf("analyze lifecycle history: %v", err)
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lifecycle explain: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("force index lifecycle plan: %v", err)
	}
	rows, err := tx.Query(ctx, `
		EXPLAIN (COSTS OFF)
		SELECT event_id, license_id, account_id, status, previous_status, current_status,
		       previous_agent_id, current_agent_id,
		       transition, revision, source, reason, provider_reference, occurred_at
		FROM platform_license_lifecycle_events
		WHERE license_id = $1 AND (license_id, event_id) > ($1, 0)
		ORDER BY license_id, event_id
		LIMIT 101`, license.ID)
	if err != nil {
		t.Fatalf("explain lifecycle history: %v", err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatalf("scan lifecycle plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("lifecycle plan rows: %v", err)
	}
	rows.Close()
	if planText := plan.String(); !strings.Contains(planText, "idx_platform_license_lifecycle_license") || strings.Contains(planText, "Seq Scan") {
		t.Fatalf("lifecycle history plan is not index bounded:\n%s", planText)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit lifecycle explain: %v", err)
	}
	events, cursor, err := ListPlatformLicenseLifecycleEvents(ctx, license.ID, 0, 1)
	if err != nil || len(events) != 1 || cursor != 0 {
		t.Fatalf("bounded lifecycle page = (%+v, %d, %v)", events, cursor, err)
	}
}

func TestPostgresPlatformLicenseTransitionRollsBackOnHistoryFailure(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-rollback@example.com", "https://id.example", "license-rollback", "License Rollback")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	bot := createCustomerCosmeticsTestBot(t, ctx, "license-rollback")
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("link bot: %v", err)
	}
	license, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "license-rollback-copy")
	if err != nil || !created {
		t.Fatalf("grant license = (%+v, %v, %v)", license, created, err)
	}
	assigned, err := AssignPlatformLicense(ctx, PlatformLicenseAssignmentCommand{
		LicenseID: license.ID, AgentID: bot.ID, ExpectedRevision: license.Revision, IdempotencyKey: "rollback-assignment-command",
	})
	if err != nil {
		t.Fatalf("assign license: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO platform_license_lifecycle_events (
			license_id, account_id, status, assigned_agent_id, transition, revision, reason
		) VALUES ($1, $2, 'active', $3, 'assigned', $4, 'forced_history_conflict')`,
		license.ID, account.ID, bot.ID, assigned.Revision+1); err != nil {
		t.Fatalf("seed history conflict: %v", err)
	}
	_, err = TransitionPlatformLicense(ctx, PlatformLicenseTransitionCommand{
		LicenseID: license.ID, TargetStatus: "revoked", ExpectedRevision: assigned.Revision,
		Reason: "rollback_probe", IdempotencyKey: "rollback-terminal-command",
	})
	if err == nil {
		t.Fatal("terminal transition unexpectedly committed through history conflict")
	}
	var status string
	var revision int64
	var assignmentCount, loadoutCount int
	if err := Pool.QueryRow(ctx, `
		SELECT licenses.status, licenses.revision,
		       (SELECT COUNT(*) FROM cosmetic_license_assignments WHERE license_id = licenses.id),
		       (SELECT COUNT(*) FROM bot_cosmetic_loadout WHERE license_id = licenses.id)
		FROM cosmetic_licenses AS licenses WHERE licenses.id = $1`, license.ID).Scan(
		&status, &revision, &assignmentCount, &loadoutCount,
	); err != nil {
		t.Fatalf("inspect rolled-back transition: %v", err)
	}
	if status != "active" || revision != assigned.Revision || assignmentCount != 1 {
		t.Fatalf("rollback state = status %q revision %d assignments %d loadouts %d", status, revision, assignmentCount, loadoutCount)
	}
}

func TestPostgresPlatformLicenseLifecycleBackfillsTerminalStateAndRestarts(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-backfill@example.com", "https://id.example", "license-backfill", "License Backfill")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	bot := createCustomerCosmeticsTestBot(t, ctx, "license-backfill-assigned")
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("link backfill bot: %v", err)
	}
	assignedLicense, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "pre-w1b4-assigned")
	if err != nil || !created {
		t.Fatalf("grant assigned backfill license = (%+v, %v, %v)", assignedLicense, created, err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, assignedLicense.ID, &bot.ID); err != nil {
		t.Fatalf("assign backfill license: %v", err)
	}
	terminalAssignedLicense, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "attachment-orbital-halo", "manual", "pre-w1b4-terminal-assigned")
	if err != nil || !created {
		t.Fatalf("grant terminal assigned backfill license = (%+v, %v, %v)", terminalAssignedLicense, created, err)
	}
	if _, err := AssignCosmeticLicense(ctx, account.ID, terminalAssignedLicense.ID, &bot.ID); err != nil {
		t.Fatalf("assign terminal backfill license: %v", err)
	}
	if _, err := Pool.Exec(ctx, `UPDATE cosmetic_licenses SET status = 'refunded' WHERE id = $1`, terminalAssignedLicense.ID); err != nil {
		t.Fatalf("seed pre-W1b.4 terminal assignment divergence: %v", err)
	}
	if err := removePlatformLicenseLifecycleSchemaForTest(ctx); err != nil {
		t.Fatalf("remove W1b.4 schema: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO cosmetic_licenses (
			id, account_id, cosmetic_id, status, source, external_reference, granted_at, updated_at
		) VALUES (
			'pre-w1b4-terminal-license', $1, 'skin-neon-grid', 'chargeback',
			'stripe', 'pre-w1b4-chargeback', NOW() - INTERVAL '1 day', NOW() - INTERVAL '1 day'
		)`, account.ID); err != nil {
		t.Fatalf("seed pre-W1b.4 terminal license: %v", err)
	}
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("upgrade pre-W1b.4 schema: %v", err)
	}
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("restart upgraded schema: %v", err)
	}
	var status, transition string
	var revision int64
	var historyCount, changeCount int
	if err := Pool.QueryRow(ctx, `
		SELECT licenses.status, licenses.revision, events.transition,
		       (SELECT COUNT(*) FROM platform_license_lifecycle_events WHERE license_id = licenses.id),
		       (SELECT COUNT(*) FROM platform_changes WHERE subject_kind = 'license' AND subject_id = licenses.id)
		FROM cosmetic_licenses AS licenses
		JOIN platform_license_lifecycle_events AS events ON events.license_id = licenses.id
		WHERE licenses.id = 'pre-w1b4-terminal-license'`,
	).Scan(&status, &revision, &transition, &historyCount, &changeCount); err != nil {
		t.Fatalf("inspect upgraded terminal license: %v", err)
	}
	if status != "chargeback" || revision != 1 || transition != "created" || historyCount != 1 || changeCount != 1 {
		t.Fatalf("upgraded terminal license = status %q revision %d transition %q history %d changes %d", status, revision, transition, historyCount, changeCount)
	}
	var currentAgentID string
	var assignedHistory, licenseChanges, assignmentChanges int
	if err := Pool.QueryRow(ctx, `
		SELECT events.current_agent_id,
		       (SELECT COUNT(*) FROM platform_license_lifecycle_events WHERE license_id = events.license_id),
		       (SELECT COUNT(*) FROM platform_changes WHERE subject_kind = 'license' AND subject_id = events.license_id),
		       (SELECT COUNT(*) FROM platform_changes WHERE subject_kind = 'license_assignment' AND subject_id = events.license_id)
		FROM platform_license_lifecycle_events AS events
		WHERE events.license_id = $1`, assignedLicense.ID).Scan(
		&currentAgentID, &assignedHistory, &licenseChanges, &assignmentChanges,
	); err != nil {
		t.Fatalf("inspect upgraded assigned license: %v", err)
	}
	if currentAgentID != bot.ID || assignedHistory != 1 || licenseChanges != 1 || assignmentChanges != 1 {
		t.Fatalf("upgraded assigned license = current %q history %d license changes %d assignment changes %d", currentAgentID, assignedHistory, licenseChanges, assignmentChanges)
	}
	var previousAgentID, terminalCurrentAgentID *string
	var remainingAssignments, terminalAssignmentChanges int
	if err := Pool.QueryRow(ctx, `
		SELECT events.previous_agent_id, events.current_agent_id,
		       (SELECT COUNT(*) FROM cosmetic_license_assignments WHERE license_id = events.license_id),
		       (SELECT COUNT(*) FROM platform_changes WHERE subject_kind = 'license_assignment' AND subject_id = events.license_id AND transition = 'unassigned')
		FROM platform_license_lifecycle_events AS events
		WHERE events.license_id = $1`, terminalAssignedLicense.ID).Scan(
		&previousAgentID, &terminalCurrentAgentID, &remainingAssignments, &terminalAssignmentChanges,
	); err != nil {
		t.Fatalf("inspect upgraded terminal assignment: %v", err)
	}
	if previousAgentID == nil || *previousAgentID != bot.ID || terminalCurrentAgentID != nil || remainingAssignments != 0 || terminalAssignmentChanges != 1 {
		t.Fatalf("upgraded terminal assignment = previous %v current %v assignments %d changes %d", previousAgentID, terminalCurrentAgentID, remainingAssignments, terminalAssignmentChanges)
	}
}

func TestPostgresPlatformLicenseLifecycleUpgradeRollsBackAtomically(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-upgrade-rollback@example.com", "https://id.example", "license-upgrade-rollback", "License Upgrade Rollback")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := removePlatformLicenseLifecycleSchemaForTest(ctx); err != nil {
		t.Fatalf("remove W1b.4 schema: %v", err)
	}
	overlongID := strings.Repeat("x", 129)
	if _, err := Pool.Exec(ctx, `
		INSERT INTO cosmetic_licenses (
			id, account_id, cosmetic_id, status, source, external_reference, granted_at, updated_at
		) VALUES ($1, $2, 'skin-neon-grid', 'active', 'manual', 'rollback-invalid-id', NOW(), NOW())`, overlongID, account.ID); err != nil {
		t.Fatalf("seed invalid legacy license: %v", err)
	}
	if err := EnsurePlatformAuthoritySchema(ctx); err == nil {
		t.Fatal("W1b.4 upgrade unexpectedly accepted an overlong legacy identifier")
	}
	var revisionColumn, terminalColumn, generationColumn, entitlementLicenseColumn, lifecycleTable, lifecycleTrigger bool
	if err := Pool.QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'cosmetic_licenses' AND column_name = 'revision'),
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'cosmetic_licenses' AND column_name = 'terminal_at'),
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'cosmetic_subscription_licenses' AND column_name = 'generation'),
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'cosmetic_entitlements' AND column_name = 'license_id'),
			TO_REGCLASS(current_schema() || '.platform_license_lifecycle_events') IS NOT NULL,
			EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'cosmetic_licenses'::regclass AND tgname = 'cosmetic_license_status_monotonic' AND NOT tgisinternal)`,
	).Scan(&revisionColumn, &terminalColumn, &generationColumn, &entitlementLicenseColumn, &lifecycleTable, &lifecycleTrigger); err != nil {
		t.Fatalf("inspect rolled-back schema: %v", err)
	}
	if revisionColumn || terminalColumn || generationColumn || entitlementLicenseColumn || lifecycleTable || lifecycleTrigger {
		t.Fatalf("failed upgrade left partial schema: revision=%v terminal=%v generation=%v entitlement=%v history=%v trigger=%v", revisionColumn, terminalColumn, generationColumn, entitlementLicenseColumn, lifecycleTable, lifecycleTrigger)
	}
}

func TestPostgresPlatformLicenseLifecycleUpgradeSerializesConcurrentStartup(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-concurrent-upgrade@example.com", "https://id.example", "license-concurrent-upgrade", "License Concurrent Upgrade")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	license, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "concurrent-upgrade-license")
	if err != nil || !created {
		t.Fatalf("grant legacy projection = (%+v, %v, %v)", license, created, err)
	}
	if err := removePlatformLicenseLifecycleSchemaForTest(ctx); err != nil {
		t.Fatalf("remove W1b.4 schema: %v", err)
	}
	errorsSeen := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsSeen <- EnsurePlatformAuthoritySchema(context.Background())
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent W1b.4 startup: %v", err)
		}
	}
	if got := countPlatformLicenseLifecycleRows(t, ctx, license.ID); got != 1 {
		t.Fatalf("concurrent startup history rows = %d, want 1", got)
	}
	var licenseChanges int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform_changes
		WHERE subject_kind = 'license' AND subject_id = $1`, license.ID).Scan(&licenseChanges); err != nil {
		t.Fatalf("count concurrent startup changes: %v", err)
	}
	if licenseChanges != 1 {
		t.Fatalf("concurrent startup license changes = %d, want 1", licenseChanges)
	}
}

func removePlatformLicenseLifecycleSchemaForTest(ctx context.Context) error {
	_, err := Pool.Exec(ctx, `
		DROP TRIGGER IF EXISTS cosmetic_license_status_monotonic ON cosmetic_licenses;
		DROP FUNCTION IF EXISTS enforce_cosmetic_license_status_monotonic();
		DROP TABLE IF EXISTS platform_license_lifecycle_events;
		DELETE FROM platform_idempotency_records WHERE subject_kind = 'license';
		DELETE FROM platform_changes WHERE subject_kind IN ('license', 'license_assignment');
		ALTER TABLE platform_changes DROP CONSTRAINT IF EXISTS platform_changes_subject_kind_check;
		ALTER TABLE platform_changes ADD CONSTRAINT platform_changes_subject_kind_check
			CHECK (subject_kind IN ('account', 'agent', 'game_profile', 'agent_link'));
		ALTER TABLE cosmetic_entitlements DROP CONSTRAINT IF EXISTS cosmetic_entitlements_license_fk;
		ALTER TABLE cosmetic_entitlements DROP COLUMN IF EXISTS license_id;
		ALTER TABLE cosmetic_subscription_licenses DROP COLUMN IF EXISTS generation;
		ALTER TABLE cosmetic_licenses DROP COLUMN IF EXISTS terminal_at;
		ALTER TABLE cosmetic_licenses DROP COLUMN IF EXISTS revision`)
	return err
}

func TestPostgresPlatformLicenseTerminalPrecedenceAndSameStateNoOp(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-precedence@example.com", "https://id.example", "license-precedence", "License Precedence")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	license, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "license-precedence-copy")
	if err != nil || !created {
		t.Fatalf("grant license = (%+v, %v, %v)", license, created, err)
	}

	current := &PlatformCosmeticLicense{LicenseID: license.ID, Revision: license.Revision, Status: license.Status}
	for index, status := range []string{"expired", "revoked", "refunded", "chargeback"} {
		current, err = TransitionPlatformLicense(ctx, PlatformLicenseTransitionCommand{
			LicenseID: license.ID, TargetStatus: status, ExpectedRevision: current.Revision,
			Reason: "precedence_" + status, IdempotencyKey: "precedence-transition-" + status,
		})
		if err != nil {
			t.Fatalf("transition %s: %v", status, err)
		}
		if current.Status != status || current.Revision != license.Revision+int64(index)+1 {
			t.Fatalf("transition %s result = %+v", status, current)
		}
		if index == 0 {
			noOp, err := TransitionPlatformLicense(ctx, PlatformLicenseTransitionCommand{
				LicenseID: license.ID, TargetStatus: status, ExpectedRevision: current.Revision,
				Reason: "same_state_probe", IdempotencyKey: "precedence-same-state",
			})
			if err != nil {
				t.Fatalf("same-state transition: %v", err)
			}
			if noOp.Status != current.Status || noOp.Revision != current.Revision {
				t.Fatalf("same-state transition = %+v, want revision %d", noOp, current.Revision)
			}
		}
	}
	if _, err := TransitionPlatformLicense(ctx, PlatformLicenseTransitionCommand{
		LicenseID: license.ID, TargetStatus: "refunded", ExpectedRevision: current.Revision,
		Reason: "weaker_provider_truth", IdempotencyKey: "precedence-downgrade",
	}); !errors.Is(err, ErrCosmeticInactive) {
		t.Fatalf("lower-precedence transition error = %v, want %v", err, ErrCosmeticInactive)
	}
	if got := countPlatformLicenseLifecycleRows(t, ctx, license.ID); got != 5 {
		t.Fatalf("lifecycle rows = %d, want create plus four material transitions", got)
	}
}

func TestPostgresPlatformExactAssignmentRejectsMoveButCompatibilityFacadeMoves(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-move@example.com", "https://id.example", "license-move", "License Move")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	firstBot := createCustomerCosmeticsTestBot(t, ctx, "license-move-first")
	secondBot := createCustomerCosmeticsTestBot(t, ctx, "license-move-second")
	for _, botID := range []string{firstBot.ID, secondBot.ID} {
		if _, err := LinkBotToCustomerAccount(ctx, account.ID, botID); err != nil {
			t.Fatalf("link bot %s: %v", botID, err)
		}
	}
	license, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "license-move-copy")
	if err != nil || !created {
		t.Fatalf("grant license = (%+v, %v, %v)", license, created, err)
	}
	assigned, err := AssignPlatformLicense(ctx, PlatformLicenseAssignmentCommand{
		LicenseID: license.ID, AgentID: firstBot.ID, ExpectedRevision: license.Revision,
		IdempotencyKey: "exact-first-assignment",
	})
	if err != nil {
		t.Fatalf("assign first bot: %v", err)
	}
	if _, err := AssignPlatformLicense(ctx, PlatformLicenseAssignmentCommand{
		LicenseID: license.ID, AgentID: secondBot.ID, ExpectedRevision: assigned.Revision,
		IdempotencyKey: "exact-second-assignment",
	}); !errors.Is(err, ErrCosmeticLicenseAlreadyAssigned) {
		t.Fatalf("exact move error = %v, want %v", err, ErrCosmeticLicenseAlreadyAssigned)
	}
	moved, err := AssignCosmeticLicense(ctx, account.ID, license.ID, &secondBot.ID)
	if err != nil {
		t.Fatalf("compatibility move: %v", err)
	}
	if moved.License.AssignedBotID == nil || *moved.License.AssignedBotID != secondBot.ID || moved.License.Revision != assigned.Revision+1 {
		t.Fatalf("compatibility move result = %+v", moved)
	}
}

func TestPostgresPlatformLicenseStorageGuardRejectsResurrectionAndDowngrade(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(ctx, "license-guard@example.com", "https://id.example", "license-guard", "License Guard")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	license, created, err := GrantCosmeticLicenseToAccount(ctx, account.ID, "skin-neon-grid", "manual", "license-guard-copy")
	if err != nil || !created {
		t.Fatalf("grant license = (%+v, %v, %v)", license, created, err)
	}
	refunded, err := TransitionPlatformLicense(ctx, PlatformLicenseTransitionCommand{
		LicenseID: license.ID, TargetStatus: "refunded", ExpectedRevision: license.Revision,
		Reason: "storage_guard", IdempotencyKey: "storage-guard-refund",
	})
	if err != nil {
		t.Fatalf("refund license: %v", err)
	}
	for _, target := range []string{"active", "revoked", "expired"} {
		if _, err := Pool.Exec(ctx, `UPDATE cosmetic_licenses SET status = $2, updated_at = NOW() WHERE id = $1`, license.ID, target); err == nil {
			t.Fatalf("storage guard allowed %s -> %s", refunded.Status, target)
		}
	}
	var status string
	if err := Pool.QueryRow(ctx, `SELECT status FROM cosmetic_licenses WHERE id = $1`, license.ID).Scan(&status); err != nil {
		t.Fatalf("load guarded license: %v", err)
	}
	if status != "refunded" {
		t.Fatalf("guarded status = %q, want refunded", status)
	}
}

func TestPostgresLegacyEntitlementRegrantCreatesNewLicenseGeneration(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	bot := createCustomerCosmeticsTestBot(t, ctx, "license-entitlement-generation")
	if created, err := GrantCosmeticEntitlement(ctx, bot.ID, "skin-neon-grid", "manual", "entitlement-generation-one"); err != nil || !created {
		t.Fatalf("initial entitlement grant = (%v, %v)", created, err)
	}
	var firstLicenseID string
	if err := Pool.QueryRow(ctx, `SELECT id FROM cosmetic_licenses WHERE legacy_bot_id = $1 AND cosmetic_id = 'skin-neon-grid'`, bot.ID).Scan(&firstLicenseID); err != nil {
		t.Fatalf("load initial license: %v", err)
	}
	if revoked, err := RevokeCosmeticEntitlement(ctx, bot.ID, "skin-neon-grid"); err != nil || !revoked {
		t.Fatalf("revoke entitlement = (%v, %v)", revoked, err)
	}
	if created, err := GrantCosmeticEntitlement(ctx, bot.ID, "skin-neon-grid", "manual", "entitlement-generation-two"); err != nil || !created {
		t.Fatalf("regrant entitlement = (%v, %v)", created, err)
	}
	var currentLicenseID, currentStatus string
	if err := Pool.QueryRow(ctx, `
		SELECT entitlements.license_id, licenses.status
		FROM cosmetic_entitlements AS entitlements
		JOIN cosmetic_licenses AS licenses ON licenses.id = entitlements.license_id
		WHERE entitlements.bot_id = $1 AND entitlements.cosmetic_id = 'skin-neon-grid'`, bot.ID).Scan(&currentLicenseID, &currentStatus); err != nil {
		t.Fatalf("load regranted generation: %v", err)
	}
	if currentLicenseID == firstLicenseID || currentStatus != "active" {
		t.Fatalf("regranted generation = (%q, %q), first %q", currentLicenseID, currentStatus, firstLicenseID)
	}
	var firstStatus string
	if err := Pool.QueryRow(ctx, `SELECT status FROM cosmetic_licenses WHERE id = $1`, firstLicenseID).Scan(&firstStatus); err != nil {
		t.Fatalf("load first generation: %v", err)
	}
	if firstStatus != "revoked" {
		t.Fatalf("first generation status = %q, want revoked", firstStatus)
	}
}

func countPlatformLicenseLifecycleRows(t *testing.T, ctx context.Context, licenseID string) int {
	t.Helper()
	var count int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM platform_license_lifecycle_events WHERE license_id = $1`, licenseID).Scan(&count); err != nil {
		t.Fatalf("count platform license lifecycle rows: %v", err)
	}
	return count
}
