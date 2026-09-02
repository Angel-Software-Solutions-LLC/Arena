package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"arena-server/internal/security/credential"

	"github.com/jackc/pgx/v5/pgxpool"
)

type platformV1ConsumerContract struct {
	Contract       string `json:"contract"`
	SourceRepo     string `json:"source_repository"`
	SourcePath     string `json:"source_path"`
	SourceCommit   string `json:"source_commit"`
	SourceBlob     string `json:"source_blob"`
	IdempotencyKey struct {
		MinimumLength int `json:"minimum_length"`
		MaximumLength int `json:"maximum_length"`
	} `json:"idempotency_key"`
	ProfileTransition struct {
		ExpectedRevisionResource string `json:"expected_revision_resource"`
	} `json:"profile_transition"`
	AgentUnlink struct {
		ExpectedRevisionResource string `json:"expected_revision_resource"`
	} `json:"agent_unlink"`
	PlatformChange struct {
		ChangeIDType string   `json:"change_id_type"`
		TimeField    string   `json:"time_field"`
		SubjectKinds []string `json:"subject_kinds"`
		Transitions  []string `json:"transitions"`
	} `json:"platform_change"`
}

func loadPlatformV1ConsumerContract(t *testing.T) platformV1ConsumerContract {
	t.Helper()
	file, err := os.Open("testdata/platform-v1-consumer-contract.json")
	if err != nil {
		t.Fatalf("open platform v1 consumer contract: %v", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var contract platformV1ConsumerContract
	if err := decoder.Decode(&contract); err != nil {
		t.Fatalf("decode platform v1 consumer contract: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("platform v1 consumer contract must contain exactly one JSON value: %v", err)
	}
	return contract
}

func TestPlatformV1ConsumerContractPinsKingdomGridAuthority(t *testing.T) {
	contract := loadPlatformV1ConsumerContract(t)
	if contract.Contract != "angel-serv.platform.v1" || contract.SourceRepo != "ablac/Kingdom-Grid" ||
		contract.SourcePath != "protocol/platform/openapi.json" ||
		contract.SourceCommit != "513656ac312115690c7c8cd5638c9e5a86b4eec0" ||
		contract.SourceBlob != "1b91d3456e1c8fc633580a4165f6e20d23815867" {
		t.Fatalf("invalid Kingdom Grid contract authority: %+v", contract)
	}
	if contract.IdempotencyKey.MinimumLength != platformIdempotencyKeyMinimum || contract.IdempotencyKey.MaximumLength != platformIdempotencyKeyMaximum {
		t.Fatalf("idempotency contract = %+v, want 8..128", contract.IdempotencyKey)
	}
	if contract.ProfileTransition.ExpectedRevisionResource != "agent_identity" || contract.AgentUnlink.ExpectedRevisionResource != "account" {
		t.Fatalf("revision boundaries = profile %q unlink %q", contract.ProfileTransition.ExpectedRevisionResource, contract.AgentUnlink.ExpectedRevisionResource)
	}
	if contract.PlatformChange.ChangeIDType != "string" || contract.PlatformChange.TimeField != "occurred_at" {
		t.Fatalf("change wire contract = %+v", contract.PlatformChange)
	}
}

func TestPostgresArenaAgentClaimVerifiesLockedControlProofAndRejectsInactiveCredentials(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-control-proof@example.com",
		"https://id.example",
		"platform-control-proof",
		"Platform Control Proof",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}

	const proof = "arena_control_proof_1234567890"
	bot := accountAPIKeyTestBot("platform-control-proof-key", "platform-control-proof-agent")
	if err := CreateAPIKeyAndBot(
		ctx,
		bot.APIKeyID,
		credential.Digest(proof),
		proof[:12],
		"127.0.0.1",
		bot,
	); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}

	wrongProof := proof[:12] + "wrong_control_proof"
	if _, err := ClaimArenaAgentWithControlProof(ctx, account.ID, wrongProof); !errors.Is(err, ErrPlatformControlProofRejected) {
		t.Fatalf("wrong proof error = %v, want %v", err, ErrPlatformControlProofRejected)
	}
	var links, ownership, events int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_bot_links WHERE bot_id = $1`, bot.ID).Scan(&links); err != nil {
		t.Fatalf("count rejected links: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_api_keys WHERE api_key_id = $1`, bot.APIKeyID).Scan(&ownership); err != nil {
		t.Fatalf("count rejected ownership: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM platform_agent_link_events WHERE agent_id = $1`, bot.ID).Scan(&events); err != nil {
		t.Fatalf("count rejected events: %v", err)
	}
	if links != 0 || ownership != 0 || events != 0 {
		t.Fatalf("wrong proof wrote links=%d ownership=%d events=%d", links, ownership, events)
	}

	linked, err := ClaimArenaAgentWithControlProof(ctx, account.ID, proof)
	if err != nil {
		t.Fatalf("ClaimArenaAgentWithControlProof: %v", err)
	}
	if linked.BotID != bot.ID || linked.LinkedAt.IsZero() {
		t.Fatalf("linked bot = %+v", linked)
	}

	const inactiveProof = "arena_inactive_proof_1234567890"
	inactive := accountAPIKeyTestBot("platform-inactive-proof-key", "platform-inactive-proof-agent")
	inactive.CreatedAt, inactive.UpdatedAt = time.Now(), time.Now()
	if err := CreateAPIKeyAndBot(
		ctx,
		inactive.APIKeyID,
		credential.Digest(inactiveProof),
		inactiveProof[:12],
		"127.0.0.1",
		inactive,
	); err != nil {
		t.Fatalf("CreateAPIKeyAndBot inactive: %v", err)
	}
	if err := DeactivateAPIKey(ctx, inactive.APIKeyID); err != nil {
		t.Fatalf("DeactivateAPIKey: %v", err)
	}
	if _, err := ClaimArenaAgentWithControlProof(ctx, account.ID, inactiveProof); !errors.Is(err, ErrPlatformControlProofRejected) {
		t.Fatalf("inactive proof error = %v, want %v", err, ErrPlatformControlProofRejected)
	}
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_bot_links WHERE bot_id = $1`, inactive.ID).Scan(&links); err != nil {
		t.Fatalf("count inactive links: %v", err)
	}
	if links != 0 {
		t.Fatalf("inactive proof wrote %d links", links)
	}

	const retiredProof = "arena_retired_proof_1234567890"
	retired := accountAPIKeyTestBot("platform-retired-proof-key", "platform-retired-proof-agent")
	retired.CreatedAt, retired.UpdatedAt = time.Now(), time.Now()
	if err := CreateAPIKeyAndBot(
		ctx,
		retired.APIKeyID,
		credential.Digest(retiredProof),
		retiredProof[:12],
		"127.0.0.1",
		retired,
	); err != nil {
		t.Fatalf("CreateAPIKeyAndBot retired: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		UPDATE platform_agents SET status = 'retired', revision = revision + 1, updated_at = NOW()
		WHERE agent_id = $1`, retired.ID); err != nil {
		t.Fatalf("retire platform agent: %v", err)
	}
	if _, err := ClaimArenaAgentWithControlProof(ctx, account.ID, retiredProof); !errors.Is(err, ErrPlatformAgentInactive) {
		t.Fatalf("retired agent error = %v, want %v", err, ErrPlatformAgentInactive)
	}
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_bot_links WHERE bot_id = $1`, retired.ID).Scan(&links); err != nil {
		t.Fatalf("count retired links: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_api_keys WHERE api_key_id = $1`, retired.APIKeyID).Scan(&ownership); err != nil {
		t.Fatalf("count retired ownership: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM platform_agent_link_events WHERE agent_id = $1`, retired.ID).Scan(&events); err != nil {
		t.Fatalf("count retired events: %v", err)
	}
	if links != 0 || ownership != 0 || events != 0 {
		t.Fatalf("retired proof wrote links=%d ownership=%d events=%d", links, ownership, events)
	}
}

func TestPostgresPlatformAgentLinkEnforcesProofRevisionAndProofFreeIdempotency(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-exact-link@example.com",
		"https://id.example",
		"platform-exact-link",
		"Platform Exact Link",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	capacity, err := GetPlatformAccountCapacity(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccountCapacity: %v", err)
	}

	const proof = "arena_exact_control_proof_1234567890"
	bot := accountAPIKeyTestBot("platform-exact-link-key", "platform-exact-link-agent")
	if err := CreateAPIKeyAndBot(ctx, bot.APIKeyID, credential.Digest(proof), proof[:12], "127.0.0.1", bot); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}
	command := PlatformAgentLinkCommand{
		AccountID:               account.ID,
		AgentID:                 bot.ID,
		ControlProof:            proof,
		ExpectedAccountRevision: capacity.Revision,
		IdempotencyKey:          "exact-link-command-001",
	}
	result, err := LinkPlatformAgent(ctx, command)
	if err != nil {
		t.Fatalf("LinkPlatformAgent: %v", err)
	}
	if result.AccountID != account.ID || result.AgentID != bot.ID || result.Status != "active" || result.Revision != 1 {
		t.Fatalf("link result = %+v", result)
	}
	replayed, err := LinkPlatformAgent(ctx, command)
	if err != nil {
		t.Fatalf("LinkPlatformAgent replay: %v", err)
	}
	if !equalPlatformAgentLinkResult(replayed, result) {
		t.Fatalf("replayed result = %+v, want %+v", replayed, result)
	}
	conflict := command
	conflict.ExpectedAccountRevision++
	if _, err := LinkPlatformAgent(ctx, conflict); !errors.Is(err, ErrPlatformIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v, want %v", err, ErrPlatformIdempotencyConflict)
	}

	var recordCount int
	var requestHashHex, responseText string
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*)::INTEGER, MIN(encode(request_hash, 'hex')), MIN(response::TEXT)
		FROM platform_idempotency_records
		WHERE operation = 'link_platform_agent' AND subject_id = $1`, bot.ID).Scan(
		&recordCount,
		&requestHashHex,
		&responseText,
	); err != nil {
		t.Fatalf("load exact link idempotency: %v", err)
	}
	if recordCount != 1 || strings.Contains(requestHashHex, proof) || strings.Contains(responseText, proof) {
		t.Fatalf("idempotency record count=%d hash=%q response=%q", recordCount, requestHashHex, responseText)
	}
	if strings.Contains(responseText, "control_proof") || strings.Contains(responseText, "api_key") {
		t.Fatalf("idempotency response disclosed credential metadata: %s", responseText)
	}

	if _, err := Pool.Exec(ctx, `
		UPDATE platform_agents SET status = 'retired', revision = revision + 1, updated_at = NOW()
		WHERE agent_id = $1`, bot.ID); err != nil {
		t.Fatalf("retire linked platform agent: %v", err)
	}
	postRetirementReplay, err := LinkPlatformAgent(ctx, command)
	if err != nil {
		t.Fatalf("LinkPlatformAgent replay after retirement: %v", err)
	}
	if !equalPlatformAgentLinkResult(postRetirementReplay, result) {
		t.Fatalf("post-retirement replay = %+v, want %+v", postRetirementReplay, result)
	}
	newAfterRetirement := command
	newAfterRetirement.IdempotencyKey = "exact-link-command-after-retirement"
	newAfterRetirement.ExpectedAccountRevision = result.Revision
	if _, err := LinkPlatformAgent(ctx, newAfterRetirement); !errors.Is(err, ErrPlatformAgentInactive) {
		t.Fatalf("new post-retirement command error = %v, want %v", err, ErrPlatformAgentInactive)
	}

	const staleProof = "arena_stale_control_proof_1234567890"
	staleBot := accountAPIKeyTestBot("platform-stale-link-key", "platform-stale-link-agent")
	if err := CreateAPIKeyAndBot(ctx, staleBot.APIKeyID, credential.Digest(staleProof), staleProof[:12], "127.0.0.1", staleBot); err != nil {
		t.Fatalf("CreateAPIKeyAndBot stale: %v", err)
	}
	stale := PlatformAgentLinkCommand{
		AccountID:               account.ID,
		AgentID:                 staleBot.ID,
		ControlProof:            staleProof,
		ExpectedAccountRevision: capacity.Revision,
		IdempotencyKey:          "exact-stale-command-001",
	}
	if _, err := LinkPlatformAgent(ctx, stale); !errors.Is(err, ErrPlatformRevisionConflict) {
		t.Fatalf("stale revision error = %v, want %v", err, ErrPlatformRevisionConflict)
	}
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_bot_links WHERE bot_id = $1`, staleBot.ID).Scan(&recordCount); err != nil {
		t.Fatalf("count stale links: %v", err)
	}
	if recordCount != 0 {
		t.Fatalf("stale revision wrote %d links", recordCount)
	}
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform_idempotency_records
		WHERE operation = 'link_platform_agent' AND subject_id = $1`, staleBot.ID).Scan(&recordCount); err != nil {
		t.Fatalf("count stale idempotency: %v", err)
	}
	if recordCount != 0 {
		t.Fatalf("stale revision wrote %d idempotency records", recordCount)
	}
	var staleLastSeen *time.Time
	if err := Pool.QueryRow(ctx, `SELECT last_seen FROM api_keys WHERE id = $1`, staleBot.APIKeyID).Scan(&staleLastSeen); err != nil {
		t.Fatalf("load stale credential timestamp: %v", err)
	}
	if staleLastSeen != nil {
		t.Fatalf("stale revision committed proof-side last_seen = %v", *staleLastSeen)
	}
}

func TestPostgresPlatformAgentLinkScopesIdempotencyByAccountAndBindsProofToAgent(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	type fixture struct {
		account  *CustomerAccount
		bot      *Bot
		proof    string
		revision int64
	}
	fixtures := make([]fixture, 2)
	for index := range fixtures {
		account, err := UpsertVerifiedCustomerAccount(
			ctx,
			fmt.Sprintf("platform-scoped-link-%d@example.com", index),
			"https://id.example",
			fmt.Sprintf("platform-scoped-link-%d", index),
			fmt.Sprintf("Platform Scoped Link %d", index),
		)
		if err != nil {
			t.Fatalf("UpsertVerifiedCustomerAccount %d: %v", index, err)
		}
		proof := fmt.Sprintf("arena_scope%d_control_proof_1234567890", index)
		bot := accountAPIKeyTestBot(
			fmt.Sprintf("platform-scoped-link-key-%d", index),
			fmt.Sprintf("platform-scoped-link-agent-%d", index),
		)
		if err := CreateAPIKeyAndBot(ctx, bot.APIKeyID, credential.Digest(proof), proof[:12], "127.0.0.1", bot); err != nil {
			t.Fatalf("CreateAPIKeyAndBot %d: %v", index, err)
		}
		capacity, err := GetPlatformAccountCapacity(ctx, account.ID)
		if err != nil {
			t.Fatalf("GetPlatformAccountCapacity %d: %v", index, err)
		}
		fixtures[index] = fixture{account: account, bot: bot, proof: proof, revision: capacity.Revision}
	}

	mismatch := PlatformAgentLinkCommand{
		AccountID:               fixtures[0].account.ID,
		AgentID:                 fixtures[1].bot.ID,
		ControlProof:            fixtures[0].proof,
		ExpectedAccountRevision: fixtures[0].revision,
		IdempotencyKey:          "shared-client-key-001",
	}
	if _, err := LinkPlatformAgent(ctx, mismatch); !errors.Is(err, ErrPlatformControlProofRejected) {
		t.Fatalf("agent/proof mismatch error = %v, want %v", err, ErrPlatformControlProofRejected)
	}

	for index, fixture := range fixtures {
		result, err := LinkPlatformAgent(ctx, PlatformAgentLinkCommand{
			AccountID:               fixture.account.ID,
			AgentID:                 fixture.bot.ID,
			ControlProof:            fixture.proof,
			ExpectedAccountRevision: fixture.revision,
			IdempotencyKey:          "shared-client-key-001",
		})
		if err != nil {
			t.Fatalf("LinkPlatformAgent %d: %v", index, err)
		}
		if result.AccountID != fixture.account.ID || result.AgentID != fixture.bot.ID {
			t.Fatalf("result %d = %+v", index, result)
		}
	}
	var records, distinctKeys int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*)::INTEGER, COUNT(DISTINCT idempotency_key)::INTEGER
		FROM platform_idempotency_records
		WHERE operation = 'link_platform_agent'`).Scan(&records, &distinctKeys); err != nil {
		t.Fatalf("count scoped idempotency records: %v", err)
	}
	if records != 2 || distinctKeys != 2 {
		t.Fatalf("scoped idempotency = %d records / %d keys, want 2 / 2", records, distinctKeys)
	}
}

func TestPostgresPlatformAgentLinkConcurrentIdenticalCommandsReplayOneCommit(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-concurrent-link@example.com",
		"https://id.example",
		"platform-concurrent-link",
		"Platform Concurrent Link",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	capacity, err := GetPlatformAccountCapacity(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccountCapacity: %v", err)
	}
	const proof = "arena_concurrent_control_proof_1234567890"
	bot := accountAPIKeyTestBot("platform-concurrent-link-key", "platform-concurrent-link-agent")
	if err := CreateAPIKeyAndBot(ctx, bot.APIKeyID, credential.Digest(proof), proof[:12], "127.0.0.1", bot); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}
	command := PlatformAgentLinkCommand{
		AccountID:               account.ID,
		AgentID:                 bot.ID,
		ControlProof:            proof,
		ExpectedAccountRevision: capacity.Revision,
		IdempotencyKey:          "concurrent-exact-link-command-001",
	}

	const callers = 8
	start := make(chan struct{})
	results := make([]*PlatformAgentLinkResult, callers)
	errorsFound := make([]error, callers)
	var wait sync.WaitGroup
	for index := range callers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errorsFound[index] = LinkPlatformAgent(context.Background(), command)
		}(index)
	}
	close(start)
	wait.Wait()

	for index := range callers {
		if errorsFound[index] != nil {
			t.Fatalf("caller %d: %v", index, errorsFound[index])
		}
		if index > 0 && !equalPlatformAgentLinkResult(results[index], results[0]) {
			t.Fatalf("caller %d result = %+v, want %+v", index, results[index], results[0])
		}
	}
	var links, events, idempotencyRecords int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_bot_links WHERE bot_id = $1`, bot.ID).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM platform_agent_link_events WHERE agent_id = $1 AND status = 'linked'`, bot.ID).Scan(&events); err != nil {
		t.Fatalf("count link events: %v", err)
	}
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform_idempotency_records
		WHERE operation = 'link_platform_agent' AND subject_id = $1`, bot.ID).Scan(&idempotencyRecords); err != nil {
		t.Fatalf("count idempotency records: %v", err)
	}
	if links != 1 || events != 1 || idempotencyRecords != 1 {
		t.Fatalf("committed links=%d events=%d idempotency=%d, want 1/1/1", links, events, idempotencyRecords)
	}
}

func equalPlatformAgentLinkResult(left, right *PlatformAgentLinkResult) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left.AccountID != right.AccountID || left.AgentID != right.AgentID || left.Status != right.Status || left.Revision != right.Revision ||
		!left.LinkedAt.Equal(right.LinkedAt) || !left.UpdatedAt.Equal(right.UpdatedAt) {
		return false
	}
	if left.UnlinkedAt == nil || right.UnlinkedAt == nil {
		return left.UnlinkedAt == nil && right.UnlinkedAt == nil
	}
	return left.UnlinkedAt.Equal(*right.UnlinkedAt)
}

func TestPostgresPlatformAgentLinkRejectsProofRevokedAheadOfCredentialLock(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-revoke-race@example.com",
		"https://id.example",
		"platform-revoke-race",
		"Platform Revoke Race",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	capacity, err := GetPlatformAccountCapacity(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccountCapacity: %v", err)
	}
	const proof = "arena_revoke_race_control_proof_1234567890"
	bot := accountAPIKeyTestBot("platform-revoke-race-key", "platform-revoke-race-agent")
	if err := CreateAPIKeyAndBot(ctx, bot.APIKeyID, credential.Digest(proof), proof[:12], "127.0.0.1", bot); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}

	blocker, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin credential blocker: %v", err)
	}
	defer blocker.Rollback(ctx)
	var lockedID string
	if err := blocker.QueryRow(ctx, `SELECT id FROM api_keys WHERE id = $1 FOR UPDATE`, bot.APIKeyID).Scan(&lockedID); err != nil {
		t.Fatalf("lock credential: %v", err)
	}

	waitUntilBlocked := func(queryFragment string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var waiting bool
			if err := Pool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM pg_stat_activity
					WHERE datname = current_database()
					  AND pid <> pg_backend_pid()
					  AND wait_event_type = 'Lock'
					  AND query LIKE '%' || $1 || '%'
				)`, queryFragment).Scan(&waiting); err != nil {
				t.Fatalf("observe blocked query %q: %v", queryFragment, err)
			}
			if waiting {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("query %q did not block on credential lock", queryFragment)
	}

	revokeResult := make(chan error, 1)
	go func() {
		revokeResult <- DeactivateAPIKey(context.Background(), bot.APIKeyID)
	}()
	waitUntilBlocked("UPDATE api_keys SET is_active = false")

	command := PlatformAgentLinkCommand{
		AccountID:               account.ID,
		AgentID:                 bot.ID,
		ControlProof:            proof,
		ExpectedAccountRevision: capacity.Revision,
		IdempotencyKey:          "revoke-race-exact-link-command-001",
	}
	linkResult := make(chan error, 1)
	go func() {
		_, err := LinkPlatformAgent(context.Background(), command)
		linkResult <- err
	}()
	waitUntilBlocked("JOIN platform_agents agents")

	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release credential blocker: %v", err)
	}
	if err := <-revokeResult; err != nil {
		t.Fatalf("DeactivateAPIKey: %v", err)
	}
	if err := <-linkResult; !errors.Is(err, ErrPlatformControlProofRejected) {
		t.Fatalf("link after queued revocation error = %v, want %v", err, ErrPlatformControlProofRejected)
	}

	var links, events, idempotencyRecords int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_bot_links WHERE bot_id = $1`, bot.ID).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM platform_agent_link_events WHERE agent_id = $1`, bot.ID).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform_idempotency_records
		WHERE operation = 'link_platform_agent' AND subject_id = $1`, bot.ID).Scan(&idempotencyRecords); err != nil {
		t.Fatalf("count idempotency records: %v", err)
	}
	if links != 0 || events != 0 || idempotencyRecords != 0 {
		t.Fatalf("revoked proof wrote links=%d events=%d idempotency=%d", links, events, idempotencyRecords)
	}
}

func TestPostgresPlatformAccountMetadataDefaultsToTenAndComputesCurrentAgents(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-capacity@example.com",
		"https://id.example",
		"platform-capacity",
		"Platform Capacity",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}

	var maximumAgents, revision int
	var status string
	if err := Pool.QueryRow(ctx, `
		SELECT maximum_agents, status, revision
		FROM platform_account_metadata
		WHERE account_id = $1`, account.ID).Scan(&maximumAgents, &status, &revision); err != nil {
		t.Fatalf("load platform account metadata: %v", err)
	}
	if maximumAgents != 10 || status != "active" || revision != 1 {
		t.Fatalf("platform account metadata = (%d, %q, %d), want (10, active, 1)", maximumAgents, status, revision)
	}

	var storedCurrentAgentsColumn bool
	if err := Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'platform_account_metadata'
			  AND column_name = 'current_agents'
		)`).Scan(&storedCurrentAgentsColumn); err != nil {
		t.Fatalf("inspect platform account metadata columns: %v", err)
	}
	if storedCurrentAgentsColumn {
		t.Fatal("platform account metadata stores current_agents instead of computing it transactionally")
	}
}

func TestPostgresPlatformAccountMetadataUpgradesRetiredStatusToClosed(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-account-status-upgrade@example.com",
		"https://id.example",
		"platform-account-status-upgrade",
		"Platform Account Status Upgrade",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	statements := []struct {
		description string
		sql         string
	}{
		{
			description: "drop current status constraint",
			sql: `ALTER TABLE platform_account_metadata
				DROP CONSTRAINT platform_account_metadata_status_check`,
		},
		{
			description: "install published status constraint",
			sql: `ALTER TABLE platform_account_metadata
				ADD CONSTRAINT platform_account_metadata_status_check
				CHECK (status IN ('active', 'suspended', 'retired'))`,
		},
	}
	for _, statement := range statements {
		if _, err := Pool.Exec(ctx, statement.sql); err != nil {
			t.Fatalf("%s: %v", statement.description, err)
		}
	}
	if _, err := Pool.Exec(ctx, `
		UPDATE platform_account_metadata
		SET status = 'retired'
		WHERE account_id = $1`, account.ID); err != nil {
		t.Fatalf("seed published account status: %v", err)
	}

	if err := EnsurePlatformAuthoritySchema(ctx); err != nil {
		t.Fatalf("EnsurePlatformAuthoritySchema upgrade: %v", err)
	}

	var status, constraintDefinition string
	var constraintOID int64
	if err := Pool.QueryRow(ctx, `
		SELECT metadata.status, constraint_row.oid::BIGINT, pg_get_constraintdef(constraint_row.oid)
		FROM platform_account_metadata AS metadata
		JOIN pg_constraint AS constraint_row
		  ON constraint_row.conrelid = 'platform_account_metadata'::regclass
		 AND constraint_row.conname = 'platform_account_metadata_status_check'
		WHERE metadata.account_id = $1`, account.ID).Scan(&status, &constraintOID, &constraintDefinition); err != nil {
		t.Fatalf("load upgraded account status contract: %v", err)
	}
	if status != "closed" {
		t.Fatalf("upgraded account status = %q, want closed", status)
	}
	if !strings.Contains(constraintDefinition, "'closed'") || strings.Contains(constraintDefinition, "'retired'") {
		t.Fatalf("account status constraint = %q, want active/suspended/closed", constraintDefinition)
	}

	if err := EnsurePlatformAuthoritySchema(ctx); err != nil {
		t.Fatalf("repeat EnsurePlatformAuthoritySchema: %v", err)
	}
	var repeatedOID int64
	var repeatedDefinition string
	if err := Pool.QueryRow(ctx, `
		SELECT oid::BIGINT, pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid = 'platform_account_metadata'::regclass
		  AND conname = 'platform_account_metadata_status_check'`).Scan(&repeatedOID, &repeatedDefinition); err != nil {
		t.Fatalf("load repeated account status constraint: %v", err)
	}
	if repeatedOID != constraintOID || repeatedDefinition != constraintDefinition {
		t.Fatalf(
			"repeat schema pass replaced account status constraint: (%d, %q) -> (%d, %q)",
			constraintOID,
			constraintDefinition,
			repeatedOID,
			repeatedDefinition,
		)
	}
}

func TestPostgresPlatformAgentCapacitySerializesConcurrentLinksIndependentlyOfAPIKeys(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-capacity-race@example.com",
		"https://id.example",
		"platform-capacity-race",
		"Platform Capacity Race",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}

	bots := make([]*Bot, 13)
	for index := range bots {
		bot := accountAPIKeyTestBot(
			fmt.Sprintf("platform-capacity-key-%02d", index),
			fmt.Sprintf("platform-capacity-agent-%02d", index),
		)
		if err := CreateAPIKeyAndBot(
			ctx,
			bot.APIKeyID,
			fmt.Sprintf("capacity-hash-%02d", index),
			fmt.Sprintf("arena_cap%02d", index),
			"127.0.0.1",
			bot,
		); err != nil {
			t.Fatalf("CreateAPIKeyAndBot %d: %v", index, err)
		}
		if _, err := Pool.Exec(ctx, `
			INSERT INTO account_api_keys (account_id, api_key_id, linked_at)
			VALUES ($1, $2, NOW())`, account.ID, bot.APIKeyID); err != nil {
			t.Fatalf("seed owned key %d: %v", index, err)
		}
		bots[index] = bot
	}

	for index := range 9 {
		if _, err := LinkBotToCustomerAccount(ctx, account.ID, bots[index].ID); err != nil {
			t.Fatalf("seed linked agent %d: %v", index, err)
		}
		if _, err := Pool.Exec(ctx, `UPDATE api_keys SET is_active = false WHERE id = $1`, bots[index].APIKeyID); err != nil {
			t.Fatalf("deactivate linked agent key %d: %v", index, err)
		}
	}

	const contenders = 4
	results := make(chan error, contenders)
	var wg sync.WaitGroup
	for index := 9; index < 9+contenders; index++ {
		botID := bots[index].ID
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := LinkBotToCustomerAccount(context.Background(), account.ID, botID)
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	var linked, rejected int
	for err := range results {
		switch {
		case err == nil:
			linked++
		case strings.Contains(err.Error(), "maximum_agents"):
			rejected++
		default:
			t.Fatalf("concurrent link returned unexpected error: %v", err)
		}
	}
	if linked != 1 || rejected != contenders-1 {
		t.Fatalf("concurrent capacity results = %d linked, %d rejected; want 1 and %d", linked, rejected, contenders-1)
	}

	var currentAgents, activeKeys, revision int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_bot_links WHERE account_id = $1`, account.ID).Scan(&currentAgents); err != nil {
		t.Fatalf("count current agents: %v", err)
	}
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM account_api_keys AS owned
		JOIN api_keys AS keys ON keys.id = owned.api_key_id
		WHERE owned.account_id = $1 AND keys.is_active`, account.ID).Scan(&activeKeys); err != nil {
		t.Fatalf("count active API keys: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT revision FROM platform_account_metadata WHERE account_id = $1`, account.ID).Scan(&revision); err != nil {
		t.Fatalf("load account metadata revision: %v", err)
	}
	if currentAgents != 10 || activeKeys != 4 || revision != 11 {
		t.Fatalf("capacity snapshot = %d current agents, %d active keys, revision %d; want 10, 4, 11", currentAgents, activeKeys, revision)
	}

	contenderIDs := []string{bots[9].ID, bots[10].ID, bots[11].ID, bots[12].ID}
	var linkedContenderID string
	if err := Pool.QueryRow(ctx, `
		SELECT links.bot_id
		FROM account_bot_links AS links
		WHERE links.account_id = $1 AND links.bot_id = ANY($2::TEXT[])
		ORDER BY links.bot_id
		LIMIT 1`, account.ID, contenderIDs).Scan(&linkedContenderID); err != nil {
		t.Fatalf("load linked contender: %v", err)
	}
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, linkedContenderID); err != nil {
		t.Fatalf("same-account link replay at capacity: %v", err)
	}
	capacity, err := GetPlatformAccountCapacity(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccountCapacity after replay: %v", err)
	}
	if capacity.CurrentAgents != 10 || capacity.MaximumAgents != 10 || capacity.Revision != 11 {
		t.Fatalf("capacity after no-op replay = %+v, want unchanged 10/10 at revision 11", capacity)
	}

	if unlinked, err := UnlinkBotFromCustomerAccount(ctx, account.ID, bots[0].ID); err != nil || !unlinked {
		t.Fatalf("UnlinkBotFromCustomerAccount at capacity = (%v, %v)", unlinked, err)
	}
	var replacementBotID string
	if err := Pool.QueryRow(ctx, `
		SELECT candidates.agent_id
		FROM unnest($2::TEXT[]) AS candidates(agent_id)
		WHERE NOT EXISTS (
			SELECT 1 FROM account_bot_links AS links
			WHERE links.account_id = $1 AND links.bot_id = candidates.agent_id
		)
		ORDER BY candidates.agent_id
		LIMIT 1`, account.ID, contenderIDs).Scan(&replacementBotID); err != nil {
		t.Fatalf("load rejected replacement agent: %v", err)
	}
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, replacementBotID); err != nil {
		t.Fatalf("link replacement after freeing capacity: %v", err)
	}
	capacity, err = GetPlatformAccountCapacity(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccountCapacity after replacement: %v", err)
	}
	if capacity.CurrentAgents != 10 || capacity.MaximumAgents != 10 || capacity.Revision != 13 {
		t.Fatalf("capacity after unlink/relink = %+v, want 10/10 at revision 13", capacity)
	}
}

func TestPostgresPlatformAuthorityBackfillsStableArenaAgentsWithoutTouchingCosmetics(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	linkedBot := createCustomerCosmeticsTestBot(t, ctx, "platform-linked")
	unlinkedBot := createCustomerCosmeticsTestBot(t, ctx, "platform-unlinked")
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-owner@example.com",
		"https://id.example",
		"platform-owner",
		"Platform Owner",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, linkedBot.ID); err != nil {
		t.Fatalf("LinkBotToCustomerAccount: %v", err)
	}

	if _, err := SetCustomerSubscription(ctx, account.ID, true, time.Now()); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := EquipCustomerCosmetic(ctx, account.ID, linkedBot.ID, CosmeticSlotBotSkin, "skin-neon-grid"); err != nil {
		t.Fatalf("EquipCustomerCosmetic: %v", err)
	}
	if _, err := GrantCosmeticEntitlement(ctx, unlinkedBot.ID, "weapon-solar-flare", "demo", "platform-demo"); err != nil {
		t.Fatalf("GrantCosmeticEntitlement: %v", err)
	}

	loadoutBefore := snapshotPlatformMigrationTable(t, ctx, "bot_cosmetic_loadout", "bot_id, slot")
	entitlementBefore := snapshotPlatformMigrationTable(t, ctx, "cosmetic_entitlements", "bot_id, cosmetic_id")
	linkBefore := snapshotPlatformMigrationTable(t, ctx, "account_bot_links", "account_id, bot_id")

	// Recreate an authentic pre-W1b.2 database: all existing Arena tables and
	// data are present, but the new platform metadata has not been installed.
	if _, err := Pool.Exec(ctx, `
		DROP TRIGGER IF EXISTS customer_accounts_platform_metadata ON customer_accounts;
		DROP FUNCTION IF EXISTS insert_platform_account_metadata();
		DROP TABLE IF EXISTS
			platform_idempotency_records,
			platform_agent_link_events,
			platform_changes,
			platform_game_profiles,
			platform_agents,
			platform_account_metadata`); err != nil {
		t.Fatalf("drop platform metadata fixture: %v", err)
	}
	if err := EnsurePlatformAuthoritySchema(ctx); err != nil {
		t.Fatalf("EnsurePlatformAuthoritySchema: %v", err)
	}

	type importedProfile struct {
		agentID            string
		registrationSource string
		agentStatus        string
		agentRevision      int64
		game               string
		profileStatus      string
		profileRevision    int64
	}
	rows, err := Pool.Query(ctx, `
		SELECT agents.agent_id, agents.registration_source, agents.status, agents.revision,
		       profiles.game, profiles.status, profiles.revision
		FROM platform_agents agents
		JOIN platform_game_profiles profiles ON profiles.agent_id = agents.agent_id
		ORDER BY agents.agent_id`)
	if err != nil {
		t.Fatalf("load imported profiles: %v", err)
	}
	defer rows.Close()
	var imported []importedProfile
	for rows.Next() {
		var profile importedProfile
		if err := rows.Scan(
			&profile.agentID,
			&profile.registrationSource,
			&profile.agentStatus,
			&profile.agentRevision,
			&profile.game,
			&profile.profileStatus,
			&profile.profileRevision,
		); err != nil {
			t.Fatalf("scan imported profile: %v", err)
		}
		imported = append(imported, profile)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate imported profiles: %v", err)
	}
	if len(imported) != 2 {
		t.Fatalf("imported profiles = %+v, want one per existing bot", imported)
	}
	for _, profile := range imported {
		if profile.agentID != linkedBot.ID && profile.agentID != unlinkedBot.ID {
			t.Fatalf("agent ID %q was not preserved from Arena", profile.agentID)
		}
		wantAgentRevision := int64(1)
		if profile.agentID == linkedBot.ID {
			wantAgentRevision = 2
		}
		if profile.registrationSource != "arena_import" || profile.agentStatus != "active" || profile.agentRevision != wantAgentRevision ||
			profile.game != "arena" || profile.profileStatus != "active" || profile.profileRevision != 1 {
			t.Fatalf("imported profile = %+v, want active revision-1 Arena metadata", profile)
		}
	}
	capacity, err := GetPlatformAccountCapacity(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccountCapacity after backfill: %v", err)
	}
	if capacity.MaximumAgents != 10 || capacity.CurrentAgents != 1 || capacity.Revision != 2 {
		t.Fatalf("backfilled account capacity = %+v, want maximum 10, current 1, revision 2", capacity)
	}

	var profileColumns []string
	if err := Pool.QueryRow(ctx, `
		SELECT array_agg(column_name ORDER BY ordinal_position)
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'platform_game_profiles'`).Scan(&profileColumns); err != nil {
		t.Fatalf("load platform profile columns: %v", err)
	}
	wantColumns := []string{"profile_id", "agent_id", "game", "status", "revision", "enrolled_at", "updated_at"}
	if strings.Join(profileColumns, ",") != strings.Join(wantColumns, ",") {
		t.Fatalf("platform profile columns = %v, want metadata only %v", profileColumns, wantColumns)
	}

	// A repeat repair and a replacement connection pool model process restart.
	// Existing authority-owned metadata must not be reset by either path.
	if _, err := Pool.Exec(ctx, `
		UPDATE platform_agents SET status = 'suspended', revision = 7 WHERE agent_id = $1`, linkedBot.ID); err != nil {
		t.Fatalf("mutate imported metadata: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		DELETE FROM platform_game_profiles WHERE agent_id = $1 AND game = 'arena'`, linkedBot.ID); err != nil {
		t.Fatalf("remove profile for repair fixture: %v", err)
	}
	if err := EnsurePlatformAuthoritySchema(ctx); err != nil {
		t.Fatalf("repeat EnsurePlatformAuthoritySchema: %v", err)
	}
	restartPool, err := pgxpool.NewWithConfig(ctx, Pool.Config().Copy())
	if err != nil {
		t.Fatalf("create replacement pool: %v", err)
	}
	defer restartPool.Close()
	originalPool := Pool
	Pool = restartPool
	if err := EnsurePlatformAuthoritySchema(ctx); err != nil {
		Pool = originalPool
		t.Fatalf("restart EnsurePlatformAuthoritySchema: %v", err)
	}
	Pool = originalPool

	var status, profileStatus string
	var revision int64
	if err := Pool.QueryRow(ctx, `
		SELECT agents.status, agents.revision, profiles.status
		FROM platform_agents agents
		JOIN platform_game_profiles profiles ON profiles.agent_id = agents.agent_id AND profiles.game = 'arena'
		WHERE agents.agent_id = $1`, linkedBot.ID).Scan(&status, &revision, &profileStatus); err != nil {
		t.Fatalf("load preserved agent metadata: %v", err)
	}
	if status != "suspended" || revision != 7 || profileStatus != "suspended" {
		t.Fatalf("metadata after restart = (%q, %d, %q), want suspended agent/profile at revision 7",
			status, revision, profileStatus)
	}

	for table, before := range map[string]string{
		"bot_cosmetic_loadout":  loadoutBefore,
		"cosmetic_entitlements": entitlementBefore,
		"account_bot_links":     linkBefore,
	} {
		orderBy := "id"
		switch table {
		case "bot_cosmetic_loadout":
			orderBy = "bot_id, slot"
		case "cosmetic_entitlements":
			orderBy = "bot_id, cosmetic_id"
		case "account_bot_links":
			orderBy = "account_id, bot_id"
		}
		if after := snapshotPlatformMigrationTable(t, ctx, table, orderBy); after != before {
			t.Fatalf("%s changed during platform metadata migration\nbefore: %s\nafter:  %s", table, before, after)
		}
	}
}

func TestPostgresPlatformAuthorityBackfillRollsBackOnInvalidLegacyAgentID(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		DROP TRIGGER IF EXISTS customer_accounts_platform_metadata ON customer_accounts;
		DROP FUNCTION IF EXISTS insert_platform_account_metadata();
		DROP TABLE IF EXISTS
			platform_idempotency_records,
			platform_agent_link_events,
			platform_changes,
			platform_game_profiles,
			platform_agents,
			platform_account_metadata`); err != nil {
		t.Fatalf("drop platform metadata fixture: %v", err)
	}

	overlongAgentID := strings.Repeat("legacy-agent-", 12)
	if _, err := Pool.Exec(ctx, `
		INSERT INTO api_keys (id, key_hash, key_prefix) VALUES ('legacy-long-key', 'hash', 'legacy_long')`); err != nil {
		t.Fatalf("seed invalid legacy key: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO bots (id, api_key_id) VALUES ($1, 'legacy-long-key')`, overlongAgentID); err != nil {
		t.Fatalf("seed invalid legacy bot: %v", err)
	}

	err := EnsurePlatformAuthoritySchema(ctx)
	if err == nil || !strings.Contains(err.Error(), overlongAgentID) {
		t.Fatalf("invalid legacy agent error = %v, want offending ID", err)
	}
	var agentsInstalled, accountsInstalled bool
	if err := Pool.QueryRow(ctx, `
		SELECT to_regclass('platform_agents') IS NOT NULL,
		       to_regclass('platform_account_metadata') IS NOT NULL`).Scan(&agentsInstalled, &accountsInstalled); err != nil {
		t.Fatalf("inspect rolled-back schema: %v", err)
	}
	if agentsInstalled || accountsInstalled {
		t.Fatal("platform metadata tables survived a failed transactional backfill")
	}
}

func TestPostgresPlatformAuthorityBackfillRollsBackWhenLegacyAccountExceedsMaximumAgents(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-over-capacity@example.com",
		"https://id.example",
		"platform-over-capacity",
		"Platform Over Capacity",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	for index := range 11 {
		bot := accountAPIKeyTestBot(
			fmt.Sprintf("platform-over-capacity-key-%02d", index),
			fmt.Sprintf("platform-over-capacity-agent-%02d", index),
		)
		if err := CreateAPIKeyAndBot(
			ctx,
			bot.APIKeyID,
			fmt.Sprintf("over-capacity-hash-%02d", index),
			fmt.Sprintf("arena_overcap%02d", index),
			"127.0.0.1",
			bot,
		); err != nil {
			t.Fatalf("CreateAPIKeyAndBot %d: %v", index, err)
		}
		if _, err := Pool.Exec(ctx, `
			INSERT INTO account_bot_links (account_id, bot_id, linked_at)
			VALUES ($1, $2, NOW())`, account.ID, bot.ID); err != nil {
			t.Fatalf("seed legacy agent link %d: %v", index, err)
		}
	}

	if _, err := Pool.Exec(ctx, `
		DROP TRIGGER IF EXISTS customer_accounts_platform_metadata ON customer_accounts;
		DROP FUNCTION IF EXISTS insert_platform_account_metadata();
		DROP TABLE IF EXISTS
			platform_idempotency_records,
			platform_agent_link_events,
			platform_changes,
			platform_game_profiles,
			platform_agents,
			platform_account_metadata`); err != nil {
		t.Fatalf("drop platform metadata fixture: %v", err)
	}

	err = EnsurePlatformAuthoritySchema(ctx)
	if err == nil || !strings.Contains(err.Error(), account.ID) ||
		!strings.Contains(err.Error(), "11 current agents") ||
		!strings.Contains(err.Error(), "maximum_agents 10") {
		t.Fatalf("over-capacity migration error = %v, want account, current 11, maximum 10", err)
	}
	var metadataInstalled bool
	if err := Pool.QueryRow(ctx, `SELECT to_regclass('platform_account_metadata') IS NOT NULL`).Scan(&metadataInstalled); err != nil {
		t.Fatalf("inspect rolled-back account metadata: %v", err)
	}
	if metadataInstalled {
		t.Fatal("platform account metadata survived failed over-capacity backfill")
	}
	var preservedLinks int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_bot_links WHERE account_id = $1`, account.ID).Scan(&preservedLinks); err != nil {
		t.Fatalf("count preserved legacy links: %v", err)
	}
	if preservedLinks != 11 {
		t.Fatalf("legacy links after failed backfill = %d, want 11 preserved", preservedLinks)
	}
}

func TestPostgresArenaRegistrationCreatesPlatformEnrollmentAtomically(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	anonymousBot := accountAPIKeyTestBot("platform-anonymous-key", "platform-anonymous-agent")
	if err := CreateAPIKeyAndBot(
		ctx,
		anonymousBot.APIKeyID,
		"anonymous-hash",
		"arena_platanon",
		"127.0.0.1",
		anonymousBot,
	); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}
	assertArenaPlatformEnrollment(t, ctx, anonymousBot.ID, "arena", 1)

	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-registration@example.com",
		"https://id.example",
		"platform-registration",
		"Platform Registration",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	ownedBot := accountAPIKeyTestBot("platform-owned-key", "platform-owned-agent")
	if _, _, err := CreateAccountAPIKeyAndBot(
		ctx,
		account.ID,
		ownedBot.APIKeyID,
		"owned-hash",
		"arena_platown",
		"127.0.0.1",
		ownedBot,
	); err != nil {
		t.Fatalf("CreateAccountAPIKeyAndBot: %v", err)
	}
	assertArenaPlatformEnrollment(t, ctx, ownedBot.ID, "arena", 2)
}

func TestPostgresArenaRegistrationRollsBackCredentialWhenPlatformEnrollmentFails(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	overlongAgentID := strings.Repeat("new-agent-", 15)
	anonymousBot := accountAPIKeyTestBot("platform-rollback-key", overlongAgentID)
	err := CreateAPIKeyAndBot(
		ctx,
		anonymousBot.APIKeyID,
		"rollback-hash",
		"arena_platroll",
		"127.0.0.1",
		anonymousBot,
	)
	if err == nil {
		t.Fatal("anonymous registration accepted an agent outside the platform contract")
	}
	assertRegistrationRowsAbsent(t, ctx, anonymousBot.APIKeyID, anonymousBot.ID)

	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-rollback@example.com",
		"https://id.example",
		"platform-rollback",
		"Platform Rollback",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	ownedBot := accountAPIKeyTestBot("platform-owned-rollback-key", overlongAgentID)
	if _, _, err := CreateAccountAPIKeyAndBot(
		ctx,
		account.ID,
		ownedBot.APIKeyID,
		"owned-rollback-hash",
		"arena_ownroll",
		"127.0.0.1",
		ownedBot,
	); err == nil {
		t.Fatal("account registration accepted an agent outside the platform contract")
	}
	assertRegistrationRowsAbsent(t, ctx, ownedBot.APIKeyID, ownedBot.ID)
}

func TestPostgresPlatformChangeFeedIsOrderedBoundedAndIndexCompatible(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	bot := accountAPIKeyTestBot("platform-feed-key", "platform-feed-agent")
	if err := CreateAPIKeyAndBot(
		ctx,
		bot.APIKeyID,
		"feed-hash",
		"arena_platfeed",
		"127.0.0.1",
		bot,
	); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}
	changes, nextCursor, err := ListPlatformChanges(ctx, 0, 10)
	if err != nil {
		t.Fatalf("ListPlatformChanges registration: %v", err)
	}
	if nextCursor != 0 || len(changes) != 2 {
		t.Fatalf("registration change page = (%+v, %d), want two terminal-page changes", changes, nextCursor)
	}
	if changes[0].SubjectKind != "agent_identity" || changes[0].SubjectID != bot.ID || changes[0].Transition != "registered" || changes[0].Revision != 1 {
		t.Fatalf("agent registration change = %+v", changes[0])
	}
	if changes[1].SubjectKind != "game_profile" || changes[1].SubjectID == "" || changes[1].Transition != "enrolled" || changes[1].Revision != 1 {
		t.Fatalf("profile enrollment change = %+v", changes[1])
	}
	wireChange, err := json.Marshal(changes[0])
	if err != nil {
		t.Fatalf("marshal platform change: %v", err)
	}
	var wireFields map[string]any
	if err := json.Unmarshal(wireChange, &wireFields); err != nil {
		t.Fatalf("decode platform change: %v", err)
	}
	if _, ok := wireFields["change_id"].(string); !ok {
		t.Fatalf("wire change_id = %T, want string", wireFields["change_id"])
	}
	if _, ok := wireFields["occurred_at"]; !ok {
		t.Fatalf("wire platform change omits occurred_at: %s", wireChange)
	}
	if _, stale := wireFields["changed_at"]; stale {
		t.Fatalf("wire platform change exposes stale changed_at: %s", wireChange)
	}

	var baseChangeID int64
	if err := Pool.QueryRow(ctx, `SELECT MAX(change_id) FROM platform_changes`).Scan(&baseChangeID); err != nil {
		t.Fatalf("load base change ID: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO platform_changes (subject_kind, subject_id, transition, revision, changed_at)
		SELECT 'agent', 'irrelevant-' || sequence, 'registered', 1, NOW()
		FROM generate_series(1, 10000) AS sequence`); err != nil {
		t.Fatalf("seed irrelevant platform changes: %v", err)
	}

	firstPage, firstNext, err := ListPlatformChanges(ctx, baseChangeID, 1000)
	if err != nil {
		t.Fatalf("ListPlatformChanges clamped page: %v", err)
	}
	if len(firstPage) != MaxPlatformChangePageSize || firstNext != firstPage[len(firstPage)-1].ChangeID {
		t.Fatalf("clamped page = (%d rows, next %d), want (%d, last change ID)", len(firstPage), firstNext, MaxPlatformChangePageSize)
	}
	secondPage, _, err := ListPlatformChanges(ctx, firstNext, MaxPlatformChangePageSize)
	if err != nil {
		t.Fatalf("ListPlatformChanges second page: %v", err)
	}
	if len(secondPage) != MaxPlatformChangePageSize || secondPage[0].ChangeID <= firstPage[len(firstPage)-1].ChangeID {
		t.Fatalf("second page overlaps or is incomplete: first last=%d second=%+v", firstPage[len(firstPage)-1].ChangeID, secondPage)
	}
	for index := 1; index < len(firstPage); index++ {
		if firstPage[index].ChangeID <= firstPage[index-1].ChangeID {
			t.Fatalf("change feed is not strictly ordered at %d: %+v", index, firstPage)
		}
	}

	planRows, err := Pool.Query(ctx, `
		EXPLAIN (COSTS OFF)
		SELECT change_id, subject_kind, subject_id, transition, revision, changed_at
		FROM platform_changes
		WHERE change_id > $1
		ORDER BY change_id
		LIMIT 101`, baseChangeID+9900)
	if err != nil {
		t.Fatalf("explain bounded platform changes: %v", err)
	}
	defer planRows.Close()
	var plan strings.Builder
	for planRows.Next() {
		var line string
		if err := planRows.Scan(&line); err != nil {
			t.Fatalf("scan platform change plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := planRows.Err(); err != nil {
		t.Fatalf("iterate platform change plan: %v", err)
	}
	if planText := plan.String(); !strings.Contains(planText, "Index Scan using platform_changes_pkey") || strings.Contains(planText, "Seq Scan") {
		t.Fatalf("platform change feed plan is not index-bounded:\n%s", planText)
	}
}

func TestPostgresPlatformChangeFeedCannotSkipAnEarlierCommit(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	var baseChangeID int64
	if err := Pool.QueryRow(ctx, `SELECT COALESCE(MAX(change_id), 0) FROM platform_changes`).Scan(&baseChangeID); err != nil {
		t.Fatalf("load base change ID: %v", err)
	}

	first, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin first platform change: %v", err)
	}
	t.Cleanup(func() { _ = first.Rollback(context.Background()) })
	var firstBackendPID int
	if err := first.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&firstBackendPID); err != nil {
		t.Fatalf("load first backend PID: %v", err)
	}
	var firstChangeID int64
	if err := first.QueryRow(ctx, `
		INSERT INTO platform_changes (subject_kind, subject_id, transition, revision, changed_at)
		VALUES ('agent', 'commit-order-first', 'registered', 1, NOW())
		RETURNING change_id`).Scan(&firstChangeID); err != nil {
		t.Fatalf("insert first uncommitted platform change: %v", err)
	}

	second, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin second platform change: %v", err)
	}
	t.Cleanup(func() { _ = second.Rollback(context.Background()) })
	var secondBackendPID int
	if err := second.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&secondBackendPID); err != nil {
		t.Fatalf("load second backend PID: %v", err)
	}
	type insertResult struct {
		changeID int64
		err      error
	}
	secondInsert := make(chan insertResult, 1)
	go func() {
		var changeID int64
		err := second.QueryRow(context.Background(), `
			INSERT INTO platform_changes (subject_kind, subject_id, transition, revision, changed_at)
			VALUES ('agent', 'commit-order-second', 'registered', 1, NOW())
			RETURNING change_id`).Scan(&changeID)
		secondInsert <- insertResult{changeID: changeID, err: err}
	}()

	var secondResult insertResult
	secondAllocatedBeforeFirstCommit := false
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case secondResult = <-secondInsert:
			if secondResult.err != nil {
				t.Fatalf("insert second platform change: %v", secondResult.err)
			}
			secondAllocatedBeforeFirstCommit = true
		default:
		}
		if secondAllocatedBeforeFirstCommit {
			break
		}
		var blockedByFirst bool
		if err := Pool.QueryRow(ctx, `SELECT $1 = ANY(pg_blocking_pids($2))`, firstBackendPID, secondBackendPID).Scan(&blockedByFirst); err != nil {
			t.Fatalf("inspect platform change allocation lock: %v", err)
		}
		if blockedByFirst {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second platform change neither allocated nor blocked behind the first transaction")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if secondAllocatedBeforeFirstCommit {
		if err := second.Commit(ctx); err != nil {
			t.Fatalf("commit second platform change first: %v", err)
		}
		visible, _, err := ListPlatformChanges(ctx, baseChangeID, MaxPlatformChangePageSize)
		if err != nil {
			t.Fatalf("read change feed after inverted commit: %v", err)
		}
		if len(visible) != 1 || visible[0].ChangeID != secondResult.changeID {
			t.Fatalf("visible inverted-commit changes = %+v, want only second change %d", visible, secondResult.changeID)
		}
		if err := first.Commit(ctx); err != nil {
			t.Fatalf("commit earlier allocated platform change last: %v", err)
		}
		remaining, _, err := ListPlatformChanges(ctx, secondResult.changeID, MaxPlatformChangePageSize)
		if err != nil {
			t.Fatalf("read change feed after advancing cursor: %v", err)
		}
		for _, change := range remaining {
			if change.ChangeID == firstChangeID {
				t.Fatalf("change %d unexpectedly remained visible after cursor %d", firstChangeID, secondResult.changeID)
			}
		}
		t.Fatalf(
			"platform change %d committed before earlier allocated change %d; a consumer cursor at %d permanently skips the late commit",
			secondResult.changeID,
			firstChangeID,
			secondResult.changeID,
		)
	}

	if err := first.Commit(ctx); err != nil {
		t.Fatalf("commit first platform change: %v", err)
	}
	select {
	case secondResult = <-secondInsert:
		if secondResult.err != nil {
			t.Fatalf("insert second platform change after first commit: %v", secondResult.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second platform change remained blocked after first commit")
	}
	if err := second.Commit(ctx); err != nil {
		t.Fatalf("commit second platform change: %v", err)
	}

	changes, _, err := ListPlatformChanges(ctx, baseChangeID, MaxPlatformChangePageSize)
	if err != nil {
		t.Fatalf("read commit-ordered platform changes: %v", err)
	}
	if len(changes) != 2 ||
		changes[0].ChangeID != firstChangeID || changes[0].SubjectID != "commit-order-first" ||
		changes[1].ChangeID != secondResult.changeID || changes[1].SubjectID != "commit-order-second" {
		t.Fatalf("commit-ordered platform changes = %+v", changes)
	}
}

func TestPostgresPlatformChangeFeedRejectsNoncanonicalDurableValues(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	invalidChanges := []struct {
		subjectKind string
		transition  string
	}{
		{subjectKind: "agent", transition: "future_uncontracted_transition"},
		{subjectKind: "account", transition: "agent_future_uncontracted_transition"},
		{subjectKind: "agent", transition: "profile_status_future_uncontracted_transition"},
		{subjectKind: "game_profile", transition: "status_updated"},
	}
	for index, invalid := range invalidChanges {
		if _, err := Pool.Exec(ctx, `
			INSERT INTO platform_changes (subject_kind, subject_id, transition, revision, changed_at)
			VALUES ($1, $2, $3, 1, NOW())`, invalid.subjectKind, fmt.Sprintf("invalid-change-%d", index), invalid.transition); err != nil {
			t.Fatalf("seed noncanonical platform change %d: %v", index, err)
		}
		if _, _, err := ListPlatformChanges(ctx, 0, 10); err == nil || !strings.Contains(err.Error(), "noncanonical platform change") {
			t.Fatalf("ListPlatformChanges noncanonical error %d = %v", index, err)
		}
		if _, err := Pool.Exec(ctx, `DELETE FROM platform_changes WHERE subject_id = $1`, fmt.Sprintf("invalid-change-%d", index)); err != nil {
			t.Fatalf("delete noncanonical platform change %d: %v", index, err)
		}
	}
}

func TestPostgresPlatformProfileTransitionEnforcesRevisionAndIdempotency(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	bot := accountAPIKeyTestBot("platform-transition-key", "platform-transition-agent")
	if err := CreateAPIKeyAndBot(
		ctx,
		bot.APIKeyID,
		"transition-hash",
		"arena_plattrans",
		"127.0.0.1",
		bot,
	); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}

	suspend := PlatformProfileTransition{
		AgentID:          bot.ID,
		Game:             "arena",
		Status:           "suspended",
		ExpectedRevision: 1,
		IdempotencyKey:   "suspend-once",
	}
	first, err := TransitionPlatformProfile(ctx, suspend)
	if err != nil {
		t.Fatalf("TransitionPlatformProfile suspend: %v", err)
	}
	if first.AgentRevision != 2 || first.ProfileRevision != 2 || first.Status != "suspended" {
		t.Fatalf("suspend result = %+v, want both revisions 2", first)
	}
	changes, _, err := ListPlatformChanges(ctx, 0, 10)
	if err != nil {
		t.Fatalf("ListPlatformChanges after suspend: %v", err)
	}
	if len(changes) != 4 || changes[2].SubjectKind != "game_profile" || changes[2].Transition != "suspended" ||
		changes[3].SubjectKind != "agent_identity" || changes[3].Transition != "updated" {
		t.Fatalf("canonical suspend changes = %+v, want game_profile suspended and agent_identity updated", changes)
	}
	replayed, err := TransitionPlatformProfile(ctx, suspend)
	if err != nil {
		t.Fatalf("TransitionPlatformProfile replay: %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("idempotent replay = %+v, want exact %+v", replayed, first)
	}

	conflictingReplay := suspend
	conflictingReplay.Status = "active"
	conflictingReplay.ExpectedRevision = 2
	if _, err := TransitionPlatformProfile(ctx, conflictingReplay); !errors.Is(err, ErrPlatformIdempotencyConflict) {
		t.Fatalf("conflicting idempotency replay error = %v, want %v", err, ErrPlatformIdempotencyConflict)
	}
	stale := PlatformProfileTransition{
		AgentID:          bot.ID,
		Game:             "arena",
		Status:           "active",
		ExpectedRevision: 1,
		IdempotencyKey:   "stale-transition",
	}
	if _, err := TransitionPlatformProfile(ctx, stale); !errors.Is(err, ErrPlatformRevisionConflict) {
		t.Fatalf("stale transition error = %v, want %v", err, ErrPlatformRevisionConflict)
	}
	var staleRecords int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform_idempotency_records
		WHERE operation = 'transition_platform_profile' AND idempotency_key = $1`, stale.IdempotencyKey).Scan(&staleRecords); err != nil {
		t.Fatalf("count stale idempotency records: %v", err)
	}
	if staleRecords != 0 {
		t.Fatalf("stale transition stored %d idempotency records, want 0", staleRecords)
	}

	activate := PlatformProfileTransition{
		AgentID:          bot.ID,
		Game:             "arena",
		Status:           "active",
		ExpectedRevision: 2,
		IdempotencyKey:   "concurrent-activate",
	}
	const attempts = 8
	results := make(chan *PlatformProfileTransitionResult, attempts)
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := TransitionPlatformProfile(context.Background(), activate)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent transition: %v", err)
		}
	}
	var canonical *PlatformProfileTransitionResult
	for result := range results {
		if canonical == nil {
			canonical = result
			continue
		}
		if !reflect.DeepEqual(result, canonical) {
			t.Fatalf("concurrent replay = %+v, want exact %+v", result, canonical)
		}
	}
	if canonical == nil || canonical.AgentRevision != 3 || canonical.ProfileRevision != 3 || canonical.Status != "active" {
		t.Fatalf("concurrent transition result = %+v, want one revision increment", canonical)
	}

	var idempotencyRecords, transitionChanges int
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform_idempotency_records
		WHERE operation = 'transition_platform_profile' AND idempotency_key = $1`, activate.IdempotencyKey).Scan(&idempotencyRecords); err != nil {
		t.Fatalf("count concurrent idempotency records: %v", err)
	}
	if err := Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform_changes
		WHERE transition IN ('status_active', 'profile_status_active')
		  AND revision = 3`).Scan(&transitionChanges); err != nil {
		t.Fatalf("count concurrent transition changes: %v", err)
	}
	if idempotencyRecords != 1 || transitionChanges != 2 {
		t.Fatalf("concurrent persistence = %d idempotency records, %d changes; want 1 and 2", idempotencyRecords, transitionChanges)
	}
}

func TestPostgresPlatformProfileTransitionGuardsAgentRevision(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	bot := accountAPIKeyTestBot("platform-agent-revision-key", "platform-agent-revision")
	if err := CreateAPIKeyAndBot(
		ctx,
		bot.APIKeyID,
		"agent-revision-hash",
		"arena_platrevision",
		"127.0.0.1",
		bot,
	); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		UPDATE platform_agents SET revision = 2 WHERE agent_id = $1`, bot.ID); err != nil {
		t.Fatalf("advance agent revision independently: %v", err)
	}

	result, err := TransitionPlatformProfile(ctx, PlatformProfileTransition{
		AgentID:          bot.ID,
		Game:             "arena",
		Status:           "suspended",
		ExpectedRevision: 2,
		IdempotencyKey:   "agent-revision-transition",
	})
	if err != nil {
		t.Fatalf("TransitionPlatformProfile with current agent revision: %v", err)
	}
	if result.AgentRevision != 3 || result.ProfileRevision != 2 || result.Status != "suspended" {
		t.Fatalf("transition result = %+v, want agent revision 3 and profile revision 2", result)
	}
}

func TestPostgresPlatformProfileTransitionRejectsShortIdempotencyKey(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	bot := accountAPIKeyTestBot("platform-short-idempotency-key", "platform-short-idempotency")
	if err := CreateAPIKeyAndBot(
		ctx,
		bot.APIKeyID,
		"short-idempotency-hash",
		"arena_platshort",
		"127.0.0.1",
		bot,
	); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}

	_, err := TransitionPlatformProfile(ctx, PlatformProfileTransition{
		AgentID:          bot.ID,
		Game:             "arena",
		Status:           "suspended",
		ExpectedRevision: 1,
		IdempotencyKey:   "short",
	})
	if err == nil || !strings.Contains(err.Error(), "8-128 character idempotency key") {
		t.Fatalf("short idempotency key error = %v, want W1a lower-bound rejection", err)
	}
}

func TestPostgresPlatformAgentLinkHistorySurvivesRelinkAndUsesAccountIndex(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-links@example.com",
		"https://id.example",
		"platform-links",
		"Platform Links",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	bot := accountAPIKeyTestBot("platform-links-key", "platform-links-agent")
	if _, _, err := CreateAccountAPIKeyAndBot(
		ctx,
		account.ID,
		bot.APIKeyID,
		"links-hash",
		"arena_platlinks",
		"127.0.0.1",
		bot,
	); err != nil {
		t.Fatalf("CreateAccountAPIKeyAndBot: %v", err)
	}
	if unlinked, err := UnlinkBotFromCustomerAccount(ctx, account.ID, bot.ID); err != nil || !unlinked {
		t.Fatalf("UnlinkBotFromCustomerAccount = (%v, %v)", unlinked, err)
	}
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("LinkBotToCustomerAccount relink: %v", err)
	}

	events, nextCursor, err := ListPlatformAgentLinkEvents(ctx, account.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListPlatformAgentLinkEvents: %v", err)
	}
	if nextCursor != 0 || len(events) != 3 {
		t.Fatalf("link history = (%+v, %d), want three terminal-page events", events, nextCursor)
	}
	for index, wantStatus := range []string{"linked", "unlinked", "linked"} {
		if events[index].AccountID != account.ID || events[index].AgentID != bot.ID ||
			events[index].Status != wantStatus || events[index].Revision != int64(index+1) {
			t.Fatalf("link event %d = %+v, want %s revision %d", index, events[index], wantStatus, index+1)
		}
	}
	var agentRevision int64
	if err := Pool.QueryRow(ctx, `SELECT revision FROM platform_agents WHERE agent_id = $1`, bot.ID).Scan(&agentRevision); err != nil {
		t.Fatalf("load linked agent revision: %v", err)
	}
	if agentRevision != 4 {
		t.Fatalf("agent revision after register/link/unlink/relink = %d, want 4", agentRevision)
	}

	// If an older writer moves the compatibility projection without history,
	// reconciliation emits the missing prior-account unlink exactly once, then
	// the current-account link. A repeat repair must remain idempotent.
	otherAccount, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-links-other@example.com",
		"https://id.example",
		"platform-links-other",
		"Platform Links Other",
	)
	if err != nil {
		t.Fatalf("create other link account: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		UPDATE account_bot_links SET account_id = $2 WHERE account_id = $1 AND bot_id = $3`,
		account.ID, otherAccount.ID, bot.ID); err != nil {
		t.Fatalf("seed compatibility-link divergence: %v", err)
	}
	if err := EnsurePlatformAuthoritySchema(ctx); err != nil {
		t.Fatalf("reconcile compatibility-link divergence: %v", err)
	}
	if err := EnsurePlatformAuthoritySchema(ctx); err != nil {
		t.Fatalf("repeat reconciled schema: %v", err)
	}
	var latestAccount, latestStatus string
	var latestRevision int64
	if err := Pool.QueryRow(ctx, `
		SELECT account_id, status, revision
		FROM platform_agent_link_events
		WHERE agent_id = $1
		ORDER BY event_id DESC
		LIMIT 1`, bot.ID).Scan(&latestAccount, &latestStatus, &latestRevision); err != nil {
		t.Fatalf("load reconciled latest link: %v", err)
	}
	if latestAccount != otherAccount.ID || latestStatus != "linked" || latestRevision != 5 {
		t.Fatalf("reconciled latest link = (%q, %q, %d), want other account linked revision 5",
			latestAccount, latestStatus, latestRevision)
	}
	priorHistory, priorNext, err := ListPlatformAgentLinkEvents(ctx, account.ID, 0, 10)
	if err != nil {
		t.Fatalf("load prior account reconciled history: %v", err)
	}
	if priorNext != 0 || len(priorHistory) != 4 || priorHistory[3].Status != "unlinked" || priorHistory[3].Revision != 4 {
		t.Fatalf("prior account reconciled history = (%+v, %d), want terminal unlinked revision 4", priorHistory, priorNext)
	}
	currentHistory, currentNext, err := ListPlatformAgentLinkEvents(ctx, otherAccount.ID, 0, 10)
	if err != nil {
		t.Fatalf("load current account reconciled history: %v", err)
	}
	if currentNext != 0 || len(currentHistory) != 1 || currentHistory[0].Status != "linked" || currentHistory[0].Revision != 5 {
		t.Fatalf("current account reconciled history = (%+v, %d), want linked revision 5", currentHistory, currentNext)
	}

	if _, err := Pool.Exec(ctx, `
		INSERT INTO platform_agent_link_events (
			account_id, agent_id, status, revision, reason, occurred_at
		)
		SELECT 'irrelevant-account', 'irrelevant-agent-' || sequence,
		       'linked', 1, 'large-history-test', NOW()
		FROM generate_series(1, 10000) AS sequence`); err != nil {
		t.Fatalf("seed irrelevant link history: %v", err)
	}
	planRows, err := Pool.Query(ctx, `
		EXPLAIN (COSTS OFF)
		SELECT event_id, account_id, agent_id, status, revision, reason, occurred_at
		FROM platform_agent_link_events
		WHERE account_id = $1
		  AND (account_id, event_id) > ($1, $2)
		ORDER BY account_id, event_id
		LIMIT 101`, account.ID, 0)
	if err != nil {
		t.Fatalf("explain bounded link history: %v", err)
	}
	defer planRows.Close()
	var plan strings.Builder
	for planRows.Next() {
		var line string
		if err := planRows.Scan(&line); err != nil {
			t.Fatalf("scan link history plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := planRows.Err(); err != nil {
		t.Fatalf("iterate link history plan: %v", err)
	}
	if planText := plan.String(); (!strings.Contains(planText, "Index Scan using idx_platform_agent_link_events_account") &&
		!strings.Contains(planText, "Index Only Scan using idx_platform_agent_link_events_account") &&
		!strings.Contains(planText, "Bitmap Index Scan on idx_platform_agent_link_events_account")) ||
		strings.Contains(planText, "Seq Scan") {
		t.Fatalf("platform link history plan is not account-index-bounded:\n%s", planText)
	}
}

func TestPostgresPlatformAgentUnlinkEnforcesRevisionAndIdempotency(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	account, err := UpsertVerifiedCustomerAccount(
		ctx,
		"platform-unlink@example.com",
		"https://id.example",
		"platform-unlink",
		"Platform Unlink",
	)
	if err != nil {
		t.Fatalf("UpsertVerifiedCustomerAccount: %v", err)
	}
	bot := accountAPIKeyTestBot("platform-unlink-key", "platform-unlink-agent")
	if _, _, err := CreateAccountAPIKeyAndBot(
		ctx,
		account.ID,
		bot.APIKeyID,
		"platform-unlink-hash",
		"arena_platunlink",
		"127.0.0.1",
		bot,
	); err != nil {
		t.Fatalf("CreateAccountAPIKeyAndBot: %v", err)
	}
	capacity, err := GetPlatformAccountCapacity(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccountCapacity: %v", err)
	}
	command := PlatformAgentUnlinkCommand{
		AccountID:               account.ID,
		AgentID:                 bot.ID,
		ExpectedAccountRevision: capacity.Revision,
		Reason:                  "customer_request",
		IdempotencyKey:          "unlink-platform-agent",
	}
	first, err := UnlinkPlatformAgent(ctx, command)
	if err != nil {
		t.Fatalf("UnlinkPlatformAgent: %v", err)
	}
	if first.Status != "unlinked" || first.Revision != 2 || first.UnlinkedAt == nil || !first.UnlinkedAt.Equal(first.UpdatedAt) {
		t.Fatalf("unlink result = %+v, want unlinked revision 2", first)
	}
	changes, _, err := ListPlatformChanges(ctx, 0, 100)
	if err != nil {
		t.Fatalf("ListPlatformChanges after unlink: %v", err)
	}
	contract := loadPlatformV1ConsumerContract(t)
	validSubjectKinds := make(map[string]bool, len(contract.PlatformChange.SubjectKinds))
	for _, subjectKind := range contract.PlatformChange.SubjectKinds {
		validSubjectKinds[subjectKind] = true
	}
	validTransitions := make(map[string]bool, len(contract.PlatformChange.Transitions))
	for _, transition := range contract.PlatformChange.Transitions {
		validTransitions[transition] = true
	}
	for _, change := range changes {
		if !validSubjectKinds[change.SubjectKind] || !validTransitions[change.Transition] {
			t.Fatalf("noncanonical platform change = %+v", change)
		}
	}
	replayed, err := UnlinkPlatformAgent(ctx, command)
	if err != nil {
		t.Fatalf("UnlinkPlatformAgent replay: %v", err)
	}
	if !equalPlatformAgentLinkResult(first, replayed) {
		t.Fatalf("unlink replay = %+v, want exact %+v", replayed, first)
	}
	conflicting := command
	conflicting.Reason = "different_reason"
	if _, err := UnlinkPlatformAgent(ctx, conflicting); !errors.Is(err, ErrPlatformIdempotencyConflict) {
		t.Fatalf("conflicting unlink replay error = %v, want %v", err, ErrPlatformIdempotencyConflict)
	}
	linked, err := IsBotLinkedToCustomerAccount(ctx, account.ID, bot.ID)
	if err != nil || linked {
		t.Fatalf("link after exact unlink = (%v, %v), want false", linked, err)
	}
	if _, err := LinkBotToCustomerAccount(ctx, account.ID, bot.ID); err != nil {
		t.Fatalf("relink after exact unlink: %v", err)
	}
	stale := command
	stale.IdempotencyKey = "stale-unlink-platform-agent"
	if _, err := UnlinkPlatformAgent(ctx, stale); !errors.Is(err, ErrPlatformRevisionConflict) {
		t.Fatalf("stale unlink error = %v, want %v", err, ErrPlatformRevisionConflict)
	}
	linked, err = IsBotLinkedToCustomerAccount(ctx, account.ID, bot.ID)
	if err != nil || !linked {
		t.Fatalf("link after stale unlink = (%v, %v), want true", linked, err)
	}
}

func assertArenaPlatformEnrollment(t *testing.T, ctx context.Context, agentID, wantSource string, wantAgentRevision int64) {
	t.Helper()
	var source, agentStatus, game, profileStatus string
	var agentRevision, profileRevision int64
	if err := Pool.QueryRow(ctx, `
		SELECT agents.registration_source, agents.status, agents.revision,
		       profiles.game, profiles.status, profiles.revision
		FROM platform_agents agents
		JOIN platform_game_profiles profiles ON profiles.agent_id = agents.agent_id
		WHERE agents.agent_id = $1 AND profiles.game = 'arena'`, agentID).Scan(
		&source,
		&agentStatus,
		&agentRevision,
		&game,
		&profileStatus,
		&profileRevision,
	); err != nil {
		t.Fatalf("load platform enrollment for %s: %v", agentID, err)
	}
	if source != wantSource || agentStatus != "active" || agentRevision != wantAgentRevision ||
		game != "arena" || profileStatus != "active" || profileRevision != 1 {
		t.Fatalf(
			"platform enrollment for %s = (%q, %q, %d, %q, %q, %d)",
			agentID,
			source,
			agentStatus,
			agentRevision,
			game,
			profileStatus,
			profileRevision,
		)
	}
}

func assertRegistrationRowsAbsent(t *testing.T, ctx context.Context, keyID, agentID string) {
	t.Helper()
	for table, column := range map[string]string{
		"api_keys":               "id",
		"bots":                   "id",
		"platform_agents":        "agent_id",
		"platform_game_profiles": "agent_id",
		"account_api_keys":       "api_key_id",
		"account_bot_links":      "bot_id",
	} {
		value := agentID
		if table == "api_keys" || table == "account_api_keys" {
			value = keyID
		}
		var count int
		if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE `+column+` = $1`, value).Scan(&count); err != nil {
			t.Fatalf("count rolled-back %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("rolled-back registration left %d row(s) in %s", count, table)
		}
	}
}

func snapshotPlatformMigrationTable(t *testing.T, ctx context.Context, table, orderBy string) string {
	t.Helper()
	var snapshot string
	query := `SELECT COALESCE(jsonb_agg(to_jsonb(rows) ORDER BY ` + orderBy + `), '[]'::jsonb)::text FROM ` + table + ` rows`
	if err := Pool.QueryRow(ctx, query).Scan(&snapshot); err != nil {
		t.Fatalf("snapshot %s: %v", table, err)
	}
	return snapshot
}
