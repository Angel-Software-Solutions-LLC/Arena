package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    commandMode
		wantErr bool
	}{
		{name: "default starts server", want: commandServe},
		{name: "migration only", args: []string{"migrate"}, want: commandMigrate},
		{name: "credential check only", args: []string{"check-oidc"}, want: commandCheckOIDC},
		{name: "credential check rejects extra arguments", args: []string{"check-oidc", "extra"}, wantErr: true},
		{name: "unknown command", args: []string{"unknown"}, wantErr: true},
		{name: "migration rejects extra arguments", args: []string{"migrate", "extra"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCommand(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseCommand(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("parseCommand(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

type schemaRow struct {
	missing string
	err     error
}

func (r schemaRow) Scan(dest ...interface{}) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*string)) = r.missing
	return nil
}

type schemaQueryerStub struct {
	row pgx.Row
}

func (s schemaQueryerStub) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return s.row
}

func TestVerifyManagedSchema(t *testing.T) {
	if err := verifyManagedSchema(context.Background(), schemaQueryerStub{row: schemaRow{}}); err != nil {
		t.Fatalf("complete schema rejected: %v", err)
	}

	err := verifyManagedSchema(context.Background(), schemaQueryerStub{row: schemaRow{missing: "rounds.persisted_order, cosmetic_items.id"}})
	if err == nil || !strings.Contains(err.Error(), "rounds.persisted_order") {
		t.Fatalf("missing schema error = %v", err)
	}

	queryErr := errors.New("catalog unavailable")
	err = verifyManagedSchema(context.Background(), schemaQueryerStub{row: schemaRow{err: queryErr}})
	if !errors.Is(err, queryErr) {
		t.Fatalf("query error = %v, want wrapped %v", err, queryErr)
	}
}

func TestManagedSchemaPreflightRequiresCosmeticCatalogAdministration(t *testing.T) {
	for _, required := range []string{
		"('cosmetic_categories', 'id')",
		"('cosmetic_items', 'category_id')",
		"('cosmetic_items', 'sort_order')",
		"('cosmetic_packs', 'id')",
		"('cosmetic_pack_items', 'pack_id')",
		"('cosmetic_catalog_audit', 'id')",
	} {
		if !strings.Contains(managedSchemaPreflightQuery, required) {
			t.Errorf("managed schema preflight is missing %s", required)
		}
	}
}

func TestManagedSchemaPreflightOmitsExternalDemoBotPersistence(t *testing.T) {
	for _, removed := range []string{"demo_bot_keys", "demo_bot_templates"} {
		if strings.Contains(managedSchemaPreflightQuery, removed) {
			t.Errorf("managed schema preflight still requires removed external-fleet table %s", removed)
		}
	}
}

func TestManagedSchemaPreflightRequiresCosmeticCommerceLedger(t *testing.T) {
	for _, required := range []string{
		"('cosmetic_orders', 'account_id')",
		"('cosmetic_orders', 'pack_description')",
		"('cosmetic_orders', 'expected_subtotal_cents')",
		"('cosmetic_orders', 'cumulative_charge_refunded_cents')",
		"('cosmetic_orders', 'stripe_checkout_session_id')",
		"('cosmetic_orders', 'stripe_payment_intent_id')",
		"('cosmetic_order_items', 'item_id')",
		"('cosmetic_order_licenses', 'license_id')",
		"('cosmetic_payment_events', 'payload_hash')",
		"('cosmetic_order_refunds', 'refund_id')",
		"('cosmetic_subscriptions', 'id')",
		"('cosmetic_subscriptions', 'stripe_subscription_id')",
		"('cosmetic_subscriptions', 'last_provider_event_created_at')",
		"('cosmetic_subscriptions', 'last_provider_state_observed_at')",
		"('cosmetic_subscription_licenses', 'license_id')",
		"('cosmetic_subscription_events', 'payload_hash')",
	} {
		if !strings.Contains(managedSchemaPreflightQuery, required) {
			t.Errorf("managed schema preflight is missing %s", required)
		}
	}
}

func TestManagedSchemaPreflightRequiresAccountAPIKeyOwnership(t *testing.T) {
	if required := "('account_api_keys', 'account_id')"; !strings.Contains(managedSchemaPreflightQuery, required) {
		t.Fatalf("managed schema preflight is missing %s", required)
	}
}

func TestManagedSchemaPreflightRequiresPlatformAuthorityMetadata(t *testing.T) {
	for _, required := range []string{
		"('platform_account_metadata', 'account_id')",
		"('platform_account_metadata', 'status')",
		"('platform_account_metadata', 'maximum_agents')",
		"('platform_account_metadata', 'revision')",
		"('platform_account_metadata', 'created_at')",
		"('platform_account_metadata', 'updated_at')",
		"('platform_agents', 'agent_id')",
		"('platform_agents', 'registration_source')",
		"('platform_agents', 'status')",
		"('platform_agents', 'revision')",
		"('platform_agents', 'created_at')",
		"('platform_agents', 'updated_at')",
		"('platform_game_profiles', 'profile_id')",
		"('platform_game_profiles', 'agent_id')",
		"('platform_game_profiles', 'game')",
		"('platform_game_profiles', 'status')",
		"('platform_game_profiles', 'revision')",
		"('platform_game_profiles', 'enrolled_at')",
		"('platform_game_profiles', 'updated_at')",
		"('platform_changes', 'change_id')",
		"('platform_changes', 'subject_kind')",
		"('platform_changes', 'subject_id')",
		"('platform_changes', 'transition')",
		"('platform_changes', 'revision')",
		"('platform_changes', 'changed_at')",
		"('platform_idempotency_records', 'operation')",
		"('platform_idempotency_records', 'idempotency_key')",
		"('platform_idempotency_records', 'request_hash')",
		"('platform_idempotency_records', 'response')",
		"('platform_idempotency_records', 'subject_kind')",
		"('platform_idempotency_records', 'subject_id')",
		"('platform_idempotency_records', 'revision')",
		"('platform_idempotency_records', 'created_at')",
		"('platform_agent_link_events', 'event_id')",
		"('platform_agent_link_events', 'account_id')",
		"('platform_agent_link_events', 'agent_id')",
		"('platform_agent_link_events', 'status')",
		"('platform_agent_link_events', 'revision')",
		"('platform_agent_link_events', 'reason')",
		"('platform_agent_link_events', 'occurred_at')",
	} {
		if !strings.Contains(managedSchemaPreflightQuery, required) {
			t.Errorf("managed schema preflight is missing %s", required)
		}
	}
}

func TestRuntimePrivilegeStatementsAreScopedAndRoleValidated(t *testing.T) {
	statements, err := runtimePrivilegeStatements("arena_app")
	if err != nil {
		t.Fatalf("runtimePrivilegeStatements: %v", err)
	}
	joined := strings.Join(statements, "\n")
	for _, required := range []string{
		`GRANT USAGE ON SCHEMA public TO "arena_app"`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO "arena_app"`,
		`GRANT TRUNCATE ON TABLE public.bot_stats, public.round_bot_stats TO "arena_app"`,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO "arena_app"`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "arena_app"`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO "arena_app"`,
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("privilege statements missing %q:\n%s", required, joined)
		}
	}
	if _, err := runtimePrivilegeStatements(`arena_app"; DROP SCHEMA public; --`); err == nil {
		t.Fatal("unsafe role name was accepted")
	}
}
