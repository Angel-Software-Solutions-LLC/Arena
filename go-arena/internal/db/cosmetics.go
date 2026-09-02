package db

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	CosmeticSlotBotSkin        = "bot_skin"
	CosmeticSlotWeaponSkin     = "weapon_skin"
	CosmeticSlotAttachment     = "attachment"
	CosmeticSlotTrail          = "trail"
	CosmeticTrailCategoryID    = "trails"
	CosmeticBodyFormCategoryID = "body-forms"
)

var (
	ErrInvalidCosmeticSlot   = errors.New("invalid cosmetic slot")
	ErrCosmeticNotFound      = errors.New("cosmetic not found")
	ErrCosmeticInactive      = errors.New("cosmetic is not active")
	ErrCosmeticSlotMismatch  = errors.New("cosmetic does not belong to that slot")
	ErrCosmeticNotOwned      = errors.New("cosmetic is not owned by this bot")
	ErrCosmeticBotNotFound   = errors.New("bot not found")
	ErrCosmeticGrantConflict = errors.New("external reference already granted a different cosmetic")
)

// CosmeticItem is intentionally presentation-only. No gameplay stat, weapon
// damage, cooldown, movement, or loadout field is stored in the cosmetics
// schema, keeping paid ownership out of the game-mechanics boundary.
type CosmeticItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	CategoryID    string `json:"category_id"`
	Slot          string `json:"slot"`
	AssetKey      string `json:"asset_key"`
	Rarity        string `json:"rarity"`
	PriceCents    int    `json:"price_cents"`
	Currency      string `json:"currency"`
	IsFree        bool   `json:"is_free"`
	IsPurchasable bool   `json:"is_purchasable"`
	IsActive      bool   `json:"is_active"`
	IsBuiltin     bool   `json:"is_builtin"`
	SortOrder     int    `json:"sort_order"`
}

// BotCosmeticItem adds ownership and equip state for an authenticated bot.
type BotCosmeticItem struct {
	CosmeticItem
	Owned    bool `json:"owned"`
	Equipped bool `json:"equipped"`
}

var legacyStarterCosmeticCatalog = []CosmeticItem{
	{
		ID: "skin-standard", Name: "Standard Chassis", Description: "The classic Arena bot finish.",
		CategoryID: "chassis", Slot: CosmeticSlotBotSkin, AssetKey: "standard", Rarity: "common", Currency: "USD", IsFree: true, IsActive: true, SortOrder: 10,
	},
	{
		ID: "skin-neon-grid", Name: "Neon Grid Chassis", Description: "Animated-looking neon armor accents with no gameplay effect.",
		CategoryID: "chassis", Slot: CosmeticSlotBotSkin, AssetKey: "neon_grid", Rarity: "rare", PriceCents: 499, Currency: "USD", IsActive: true, SortOrder: 20,
	},
	{
		ID: "skin-carbon-armor", Name: "Carbon Armor Chassis", Description: "Dark geometric armor plates with no defensive bonus.",
		CategoryID: "chassis", Slot: CosmeticSlotBotSkin, AssetKey: "carbon_armor", Rarity: "rare", PriceCents: 399, Currency: "USD", IsActive: true, SortOrder: 30,
	},
	{
		ID: "weapon-standard", Name: "Standard Weapon Finish", Description: "The default Arena weapon materials.",
		CategoryID: "weapon-finishes", Slot: CosmeticSlotWeaponSkin, AssetKey: "standard", Rarity: "common", Currency: "USD", IsFree: true, IsActive: true, SortOrder: 10,
	},
	{
		ID: "weapon-solar-flare", Name: "Solar Flare Finish", Description: "A warm gold weapon finish with no damage bonus.",
		CategoryID: "weapon-finishes", Slot: CosmeticSlotWeaponSkin, AssetKey: "solar_flare", Rarity: "rare", PriceCents: 299, Currency: "USD", IsActive: true, SortOrder: 20,
	},
	{
		ID: "weapon-void-edge", Name: "Void Edge Finish", Description: "A violet-black weapon finish with no cooldown bonus.",
		CategoryID: "weapon-finishes", Slot: CosmeticSlotWeaponSkin, AssetKey: "void_edge", Rarity: "rare", PriceCents: 299, Currency: "USD", IsActive: true, SortOrder: 30,
	},
	{
		ID: "attachment-none", Name: "No Attachment", Description: "Run the chassis without an attachment.",
		CategoryID: "attachments", Slot: CosmeticSlotAttachment, AssetKey: "none", Rarity: "common", Currency: "USD", IsFree: true, IsActive: true, SortOrder: 10,
	},
	{
		ID: "attachment-signal-antenna", Name: "Signal Antenna", Description: "A free starter antenna for proving the customization flow end to end.",
		CategoryID: "attachments", Slot: CosmeticSlotAttachment, AssetKey: "signal_antenna", Rarity: "common", Currency: "USD", IsFree: true, IsActive: true, SortOrder: 20,
	},
	{
		ID: "attachment-orbital-halo", Name: "Orbital Halo", Description: "A luminous halo attachment with no gameplay effect.",
		CategoryID: "attachments", Slot: CosmeticSlotAttachment, AssetKey: "orbital_halo", Rarity: "epic", PriceCents: 299, Currency: "USD", IsActive: true, SortOrder: 30,
	},
	{
		ID: "trail-standard", Name: "Standard Wake", Description: "The free Arena movement wake with no gameplay effect.",
		CategoryID: CosmeticTrailCategoryID, Slot: CosmeticSlotTrail, AssetKey: "standard", Rarity: "common", Currency: "USD", IsFree: true, IsActive: true, SortOrder: 10,
	},
}

type trailCosmeticSeed struct {
	Name        string
	Slug        string
	Description string
	Rarity      string
}

type bodyFormCosmeticSeed struct {
	Name        string
	Key         string
	Description string
	Rarity      string
}

// bodyFormCosmeticSeeds is deliberately explicit. Each entry is an original,
// locally rendered Arena silhouette with one stable, allowlisted asset key;
// the catalog never accepts a caller-supplied model URL or arbitrary body key.
var bodyFormCosmeticSeeds = []bodyFormCosmeticSeed{
	{Name: "Giant Chicken", Key: "giant_chicken", Description: "A towering feathered fighter with a bold comb and oversized wings.", Rarity: "uncommon"},
	{Name: "Highland Cow", Key: "highland_cow", Description: "A shaggy highland bruiser with broad horns and a sturdy silhouette.", Rarity: "uncommon"},
	{Name: "Corgi", Key: "corgi", Description: "A compact corgi runner with bright ears and a confident stride.", Rarity: "uncommon"},
	{Name: "Tabby Cat", Key: "tabby_cat", Description: "A nimble tabby combatant with striped markings and alert ears.", Rarity: "uncommon"},
	{Name: "Red Fox", Key: "red_fox", Description: "A quick red fox form with a white chest and sweeping tail.", Rarity: "rare"},
	{Name: "Battle Rabbit", Key: "battle_rabbit", Description: "An armored rabbit silhouette built around long ears and quick footwork.", Rarity: "rare"},
	{Name: "Emperor Penguin", Key: "emperor_penguin", Description: "A stately penguin form with a crisp tuxedo palette and compact wings.", Rarity: "rare"},
	{Name: "Bullfrog", Key: "bullfrog", Description: "A broad bullfrog brawler with powerful legs and bright watchful eyes.", Rarity: "rare"},
	{Name: "Land Shark", Key: "land_shark", Description: "A finned land-shark form with a low center of gravity and toothy profile.", Rarity: "epic"},
	{Name: "Tyrant Rex", Key: "tyrant_rex", Description: "A heavy tyrant-lizard silhouette with a massive head and counterbalancing tail.", Rarity: "epic"},
	{Name: "Human Adventurer", Key: "human_adventurer", Description: "A classic human adventurer equipped for a long night in the Arena.", Rarity: "uncommon"},
	{Name: "Astronaut", Key: "astronaut", Description: "A sealed astronaut suit with a reflective visor and compact life-support pack.", Rarity: "rare"},
	{Name: "Knight", Key: "knight", Description: "A plated knight form with a crested helm and tournament-ready stance.", Rarity: "rare"},
	{Name: "Wizard", Key: "wizard", Description: "A robed wizard silhouette with a tall hat and luminous arcane accents.", Rarity: "epic"},
	{Name: "Skeleton", Key: "skeleton", Description: "A stylized skeletal fighter assembled from bright Arena-safe bones.", Rarity: "epic"},
	{Name: "Stone Golem", Key: "stone_golem", Description: "A hulking stone construct held together by glowing runic seams.", Rarity: "legendary"},
	{Name: "Slime Monarch", Key: "slime_monarch", Description: "A crowned translucent slime with a lively elastic silhouette.", Rarity: "legendary"},
	{Name: "Spider Drone", Key: "spider_drone", Description: "A low mechanical spider platform with articulated legs and a bright sensor core.", Rarity: "legendary"},
}

var trailCosmeticSeeds = []trailCosmeticSeed{
	{Name: "Ember Sparks", Slug: "ember_sparks", Description: "Hot cinders tumble through a narrow fire-red wake.", Rarity: "uncommon"},
	{Name: "Frost Shards", Slug: "frost_shards", Description: "Ice-blue fragments drift and fall behind the chassis.", Rarity: "uncommon"},
	{Name: "Ion Stream", Slug: "ion_stream", Description: "A focused electric-blue stream with quick rising ions.", Rarity: "rare"},
	{Name: "Plasma Ribbon", Slug: "plasma_ribbon", Description: "A broad magenta plasma band with bright charged motes.", Rarity: "rare"},
	{Name: "Void Motes", Slug: "void_motes", Description: "Dark violet particles orbit a restrained abyssal wake.", Rarity: "epic"},
	{Name: "Solar Wake", Slug: "solar_wake", Description: "Gold-white sparks flare from a warm solar ribbon.", Rarity: "rare"},
	{Name: "Lunar Dust", Slug: "lunar_dust", Description: "Soft silver dust hangs briefly above a moonlit trail.", Rarity: "uncommon"},
	{Name: "Comet Tail", Slug: "comet_tail", Description: "A long pale-blue tail sheds fast star-like particles.", Rarity: "epic"},
	{Name: "Nebula Pulse", Slug: "nebula_pulse", Description: "Alternating rose and blue motes pulse through deep space color.", Rarity: "epic"},
	{Name: "Storm Arcs", Slug: "storm_arcs", Description: "Sharp lightning-blue sparks jump across a compact wake.", Rarity: "rare"},
	{Name: "Static Glitch", Slug: "static_glitch", Description: "Short digital bursts scatter cyan and red interference.", Rarity: "rare"},
	{Name: "Pixel Scatter", Slug: "pixel_scatter", Description: "Block-like light fragments peel away in arcade colors.", Rarity: "uncommon"},
	{Name: "Data Stream", Slug: "data_stream", Description: "Green signal particles rise from a precise synthetic ribbon.", Rarity: "rare"},
	{Name: "Holo Prism", Slug: "holo_prism", Description: "Prismatic particles shift between cool holographic tones.", Rarity: "epic"},
	{Name: "Toxic Spores", Slug: "toxic_spores", Description: "Acid-green spores float upward from a muted toxic wake.", Rarity: "rare"},
	{Name: "Verdant Leaves", Slug: "verdant_leaves", Description: "Leaf-green flecks lift and settle through a natural trail.", Rarity: "uncommon"},
	{Name: "Sand Wake", Slug: "sand_wake", Description: "Warm dust falls low and wide like disturbed desert sand.", Rarity: "uncommon"},
	{Name: "Magma Cinders", Slug: "magma_cinders", Description: "Dense orange cinders rise from a molten red ribbon.", Rarity: "epic"},
	{Name: "Ocean Spray", Slug: "ocean_spray", Description: "Bright aqua droplets arc and fall through a deep-blue wake.", Rarity: "rare"},
	{Name: "Gilded Dust", Slug: "gilded_dust", Description: "Fine gold particles descend from a restrained royal trail.", Rarity: "legendary"},
	{Name: "Rune Sparks", Slug: "rune_sparks", Description: "Teal arcane sparks hover before fading into indigo.", Rarity: "epic"},
	{Name: "Phantom Smoke", Slug: "phantom_smoke", Description: "Large dim motes rise slowly from a spectral smoke wake.", Rarity: "legendary"},
	{Name: "Gear Sparks", Slug: "gear_sparks", Description: "Compact brass sparks fall quickly from an industrial trail.", Rarity: "rare"},
	{Name: "Bounty Flare", Slug: "bounty_flare", Description: "Red and gold particles burst through a high-contrast hunter wake.", Rarity: "legendary"},
}

var generatedCosmeticAssetPattern = regexp.MustCompile(`^arena_set_([0-9]{3})_([a-z0-9]+(?:_[a-z0-9]+)*)$`)

var legacyCosmeticAssets = map[string]map[string]bool{
	CosmeticSlotBotSkin: {
		"standard": true, "neon_grid": true, "carbon_armor": true,
	},
	CosmeticSlotWeaponSkin: {
		"standard": true, "solar_flare": true, "void_edge": true,
	},
	CosmeticSlotAttachment: {
		"none": true, "signal_antenna": true, "orbital_halo": true,
	},
	CosmeticSlotTrail: {
		"standard": true,
	},
}

var supportedTrailAssets = buildSupportedTrailAssets()
var supportedBodyFormAssets = buildSupportedBodyFormAssets()

func buildSupportedTrailAssets() map[string]bool {
	assets := map[string]bool{"standard": true}
	for _, seed := range trailCosmeticSeeds {
		assets[seed.Slug] = true
	}
	return assets
}

func buildSupportedBodyFormAssets() map[string]bool {
	assets := make(map[string]bool, len(bodyFormCosmeticSeeds))
	for _, seed := range bodyFormCosmeticSeeds {
		assets["body_"+seed.Key] = true
	}
	return assets
}

// IsSupportedCosmeticAsset is the shared server-side render contract. Legacy
// procedural assets stay explicitly allowlisted; launch-set assets use one
// strict, bounded key shared by the set's three presentation slots.
func IsSupportedCosmeticAsset(slot, assetKey string) bool {
	slot = strings.TrimSpace(strings.ToLower(slot))
	if slot == CosmeticSlotTrail {
		return supportedTrailAssets[assetKey]
	}
	if slot == CosmeticSlotBotSkin && supportedBodyFormAssets[assetKey] {
		return true
	}
	if assets, ok := legacyCosmeticAssets[slot]; ok && assets[assetKey] {
		return true
	}
	if !IsValidCosmeticSlot(slot) || len(assetKey) > 80 {
		return false
	}
	matches := generatedCosmeticAssetPattern.FindStringSubmatch(assetKey)
	if len(matches) != 3 {
		return false
	}
	number, err := strconv.Atoi(matches[1])
	return err == nil && number >= 1 && number <= 999
}

var starterCosmeticCatalog = buildStarterCosmeticCatalog()

func buildStarterCosmeticCatalog() []CosmeticItem {
	items := append([]CosmeticItem(nil), legacyStarterCosmeticCatalog...)
	for index, seed := range launchSetSeeds {
		number := index + 3
		assetKey := fmt.Sprintf("arena_set_%03d_%s", number, seed.Slug)
		idSlug := strings.ReplaceAll(seed.Slug, "_", "-")
		rarity, itemPrice, _ := launchSetEconomy(number)
		sortOrder := number * 10
		items = append(items,
			CosmeticItem{
				ID: fmt.Sprintf("skin-arena-set-%03d-%s", number, idSlug), Name: seed.Name + " Chassis",
				Description: "A presentation-only " + seed.Name + " chassis from Arena's " + seed.CollectionName + " collection.",
				CategoryID:  "chassis", Slot: CosmeticSlotBotSkin, AssetKey: assetKey, Rarity: rarity,
				PriceCents: itemPrice, Currency: "USD", IsActive: true, SortOrder: sortOrder,
			},
			CosmeticItem{
				ID: fmt.Sprintf("weapon-arena-set-%03d-%s", number, idSlug), Name: seed.Name + " Weapon Finish",
				Description: "A presentation-only " + seed.Name + " weapon finish from Arena's " + seed.CollectionName + " collection.",
				CategoryID:  "weapon-finishes", Slot: CosmeticSlotWeaponSkin, AssetKey: assetKey, Rarity: rarity,
				PriceCents: itemPrice, Currency: "USD", IsActive: true, SortOrder: sortOrder,
			},
			CosmeticItem{
				ID: fmt.Sprintf("attachment-arena-set-%03d-%s", number, idSlug), Name: seed.Name + " Attachment",
				Description: "A presentation-only " + seed.Name + " attachment from Arena's " + seed.CollectionName + " collection.",
				CategoryID:  "attachments", Slot: CosmeticSlotAttachment, AssetKey: assetKey, Rarity: rarity,
				PriceCents: itemPrice, Currency: "USD", IsActive: true, SortOrder: sortOrder,
			},
		)
	}
	for index, seed := range trailCosmeticSeeds {
		items = append(items, CosmeticItem{
			ID: "trail-" + strings.ReplaceAll(seed.Slug, "_", "-"), Name: seed.Name + " Trail",
			Description: seed.Description, CategoryID: CosmeticTrailCategoryID, Slot: CosmeticSlotTrail,
			AssetKey: seed.Slug, Rarity: seed.Rarity, PriceCents: CosmeticTrailPriceCents,
			Currency: "USD", IsActive: true, SortOrder: (index + 1) * 10,
		})
	}
	for index, seed := range bodyFormCosmeticSeeds {
		idSlug := strings.ReplaceAll(seed.Key, "_", "-")
		items = append(items, CosmeticItem{
			ID: "skin-body-" + idSlug, Name: seed.Name + " Body Form",
			Description: seed.Description + " This full-body cosmetic is presentation-only and has no gameplay effect.",
			CategoryID:  CosmeticBodyFormCategoryID, Slot: CosmeticSlotBotSkin,
			AssetKey: "body_" + seed.Key, Rarity: seed.Rarity, PriceCents: CosmeticPackPriceCents,
			Currency: "USD", IsActive: true, SortOrder: (index + 1) * 10,
		})
	}
	for index := range items {
		items[index].IsBuiltin = true
	}
	return items
}

// DefaultCosmeticCatalog returns a copy so callers cannot mutate the seed
// catalog shared by schema setup and DB-unavailable public responses.
func DefaultCosmeticCatalog() []CosmeticItem {
	items := make([]CosmeticItem, len(starterCosmeticCatalog))
	copy(items, starterCosmeticCatalog)
	return items
}

func IsValidCosmeticSlot(slot string) bool {
	switch strings.TrimSpace(strings.ToLower(slot)) {
	case CosmeticSlotBotSkin, CosmeticSlotWeaponSkin, CosmeticSlotAttachment, CosmeticSlotTrail:
		return true
	default:
		return false
	}
}

// EnsureCosmeticsSchema creates the catalog, customer account, bot link and
// equip tables. There is no payment state here: the Arena subscription is
// sold and held by Angel Accounts, and the only trace of it in this schema is
// the `subscription_active` flag on `customer_accounts`. The game engine
// consumes the resulting visual asset keys and never price metadata.
func EnsureCosmeticsSchema(ctx context.Context) error {
	if Pool == nil {
		return ErrNoDatabase
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("EnsureCosmeticsSchema begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// Startup can race across multiple arena-server replicas. PostgreSQL DDL is
	// transactional, but IF NOT EXISTS plus follow-up ALTER/migration statements
	// still need one schema owner at a time to avoid duplicate-constraint races.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(2026071001::BIGINT)`); err != nil {
		return fmt.Errorf("EnsureCosmeticsSchema migration lock: %w", err)
	}

	statements := []string{
		`CREATE TABLE IF NOT EXISTS cosmetic_categories (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			is_active BOOLEAN NOT NULL DEFAULT true,
			is_builtin BOOLEAN NOT NULL DEFAULT false,
			sort_order INT NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 1000000),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE cosmetic_categories ADD COLUMN IF NOT EXISTS is_builtin BOOLEAN NOT NULL DEFAULT false`,
		`CREATE TABLE IF NOT EXISTS cosmetic_items (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			category_id TEXT NOT NULL CONSTRAINT cosmetic_items_category_fk REFERENCES cosmetic_categories(id) ON DELETE RESTRICT,
			 slot TEXT NOT NULL CHECK (slot IN ('bot_skin', 'weapon_skin', 'attachment', 'trail')),
			asset_key TEXT NOT NULL,
			rarity TEXT NOT NULL DEFAULT 'common',
			price_cents INT NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
			currency TEXT NOT NULL DEFAULT 'USD',
			is_free BOOLEAN NOT NULL DEFAULT false,
			is_purchasable BOOLEAN NOT NULL DEFAULT false,
			is_active BOOLEAN NOT NULL DEFAULT true,
			is_builtin BOOLEAN NOT NULL DEFAULT false,
			sort_order INT NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 1000000),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (slot, asset_key)
		)`,
		`ALTER TABLE cosmetic_items ADD COLUMN IF NOT EXISTS category_id TEXT`,
		`ALTER TABLE cosmetic_items ADD COLUMN IF NOT EXISTS sort_order INT NOT NULL DEFAULT 0`,
		`ALTER TABLE cosmetic_items ADD COLUMN IF NOT EXISTS is_builtin BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE cosmetic_items DROP CONSTRAINT IF EXISTS cosmetic_items_slot_check`,
		`ALTER TABLE cosmetic_items ADD CONSTRAINT cosmetic_items_slot_check CHECK (slot IN ('bot_skin', 'weapon_skin', 'attachment', 'trail'))`,
		`CREATE TABLE IF NOT EXISTS cosmetic_packs (
			id TEXT PRIMARY KEY,
			category_id TEXT NOT NULL REFERENCES cosmetic_categories(id) ON DELETE RESTRICT,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			price_cents INT NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
			currency TEXT NOT NULL DEFAULT 'USD',
			is_free BOOLEAN NOT NULL DEFAULT false,
			is_purchasable BOOLEAN NOT NULL DEFAULT false,
			is_active BOOLEAN NOT NULL DEFAULT true,
			is_builtin BOOLEAN NOT NULL DEFAULT false,
			sort_order INT NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 1000000),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE cosmetic_packs ADD COLUMN IF NOT EXISTS is_builtin BOOLEAN NOT NULL DEFAULT false`,
		`CREATE TABLE IF NOT EXISTS cosmetic_pack_items (
			pack_id TEXT NOT NULL REFERENCES cosmetic_packs(id) ON DELETE CASCADE,
			item_id TEXT NOT NULL REFERENCES cosmetic_items(id) ON DELETE RESTRICT,
			sort_order INT NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 1000000),
			PRIMARY KEY (pack_id, item_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cosmetic_pack_items_item ON cosmetic_pack_items (item_id, pack_id)`,
		`CREATE TABLE IF NOT EXISTS cosmetic_catalog_audit (
			id BIGSERIAL PRIMARY KEY,
			actor TEXT NOT NULL,
			action TEXT NOT NULL CHECK (action IN ('create', 'update', 'delete')),
			entity_type TEXT NOT NULL CHECK (entity_type IN ('category', 'item', 'pack')),
			entity_id TEXT NOT NULL,
			before_data JSONB,
			after_data JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cosmetic_catalog_audit_created ON cosmetic_catalog_audit (created_at DESC, id DESC)`,
		// Bot-scoped complimentary grants, written only by the admin
		// demo-loadout tool so the demo fleet can wear paid looks without an
		// account. Not a purchase record: customers never get rows here.
		`CREATE TABLE IF NOT EXISTS cosmetic_entitlements (
			bot_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
			cosmetic_id TEXT NOT NULL REFERENCES cosmetic_items(id) ON DELETE RESTRICT,
			source TEXT NOT NULL DEFAULT 'manual',
			external_reference TEXT,
			granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (bot_id, cosmetic_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cosmetic_entitlements_external
			ON cosmetic_entitlements (source, external_reference)
			WHERE external_reference IS NOT NULL AND external_reference <> ''`,
		`CREATE TABLE IF NOT EXISTS customer_accounts (
			id TEXT PRIMARY KEY,
			-- Nullable, and null for every account linked to an Accounts
			-- identity. Arena stopped being the place a person's address
			-- lives when Accounts became the identity provider; the column
			-- survives only so a pre-cutover row can be matched once and
			-- emptied. UNIQUE still holds, because Postgres does not consider
			-- two nulls equal — any number of linked accounts coexist.
			email TEXT UNIQUE CHECK (email = LOWER(email)),
			display_name TEXT NOT NULL DEFAULT '',
			email_verified_at TIMESTAMPTZ,
			oidc_issuer TEXT,
			oidc_subject TEXT,
			subscription_active BOOLEAN NOT NULL DEFAULT false,
			subscription_synced_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT customer_accounts_oidc_pair_check CHECK (
				(oidc_issuer IS NULL AND oidc_subject IS NULL) OR
				(oidc_issuer IS NOT NULL AND oidc_subject IS NOT NULL)
			)
		)`,
		// An existing deployment already has this table, so `CREATE TABLE IF
		// NOT EXISTS` above changed nothing for it. Dropping NOT NULL is what
		// lets the binder empty the column on the next sign-in; it is
		// idempotent, and it only ever widens what the column accepts, so it
		// is safe to run against a database that has already had it applied
		// and against one still serving the previous build.
		`ALTER TABLE customer_accounts ALTER COLUMN email DROP NOT NULL`,
		`DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid = 'customer_accounts'::regclass
				  AND conname = 'customer_accounts_oidc_pair_check'
			) THEN
				ALTER TABLE customer_accounts
					ADD CONSTRAINT customer_accounts_oidc_pair_check CHECK (
						(oidc_issuer IS NULL AND oidc_subject IS NULL) OR
						(oidc_issuer IS NOT NULL AND oidc_subject IS NOT NULL)
					);
			END IF;
		END
		$$`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_customer_accounts_oidc_identity
			ON customer_accounts (oidc_issuer, oidc_subject)
			WHERE oidc_issuer IS NOT NULL AND oidc_subject IS NOT NULL`,
		// Public profile fields, editable from the dashboard. show_bots_public
		// defaults to true so a fresh account's linked bots are visible unless
		// the owner opts out.
		`ALTER TABLE customer_accounts ADD COLUMN IF NOT EXISTS bio TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE customer_accounts ADD COLUMN IF NOT EXISTS avatar_color TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE customer_accounts ADD COLUMN IF NOT EXISTS show_bots_public BOOLEAN NOT NULL DEFAULT true`,
		// The one commerce fact Arena keeps: whether Accounts reported an
		// active Arena subscription at the account's last sign-in, and
		// when. Additive; a database from before subscriptions were the
		// only product starts every account as not subscribed until its
		// owner next signs in and the sync sets it.
		`ALTER TABLE customer_accounts ADD COLUMN IF NOT EXISTS subscription_active BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE customer_accounts ADD COLUMN IF NOT EXISTS subscription_synced_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS customer_email_verifications (
			email TEXT PRIMARY KEY CHECK (email = LOWER(email)),
			display_name TEXT NOT NULL DEFAULT '',
			return_to TEXT NOT NULL,
			token_hash BYTEA NOT NULL UNIQUE CHECK (OCTET_LENGTH(token_hash) = 32),
			created_at TIMESTAMPTZ NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			CHECK (expires_at > created_at)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_customer_email_verifications_expires
			ON customer_email_verifications (expires_at)`,
		`CREATE TABLE IF NOT EXISTS account_bot_links (
			account_id TEXT NOT NULL REFERENCES customer_accounts(id) ON DELETE CASCADE,
			bot_id TEXT NOT NULL UNIQUE REFERENCES bots(id) ON DELETE CASCADE,
			linked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (account_id, bot_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_account_bot_links_account
			ON account_bot_links (account_id, linked_at, bot_id)`,
		`CREATE TABLE IF NOT EXISTS account_api_keys (
			account_id TEXT NOT NULL REFERENCES customer_accounts(id) ON DELETE CASCADE,
			api_key_id TEXT NOT NULL UNIQUE REFERENCES api_keys(id) ON DELETE CASCADE,
			linked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (account_id, api_key_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_account_api_keys_account
			ON account_api_keys (account_id, linked_at, api_key_id)`,
		`INSERT INTO account_api_keys (account_id, api_key_id, linked_at)
			SELECT links.account_id, bots.api_key_id, links.linked_at
			FROM account_bot_links links
			JOIN bots ON bots.id = links.bot_id
			ON CONFLICT (api_key_id) DO NOTHING`,
		/*
		 * The loadout: one equipped item per bot and slot. Whether a bot may
		 * wear a paid item is not recorded here — it is decided at read time
		 * from the linked account's subscription flag (see
		 * botMayWearCosmeticSQL), so this table carries presentation state
		 * only.
		 *
		 * A database from before the subscription-only model also has
		 * `license_id` and `account_id` columns on this table, both
		 * nullable, plus the retired `cosmetic_licenses`,
		 * `cosmetic_license_assignments`, `cosmetic_orders*`,
		 * `cosmetic_payment_events`, `cosmetic_order_refunds`,
		 * `cosmetic_subscription*`, `cosmetic_admin_membership*`,
		 * `customer_accounts_grant*` and `platform_license_lifecycle_events`
		 * tables. Nothing reads or writes them any more; they are left in
		 * place rather than dropped, because migrations here are additive
		 * and an operator may still want the ledger for their records. New
		 * rows leave the two columns null, which every retained constraint
		 * accepts. See docs/cosmetics-and-monetization.md.
		 */
		`CREATE TABLE IF NOT EXISTS bot_cosmetic_loadout (
			bot_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
			slot TEXT NOT NULL CHECK (slot IN ('bot_skin', 'weapon_skin', 'attachment', 'trail')),
			cosmetic_id TEXT NOT NULL REFERENCES cosmetic_items(id) ON DELETE CASCADE,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (bot_id, slot)
		)`,
		`ALTER TABLE bot_cosmetic_loadout DROP CONSTRAINT IF EXISTS bot_cosmetic_loadout_slot_check`,
		`ALTER TABLE bot_cosmetic_loadout ADD CONSTRAINT bot_cosmetic_loadout_slot_check CHECK (slot IN ('bot_skin', 'weapon_skin', 'attachment', 'trail'))`,
		`CREATE INDEX IF NOT EXISTS idx_bot_cosmetic_loadout_item ON bot_cosmetic_loadout (cosmetic_id)`,
	}

	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("EnsureCosmeticsSchema exec: %w", err)
		}
	}

	for _, category := range starterCosmeticCategories {
		if _, err := tx.Exec(ctx, `
			INSERT INTO cosmetic_categories (id, name, description, is_active, is_builtin, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO NOTHING`,
			category.ID, category.Name, category.Description, category.IsActive, category.IsBuiltin, category.SortOrder,
		); err != nil {
			return fmt.Errorf("EnsureCosmeticsSchema seed category %s: %w", category.ID, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE cosmetic_items
		SET category_id = CASE slot
			WHEN 'bot_skin' THEN 'chassis'
			WHEN 'weapon_skin' THEN 'weapon-finishes'
			ELSE 'attachments'
		END
		WHERE category_id IS NULL OR category_id = ''`); err != nil {
		return fmt.Errorf("EnsureCosmeticsSchema backfill item categories: %w", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE cosmetic_items ALTER COLUMN category_id SET NOT NULL`); err != nil {
		return fmt.Errorf("EnsureCosmeticsSchema require item category: %w", err)
	}
	if _, err := tx.Exec(ctx, `DO $$
	BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conrelid = 'cosmetic_items'::regclass
			  AND conname = 'cosmetic_items_category_fk'
		) THEN
			ALTER TABLE cosmetic_items
				ADD CONSTRAINT cosmetic_items_category_fk
				FOREIGN KEY (category_id) REFERENCES cosmetic_categories(id) ON DELETE RESTRICT;
		END IF;
	END
	$$`); err != nil {
		return fmt.Errorf("EnsureCosmeticsSchema item category constraint: %w", err)
	}

	for _, item := range starterCosmeticCatalog {
		if _, err := tx.Exec(ctx, `
			INSERT INTO cosmetic_items
				(id, name, description, category_id, slot, asset_key, rarity, price_cents, currency, is_free, is_purchasable, is_active, is_builtin, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			ON CONFLICT (id) DO NOTHING`,
			item.ID, item.Name, item.Description, item.CategoryID, item.Slot, item.AssetKey, item.Rarity,
			item.PriceCents, item.Currency, item.IsFree, item.IsPurchasable, item.IsActive, item.IsBuiltin, item.SortOrder,
		); err != nil {
			return fmt.Errorf("EnsureCosmeticsSchema seed %s: %w", item.ID, err)
		}
	}

	for _, pack := range starterCosmeticPacks {
		result, err := tx.Exec(ctx, `
			INSERT INTO cosmetic_packs
				(id, category_id, name, description, price_cents, currency, is_free, is_purchasable, is_active, is_builtin, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (id) DO NOTHING`,
			pack.ID, pack.CategoryID, pack.Name, pack.Description, pack.PriceCents,
			pack.Currency, pack.IsFree, pack.IsPurchasable, pack.IsActive, pack.IsBuiltin, pack.SortOrder,
		)
		if err != nil {
			return fmt.Errorf("EnsureCosmeticsSchema seed pack %s: %w", pack.ID, err)
		}
		// Seed membership only alongside a newly inserted pack. Re-running schema
		// repair must preserve an operator's edits to a starter pack.
		if result.RowsAffected() == 0 {
			continue
		}
		for index, itemID := range pack.ItemIDs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO cosmetic_pack_items (pack_id, item_id, sort_order)
				VALUES ($1,$2,$3)
				ON CONFLICT (pack_id, item_id) DO NOTHING`, pack.ID, itemID, (index+1)*10); err != nil {
				return fmt.Errorf("EnsureCosmeticsSchema seed pack item %s/%s: %w", pack.ID, itemID, err)
			}
		}
	}
	// Every set keeps one fixed reference price and one-item trail products
	// their own; the values are catalog metadata for the admin editor now
	// that nothing is sold per item, and the normalisation keeps built-in
	// rows consistent across deployments.
	if _, err := tx.Exec(ctx, `
		UPDATE cosmetic_packs
		SET price_cents = CASE WHEN category_id = $2::text THEN $3::integer ELSE $1::integer END,
		    currency = 'USD', updated_at = NOW()
		WHERE is_purchasable = true AND is_free = false
		  AND (price_cents <> CASE WHEN category_id = $2::text THEN $3::integer ELSE $1::integer END OR currency <> 'USD')`,
		CosmeticPackPriceCents, CosmeticTrailCategoryID, CosmeticTrailPriceCents); err != nil {
		return fmt.Errorf("EnsureCosmeticsSchema normalize pack prices: %w", err)
	}
	categoryIDs := make([]string, 0, len(starterCosmeticCategories))
	for _, category := range starterCosmeticCategories {
		categoryIDs = append(categoryIDs, category.ID)
	}
	itemIDs := make([]string, 0, len(starterCosmeticCatalog))
	for _, item := range starterCosmeticCatalog {
		itemIDs = append(itemIDs, item.ID)
	}
	packIDs := make([]string, 0, len(starterCosmeticPacks))
	for _, pack := range starterCosmeticPacks {
		packIDs = append(packIDs, pack.ID)
	}
	for _, builtin := range []struct {
		entity string
		query  string
		ids    []string
	}{
		{entity: "categories", query: `UPDATE cosmetic_categories SET is_builtin = true WHERE id = ANY($1)`, ids: categoryIDs},
		{entity: "items", query: `UPDATE cosmetic_items SET is_builtin = true WHERE id = ANY($1)`, ids: itemIDs},
		{entity: "packs", query: `UPDATE cosmetic_packs SET is_builtin = true WHERE id = ANY($1)`, ids: packIDs},
	} {
		if _, err := tx.Exec(ctx, builtin.query, builtin.ids); err != nil {
			return fmt.Errorf("EnsureCosmeticsSchema mark built-in %s: %w", builtin.entity, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("EnsureCosmeticsSchema commit: %w", err)
	}
	return nil
}

func ListCosmeticCatalog(ctx context.Context) ([]CosmeticItem, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx, `
		SELECT i.id, i.name, i.description, i.category_id, i.slot, i.asset_key, i.rarity, i.price_cents, i.currency,
		       i.is_free, i.is_purchasable, i.is_active, i.is_builtin, i.sort_order
		FROM cosmetic_items i
		JOIN cosmetic_categories c ON c.id = i.category_id
		WHERE i.is_active = true AND c.is_active = true
		ORDER BY CASE i.slot WHEN 'bot_skin' THEN 1 WHEN 'weapon_skin' THEN 2 WHEN 'attachment' THEN 3 ELSE 4 END,
		         i.sort_order, i.price_cents, i.name`)
	if err != nil {
		return nil, fmt.Errorf("ListCosmeticCatalog: %w", err)
	}
	defer rows.Close()

	items := make([]CosmeticItem, 0)
	for rows.Next() {
		var item CosmeticItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.CategoryID, &item.Slot, &item.AssetKey,
			&item.Rarity, &item.PriceCents, &item.Currency, &item.IsFree, &item.IsPurchasable, &item.IsActive, &item.IsBuiltin, &item.SortOrder); err != nil {
			return nil, fmt.Errorf("ListCosmeticCatalog scan: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func ListBotCosmetics(ctx context.Context, botID string) ([]BotCosmeticItem, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx, `
		SELECT i.id, i.name, i.description, i.category_id, i.slot, i.asset_key, i.rarity,
		       i.price_cents, i.currency, i.is_free, i.is_purchasable, i.is_active, i.is_builtin, i.sort_order,
		       `+botMayWearCosmeticSQL+` AS owned,
		       CASE
		         WHEN equipped.cosmetic_id IS NOT NULL THEN equipped.cosmetic_id = i.id
		         ELSE (i.slot = 'bot_skin' AND i.asset_key = 'standard')
		           OR (i.slot = 'weapon_skin' AND i.asset_key = 'standard')
		           OR (i.slot = 'attachment' AND i.asset_key = 'none')
		           OR (i.slot = 'trail' AND i.asset_key = 'standard')
		       END AS equipped
		FROM cosmetic_items i
		JOIN cosmetic_categories c ON c.id = i.category_id
		CROSS JOIN (SELECT $1::text AS bot_id) l
		LEFT JOIN bot_cosmetic_loadout equipped ON equipped.bot_id = $1 AND equipped.slot = i.slot
		  AND EXISTS (
		    SELECT 1 FROM cosmetic_items equipped_item
		    JOIN cosmetic_categories equipped_category ON equipped_category.id = equipped_item.category_id
		    WHERE equipped_item.id = equipped.cosmetic_id AND equipped_item.is_active = true
		      AND equipped_category.is_active = true
		  )
		WHERE i.is_active = true AND c.is_active = true
		ORDER BY CASE i.slot WHEN 'bot_skin' THEN 1 WHEN 'weapon_skin' THEN 2 WHEN 'attachment' THEN 3 ELSE 4 END,
		         i.price_cents, i.name`, botID)
	if err != nil {
		return nil, fmt.Errorf("ListBotCosmetics: %w", err)
	}
	defer rows.Close()

	items := make([]BotCosmeticItem, 0)
	for rows.Next() {
		var item BotCosmeticItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.CategoryID, &item.Slot, &item.AssetKey,
			&item.Rarity, &item.PriceCents, &item.Currency, &item.IsFree, &item.IsPurchasable,
			&item.IsActive, &item.IsBuiltin, &item.SortOrder, &item.Owned, &item.Equipped); err != nil {
			return nil, fmt.Errorf("ListBotCosmetics scan: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// equippedCosmeticsSQL resolves a loadout row to an asset key only when the
// bot may still wear it: the item and its category are active, and the item
// is free, demo-granted, or included with a linked account's active Arena
// subscription. A paid look saved while subscribed simply stops resolving
// when the subscription lapses, and resolves again when it resumes.
const equippedCosmeticsSQL = `
	SELECT l.bot_id, l.slot, i.asset_key
	FROM bot_cosmetic_loadout l
	JOIN cosmetic_items i ON i.id = l.cosmetic_id AND i.slot = l.slot
	JOIN cosmetic_categories c ON c.id = i.category_id
	WHERE l.bot_id = ANY($1::text[]) AND i.is_active = true AND c.is_active = true
	  AND ` + botMayWearCosmeticSQL + `
	ORDER BY l.bot_id, l.slot`

// GetEquippedCosmetics returns only allowlisted asset identifiers for the
// spectator protocol. Price and subscription metadata never enters BotState.
func GetEquippedCosmetics(ctx context.Context, botID string) (map[string]string, error) {
	loadouts, err := GetEquippedCosmeticsForBots(ctx, []string{botID})
	if err != nil {
		return nil, err
	}
	if equipped, ok := loadouts[strings.TrimSpace(botID)]; ok {
		return equipped, nil
	}
	return defaultEquippedCosmetics(), nil
}

// GetEquippedCosmeticsForBots resolves a bounded connected-bot snapshot with
// one database query. Every requested bot receives a fallback loadout even if
// it has no active rows, allowing a subscription change to clear stale visuals
// without an N+1 query loop.
func GetEquippedCosmeticsForBots(ctx context.Context, botIDs []string) (map[string]map[string]string, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	unique := make([]string, 0, len(botIDs))
	result := make(map[string]map[string]string, len(botIDs))
	for _, rawBotID := range botIDs {
		botID := strings.TrimSpace(rawBotID)
		if botID == "" {
			continue
		}
		if _, exists := result[botID]; exists {
			continue
		}
		result[botID] = defaultEquippedCosmetics()
		unique = append(unique, botID)
	}
	if len(unique) == 0 {
		return result, nil
	}

	rows, err := Pool.Query(ctx, equippedCosmeticsSQL, unique)
	if err != nil {
		return nil, fmt.Errorf("GetEquippedCosmeticsForBots: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var botID, slot, assetKey string
		if err := rows.Scan(&botID, &slot, &assetKey); err != nil {
			return nil, fmt.Errorf("GetEquippedCosmeticsForBots scan: %w", err)
		}
		if IsValidCosmeticSlot(slot) {
			result[botID][slot] = assetKey
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetEquippedCosmeticsForBots rows: %w", err)
	}
	return result, nil
}

func defaultEquippedCosmetics() map[string]string {
	return map[string]string{
		CosmeticSlotBotSkin:    "standard",
		CosmeticSlotWeaponSkin: "standard",
		CosmeticSlotAttachment: "none",
		CosmeticSlotTrail:      "standard",
	}
}

// lockActiveCosmeticItemTx loads one catalog item under a share lock and
// refuses an unknown or inactive one, so an equip cannot race an admin
// retiring the item or its category.
func lockActiveCosmeticItemTx(ctx context.Context, tx pgx.Tx, cosmeticID string) (*CosmeticItem, error) {
	var item CosmeticItem
	var categoryActive bool
	err := tx.QueryRow(ctx, `
		SELECT i.id, i.name, i.description, i.category_id, i.slot, i.asset_key, i.rarity, i.price_cents, i.currency,
		       i.is_free, i.is_purchasable, i.is_active, i.is_builtin, i.sort_order, c.is_active
		FROM cosmetic_items i
		JOIN cosmetic_categories c ON c.id = i.category_id
		WHERE i.id = $1
		FOR SHARE OF i, c`, cosmeticID).
		Scan(&item.ID, &item.Name, &item.Description, &item.CategoryID, &item.Slot, &item.AssetKey, &item.Rarity,
			&item.PriceCents, &item.Currency, &item.IsFree, &item.IsPurchasable, &item.IsActive, &item.IsBuiltin, &item.SortOrder, &categoryActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCosmeticNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cosmetic item lookup: %w", err)
	}
	if !item.IsActive || !categoryActive {
		return nil, ErrCosmeticInactive
	}
	return &item, nil
}

func upsertBotCosmeticLoadoutTx(ctx context.Context, tx pgx.Tx, botID, slot, cosmeticID string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_cosmetic_loadout (bot_id, slot, cosmetic_id, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (bot_id, slot) DO UPDATE
		SET cosmetic_id = EXCLUDED.cosmetic_id, updated_at = NOW()`,
		botID, slot, cosmeticID); err != nil {
		return fmt.Errorf("cosmetic loadout upsert: %w", err)
	}
	return nil
}

// EquipCosmetic is the bot-key equip (`PUT /api/v1/bot/cosmetics`): the bot
// wears this item if it may — free, demo-granted, or included with the
// subscription of the account it is linked to.
func EquipCosmetic(ctx context.Context, botID, slot, cosmeticID string) (*CosmeticItem, error) {
	slot = strings.TrimSpace(strings.ToLower(slot))
	if !IsValidCosmeticSlot(slot) {
		return nil, ErrInvalidCosmeticSlot
	}
	if Pool == nil {
		return nil, ErrNoDatabase
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("EquipCosmetic begin: %w", err)
	}
	defer tx.Rollback(ctx)

	item, err := lockActiveCosmeticItemTx(ctx, tx, cosmeticID)
	if err != nil {
		return nil, err
	}
	if item.Slot != slot {
		return nil, ErrCosmeticSlotMismatch
	}
	if !item.IsFree {
		var allowed bool
		if err := tx.QueryRow(ctx, `
			SELECT `+botMayWearCosmeticSQL+`
			FROM cosmetic_items i
			CROSS JOIN (SELECT $1::text AS bot_id) l
			WHERE i.id = $2`, botID, cosmeticID).Scan(&allowed); err != nil {
			return nil, fmt.Errorf("EquipCosmetic entitlement: %w", err)
		}
		if !allowed {
			return nil, ErrCosmeticNotOwned
		}
	}
	if err := upsertBotCosmeticLoadoutTx(ctx, tx, botID, slot, cosmeticID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("EquipCosmetic commit: %w", err)
	}
	return item, nil
}

// GrantCosmeticEntitlement is the bot-scoped complimentary grant behind the
// admin demo-loadout tool. It is not a purchase path: customers get every
// paid cosmetic through the Arena subscription, never through a row here.
// An external reference makes a grant idempotent; the same reference naming
// a different bot or item is a conflict.
func GrantCosmeticEntitlement(ctx context.Context, botID, cosmeticID, source, externalReference string) (bool, error) {
	if Pool == nil {
		return false, ErrNoDatabase
	}
	if source == "" {
		source = "manual"
	}

	var botExists bool
	if err := Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bots WHERE id = $1)`, botID).Scan(&botExists); err != nil {
		return false, fmt.Errorf("GrantCosmeticEntitlement bot lookup: %w", err)
	}
	if !botExists {
		return false, ErrCosmeticBotNotFound
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("GrantCosmeticEntitlement begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if externalReference != "" {
		var existingBotID, existingCosmeticID string
		err := tx.QueryRow(ctx, `
			SELECT bot_id, cosmetic_id FROM cosmetic_entitlements
			WHERE source = $1 AND external_reference = $2
			FOR UPDATE`, source, externalReference).
			Scan(&existingBotID, &existingCosmeticID)
		if err == nil {
			if existingBotID == botID && existingCosmeticID == cosmeticID {
				return false, nil
			}
			return false, ErrCosmeticGrantConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("GrantCosmeticEntitlement idempotency lookup: %w", err)
		}
	}
	if _, err := lockActiveCosmeticItemTx(ctx, tx, cosmeticID); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO cosmetic_entitlements (bot_id, cosmetic_id, source, external_reference, granted_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), NOW())
		ON CONFLICT DO NOTHING`, botID, cosmeticID, source, externalReference)
	if err != nil {
		return false, fmt.Errorf("GrantCosmeticEntitlement: %w", err)
	}
	created := tag.RowsAffected() > 0
	if !created && externalReference != "" {
		var existingBotID, existingCosmeticID string
		if err := tx.QueryRow(ctx, `
			SELECT bot_id, cosmetic_id FROM cosmetic_entitlements
			WHERE source = $1 AND external_reference = $2`, source, externalReference).
			Scan(&existingBotID, &existingCosmeticID); err == nil &&
			(existingBotID != botID || existingCosmeticID != cosmeticID) {
			return false, ErrCosmeticGrantConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("GrantCosmeticEntitlement commit: %w", err)
	}
	return created, nil
}
