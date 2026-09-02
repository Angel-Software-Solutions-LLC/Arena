package config

import (
	"bytes"
	"log/slog"
	"math"
	"os"
	"reflect"
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

func TestValidateCosmeticsConfig(t *testing.T) {
	if err := ValidateCosmeticsConfig(Config{CosmeticsAccountReadRPM: 60}); err != nil {
		t.Fatalf("no shop address is a supported configuration: %v", err)
	}
	if err := ValidateCosmeticsConfig(Config{CosmeticsAccountReadRPM: 60, AccountsShopURL: "https://accounts.angel-serv.com/portal"}); err != nil {
		t.Fatalf("an https shop address was rejected: %v", err)
	}
	if err := ValidateCosmeticsConfig(Config{CosmeticsAccountReadRPM: 0}); err == nil || !strings.Contains(err.Error(), "ARENA_COSMETICS_ACCOUNT_READ_RPM") {
		t.Fatalf("read-rate validation error = %v", err)
	}
	for _, bad := range []string{"/portal", "http://accounts.angel-serv.com/portal", "javascript:alert(1)", "https://"} {
		if err := ValidateCosmeticsConfig(Config{CosmeticsAccountReadRPM: 60, AccountsShopURL: bad}); err == nil ||
			!strings.Contains(err.Error(), "ARENA_ACCOUNTS_SHOP_URL") {
			t.Fatalf("shop address %q validation error = %v, want the https requirement", bad, err)
		}
	}
}

// TestStripeConfigurationIsRetired pins the removal: the variables that used
// to configure Arena's own checkout are not read, and setting them changes
// nothing. Commerce lives in Angel Accounts.
func TestStripeConfigurationIsRetired(t *testing.T) {
	previous := C
	t.Cleanup(func() { C = previous })
	for _, key := range []string{
		"ARENA_COSMETICS_CHECKOUT_ENABLED",
		"ARENA_COSMETICS_CHECKOUT_RPM",
		"ARENA_STRIPE_SECRET_KEY",
		"ARENA_STRIPE_PUBLISHABLE_KEY",
		"ARENA_STRIPE_WEBHOOK_SECRETS",
		"ARENA_STRIPE_SUCCESS_URL",
		"ARENA_STRIPE_CANCEL_URL",
		"ARENA_STRIPE_RETURN_URL",
		"ARENA_STRIPE_PORTAL_RETURN_URL",
		"ARENA_STRIPE_AUTOMATIC_TAX",
	} {
		t.Setenv(key, "set-but-ignored")
	}
	t.Setenv("ARENA_ACCOUNTS_SHOP_URL", "https://accounts.angel-serv.com/portal")
	C = Config{}
	Load()
	if C.AccountsShopURL != "https://accounts.angel-serv.com/portal" {
		t.Fatalf("AccountsShopURL = %q", C.AccountsShopURL)
	}
	if C.CosmeticsAccountReadRPM != 60 {
		t.Fatalf("CosmeticsAccountReadRPM = %d, want 60", C.CosmeticsAccountReadRPM)
	}
	configType := reflect.TypeOf(C)
	for index := 0; index < configType.NumField(); index++ {
		field := configType.Field(index)
		if strings.Contains(field.Name, "Stripe") || strings.Contains(field.Name, "Checkout") {
			t.Fatalf("Config still carries a checkout field: %s", field.Name)
		}
	}
}

func TestLoadInvokesCosmeticsValidation(t *testing.T) {
	previous := C
	t.Cleanup(func() { C = previous })
	t.Setenv("ARENA_ACCOUNTS_SHOP_URL", "http://accounts.example/portal")
	C = Config{}

	defer func() {
		if recover() == nil {
			t.Fatal("Load() did not fail closed for an insecure shop address")
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
