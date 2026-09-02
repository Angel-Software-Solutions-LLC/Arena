package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arena-server/internal/config"
	"arena-server/internal/db"
	"arena-server/internal/game"
	"arena-server/internal/security"

	"github.com/go-chi/chi/v5"
)

type fakeCosmeticsStore struct {
	publicCatalog    *db.CosmeticCatalog
	adminCatalog     *db.CosmeticCatalog
	audit            []db.CosmeticCatalogAudit
	items            []db.BotCosmeticItem
	equipped         map[string]string
	equippedErr      error
	equippedBotIDs   []string
	equipItem        *db.CosmeticItem
	equipErr         error
	grantErr         error
	revoked          bool
	revokeErr        error
	inventory        *db.CustomerCosmeticsInventory
	linkBot          *db.AccountBot
	lastBotID        string
	lastAccount      string
	lastSlot         string
	lastCosmetic     string
	lastActor        string
	lastLimit        int
	lastControlProof string
	claimCalls       int
}

func (f *fakeCosmeticsStore) PublicCatalog(context.Context) (*db.CosmeticCatalog, error) {
	return f.publicCatalog, f.grantErr
}
func (f *fakeCosmeticsStore) AdminCatalog(context.Context) (*db.CosmeticCatalog, error) {
	return f.adminCatalog, f.grantErr
}
func (f *fakeCosmeticsStore) UpsertCategory(_ context.Context, category db.CosmeticCategory, actor string) (*db.CosmeticCategory, error) {
	f.lastActor = actor
	return &category, f.grantErr
}
func (f *fakeCosmeticsStore) DeleteCategory(_ context.Context, id, actor string) (bool, error) {
	f.lastCosmetic, f.lastActor = id, actor
	return f.revoked, f.revokeErr
}
func (f *fakeCosmeticsStore) UpsertItem(_ context.Context, item db.CosmeticItem, actor string) (*db.CosmeticItem, error) {
	f.lastCosmetic, f.lastActor = item.ID, actor
	return &item, f.grantErr
}
func (f *fakeCosmeticsStore) DeleteItem(_ context.Context, id, actor string) (bool, error) {
	f.lastCosmetic, f.lastActor = id, actor
	return f.revoked, f.revokeErr
}
func (f *fakeCosmeticsStore) UpsertPack(_ context.Context, pack db.CosmeticPack, actor string) (*db.CosmeticPack, error) {
	f.lastCosmetic, f.lastActor = pack.ID, actor
	return &pack, f.grantErr
}
func (f *fakeCosmeticsStore) DeletePack(_ context.Context, id, actor string) (bool, error) {
	f.lastCosmetic, f.lastActor = id, actor
	return f.revoked, f.revokeErr
}
func (f *fakeCosmeticsStore) ListAudit(_ context.Context, limit int) ([]db.CosmeticCatalogAudit, error) {
	f.lastLimit = limit
	return f.audit, f.grantErr
}
func (f *fakeCosmeticsStore) ListForBot(context.Context, string) ([]db.BotCosmeticItem, error) {
	return f.items, nil
}
func (f *fakeCosmeticsStore) Equipped(_ context.Context, botID string) (map[string]string, error) {
	f.equippedBotIDs = append(f.equippedBotIDs, botID)
	return f.equipped, f.equippedErr
}
func (f *fakeCosmeticsStore) Equip(_ context.Context, botID, slot, cosmeticID string) (*db.CosmeticItem, error) {
	f.lastBotID, f.lastSlot, f.lastCosmetic = botID, slot, cosmeticID
	return f.equipItem, f.equipErr
}

func (f *fakeCosmeticsStore) AccountInventory(_ context.Context, accountID string) (*db.CustomerCosmeticsInventory, error) {
	f.lastAccount = accountID
	if f.inventory == nil {
		f.inventory = &db.CustomerCosmeticsInventory{}
	}
	return f.inventory, nil
}

func TestAccountCosmeticsInventoryAccountQuotaStopsStoreWork(t *testing.T) {
	store := &fakeCosmeticsStore{}
	handler := newCosmeticsHandlerWithStore(store, nil)
	var quotaAccount string
	var quotaLimit int
	handler.checkAccountInventoryQuota = func(_ context.Context, accountID string, limit int) (bool, error) {
		quotaAccount, quotaLimit = accountID, limit
		return false, nil
	}
	previous := config.C
	t.Cleanup(func() { config.C = previous })
	config.C.CosmeticsAccountReadRPM = 60
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/account/cosmetics", nil)
	request = request.WithContext(withCustomerSession(request.Context(), &CustomerSession{AccountID: "account-quota"}))
	handler.AccountInventory(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || quotaAccount != "account-quota" || quotaLimit != 60 || store.lastAccount != "" {
		t.Fatalf("inventory quota status=%d quota=%q/%d store=%q body=%s",
			recorder.Code, quotaAccount, quotaLimit, store.lastAccount, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"ACCOUNT_COSMETICS_RATE_LIMIT"`) {
		t.Fatalf("inventory quota response=%s", recorder.Body.String())
	}
}

func TestAccountCosmeticsInventoryIncludesLinkedBotPreviewMetadata(t *testing.T) {
	store := &fakeCosmeticsStore{inventory: &db.CustomerCosmeticsInventory{
		Bots: []db.AccountBot{{
			BotID:         "bot-preview",
			Name:          "Preview Bot",
			KeyPrefix:     "arena_preview",
			KeyIsActive:   true,
			AvatarColor:   "#22ccff",
			DefaultWeapon: "spear",
		}},
	}}
	handler := newCosmeticsHandlerWithStore(store, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/account/cosmetics", nil)
	request = request.WithContext(withCustomerSession(request.Context(), &CustomerSession{
		AccountID: "account-preview",
		Email:     "preview@example.com",
	}))

	handler.AccountInventory(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Bots []db.AccountBot `json:"bots"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	if len(response.Bots) != 1 || response.Bots[0].AvatarColor != "#22ccff" || response.Bots[0].DefaultWeapon != "spear" {
		t.Fatalf("linked bot preview metadata = %+v", response.Bots)
	}
}

func TestAccountCosmeticsInventoryUsesPlatformAuthority(t *testing.T) {
	authority := &fakeCosmeticsStore{inventory: &db.CustomerCosmeticsInventory{
		Account: db.CustomerAccount{ID: "platform-account"},
	}}
	arenaStore := &fakeCosmeticsStore{inventory: &db.CustomerCosmeticsInventory{
		Account: db.CustomerAccount{ID: "arena-private-account"},
	}}
	handler := newCosmeticsHandlerWithStores(authority, arenaStore, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/account/cosmetics", nil)
	request = request.WithContext(withCustomerSession(request.Context(), &CustomerSession{
		AccountID: "account-owner",
		Email:     "owner@example.com",
	}))

	handler.AccountInventory(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response db.CustomerCosmeticsInventory
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	if response.Account.ID != "platform-account" {
		t.Fatalf("inventory account = %q, want platform authority result", response.Account.ID)
	}
	if authority.lastAccount != "account-owner" {
		t.Fatalf("platform authority account = %q, want account-owner", authority.lastAccount)
	}
	if arenaStore.lastAccount != "" {
		t.Fatalf("Arena-private store handled platform inventory for %q", arenaStore.lastAccount)
	}
}

func (f *fakeCosmeticsStore) ClaimArenaAgent(_ context.Context, accountID, controlProof string) (*db.AccountBot, error) {
	f.lastAccount, f.lastControlProof = accountID, controlProof
	f.claimCalls++
	return f.linkBot, f.grantErr
}
func (f *fakeCosmeticsStore) UnlinkAgent(_ context.Context, accountID, botID string) (bool, error) {
	f.lastAccount, f.lastBotID = accountID, botID
	return f.revoked, f.revokeErr
}
func (f *fakeCosmeticsStore) EquipForAccount(_ context.Context, accountID, botID, slot, cosmeticID string) (*db.CosmeticItem, error) {
	f.lastAccount, f.lastBotID, f.lastSlot, f.lastCosmetic = accountID, botID, slot, cosmeticID
	return f.equipItem, f.equipErr
}

func requestWithBot(method, target string, body []byte, bot *db.Bot) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if bot != nil {
		req = req.WithContext(security.WithBotContext(req.Context(), bot))
	}
	return req
}

func TestCosmeticAdminActorUsesAuthenticatedPrincipal(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/cosmetics/catalog", nil)
	request = request.WithContext(withAdminPrincipal(request.Context(), "oidc:operator@example.com"))
	if actor := cosmeticAdminActor(request); actor != "oidc:operator@example.com" {
		t.Fatalf("actor=%q", actor)
	}
}

func requestWithCustomerParam(method, target string, body []byte, param, value string) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add(param, value)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withCustomerSession(ctx, &CustomerSession{AccountID: "account-1"})
	return req.WithContext(ctx)
}

func requestWithRouteParam(method, target string, body []byte, param, value string) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add(param, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func TestLinkAccountBotMapsDurableKeyOwnershipConflicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "owned by another account", err: db.ErrCustomerAPIKeyAlreadyOwned},
		{name: "active key limit", err: db.ErrCustomerAPIKeyLimit},
		{name: "lifetime history limit", err: db.ErrCustomerAPIKeyHistoryLimit},
		{name: "platform agent limit", err: db.ErrPlatformAgentLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeCosmeticsStore{grantErr: tc.err}
			handler := newCosmeticsHandlerWithStore(store, nil)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/account/bots", strings.NewReader(`{"api_key":"arena_valid"}`))
			req = req.WithContext(withCustomerSession(req.Context(), &CustomerSession{AccountID: "account-1"}))
			rec := httptest.NewRecorder()

			handler.LinkAccountBot(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusConflict, rec.Body.String())
			}
		})
	}
}

func TestLinkAccountBotMapsRetiredPlatformAgent(t *testing.T) {
	store := &fakeCosmeticsStore{grantErr: db.ErrPlatformAgentInactive}
	handler := newCosmeticsHandlerWithStore(store, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/bots", strings.NewReader(`{"api_key":"arena_retired_control_proof"}`))
	req = req.WithContext(withCustomerSession(req.Context(), &CustomerSession{AccountID: "account-1"}))
	recorder := httptest.NewRecorder()

	handler.LinkAccountBot(recorder, req)

	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), `"code":"PLATFORM_AGENT_INACTIVE"`) {
		t.Fatalf("retired agent response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestLinkAccountBotPassesControlProofOnceToAuthority(t *testing.T) {
	store := &fakeCosmeticsStore{linkBot: &db.AccountBot{BotID: "bot-1"}}
	handler := newCosmeticsHandlerWithStore(store, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/bots", strings.NewReader(`{"api_key":"arena_control_proof_1234567890"}`))
	req = req.WithContext(withCustomerSession(req.Context(), &CustomerSession{AccountID: "account-1"}))
	recorder := httptest.NewRecorder()

	handler.LinkAccountBot(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if store.claimCalls != 1 || store.lastAccount != "account-1" || store.lastControlProof != "arena_control_proof_1234567890" {
		t.Fatalf("authority claim = (%q, %q)", store.lastAccount, store.lastControlProof)
	}
}

func TestLinkAccountBotRejectsOversizedBodyBeforeQuotaOrBcrypt(t *testing.T) {
	store := &fakeCosmeticsStore{}
	handler := newCosmeticsHandlerWithStore(store, nil)
	quotaCalls := 0
	handler.consumeAccountKeyQuota = func(context.Context, string, db.AccountAPIKeyQuotaAction, int) (bool, int, error) {
		quotaCalls++
		return true, 1, nil
	}
	body := `{"api_key":"` + strings.Repeat("x", 8<<10) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/bots", strings.NewReader(body))
	req = req.WithContext(withCustomerSession(req.Context(), &CustomerSession{AccountID: "account-1"}))
	recorder := httptest.NewRecorder()

	handler.LinkAccountBot(recorder, req)

	if recorder.Code != http.StatusBadRequest || quotaCalls != 0 || store.claimCalls != 0 {
		t.Fatalf("oversized link = status %d quota=%d claims=%d body=%s", recorder.Code, quotaCalls, store.claimCalls, recorder.Body.String())
	}
}

func TestLinkAccountBotQuotaIsPerAccountAcrossSourceIPsAndRunsBeforeBcrypt(t *testing.T) {
	previous := config.C.CustomerBotLinkPerHour
	config.C.CustomerBotLinkPerHour = 1
	t.Cleanup(func() { config.C.CustomerBotLinkPerHour = previous })

	store := &fakeCosmeticsStore{linkBot: &db.AccountBot{BotID: "bot-1"}}
	handler := newCosmeticsHandlerWithStore(store, nil)
	quotaCount := 0
	handler.consumeAccountKeyQuota = func(_ context.Context, accountID string, action db.AccountAPIKeyQuotaAction, limit int) (bool, int, error) {
		if accountID != "account-1" || action != db.AccountAPIKeyQuotaLink || limit != 1 {
			t.Fatalf("quota input account=%q action=%q limit=%d", accountID, action, limit)
		}
		if quotaCount >= limit {
			return false, 0, nil
		}
		quotaCount++
		return true, 0, nil
	}

	for index, remote := range []string{"198.51.100.10:1000", "203.0.113.20:2000"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/account/bots", strings.NewReader(`{"api_key":"arena_valid"}`))
		req.RemoteAddr = remote
		req = req.WithContext(withCustomerSession(req.Context(), &CustomerSession{AccountID: "account-1"}))
		recorder := httptest.NewRecorder()
		handler.LinkAccountBot(recorder, req)
		if index == 0 && recorder.Code != http.StatusOK {
			t.Fatalf("first link = %d %s", recorder.Code, recorder.Body.String())
		}
		if index == 1 && (recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "ACCOUNT_BOT_LINK_RATE_LIMIT")) {
			t.Fatalf("second link = %d %s", recorder.Code, recorder.Body.String())
		}
	}
	if store.claimCalls != 1 {
		t.Fatalf("authority claim calls = %d, want 1", store.claimCalls)
	}
}

func TestAdminCosmeticsCatalogIncludesInactiveEntries(t *testing.T) {
	store := &fakeCosmeticsStore{adminCatalog: &db.CosmeticCatalog{
		Categories: []db.CosmeticCategory{{ID: "drafts", Name: "Drafts", IsActive: false}},
		Items:      []db.CosmeticItem{{ID: "draft-item", CategoryID: "drafts", IsActive: false}},
		Packs:      []db.CosmeticPack{{ID: "draft-pack", CategoryID: "drafts", PriceCents: 99, Currency: "USD", IsPurchasable: true, IsActive: false}},
	}}
	handler := newCosmeticsHandlerWithStore(store, nil)
	rec := httptest.NewRecorder()
	handler.AdminCatalog(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/cosmetics/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Categories []db.CosmeticCategory `json:"categories"`
		Packs      []db.CosmeticPack     `json:"packs"`
		Items      []db.CosmeticItem     `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Categories) != 1 || len(response.Packs) != 1 || len(response.Items) != 1 {
		t.Fatalf("unexpected admin catalog response: %+v", response)
	}
	if strings.Contains(rec.Body.String(), "checkout") {
		t.Fatalf("admin catalog still publishes a checkout fact: %s", rec.Body.String())
	}
}

func TestAdminCosmeticCatalogMutationsUsePathIdentityAndSafeAuditActor(t *testing.T) {
	store := &fakeCosmeticsStore{revoked: true}
	handler := newCosmeticsHandlerWithStore(store, nil)

	category := httptest.NewRecorder()
	categoryReq := requestWithRouteParam(http.MethodPut, "/api/v1/admin/cosmetics/categories/event", []byte(`{
		"name":"Event", "description":"Limited event cosmetics", "is_active":true, "sort_order":50
	}`), "category_id", "event")
	categoryReq.Header.Set("X-Admin-Token", "never-store-this-secret")
	handler.UpsertAdminCategory(category, categoryReq)
	if category.Code != http.StatusOK || store.lastActor != "admin-token" {
		t.Fatalf("category mutation status=%d actor=%q body=%s", category.Code, store.lastActor, category.Body.String())
	}

	item := httptest.NewRecorder()
	handler.UpsertAdminItem(item, requestWithRouteParam(http.MethodPut, "/api/v1/admin/cosmetics/items/attachment-event", []byte(`{
		"name":"Event Crown", "description":"Presentation only", "category_id":"event",
		"slot":"attachment", "asset_key":"signal_antenna", "rarity":"rare", "price_cents":99,
		"currency":"USD", "is_free":false, "is_purchasable":true, "is_active":true, "sort_order":10
	}`), "item_id", "attachment-event"))
	if item.Code != http.StatusOK || store.lastCosmetic != "attachment-event" {
		t.Fatalf("item mutation status=%d id=%q body=%s", item.Code, store.lastCosmetic, item.Body.String())
	}

	pack := httptest.NewRecorder()
	handler.UpsertAdminPack(pack, requestWithRouteParam(http.MethodPut, "/api/v1/admin/cosmetics/packs/event-pack", []byte(`{
		"name":"Event Pack", "description":"A tiny pack", "category_id":"event", "price_cents":199,
		"currency":"USD", "is_free":false, "is_purchasable":true, "is_active":true,
		"sort_order":20, "item_ids":["attachment-event"]
	}`), "pack_id", "event-pack"))
	if pack.Code != http.StatusOK || store.lastCosmetic != "event-pack" {
		t.Fatalf("pack mutation status=%d id=%q body=%s", pack.Code, store.lastCosmetic, pack.Body.String())
	}

	deleted := httptest.NewRecorder()
	handler.DeleteAdminPack(deleted, requestWithRouteParam(http.MethodDelete, "/api/v1/admin/cosmetics/packs/event-pack", nil, "pack_id", "event-pack"))
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("pack delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if strings.Contains(category.Body.String(), "never-store-this-secret") {
		t.Fatal("admin token leaked into mutation response")
	}
}

func TestAdminCosmeticItemMutationRefreshesConnectedBotVisuals(t *testing.T) {
	engine := game.NewGameEngine()
	engine.Bots["bot-active"] = &game.BotState{
		BotID: "bot-active", Cosmetics: map[string]string{db.CosmeticSlotBotSkin: "neon_grid"},
	}
	engine.WaitingBots["bot-waiting"] = &game.BotState{
		BotID: "bot-waiting", Cosmetics: map[string]string{db.CosmeticSlotBotSkin: "neon_grid"},
	}
	// This mirrors the DB projection after the item is deactivated: the
	// previously equipped asset is no longer resolved for either live bot.
	store := &fakeCosmeticsStore{equipped: map[string]string{}}
	handler := newCosmeticsHandlerWithStore(store, engine)

	recorder := httptest.NewRecorder()
	handler.UpsertAdminItem(recorder, requestWithRouteParam(
		http.MethodPut,
		"/api/v1/admin/cosmetics/items/skin-neon-grid",
		[]byte(`{
			"name":"Neon Grid Chassis", "description":"Presentation only", "category_id":"chassis",
			"slot":"bot_skin", "asset_key":"neon_grid", "rarity":"rare", "price_cents":499,
			"currency":"USD", "is_free":false, "is_purchasable":false, "is_active":false, "sort_order":20
		}`),
		"item_id",
		"skin-neon-grid",
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, bot := range []*game.BotState{engine.Bots["bot-active"], engine.WaitingBots["bot-waiting"]} {
		if len(bot.Cosmetics) != 0 {
			t.Fatalf("bot %s retained stale cosmetics after admin mutation: %+v", bot.BotID, bot.Cosmetics)
		}
	}
}

func TestAdminCosmeticItemDeletionRefreshesConnectedBotVisuals(t *testing.T) {
	engine := game.NewGameEngine()
	engine.Bots["bot-active"] = &game.BotState{
		BotID: "bot-active", Cosmetics: map[string]string{db.CosmeticSlotAttachment: "arena_set_003_ember_vanguard"},
	}
	store := &fakeCosmeticsStore{revoked: true, equipped: map[string]string{}}
	handler := newCosmeticsHandlerWithStore(store, engine)
	recorder := httptest.NewRecorder()
	handler.DeleteAdminItem(
		recorder,
		requestWithRouteParam(http.MethodDelete, "/api/v1/admin/cosmetics/items/custom", nil, "item_id", "custom"),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(engine.Bots["bot-active"].Cosmetics) != 0 {
		t.Fatalf("connected bot retained deleted cosmetic: %+v", engine.Bots["bot-active"].Cosmetics)
	}
}

func TestAdminCosmeticCatalogMutationsValidateAndMapConflicts(t *testing.T) {
	badJSON := httptest.NewRecorder()
	newCosmeticsHandlerWithStore(&fakeCosmeticsStore{}, nil).UpsertAdminCategory(badJSON,
		requestWithRouteParam(http.MethodPut, "/api/v1/admin/cosmetics/categories/event", []byte(`{"name":"Event","unknown":true}`), "category_id", "event"))
	if badJSON.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want 400", badJSON.Code)
	}

	conflictStore := &fakeCosmeticsStore{grantErr: db.ErrCosmeticCatalogConflict}
	conflict := httptest.NewRecorder()
	newCosmeticsHandlerWithStore(conflictStore, nil).UpsertAdminItem(conflict,
		requestWithRouteParam(http.MethodPut, "/api/v1/admin/cosmetics/items/item", []byte(`{
			"name":"Item", "category_id":"event", "slot":"attachment", "asset_key":"signal_antenna", "rarity":"common",
			"currency":"USD", "is_free":true, "is_active":true
		}`), "item_id", "item"))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("catalog conflict status = %d, want 409; body=%s", conflict.Code, conflict.Body.String())
	}

	builtinStore := &fakeCosmeticsStore{revokeErr: db.ErrCosmeticCatalogBuiltin}
	builtin := httptest.NewRecorder()
	newCosmeticsHandlerWithStore(builtinStore, nil).DeleteAdminItem(
		builtin,
		requestWithRouteParam(http.MethodDelete, "/api/v1/admin/cosmetics/items/skin-standard", nil, "item_id", "skin-standard"),
	)
	if builtin.Code != http.StatusConflict || !strings.Contains(builtin.Body.String(), "deactivate") {
		t.Fatalf("built-in delete status=%d body=%s", builtin.Code, builtin.Body.String())
	}
}

func TestAdminCosmeticAuditCapsLimit(t *testing.T) {
	store := &fakeCosmeticsStore{audit: []db.CosmeticCatalogAudit{{ID: 1, Actor: "admin-token"}}}
	handler := newCosmeticsHandlerWithStore(store, nil)
	rec := httptest.NewRecorder()
	handler.AdminAudit(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/cosmetics/audit?limit=9999", nil))
	if rec.Code != http.StatusOK || store.lastLimit != 200 {
		t.Fatalf("audit status=%d limit=%d body=%s", rec.Code, store.lastLimit, rec.Body.String())
	}
}

func TestRegisterCosmeticsAdminRoutes(t *testing.T) {
	store := &fakeCosmeticsStore{
		adminCatalog: &db.CosmeticCatalog{},
		revoked:      true,
	}
	handler := newCosmeticsHandlerWithStore(store, nil)
	router := chi.NewRouter()
	registerCosmeticsAdminRoutes(router, handler)

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/cosmetics/catalog", ""},
		{http.MethodGet, "/cosmetics/audit", ""},
		{http.MethodPut, "/cosmetics/categories/event", `{"name":"Event","is_active":true}`},
		{http.MethodDelete, "/cosmetics/categories/event", ""},
		{http.MethodPut, "/cosmetics/items/event-item", `{
			"name":"Event Item","category_id":"event","slot":"attachment","asset_key":"signal_antenna",
			"rarity":"common","currency":"USD","is_free":true,"is_active":true
		}`},
		{http.MethodDelete, "/cosmetics/items/event-item", ""},
		{http.MethodPut, "/cosmetics/packs/event-pack", `{
			"name":"Event Pack","category_id":"event","price_cents":199,"currency":"USD",
			"is_purchasable":true,"is_active":true,"item_ids":["event-item"]
		}`},
		{http.MethodDelete, "/cosmetics/packs/event-pack", ""},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestEquipCosmeticRequiresOwnershipAndAuth(t *testing.T) {
	store := &fakeCosmeticsStore{equipErr: db.ErrCosmeticNotOwned}
	handler := newCosmeticsHandlerWithStore(store, nil)
	body := []byte(`{"slot":"weapon_skin","cosmetic_id":"weapon-solar-flare"}`)

	unauth := httptest.NewRecorder()
	handler.Equip(unauth, requestWithBot(http.MethodPut, "/api/v1/bot/cosmetics", body, nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", unauth.Code)
	}

	forbidden := httptest.NewRecorder()
	handler.Equip(forbidden, requestWithBot(http.MethodPut, "/api/v1/bot/cosmetics", body, &db.Bot{ID: "bot-1"}))
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("unowned status = %d, want 403; body=%s", forbidden.Code, forbidden.Body.String())
	}
}

func TestEquipCosmeticRefreshesConnectedBotVisuals(t *testing.T) {
	store := &fakeCosmeticsStore{
		equipItem: &db.CosmeticItem{ID: "attachment-signal-antenna", Slot: db.CosmeticSlotAttachment, AssetKey: "signal_antenna"},
		equipped:  map[string]string{db.CosmeticSlotAttachment: "signal_antenna"},
	}
	engine := game.NewGameEngine()
	engine.Bots["bot-1"] = &game.BotState{BotID: "bot-1"}
	handler := newCosmeticsHandlerWithStore(store, engine)
	body := []byte(`{"slot":"attachment","cosmetic_id":"attachment-signal-antenna"}`)

	rec := httptest.NewRecorder()
	handler.Equip(rec, requestWithBot(http.MethodPut, "/api/v1/bot/cosmetics", body, &db.Bot{ID: "bot-1"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := engine.Bots["bot-1"].Cosmetics[db.CosmeticSlotAttachment]; got != "signal_antenna" {
		t.Fatalf("live cosmetic = %q, want signal_antenna", got)
	}
}

func TestEquipCosmeticMapsStorageFailure(t *testing.T) {
	store := &fakeCosmeticsStore{equipErr: errors.New("boom")}
	handler := newCosmeticsHandlerWithStore(store, nil)
	rec := httptest.NewRecorder()
	handler.Equip(rec, requestWithBot(http.MethodPut, "/api/v1/bot/cosmetics",
		[]byte(`{"slot":"bot_skin","cosmetic_id":"skin-standard"}`), &db.Bot{ID: "bot-1"}))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestCustomerCosmeticEquipRejectsInactiveBotKey(t *testing.T) {
	store := &fakeCosmeticsStore{equipErr: db.ErrCustomerBotKeyInactive}
	handler := newCosmeticsHandlerWithStore(store, nil)
	rec := httptest.NewRecorder()
	handler.EquipAccountCosmetic(rec, requestWithCustomerParam(
		http.MethodPut,
		"/api/v1/account/bots/bot-inactive/cosmetics",
		[]byte(`{"slot":"bot_skin","cosmetic_id":"skin-neon-grid"}`),
		"bot_id", "bot-inactive",
	))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if store.lastAccount != "account-1" || store.lastBotID != "bot-inactive" || store.lastSlot != "bot_skin" || store.lastCosmetic != "skin-neon-grid" {
		t.Fatalf("equip reached the store as (%q, %q, %q, %q)", store.lastAccount, store.lastBotID, store.lastSlot, store.lastCosmetic)
	}
}
