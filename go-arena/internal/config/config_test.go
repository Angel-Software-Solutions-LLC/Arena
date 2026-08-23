package config

import (
	"bytes"
	"log/slog"
	"math"
	"os"
	"strings"
	"testing"
)

func TestWarnInsecureDefaultsRecognizesExampleCredentials(t *testing.T) {
	previousConfig := C
	previousLogger := slog.Default()
	t.Cleanup(func() {
		C = previousConfig
		slog.SetDefault(previousLogger)
	})

	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	C = Config{
		DBPassword:           "changeme_arena_2026",
		AdminToken:           "changeme_admin_token",
		AdminLocalhostBypass: false,
	}

	warnInsecureDefaults()
	output := logs.String()
	for _, credential := range []string{"ARENA_DB_PASSWORD", "ARENA_ADMIN_TOKEN"} {
		if !strings.Contains(output, credential) {
			t.Fatalf("startup warnings = %q, want warning for %s example credential", output, credential)
		}
	}
}

func TestValidateMovementConfigRejectsUnsafeTerrainCadence(t *testing.T) {
	for _, value := range []float64{0, -0.1, math.NaN(), math.Inf(1), math.Inf(-1), 2.01} {
		if err := ValidateMovementConfig(Config{TerrainMoveCellsPerTick: value}); err == nil ||
			!strings.Contains(err.Error(), "ARENA_TERRAIN_MOVE_CELLS_PER_TICK") {
			t.Fatalf("ValidateMovementConfig(%v) error=%v, want terrain cadence error", value, err)
		}
	}
	for _, value := range []float64{0.01, 0.35, 2} {
		if err := ValidateMovementConfig(Config{TerrainMoveCellsPerTick: value}); err != nil {
			t.Fatalf("ValidateMovementConfig(%v) error=%v", value, err)
		}
	}
}

func TestLoadDefaultsAFKTimeoutToThirtySeconds(t *testing.T) {
	previous := C
	t.Cleanup(func() { C = previous })

	for _, key := range []string{"ARENA_TICK_RATE", "ARENA_AFK_TIMEOUT_TICKS"} {
		value, existed := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
	if err := os.Setenv("ARENA_TICK_RATE", "20"); err != nil {
		t.Fatal(err)
	}

	C = Config{}
	Load()
	if C.AFKTimeoutTicks != C.TickRate*30 {
		t.Fatalf("AFK timeout = %d ticks at %d Hz, want 30 seconds", C.AFKTimeoutTicks, C.TickRate)
	}
}

func TestValidateAFKConfigRejectsUnsafeTimeouts(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "zero tick rate", cfg: Config{TickRate: 0, AFKTimeoutTicks: 300}},
		{name: "negative timeout", cfg: Config{TickRate: 10, AFKTimeoutTicks: -1}},
		{name: "legacy three second timeout", cfg: Config{TickRate: 10, AFKTimeoutTicks: 30}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateAFKConfig(tt.cfg); err == nil || !strings.Contains(err.Error(), "ARENA_AFK_TIMEOUT_TICKS") {
				t.Fatalf("ValidateAFKConfig(%+v) error=%v, want AFK timeout error", tt.cfg, err)
			}
		})
	}

	if err := ValidateAFKConfig(Config{TickRate: 10, AFKTimeoutTicks: 300}); err != nil {
		t.Fatalf("ValidateAFKConfig(valid) error=%v", err)
	}
}

func validCosmeticsCheckoutConfig() Config {
	return Config{
		DBOptional:               false,
		CustomerOIDCEnabled:      true,
		CustomerOIDCIssuer:       "https://identity.example",
		CustomerOIDCClientID:     "arena-customers",
		CustomerOIDCClientSecret: "client-secret",
		CustomerOIDCRedirectURI:  "https://arena.example/api/v1/account/callback",
		CustomerOIDCSessionTTL:   24,
		CosmeticsCheckoutEnabled: true,
		StripeSecretKey:          "sk_test_checkout",
		StripePublishableKey:     "pk_test_checkout",
		StripeWebhookSecrets:     "whsec_current,whsec_previous",
		StripeSuccessURL:         "https://arena.example/dashboard?checkout=success",
		StripeCancelURL:          "https://arena.example/shop?checkout=cancelled",
		StripeReturnURL:          "https://arena.example/dashboard?checkout=return&session_id={CHECKOUT_SESSION_ID}",
		StripePortalReturnURL:    "https://arena.example/dashboard?tab=cosmetics",
		CosmeticsCheckoutRPM:     10,
		CosmeticsAccountReadRPM:  60,
	}
}

func TestValidateCosmeticsCheckoutConfigFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "customer OIDC disabled", mutate: func(cfg *Config) { cfg.CustomerOIDCEnabled = false }, want: "customer OIDC"},
		{name: "customer OIDC issuer missing", mutate: func(cfg *Config) { cfg.CustomerOIDCIssuer = "" }, want: "customer OIDC"},
		{name: "customer OIDC client ID missing", mutate: func(cfg *Config) { cfg.CustomerOIDCClientID = "" }, want: "customer OIDC"},
		{name: "customer OIDC client secret missing", mutate: func(cfg *Config) { cfg.CustomerOIDCClientSecret = "" }, want: "customer OIDC"},
		{name: "customer OIDC redirect missing", mutate: func(cfg *Config) { cfg.CustomerOIDCRedirectURI = "" }, want: "customer OIDC"},
		{name: "customer OIDC session disabled", mutate: func(cfg *Config) { cfg.CustomerOIDCSessionTTL = 0 }, want: "customer OIDC"},
		{name: "database optional", mutate: func(cfg *Config) { cfg.DBOptional = true }, want: "database"},
		{name: "Stripe key missing", mutate: func(cfg *Config) { cfg.StripeSecretKey = " " }, want: "ARENA_STRIPE_SECRET_KEY"},
		{name: "Stripe publishable key missing", mutate: func(cfg *Config) { cfg.StripePublishableKey = " " }, want: "ARENA_STRIPE_PUBLISHABLE_KEY"},
		{name: "Stripe secret key unknown", mutate: func(cfg *Config) { cfg.StripeSecretKey = "private_checkout" }, want: "ARENA_STRIPE_SECRET_KEY"},
		{name: "Stripe publishable key unknown", mutate: func(cfg *Config) { cfg.StripePublishableKey = "browser_checkout" }, want: "ARENA_STRIPE_PUBLISHABLE_KEY"},
		{name: "Stripe key modes mismatch", mutate: func(cfg *Config) { cfg.StripeSecretKey = "rk_live_checkout" }, want: "same Stripe mode"},
		{name: "webhook secrets missing", mutate: func(cfg *Config) { cfg.StripeWebhookSecrets = ", " }, want: "ARENA_STRIPE_WEBHOOK_SECRETS"},
		{name: "relative success URL", mutate: func(cfg *Config) { cfg.StripeSuccessURL = "/dashboard" }, want: "ARENA_STRIPE_SUCCESS_URL"},
		{name: "insecure public success URL", mutate: func(cfg *Config) { cfg.StripeSuccessURL = "http://arena.example/dashboard" }, want: "ARENA_STRIPE_SUCCESS_URL"},
		{name: "relative cancel URL", mutate: func(cfg *Config) { cfg.StripeCancelURL = "/shop" }, want: "ARENA_STRIPE_CANCEL_URL"},
		{name: "unsupported cancel URL scheme", mutate: func(cfg *Config) { cfg.StripeCancelURL = "ftp://arena.example/shop" }, want: "ARENA_STRIPE_CANCEL_URL"},
		{name: "relative embedded return URL", mutate: func(cfg *Config) { cfg.StripeReturnURL = "/dashboard" }, want: "ARENA_STRIPE_RETURN_URL"},
		{name: "embedded return URL missing session token", mutate: func(cfg *Config) { cfg.StripeReturnURL = "https://arena.example/dashboard" }, want: "{CHECKOUT_SESSION_ID}"},
		{name: "relative portal return URL", mutate: func(cfg *Config) { cfg.StripePortalReturnURL = "/dashboard" }, want: "ARENA_STRIPE_PORTAL_RETURN_URL"},
		{name: "checkout rate disabled", mutate: func(cfg *Config) { cfg.CosmeticsCheckoutRPM = 0 }, want: "ARENA_COSMETICS_CHECKOUT_RPM"},
		{name: "account cosmetics read rate disabled", mutate: func(cfg *Config) { cfg.CosmeticsAccountReadRPM = 0 }, want: "ARENA_COSMETICS_ACCOUNT_READ_RPM"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validCosmeticsCheckoutConfig()
			tt.mutate(&cfg)
			err := ValidateCosmeticsCheckoutConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateCosmeticsCheckoutConfig() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateCosmeticsCheckoutConfigRequiresStripeAPIKeyDuringWebhookSalesPause(t *testing.T) {
	cfg := Config{
		CosmeticsCheckoutEnabled: false,
		StripeWebhookSecrets:     "whsec_existing_subscriptions",
		CosmeticsAccountReadRPM:  60,
		StripePortalReturnURL:    "https://arena.example/dashboard?tab=cosmetics",
	}
	if err := ValidateCosmeticsCheckoutConfig(cfg); err == nil || !strings.Contains(err.Error(), "ARENA_STRIPE_SECRET_KEY") {
		t.Fatalf("sales-pause validation error = %v, want retained Stripe API key requirement", err)
	}
	cfg.StripeSecretKey = "mistyped-retained-key"
	if err := ValidateCosmeticsCheckoutConfig(cfg); err == nil || !strings.Contains(err.Error(), "sk_test") {
		t.Fatalf("sales-pause malformed key error = %v, want Stripe API key shape validation", err)
	}
	cfg.StripeSecretKey = "sk_live_retained_for_subscription_reconciliation"
	portalReturnURL := cfg.StripePortalReturnURL
	cfg.StripePortalReturnURL = ""
	if err := ValidateCosmeticsCheckoutConfig(cfg); err == nil || !strings.Contains(err.Error(), "ARENA_STRIPE_PORTAL_RETURN_URL") {
		t.Fatalf("sales-pause portal validation error = %v", err)
	}
	cfg.StripePortalReturnURL = portalReturnURL
	if err := ValidateCosmeticsCheckoutConfig(cfg); err != nil {
		t.Fatalf("sales pause with retained Stripe API key rejected: %v", err)
	}
}

func TestValidateCosmeticsCheckoutConfigRequiresAccountReadRateDuringSalesPause(t *testing.T) {
	cfg := Config{CosmeticsAccountReadRPM: 0}
	if err := ValidateCosmeticsCheckoutConfig(cfg); err == nil || !strings.Contains(err.Error(), "ARENA_COSMETICS_ACCOUNT_READ_RPM") {
		t.Fatalf("disabled checkout read-rate validation error = %v", err)
	}
}

func TestValidateCosmeticsCheckoutConfigAllowsDisabledAndLoopbackDevelopment(t *testing.T) {
	if err := ValidateCosmeticsCheckoutConfig(Config{CosmeticsAccountReadRPM: 60}); err != nil {
		t.Fatalf("disabled checkout should not require payment config: %v", err)
	}

	cfg := validCosmeticsCheckoutConfig()
	cfg.StripeSuccessURL = "http://localhost:8000/dashboard"
	cfg.StripeCancelURL = "http://127.0.0.1:8000/shop"
	cfg.StripeReturnURL = "http://localhost:8000/dashboard?session_id={CHECKOUT_SESSION_ID}"
	cfg.StripePortalReturnURL = "http://localhost:8000/dashboard"
	if err := ValidateCosmeticsCheckoutConfig(cfg); err != nil {
		t.Fatalf("loopback HTTP checkout URLs should be allowed for development: %v", err)
	}

	cfg.StripeSuccessURL = "http://[::1]:8000/dashboard"
	if err := ValidateCosmeticsCheckoutConfig(cfg); err != nil {
		t.Fatalf("IPv6 loopback HTTP checkout URL should be allowed for development: %v", err)
	}
}

func TestCosmeticsCheckoutRequiresAccountsSignIn(t *testing.T) {
	// The inverse of what used to be asserted here. Checkout could once be
	// satisfied by Arena's own verified-email sign-in *instead of* OIDC; that
	// path is gone, so the only thing that can satisfy it is a fully
	// configured Accounts client, and its absence must fail closed.
	cfg := validCosmeticsCheckoutConfig()
	cfg.CustomerOIDCEnabled = false
	if err := ValidateCosmeticsCheckoutConfig(cfg); err == nil {
		t.Fatal("checkout was accepted with no way for a customer to sign in")
	}

	cfg = validCosmeticsCheckoutConfig()
	cfg.CustomerOIDCClientSecret = ""
	if err := ValidateCosmeticsCheckoutConfig(cfg); err == nil {
		t.Fatal("checkout was accepted with a half-configured Accounts client")
	}
}

func TestLoadReadsCosmeticsCheckoutDefaultsAndSecretRotation(t *testing.T) {
	previous := C
	t.Cleanup(func() { C = previous })
	for _, key := range []string{
		"ARENA_COSMETICS_CHECKOUT_ENABLED",
		"ARENA_STRIPE_SECRET_KEY",
		"ARENA_STRIPE_PUBLISHABLE_KEY",
		"ARENA_STRIPE_WEBHOOK_SECRETS",
		"ARENA_STRIPE_SUCCESS_URL",
		"ARENA_STRIPE_CANCEL_URL",
		"ARENA_STRIPE_RETURN_URL",
		"ARENA_STRIPE_PORTAL_RETURN_URL",
		"ARENA_STRIPE_AUTOMATIC_TAX",
		"ARENA_COSMETICS_CHECKOUT_RPM",
	} {
		value, existed := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
	C = Config{}
	Load()
	if C.CosmeticsCheckoutEnabled || C.StripeAutomaticTax {
		t.Fatal("cosmetics checkout and automatic tax must default disabled")
	}
	if C.CosmeticsCheckoutRPM != 10 {
		t.Fatalf("CosmeticsCheckoutRPM = %d, want 10", C.CosmeticsCheckoutRPM)
	}
	if C.CosmeticsAccountReadRPM != 60 {
		t.Fatalf("CosmeticsAccountReadRPM = %d, want 60", C.CosmeticsAccountReadRPM)
	}

	t.Setenv("ARENA_COSMETICS_CHECKOUT_ENABLED", "true")
	t.Setenv("ARENA_CUSTOMER_OIDC_ENABLED", "true")
	t.Setenv("ARENA_CUSTOMER_OIDC_ISSUER", "https://identity.example")
	t.Setenv("ARENA_CUSTOMER_OIDC_CLIENT_ID", "arena-customers")
	t.Setenv("ARENA_CUSTOMER_OIDC_CLIENT_SECRET", "client-secret")
	t.Setenv("ARENA_CUSTOMER_OIDC_REDIRECT_URI", "https://arena.example/api/v1/account/callback")
	t.Setenv("ARENA_CUSTOMER_OIDC_SESSION_TTL_HOURS", "24")
	t.Setenv("ARENA_DB_OPTIONAL", "false")
	t.Setenv("ARENA_STRIPE_SECRET_KEY", "sk_test_checkout")
	t.Setenv("ARENA_STRIPE_PUBLISHABLE_KEY", "pk_test_checkout")
	t.Setenv("ARENA_STRIPE_WEBHOOK_SECRETS", "whsec_current, whsec_previous")
	t.Setenv("ARENA_STRIPE_SUCCESS_URL", "https://arena.example/dashboard")
	t.Setenv("ARENA_STRIPE_CANCEL_URL", "https://arena.example/shop")
	t.Setenv("ARENA_STRIPE_RETURN_URL", "https://arena.example/dashboard?session_id={CHECKOUT_SESSION_ID}")
	t.Setenv("ARENA_STRIPE_PORTAL_RETURN_URL", "https://arena.example/dashboard")
	C = Config{}
	Load()
	secrets := ParseStripeWebhookSecrets(C.StripeWebhookSecrets)
	if len(secrets) != 2 || secrets[0] != "whsec_current" || secrets[1] != "whsec_previous" {
		t.Fatalf("ParseStripeWebhookSecrets() = %#v, want normalized rotation list", secrets)
	}
}

func TestLoadInvokesCosmeticsCheckoutValidation(t *testing.T) {
	previous := C
	t.Cleanup(func() { C = previous })
	t.Setenv("ARENA_COSMETICS_CHECKOUT_ENABLED", "true")
	t.Setenv("ARENA_CUSTOMER_OIDC_ENABLED", "false")
	C = Config{}

	defer func() {
		if recover() == nil {
			t.Fatal("Load() did not fail closed for incomplete checkout configuration")
		}
	}()
	Load()
}

func TestLoadReadsManagedMigrationRoles(t *testing.T) {
	previous := C
	t.Cleanup(func() { C = previous })
	t.Setenv("ARENA_DB_MIGRATIONS_MANAGED", "true")
	t.Setenv("ARENA_RUNTIME_DB_USER", "arena_app")

	Load()
	if !C.DBMigrationsManaged {
		t.Fatal("ARENA_DB_MIGRATIONS_MANAGED=true was not loaded")
	}
	if C.DBRuntimeUser != "arena_app" {
		t.Fatalf("ARENA_RUNTIME_DB_USER = %q, want arena_app", C.DBRuntimeUser)
	}
	if ShouldAutoMigrateDatabase() {
		t.Fatal("managed runtime must not attempt schema DDL")
	}

	C.DBMigrationsManaged = false
	if !ShouldAutoMigrateDatabase() {
		t.Fatal("single-role local runtime should retain automatic migrations")
	}
}

func TestLoadReadsCustomerAPIKeyAbuseLimits(t *testing.T) {
	previous := C
	t.Cleanup(func() { C = previous })
	t.Setenv("ARENA_CUSTOMER_API_KEY_MUTATION_RPM", "31")
	t.Setenv("ARENA_CUSTOMER_API_KEY_CREATE_PER_HOUR", "11")
	t.Setenv("ARENA_CUSTOMER_API_KEY_REVOKE_PER_HOUR", "21")
	t.Setenv("ARENA_CUSTOMER_BOT_LINK_PER_HOUR", "12")
	C = Config{}

	Load()

	if C.CustomerAPIKeyMutationRPM != 31 || C.CustomerAPIKeyCreatePerHour != 11 ||
		C.CustomerAPIKeyRevokePerHour != 21 || C.CustomerBotLinkPerHour != 12 {
		t.Fatalf("customer API-key abuse limits were not loaded: %+v", C)
	}
}

func TestLoadReadsPublicAPIKeyRegistrationLimits(t *testing.T) {
	previous := C
	t.Cleanup(func() { C = previous })
	t.Setenv("ARENA_RATE_LIMIT_REGISTER_RPM", "7")
	t.Setenv("ARENA_RATE_LIMIT_REGISTER_PER_HOUR", "73")
	t.Setenv("ARENA_TRUSTED_PROXY_CIDRS", "10.20.30.0/24,2001:db8:100::/48")
	t.Setenv("ARENA_TRUSTED_CLOUDFLARE_PROXY_CIDRS", "10.20.30.12/32")
	C = Config{}

	Load()

	if C.RateLimitRegisterRPM != 7 || C.RateLimitRegisterPerHour != 73 {
		t.Fatalf("public API-key registration limits were not loaded: %+v", C)
	}
	if C.TrustedProxyCIDRs != "10.20.30.0/24,2001:db8:100::/48" {
		t.Fatalf("trusted proxy CIDRs were not loaded: %q", C.TrustedProxyCIDRs)
	}
	if C.TrustedCloudflareProxyCIDRs != "10.20.30.12/32" {
		t.Fatalf("trusted Cloudflare proxy CIDRs were not loaded: %q", C.TrustedCloudflareProxyCIDRs)
	}
}

func TestResolveShoveSettingsUsesWholePositiveGridTiles(t *testing.T) {
	tests := []struct {
		name                     string
		rangeIn, knockbackIn     float64
		rangeWant, knockbackWant float64
	}{
		{name: "defaults remain exact", rangeIn: 1, knockbackIn: 2, rangeWant: 1, knockbackWant: 2},
		{name: "fractional overrides round once", rangeIn: 1.6, knockbackIn: 3.4, rangeWant: 2, knockbackWant: 3},
		{name: "nonpositive values use defaults", rangeIn: 0, knockbackIn: -2, rangeWant: DefaultShoveRangeTiles, knockbackWant: DefaultShoveKnockbackTiles},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rangeGot, knockbackGot := resolveShoveSettings(tt.rangeIn, tt.knockbackIn)
			if rangeGot != tt.rangeWant || knockbackGot != tt.knockbackWant {
				t.Fatalf("resolveShoveSettings(%v, %v) = (%v, %v), want (%v, %v)", tt.rangeIn, tt.knockbackIn, rangeGot, knockbackGot, tt.rangeWant, tt.knockbackWant)
			}
		})
	}
}

func TestResolveEloSettings(t *testing.T) {
	tests := []struct {
		name                           string
		min, max, starting             int
		wantMin, wantMax, wantStarting int
	}{
		{name: "valid custom values", min: 800, max: 1600, starting: 1200, wantMin: 800, wantMax: 1600, wantStarting: 1200},
		{name: "inverted bounds use one default pair", min: 5000, max: 2000, starting: 1500, wantMin: DefaultEloMin, wantMax: DefaultEloMax, wantStarting: 1500},
		{name: "nonpositive bounds use one default pair", min: 0, max: 0, starting: 1000, wantMin: DefaultEloMin, wantMax: DefaultEloMax, wantStarting: 1000},
		{name: "high starting rating clamps", min: 800, max: 1200, starting: 5000, wantMin: 800, wantMax: 1200, wantStarting: 1200},
		{name: "low starting rating clamps", min: 800, max: 1200, starting: 200, wantMin: 800, wantMax: 1200, wantStarting: 800},
		{name: "missing starting rating uses bounded default", min: 1500, max: 2000, starting: 0, wantMin: 1500, wantMax: 2000, wantStarting: 1500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			minElo, maxElo, startingElo := resolveEloSettings(tt.min, tt.max, tt.starting)
			if minElo != tt.wantMin || maxElo != tt.wantMax || startingElo != tt.wantStarting {
				t.Fatalf("resolveEloSettings(%d, %d, %d) = (%d, %d, %d), want (%d, %d, %d)",
					tt.min, tt.max, tt.starting, minElo, maxElo, startingElo,
					tt.wantMin, tt.wantMax, tt.wantStarting)
			}
		})
	}
}

func TestEloHelpersUseSameDefensiveBounds(t *testing.T) {
	previous := C
	t.Cleanup(func() { C = previous })
	C.EloMin = 5000
	C.EloMax = 2000
	C.EloStarting = 9000

	minElo, maxElo := EloBounds()
	if minElo != DefaultEloMin || maxElo != DefaultEloMax {
		t.Fatalf("EloBounds() = %d..%d, want %d..%d", minElo, maxElo, DefaultEloMin, DefaultEloMax)
	}
	if got := StartingElo(); got != DefaultEloMax {
		t.Fatalf("StartingElo() = %d, want %d", got, DefaultEloMax)
	}
	if got := ClampElo(-1); got != DefaultEloMin {
		t.Fatalf("ClampElo(-1) = %d, want %d", got, DefaultEloMin)
	}
	if got := ClampElo(99999); got != DefaultEloMax {
		t.Fatalf("ClampElo(99999) = %d, want %d", got, DefaultEloMax)
	}
}

func TestResolveWeaponAutoBalanceSettings(t *testing.T) {
	tests := []struct {
		name                             string
		minDamage, maxDamage             float64
		minCooldown, maxCooldown         float64
		maxEvidenceRounds                int
		wantMinDamage, wantMaxDamage     float64
		wantMinCooldown, wantMaxCooldown float64
		wantMaxEvidenceRounds            int
	}{
		{
			name: "valid widened rails", minDamage: 0.65, maxDamage: 1.50,
			minCooldown: 0.70, maxCooldown: 1.45, maxEvidenceRounds: 72,
			wantMinDamage: 0.65, wantMaxDamage: 1.50,
			wantMinCooldown: 0.70, wantMaxCooldown: 1.45, wantMaxEvidenceRounds: 72,
		},
		{
			name: "inverted damage rail falls back", minDamage: 1.20, maxDamage: 0.80,
			minCooldown: 0.75, maxCooldown: 1.35, maxEvidenceRounds: 48,
			wantMinDamage: DefaultWeaponAutoBalanceMinDamageScale, wantMaxDamage: DefaultWeaponAutoBalanceMaxDamageScale,
			wantMinCooldown: 0.75, wantMaxCooldown: 1.35, wantMaxEvidenceRounds: 48,
		},
		{
			name: "rails must contain neutral", minDamage: 1.05, maxDamage: 1.50,
			minCooldown: 0.20, maxCooldown: 0.90, maxEvidenceRounds: 1,
			wantMinDamage: DefaultWeaponAutoBalanceMinDamageScale, wantMaxDamage: DefaultWeaponAutoBalanceMaxDamageScale,
			wantMinCooldown: DefaultWeaponAutoBalanceMinCooldownScale, wantMaxCooldown: DefaultWeaponAutoBalanceMaxCooldownScale,
			wantMaxEvidenceRounds: DefaultWeaponAutoBalanceMaxEvidenceRounds,
		},
		{
			name: "absolute safety rails reject extreme values", minDamage: 0.01, maxDamage: 9,
			minCooldown: 0.01, maxCooldown: 9, maxEvidenceRounds: 9999,
			wantMinDamage: DefaultWeaponAutoBalanceMinDamageScale, wantMaxDamage: DefaultWeaponAutoBalanceMaxDamageScale,
			wantMinCooldown: DefaultWeaponAutoBalanceMinCooldownScale, wantMaxCooldown: DefaultWeaponAutoBalanceMaxCooldownScale,
			wantMaxEvidenceRounds: DefaultWeaponAutoBalanceMaxEvidenceRounds,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			minDamage, maxDamage, minCooldown, maxCooldown, maxEvidenceRounds := resolveWeaponAutoBalanceSettings(
				tt.minDamage, tt.maxDamage, tt.minCooldown, tt.maxCooldown, tt.maxEvidenceRounds,
			)
			if minDamage != tt.wantMinDamage || maxDamage != tt.wantMaxDamage ||
				minCooldown != tt.wantMinCooldown || maxCooldown != tt.wantMaxCooldown ||
				maxEvidenceRounds != tt.wantMaxEvidenceRounds {
				t.Fatalf("resolved balance settings = %.2f..%.2f / %.2f..%.2f / %d, want %.2f..%.2f / %.2f..%.2f / %d",
					minDamage, maxDamage, minCooldown, maxCooldown, maxEvidenceRounds,
					tt.wantMinDamage, tt.wantMaxDamage, tt.wantMinCooldown, tt.wantMaxCooldown, tt.wantMaxEvidenceRounds)
			}
		})
	}
}

func TestWeaponAutoBalanceHelpersUseDefensiveDefaults(t *testing.T) {
	previous := C
	t.Cleanup(func() { C = previous })
	C.WeaponAutoBalanceMinDamageScale = 2
	C.WeaponAutoBalanceMaxDamageScale = 3
	C.WeaponAutoBalanceMinCooldownScale = -1
	C.WeaponAutoBalanceMaxCooldownScale = 4
	C.WeaponAutoBalanceMaxEvidenceRounds = 0

	minDamage, maxDamage := WeaponAutoBalanceDamageBounds()
	minCooldown, maxCooldown := WeaponAutoBalanceCooldownBounds()
	if minDamage != DefaultWeaponAutoBalanceMinDamageScale || maxDamage != DefaultWeaponAutoBalanceMaxDamageScale {
		t.Fatalf("damage bounds = %.2f..%.2f", minDamage, maxDamage)
	}
	if minCooldown != DefaultWeaponAutoBalanceMinCooldownScale || maxCooldown != DefaultWeaponAutoBalanceMaxCooldownScale {
		t.Fatalf("cooldown bounds = %.2f..%.2f", minCooldown, maxCooldown)
	}
	if got := WeaponAutoBalanceEvidenceLimit(6); got != DefaultWeaponAutoBalanceMaxEvidenceRounds {
		t.Fatalf("evidence limit = %d, want %d", got, DefaultWeaponAutoBalanceMaxEvidenceRounds)
	}
}

func TestWeaponAutoBalanceStepBounds(t *testing.T) {
	previous := C
	t.Cleanup(func() { C = previous })

	C.WeaponAutoBalanceMinStep = 0.004
	C.WeaponAutoBalanceStartStep = 0.04
	if minStep, startStep := WeaponAutoBalanceStepBounds(); minStep != 0.004 || startStep != 0.04 {
		t.Fatalf("valid step bounds = %.3f/%.3f, want 0.004/0.040", minStep, startStep)
	}

	C.WeaponAutoBalanceMinStep = -1
	C.WeaponAutoBalanceStartStep = 9
	if minStep, startStep := WeaponAutoBalanceStepBounds(); minStep != 0.005 || startStep != 0.05 {
		t.Fatalf("defensive step bounds = %.3f/%.3f, want 0.005/0.050", minStep, startStep)
	}
}
