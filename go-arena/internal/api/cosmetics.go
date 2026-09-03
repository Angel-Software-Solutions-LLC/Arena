package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"arena-server/internal/config"
	"arena-server/internal/db"
	"arena-server/internal/game"
	"arena-server/internal/platform"
	"arena-server/internal/security"

	"github.com/go-chi/chi/v5"
)

type arenaCosmeticsStore interface {
	ListForBot(context.Context, string) ([]db.BotCosmeticItem, error)
	Equipped(context.Context, string) (map[string]string, error)
	Equip(context.Context, string, string, string) (*db.CosmeticItem, error)
	EquipForAccount(context.Context, string, string, string, string) (*db.CosmeticItem, error)
}

type cosmeticsStore interface {
	platform.CosmeticsAuthority
	arenaCosmeticsStore
}

type databaseCosmeticsStore struct{}

func (databaseCosmeticsStore) ListForBot(ctx context.Context, botID string) ([]db.BotCosmeticItem, error) {
	return db.ListBotCosmetics(ctx, botID)
}
func (databaseCosmeticsStore) Equipped(ctx context.Context, botID string) (map[string]string, error) {
	return db.GetEquippedCosmetics(ctx, botID)
}
func (databaseCosmeticsStore) Equip(ctx context.Context, botID, slot, cosmeticID string) (*db.CosmeticItem, error) {
	return db.EquipCosmetic(ctx, botID, slot, cosmeticID)
}
func (databaseCosmeticsStore) EquipForAccount(ctx context.Context, accountID, botID, slot, cosmeticID string) (*db.CosmeticItem, error) {
	return db.EquipCustomerCosmetic(ctx, accountID, botID, slot, cosmeticID)
}

// CosmeticsHandler owns catalog, inventory and equip HTTP behavior. There is
// no payment or licence behaviour behind it: the Arena subscription is bought
// in Angel Accounts, and the only commerce fact here is the account's cached
// subscription flag.
type CosmeticsHandler struct {
	authority                  platform.CosmeticsAuthority
	store                      arenaCosmeticsStore
	engine                     *game.GameEngine
	consumeAccountKeyQuota     func(context.Context, string, db.AccountAPIKeyQuotaAction, int) (bool, int, error)
	checkAccountInventoryQuota func(context.Context, string, int) (bool, error)
	// catalogCache serves the public catalog (4 DB queries + a 100-250 KB
	// encode of ~340 items, many embedded twice) from memory with ETag/304s.
	// Per-instance so test handlers with different stores stay isolated.
	// Admin catalog mutations show up within the TTL; AdminCatalog itself is
	// deliberately uncached.
	catalogCache *responseCache
}

const cosmeticCatalogCacheTTL = time.Minute

func newCosmeticsHandlerWithStores(authority platform.CosmeticsAuthority, store arenaCosmeticsStore, engine *game.GameEngine) *CosmeticsHandler {
	return &CosmeticsHandler{
		authority: authority, store: store, engine: engine,
		consumeAccountKeyQuota: db.ConsumeAccountAPIKeyQuota,
		checkAccountInventoryQuota: func(ctx context.Context, accountID string, limit int) (bool, error) {
			allowed, _, _, err := security.CheckRateLimit(ctx, "cosmetics-inventory-account:"+accountID, limit, 60)
			return allowed, err
		},
		catalogCache: newResponseCache(cosmeticCatalogCacheTTL, 10*time.Second, time.Now),
	}
}

func newCosmeticsHandlerWithStore(store cosmeticsStore, engine *game.GameEngine) *CosmeticsHandler {
	return &CosmeticsHandler{
		authority: store, store: store, engine: engine,
		consumeAccountKeyQuota: func(context.Context, string, db.AccountAPIKeyQuotaAction, int) (bool, int, error) {
			return true, 0, nil
		},
		checkAccountInventoryQuota: func(context.Context, string, int) (bool, error) { return true, nil },
		// Zero TTL: this constructor is the test seam, and the catalog tests
		// mutate the fake store between requests. Every request reloads (the
		// single-flight still coalesces concurrent ones).
		catalogCache: newResponseCache(0, 10*time.Second, time.Now),
	}
}

// catalogSubscription is the one commerce fact the public catalog publishes:
// every paid cosmetic is included with the Arena subscription, and here is
// where to get one. No prices to pay Arena, no checkout flag, no offer —
// there is nothing for a browser to start here.
func catalogSubscription() map[string]any {
	body := map[string]any{"product": arenaProductID, "includes_all_cosmetics": true}
	if url := accountsShopURL(); url != "" {
		body["url"] = url
	}
	/*
	 * What it costs, when Accounts has said so. Still not a price Arena
	 * charges — it is a quote of somebody else's, published so the Shop can
	 * answer "how much" without inventing a number that can disagree with the
	 * card. Absent until a read succeeds, and the Shop says nothing about
	 * price until it is here.
	 */
	if plan, ok := arenaPlanForCatalog(); ok {
		body["price_cents"] = plan.PriceCents
		body["currency"] = plan.Currency
		if plan.Interval != "" {
			body["interval"] = plan.Interval
		}
	}
	return body
}

func (h *CosmeticsHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	h.catalogCache.Serve(w, r, "catalog", func(ctx context.Context) ([]byte, error) {
		catalog, err := h.authority.PublicCatalog(ctx)
		if err != nil {
			return nil, err
		}
		// The subscription address comes from a setting read at startup, and
		// a deploy is what changes it, so baking it into the cached body is
		// safe.
		return json.Marshal(map[string]interface{}{
			"categories":   catalog.Categories,
			"packs":        catalog.Packs,
			"items":        catalog.Items,
			"subscription": catalogSubscription(),
		})
	}, "cosmetics catalog is unavailable", http.StatusServiceUnavailable)
}

func (h *CosmeticsHandler) BotInventory(w http.ResponseWriter, r *http.Request) {
	bot := security.GetBotFromContext(r.Context())
	if bot == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	items, err := h.store.ListForBot(r.Context(), bot.ID)
	if err != nil {
		if errors.Is(err, db.ErrNoDatabase) {
			writeError(w, http.StatusServiceUnavailable, "database not available")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load cosmetics")
		return
	}
	equipped := make(map[string]string)
	for _, item := range items {
		if item.Equipped {
			equipped[item.Slot] = item.ID
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"bot_id":   bot.ID,
		"items":    items,
		"equipped": equipped,
	})
}

type equipCosmeticRequest struct {
	Slot       string `json:"slot"`
	CosmeticID string `json:"cosmetic_id"`
}

func decodeEquipCosmeticRequest(r *http.Request) (equipCosmeticRequest, bool) {
	var req equipCosmeticRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return req, false
	}
	req.Slot = strings.TrimSpace(strings.ToLower(req.Slot))
	req.CosmeticID = strings.TrimSpace(req.CosmeticID)
	if !db.IsValidCosmeticSlot(req.Slot) || req.CosmeticID == "" || len(req.CosmeticID) > 80 {
		return req, false
	}
	return req, true
}

func (h *CosmeticsHandler) Equip(w http.ResponseWriter, r *http.Request) {
	bot := security.GetBotFromContext(r.Context())
	if bot == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	req, ok := decodeEquipCosmeticRequest(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "slot and cosmetic_id are required and must be valid")
		return
	}

	item, err := h.store.Equip(r.Context(), bot.ID, req.Slot, req.CosmeticID)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrInvalidCosmeticSlot), errors.Is(err, db.ErrCosmeticSlotMismatch):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, db.ErrCosmeticNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, db.ErrCosmeticNotOwned):
			writeSubscriptionRequired(w, "this cosmetic is included with an Arena subscription; link this bot to a subscribed account")
		case errors.Is(err, db.ErrCosmeticInactive):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, db.ErrNoDatabase):
			writeError(w, http.StatusServiceUnavailable, "database not available")
		default:
			writeError(w, http.StatusInternalServerError, "failed to equip cosmetic")
		}
		return
	}

	equipped, err := h.store.Equipped(r.Context(), bot.ID)
	liveRefreshed := false
	if err == nil && h.engine != nil {
		liveRefreshed = h.engine.UpdateBotCosmetics(bot.ID, equipped)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":         "cosmetic equipped",
		"item":            item,
		"equipped_assets": equipped,
		"live_refreshed":  liveRefreshed,
		"gameplay":        "unchanged",
	})
}

// writeSubscriptionRequired is the one refusal a customer can act on: the
// cosmetic is real and active, it is simply included with a subscription
// they do not hold. The address is included so a client can send them there.
func writeSubscriptionRequired(w http.ResponseWriter, message string) {
	body := map[string]interface{}{
		"error": message,
		"code":  "SUBSCRIPTION_REQUIRED",
	}
	if url := accountsShopURL(); url != "" {
		body["subscription_url"] = url
	}
	writeJSON(w, http.StatusForbidden, body)
}

func (h *CosmeticsHandler) refreshBotVisuals(ctx context.Context, botID *string) bool {
	if h.engine == nil || botID == nil || strings.TrimSpace(*botID) == "" {
		return false
	}
	equipped, err := h.store.Equipped(ctx, *botID)
	if err != nil {
		return false
	}
	return h.engine.UpdateBotCosmetics(*botID, equipped)
}

// refreshBotVisualsFor re-reads the loadouts of the given bots that are
// connected right now. It is what a subscription sync calls after the flag
// flips, so a bot in the arena changes its look at once rather than at its
// next reconnect. Failures leave that bot unchanged; the database is
// authoritative and its next equip or reconnect repairs it.
func (h *CosmeticsHandler) refreshBotVisualsFor(ctx context.Context, botIDs []string) int {
	if h.engine == nil || len(botIDs) == 0 {
		return 0
	}
	connected := make(map[string]struct{})
	for _, botID := range h.engine.ConnectedBotIDs() {
		connected[botID] = struct{}{}
	}
	refreshed := 0
	seen := make(map[string]struct{}, len(botIDs))
	for _, rawBotID := range botIDs {
		botID := strings.TrimSpace(rawBotID)
		if botID == "" {
			continue
		}
		if _, duplicate := seen[botID]; duplicate {
			continue
		}
		seen[botID] = struct{}{}
		if _, ok := connected[botID]; !ok {
			continue
		}
		if h.refreshBotVisuals(ctx, &botID) {
			refreshed++
		}
	}
	return refreshed
}

// refreshConnectedBotVisuals invalidates the engine's presentation-only cache
// after an administrator changes item/category availability. DB resolution is
// authoritative; failures leave that bot unchanged and are repaired by its
// next equip/reconnect instead of making the catalog mutation itself fail.
func (h *CosmeticsHandler) refreshConnectedBotVisuals(ctx context.Context) int {
	if h.engine == nil {
		return 0
	}
	refreshed := 0
	for _, botID := range h.engine.ConnectedBotIDs() {
		equipped, err := h.store.Equipped(ctx, botID)
		if err != nil {
			continue
		}
		if h.engine.UpdateBotCosmetics(botID, equipped) {
			refreshed++
		}
	}
	return refreshed
}

func customerSession(r *http.Request) (*CustomerSession, bool) {
	session := CustomerSessionFromContext(r.Context())
	return session, session != nil && session.AccountID != ""
}

func (h *CosmeticsHandler) AccountInventory(w http.ResponseWriter, r *http.Request) {
	session, ok := customerSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "customer authentication required")
		return
	}
	if h.checkAccountInventoryQuota != nil {
		allowed, err := h.checkAccountInventoryQuota(r.Context(), session.AccountID, config.C.CosmeticsAccountReadRPM)
		// Inventory reads degrade open when Redis is unavailable, but an
		// authoritative over-quota result stops the account-locking DB sync.
		if err == nil && !allowed {
			writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
				"error": "account cosmetics rate limit exceeded", "code": "ACCOUNT_COSMETICS_RATE_LIMIT",
			})
			return
		}
	}
	h.writeAccountInventory(w, r, session.AccountID, http.StatusOK, nil)
}

// writeAccountInventory answers every account cosmetics route with the same
// document, so the Dashboard has one shape to read: the inventory, plus the
// address where the subscription is bought when the operator has set one.
func (h *CosmeticsHandler) writeAccountInventory(w http.ResponseWriter, r *http.Request, accountID string, status int, extra map[string]interface{}) {
	inventory, err := h.authority.AccountInventory(r.Context(), accountID)
	if err != nil {
		if errors.Is(err, db.ErrNoDatabase) {
			writeError(w, http.StatusServiceUnavailable, "database not available")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load customer cosmetics")
		return
	}
	inventory.Subscription.URL = accountsShopURL()
	if len(extra) == 0 {
		writeJSON(w, status, inventory)
		return
	}
	body := make(map[string]interface{}, len(extra)+1)
	for key, value := range extra {
		body[key] = value
	}
	body["inventory"] = inventory
	writeJSON(w, status, body)
}

type linkAccountBotRequest struct {
	APIKey string `json:"api_key"`
}

func (h *CosmeticsHandler) LinkAccountBot(w http.ResponseWriter, r *http.Request) {
	session, ok := customerSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "customer authentication required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req linkAccountBotRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || strings.TrimSpace(req.APIKey) == "" || len(req.APIKey) > 256 {
		writeError(w, http.StatusBadRequest, "api_key is required")
		return
	}
	allowed, remaining, err := h.consumeAccountKeyQuota(
		r.Context(), session.AccountID, db.AccountAPIKeyQuotaLink, config.C.CustomerBotLinkPerHour,
	)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "account bot-link quota is temporarily unavailable")
		return
	}
	if !allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
			"error": "account bot-link rate limit exceeded", "code": "ACCOUNT_BOT_LINK_RATE_LIMIT",
			"limit": config.C.CustomerBotLinkPerHour, "remaining": remaining,
			"window": "1h", "retry_after": 3600,
		})
		return
	}
	linkedBot, err := h.authority.ClaimArenaAgent(r.Context(), session.AccountID, strings.TrimSpace(req.APIKey))
	if err != nil {
		switch {
		case errors.Is(err, db.ErrPlatformControlProofRejected):
			writeError(w, http.StatusUnauthorized, "invalid API key")
		case errors.Is(err, db.ErrCustomerBotAlreadyLinked):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, db.ErrCustomerAPIKeyAlreadyOwned):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, db.ErrCustomerAPIKeyLimit):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, db.ErrCustomerAPIKeyHistoryLimit):
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error": err.Error(), "code": "API_KEY_HISTORY_LIMIT",
				"history_limit": db.MaxAccountAPIKeyHistory,
				"support":       "Contact Arena support to review your account's archived API-key history.",
			})
		case errors.Is(err, db.ErrPlatformAgentLimit):
			writePlatformAgentLimit(w, err)
		case errors.Is(err, db.ErrPlatformAccountInactive):
			writeJSON(w, http.StatusForbidden, map[string]interface{}{
				"error": err.Error(), "code": "PLATFORM_ACCOUNT_INACTIVE",
			})
		case errors.Is(err, db.ErrPlatformAgentInactive):
			writeJSON(w, http.StatusForbidden, map[string]interface{}{
				"error": err.Error(), "code": "PLATFORM_AGENT_INACTIVE",
			})
		case errors.Is(err, db.ErrCustomerBotKeyInactive):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, db.ErrCustomerAccountUnverified):
			writeError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, db.ErrNoDatabase):
			writeError(w, http.StatusServiceUnavailable, "database not available")
		default:
			writeError(w, http.StatusInternalServerError, "failed to link bot")
		}
		return
	}
	// A newly linked bot inherits the account's subscription at once; if it
	// is already in the arena, its saved paid look (if any) is live now.
	h.refreshBotVisuals(r.Context(), &linkedBot.BotID)
	h.writeAccountInventory(w, r, session.AccountID, http.StatusOK, map[string]interface{}{"linked_bot": linkedBot})
}

func (h *CosmeticsHandler) UnlinkAccountBot(w http.ResponseWriter, r *http.Request) {
	session, ok := customerSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "customer authentication required")
		return
	}
	botID := strings.TrimSpace(chi.URLParam(r, "bot_id"))
	if botID == "" || len(botID) > 80 {
		writeError(w, http.StatusBadRequest, "invalid bot_id")
		return
	}
	unlinked, err := h.authority.UnlinkAgent(r.Context(), session.AccountID, botID)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrCustomerBotNotLinked):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, db.ErrNoDatabase):
			writeError(w, http.StatusServiceUnavailable, "database not available")
		default:
			writeError(w, http.StatusInternalServerError, "failed to unlink bot")
		}
		return
	}
	h.refreshBotVisuals(r.Context(), &botID)
	h.writeAccountInventory(w, r, session.AccountID, http.StatusOK, map[string]interface{}{"unlinked": unlinked, "bot_id": botID})
}

/*
 * EquipAccountCosmetic is the Dashboard's equip: PUT
 * /account/bots/{bot_id}/cosmetics with {"slot", "cosmetic_id"}.
 *
 * The bot must be linked to this account with an active key; a paid item
 * additionally needs the account's Arena subscription. That refusal is the
 * one a customer can act on, so it names the address where the subscription
 * is bought rather than just saying no.
 */
func (h *CosmeticsHandler) EquipAccountCosmetic(w http.ResponseWriter, r *http.Request) {
	session, ok := customerSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "customer authentication required")
		return
	}
	botID := strings.TrimSpace(chi.URLParam(r, "bot_id"))
	req, valid := decodeEquipCosmeticRequest(r)
	if !valid || botID == "" || len(botID) > 80 {
		writeError(w, http.StatusBadRequest, "bot_id, slot and cosmetic_id are required")
		return
	}
	item, err := h.store.EquipForAccount(r.Context(), session.AccountID, botID, req.Slot, req.CosmeticID)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrSubscriptionRequired):
			writeSubscriptionRequired(w, err.Error())
		case errors.Is(err, db.ErrCustomerBotNotLinked):
			writeError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, db.ErrCustomerBotKeyInactive):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, db.ErrCosmeticNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, db.ErrCosmeticInactive):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, db.ErrInvalidCosmeticSlot), errors.Is(err, db.ErrCosmeticSlotMismatch):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, db.ErrCustomerAccountUnverified):
			writeError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, db.ErrNoDatabase):
			writeError(w, http.StatusServiceUnavailable, "database not available")
		default:
			writeError(w, http.StatusInternalServerError, "failed to equip cosmetic")
		}
		return
	}
	equipped, _ := h.store.Equipped(r.Context(), botID)
	liveRefreshed := false
	if h.engine != nil {
		liveRefreshed = h.engine.UpdateBotCosmetics(botID, equipped)
	}
	h.writeAccountInventory(w, r, session.AccountID, http.StatusOK, map[string]interface{}{
		"item":            item,
		"equipped_assets": equipped,
		"live_refreshed":  liveRefreshed,
		"gameplay":        "unchanged",
	})
}
