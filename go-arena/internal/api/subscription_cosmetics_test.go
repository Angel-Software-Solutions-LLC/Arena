package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arena-server/internal/config"
	"arena-server/internal/db"
	"arena-server/internal/game"

	"github.com/go-chi/chi/v5"
)

/*
 * Arena sells one thing, and does not sell it itself: the Arena subscription,
 * bought and held in Angel Accounts. These tests pin what that means for the
 * HTTP surface —
 *
 *   - every route that could once start, resume or receive a payment is gone;
 *   - the catalog publishes no checkout fact, only where to subscribe;
 *   - a signed-in customer with an ACTIVE Arena entitlement, as read from
 *     Accounts at sign-in, has every cosmetic unlocked, and one without is
 *     told what is included with a subscription and where to get one.
 *
 * The database half — that the flag really unlocks every paid item for every
 * linked bot — is TestPostgresArenaSubscriptionUnlocksEveryCosmeticForLinkedBots.
 */

const testAccountsShopURL = "https://accounts.angel-serv.com/portal/products/arena"

func withAccountsShop(t *testing.T, url string) {
	t.Helper()
	previous := config.C.AccountsShopURL
	t.Cleanup(func() { config.C.AccountsShopURL = previous })
	config.C.AccountsShopURL = url
}

// TestCheckoutRoutesAreGone walks the real router and refuses to find any
// route that names a checkout, an order, a licence, a membership, a grant or
// a payment provider. A route table is the honest place to assert absence:
// an HTTP 404 could also mean a middleware refused first.
func TestCheckoutRoutesAreGone(t *testing.T) {
	previous := config.C
	t.Cleanup(func() { config.C = previous })
	config.C.CustomerOIDCEnabled = false
	config.C.AdminToken = "admin-secret"
	config.C.AdminLocalhostBypass = false
	router := NewRouter(game.NewGameEngine())

	var routes []string
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, method+" "+route)
		return nil
	}); err != nil {
		t.Fatalf("walk router: %v", err)
	}
	if len(routes) == 0 {
		t.Fatal("the router walk found no routes")
	}
	for _, retired := range []string{
		"checkout", "webhooks", "stripe", "orders", "cosmetic-licenses",
		"memberships", "grants", "licenses", "cosmetics/access", "subscription/portal",
	} {
		for _, route := range routes {
			if strings.Contains(route, retired) {
				t.Errorf("route %q still exists; Arena's own commerce is retired", route)
			}
		}
	}

	// And the wire answer for the public and the machine-authenticated ones,
	// where no earlier middleware can be what refused.
	for _, request := range []struct {
		method, path string
		admin        bool
	}{
		{http.MethodGet, "/api/v1/cosmetics/checkout/config", false},
		{http.MethodPost, "/api/v1/cosmetics/webhooks/stripe", false},
		{http.MethodPost, "/arena/api/v1/cosmetics/webhooks/stripe", false},
		{http.MethodGet, "/api/v1/admin/cosmetics/orders", true},
		{http.MethodGet, "/api/v1/admin/cosmetics/access?email=a@b.c", true},
		{http.MethodPost, "/api/v1/admin/cosmetics/grants", true},
		{http.MethodPost, "/api/v1/admin/cosmetics/memberships", true},
		{http.MethodDelete, "/api/v1/admin/cosmetics/licenses/x", true},
	} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(request.method, "https://arena.example"+request.path, strings.NewReader(`{}`))
		req.RemoteAddr = "198.51.100.10:4444"
		if request.admin {
			req.Header.Set("X-Admin-Token", "admin-secret")
		}
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNotFound && recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want the route gone (404/405); body=%s", request.method, request.path, recorder.Code, recorder.Body.String())
		}
	}
}

// TestCatalogPublishesWhereToSubscribeAndNoCheckoutFact is what a browser is
// told: prices are reference metadata, nothing is enabled, and the one
// actionable fact is that every cosmetic is included with a subscription
// bought at the Accounts address.
func TestCatalogPublishesWhereToSubscribeAndNoCheckoutFact(t *testing.T) {
	store := &fakeCosmeticsStore{publicCatalog: &db.CosmeticCatalog{
		Categories: []db.CosmeticCategory{{ID: "starter-packs", Name: "Starter Packs", IsActive: true}},
		Items:      []db.CosmeticItem{{ID: "paid", CategoryID: "starter-packs", Slot: db.CosmeticSlotAttachment, PriceCents: 199, IsPurchasable: true, IsActive: true}},
		Packs: []db.CosmeticPack{{
			ID: "neon-pack", CategoryID: "starter-packs", PriceCents: db.CosmeticPackPriceCents, Currency: "USD",
			IsPurchasable: true, IsActive: true, ItemIDs: []string{"paid"},
		}},
	}}
	handler := newCosmeticsHandlerWithStore(store, nil)

	read := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.Catalog(rec, httptest.NewRequest(http.MethodGet, "/api/v1/cosmetics/catalog", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, retired := range []string{"checkout_enabled", "subscription_offer", "purchase_handoff_url"} {
			if _, present := body[retired]; present {
				t.Fatalf("catalog still publishes %s: %v", retired, body)
			}
		}
		return body
	}

	withAccountsShop(t, testAccountsShopURL)
	body := read()
	subscription, _ := body["subscription"].(map[string]any)
	if subscription["product"] != "arena" || subscription["includes_all_cosmetics"] != true || subscription["url"] != testAccountsShopURL {
		t.Fatalf("catalog subscription = %v, want the Arena product, everything included, and the Accounts address", subscription)
	}
	if len(body["packs"].([]any)) != 1 || len(body["items"].([]any)) != 1 {
		t.Fatalf("catalog dropped its content: %v", body)
	}

	// No address configured is a supported state: the fact stays, the link
	// is simply absent rather than invented.
	withAccountsShop(t, "")
	body = read()
	subscription, _ = body["subscription"].(map[string]any)
	if _, present := subscription["url"]; present || subscription["includes_all_cosmetics"] != true {
		t.Fatalf("catalog subscription without a shop address = %v", subscription)
	}
}

// TestAccountInventoryReportsTheSubscriptionAndWhereToGetOne is the Dashboard
// document: the flag the sync recorded, when it was read, and the address.
func TestAccountInventoryReportsTheSubscriptionAndWhereToGetOne(t *testing.T) {
	withAccountsShop(t, testAccountsShopURL)
	syncedAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	store := &fakeCosmeticsStore{inventory: &db.CustomerCosmeticsInventory{
		Account:      db.CustomerAccount{ID: "account-1", SubscriptionActive: true, SubscriptionSyncedAt: &syncedAt},
		Bots:         []db.AccountBot{{BotID: "bot-1", Name: "Alpha", KeyIsActive: true}},
		Subscription: db.CustomerSubscription{Active: true, SyncedAt: &syncedAt},
		Items:        []db.CosmeticItem{{ID: "skin-neon-grid", Slot: db.CosmeticSlotBotSkin, AssetKey: "neon_grid", IsActive: true}},
		Loadouts:     map[string]map[string]string{"bot-1": {db.CosmeticSlotBotSkin: "skin-neon-grid"}},
	}}
	handler := newCosmeticsHandlerWithStore(store, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/account/cosmetics", nil)
	request = request.WithContext(withCustomerSession(request.Context(), &CustomerSession{AccountID: "account-1"}))
	handler.AccountInventory(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Subscription struct {
			Active   bool   `json:"active"`
			SyncedAt string `json:"synced_at"`
			URL      string `json:"url"`
		} `json:"subscription"`
		Items    []db.CosmeticItem            `json:"items"`
		Loadouts map[string]map[string]string `json:"loadouts"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	if !response.Subscription.Active || response.Subscription.URL != testAccountsShopURL || !strings.HasPrefix(response.Subscription.SyncedAt, "2026-09-02T12:00:00") {
		t.Fatalf("subscription = %+v", response.Subscription)
	}
	if len(response.Items) != 1 || response.Loadouts["bot-1"][db.CosmeticSlotBotSkin] != "skin-neon-grid" {
		t.Fatalf("inventory = items %d loadouts %v", len(response.Items), response.Loadouts)
	}
	for _, retired := range []string{"licenses", "subscription_offer", "membership", "orders"} {
		if strings.Contains(recorder.Body.String(), `"`+retired+`"`) {
			t.Fatalf("inventory still carries %s: %s", retired, recorder.Body.String())
		}
	}
}

// TestEquipAccountCosmeticIsGatedByTheSubscription is the customer's equip:
// a paid item without a subscription is refused with the one code and the
// one address the Dashboard can act on; with one, the bot changes at once.
func TestEquipAccountCosmeticIsGatedByTheSubscription(t *testing.T) {
	withAccountsShop(t, testAccountsShopURL)
	body := []byte(`{"slot":"bot_skin","cosmetic_id":"skin-neon-grid"}`)

	locked := &fakeCosmeticsStore{equipErr: db.ErrSubscriptionRequired}
	rec := httptest.NewRecorder()
	newCosmeticsHandlerWithStore(locked, nil).EquipAccountCosmetic(rec, requestWithCustomerParam(
		http.MethodPut, "/api/v1/account/bots/bot-1/cosmetics", body, "bot_id", "bot-1"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	var refusal map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal["code"] != "SUBSCRIPTION_REQUIRED" || refusal["subscription_url"] != testAccountsShopURL {
		t.Fatalf("refusal = %v, want SUBSCRIPTION_REQUIRED and the Accounts address", refusal)
	}

	engine := game.NewGameEngine()
	engine.Bots["bot-1"] = &game.BotState{BotID: "bot-1"}
	unlocked := &fakeCosmeticsStore{
		equipItem: &db.CosmeticItem{ID: "skin-neon-grid", Slot: db.CosmeticSlotBotSkin, AssetKey: "neon_grid"},
		equipped:  map[string]string{db.CosmeticSlotBotSkin: "neon_grid"},
		inventory: &db.CustomerCosmeticsInventory{Subscription: db.CustomerSubscription{Active: true}},
	}
	rec = httptest.NewRecorder()
	newCosmeticsHandlerWithStore(unlocked, engine).EquipAccountCosmetic(rec, requestWithCustomerParam(
		http.MethodPut, "/api/v1/account/bots/bot-1/cosmetics", body, "bot_id", "bot-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if unlocked.lastAccount != "account-1" || unlocked.lastBotID != "bot-1" || unlocked.lastSlot != "bot_skin" || unlocked.lastCosmetic != "skin-neon-grid" {
		t.Fatalf("equip reached the store as (%q, %q, %q, %q)", unlocked.lastAccount, unlocked.lastBotID, unlocked.lastSlot, unlocked.lastCosmetic)
	}
	if got := engine.Bots["bot-1"].Cosmetics[db.CosmeticSlotBotSkin]; got != "neon_grid" {
		t.Fatalf("live cosmetic = %q, want neon_grid", got)
	}
	if !strings.Contains(rec.Body.String(), `"live_refreshed":true`) || !strings.Contains(rec.Body.String(), `"inventory"`) {
		t.Fatalf("equip response = %s", rec.Body.String())
	}

	// The request shape is the bot-key one: a licence id is not a thing.
	rec = httptest.NewRecorder()
	newCosmeticsHandlerWithStore(unlocked, nil).EquipAccountCosmetic(rec, requestWithCustomerParam(
		http.MethodPut, "/api/v1/account/bots/bot-1/cosmetics", []byte(`{"license_id":"l-1"}`), "bot_id", "bot-1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("licence-shaped equip status = %d, want 400", rec.Code)
	}
}

// TestBotKeyEquipOfALockedCosmeticNamesTheSubscription: the bot-key path
// answers the same way, so an SDK author learns what to do rather than just
// "not owned".
func TestBotKeyEquipOfALockedCosmeticNamesTheSubscription(t *testing.T) {
	withAccountsShop(t, testAccountsShopURL)
	store := &fakeCosmeticsStore{equipErr: db.ErrCosmeticNotOwned}
	rec := httptest.NewRecorder()
	newCosmeticsHandlerWithStore(store, nil).Equip(rec, requestWithBot(http.MethodPut, "/api/v1/bot/cosmetics",
		[]byte(`{"slot":"bot_skin","cosmetic_id":"skin-neon-grid"}`), &db.Bot{ID: "bot-1"}))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"SUBSCRIPTION_REQUIRED"`) ||
		!strings.Contains(rec.Body.String(), testAccountsShopURL) {
		t.Fatalf("locked bot-key equip = %d %s", rec.Code, rec.Body.String())
	}
}

/*
 * TestSignInSyncsTheArenaSubscriptionFromAccounts drives the real sign-in
 * against a fake Angel Accounts that publishes an entitlements endpoint, and
 * checks that what Accounts says about the Arena product is what lands on
 * the account — and that a change tells the arena which bots to re-read.
 */
func TestSignInSyncsTheArenaSubscriptionFromAccounts(t *testing.T) {
	arenaActive := `{"account":{"id":"acct_1"},"user":{"id":"usr_1"},"entitlements":[
		{"productId":"arena","productSlug":"arena","planSlug":"all-access","status":"active","active":true}
	],"purchases":[],"refreshAfter":"2026-09-02T13:00:00Z"}`
	arenaLapsed := `{"account":{"id":"acct_1"},"user":{"id":"usr_1"},"entitlements":[
		{"productId":"arena","productSlug":"arena","planSlug":"all-access","status":"canceled","active":false}
	],"purchases":[],"refreshAfter":"2026-09-02T13:00:00Z"}`
	otherProductOnly := `{"account":{"id":"acct_1"},"user":{"id":"usr_1"},"entitlements":[
		{"productId":"kynetik","productSlug":"kynetik","planSlug":"pro","status":"active","active":true}
	],"purchases":[],"refreshAfter":"2026-09-02T13:00:00Z"}`

	accounts := newAngelAccounts(t)
	handler, authority := newArenaSignedInWithAngel(t, accounts)
	// The endpoint a deployment takes from the discovery document, or from
	// ARENA_ACCOUNTS_ENTITLEMENTS_URL; pointed at the fake here.
	handler.entitlements = accountsEntitlementsClientAt(accounts.server.URL + "/entitlements")
	authority.subscriptionBots = []string{"bot-1", "bot-2"}
	var refreshed [][]string
	handler.onSubscriptionSynced = func(_ context.Context, botIDs []string) {
		refreshed = append(refreshed, append([]string(nil), botIDs...))
	}

	accounts.serveEntitlements(arenaActive)
	signInThroughAngel(t, handler, accounts, nil)
	if len(authority.subscriptionCalls) != 1 || authority.subscriptionCalls[0] != (fakeSubscriptionCall{accountID: "account-1", active: true}) {
		t.Fatalf("subscription calls after an active entitlement = %+v", authority.subscriptionCalls)
	}
	if len(refreshed) != 1 || strings.Join(refreshed[0], ",") != "bot-1,bot-2" {
		t.Fatalf("bots refreshed after subscribing = %v, want both linked bots", refreshed)
	}

	// Same answer again: recorded, but nothing changed and nothing is re-read.
	signInThroughAngel(t, handler, accounts, nil)
	if len(authority.subscriptionCalls) != 2 || !authority.subscriptionCalls[1].active || len(refreshed) != 1 {
		t.Fatalf("repeat sign-in = calls %+v refreshed %v", authority.subscriptionCalls, refreshed)
	}

	// Accounts now says the subscription lapsed: the flag follows, and the
	// bots are re-read so their paid looks drop at once.
	accounts.serveEntitlements(arenaLapsed)
	signInThroughAngel(t, handler, accounts, nil)
	if len(authority.subscriptionCalls) != 3 || authority.subscriptionCalls[2].active || len(refreshed) != 2 {
		t.Fatalf("lapsed sign-in = calls %+v refreshed %v", authority.subscriptionCalls, refreshed)
	}

	// A subscription to some other Angel product is not an Arena subscription.
	accounts.serveEntitlements(arenaActive)
	signInThroughAngel(t, handler, accounts, nil)
	accounts.serveEntitlements(otherProductOnly)
	signInThroughAngel(t, handler, accounts, nil)
	if last := authority.subscriptionCalls[len(authority.subscriptionCalls)-1]; last.active {
		t.Fatalf("another product's entitlement unlocked Arena: %+v", authority.subscriptionCalls)
	}

	// A read that fails leaves the previous answer standing: an outage is
	// not a cancellation, and the sign-in itself still succeeds.
	calls := len(authority.subscriptionCalls)
	accounts.serveEntitlements("")
	session, _ := signInThroughAngel(t, handler, accounts, nil)
	if session == nil || len(authority.subscriptionCalls) != calls {
		t.Fatalf("an unreadable entitlements endpoint changed the subscription or broke sign-in: %+v", authority.subscriptionCalls)
	}
}

// TestEntitlementsSyncIsNeverOnThePathToASession states the rule that decides
// how a commerce outage is felt: signing in still works.
func TestEntitlementsSyncIsNeverOnThePathToASession(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)

	authority := &fakeIdentityAuthority{}
	handler := &CustomerOIDCHandler{entitlements: newTestEntitlementsClient(failing), authority: authority}
	if _, err := handler.syncEntitlementsFromAccounts(t.Context(), "account-1", "at_token"); err == nil {
		t.Fatalf("a failing entitlements service reported success")
	}
	if len(authority.subscriptionCalls) != 0 {
		t.Fatalf("a failed read wrote a subscription answer: %+v", authority.subscriptionCalls)
	}

	// And with no entitlements source at all — an Accounts that has not
	// advertised the endpoint — the read is a no-op rather than an error a
	// caller has to know to ignore.
	quiet := &CustomerOIDCHandler{authority: authority}
	result, err := quiet.syncEntitlementsFromAccounts(t.Context(), "account-1", "at_token")
	if err != nil || result != nil {
		t.Fatalf("unconfigured sync = (%+v, %v), want a silent no-op", result, err)
	}
}
