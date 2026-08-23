package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"arena-server/internal/accounts"
	"arena-server/internal/config"
	"arena-server/internal/db"
)

// withAccountsShop points this process at an Accounts shop for one test, the
// way a deployment does with ARENA_ACCOUNTS_SHOP_URL.
func withAccountsShop(t *testing.T, handoff string) {
	t.Helper()
	previous := config.C.AccountsShopURL
	t.Cleanup(func() { config.C.AccountsShopURL = previous })
	config.C.AccountsShopURL = handoff
}

const testHandoffURL = "https://accounts.angel-serv.com/portal/items"

// TestCheckoutEndpointsRefuseOncePurchasingMoved is the gate that makes the
// cutover safe to throw.
//
// A shop that hands off while the API behind it still opens Stripe sessions is
// one stale browser tab away from taking money in the wrong place — twice, for
// a thing the buyer is about to buy again on the other side. So every endpoint
// that could start or resume a charge refuses, and refuses in a way an old
// client can act on: the reply names where the purchase actually happens.
func TestCheckoutEndpointsRefuseOncePurchasingMoved(t *testing.T) {
	withAccountsShop(t, testHandoffURL)

	subscription := &db.CosmeticSubscription{
		ID: "subscription-record", AccountID: "account-1", AccountEmail: "owner@example.com",
		Status: db.CosmeticSubscriptionStatusCreated, PriceCents: 1999, Currency: "USD",
		Interval: "month", CustomerID: "cus_owner", CanManage: true,
	}
	provider := &fakeCosmeticPaymentProvider{
		session: &CosmeticCheckoutSession{ID: "cs_should_never_open", ClientSecret: "cs_secret"},
		portal:  &CosmeticPortalSession{URL: "https://billing.stripe.com/p/session/test"},
	}
	store := &fakeCosmeticCommerceStore{order: commerceTestOrder()}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(
		store, &fakeCosmeticSubscriptionStore{subscription: subscription}, provider, true)

	for _, endpoint := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
		body string
	}{
		{name: "pack checkout", call: handler.Checkout, body: `{"pack_id":"arena-set-003-ember-vanguard-pack","quantity":1}`},
		{name: "resume an order", call: handler.ResumeCheckout, body: `{}`},
		{name: "subscription checkout", call: handler.SubscriptionCheckout, body: `{}`},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			endpoint.call(recorder, commerceCustomerRequest(http.MethodPost, "/", endpoint.body))
			/*
			 * 409 rather than 503: nothing is broken or temporarily away. This
			 * endpoint is no longer where the act happens, and a client that
			 * cannot tell those apart will retry the one it should not.
			 */
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body = %s", recorder.Code, recorder.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode refusal: %v (%s)", err, recorder.Body.String())
			}
			if body["handoff_url"] != testHandoffURL {
				t.Fatalf("refusal = %v, want it to name where buying happens", body)
			}
		})
	}

	// Nothing was reserved, attached, or asked of Stripe on the way to any of
	// those refusals. The gate is the first statement in each handler for this
	// reason: a half-created order is a support ticket nobody can close.
	if provider.request.OrderID != "" || provider.subscriptionRequest.AccountID != "" || provider.portalCustomer != "" {
		t.Fatalf("a refused checkout still reached the payment provider: %+v", provider.request)
	}
	if store.createdAccount != "" || store.attachedID != "" {
		t.Fatalf("a refused checkout still wrote an order: create=%q attach=%q", store.createdAccount, store.attachedID)
	}

	/*
	 * And the one endpoint that is deliberately left open.
	 *
	 * A subscription started before the switch is live money in Arena's own
	 * Stripe account, and this portal is the only place its holder can cancel
	 * it. Sending them to an Angel account that does not hold that
	 * subscription would leave a recurring charge nobody can reach. The
	 * repository already drew this line for a sales pause
	 * (TestCosmeticSubscriptionPortalRemainsAvailableWhenNewSalesArePaused);
	 * a handoff is the same kind of event and gets the same answer.
	 */
	portal := httptest.NewRecorder()
	handler.SubscriptionPortal(portal, commerceCustomerRequest(http.MethodPost, "/", `{}`))
	if portal.Code != http.StatusOK || provider.portalCustomer != "cus_owner" {
		t.Fatalf("an existing subscriber lost their billing portal: status=%d customer=%q body=%s",
			portal.Code, provider.portalCustomer, portal.Body.String())
	}
}

// TestCheckoutConfigReportsTheHandoffInsteadOfAStripeKey covers what a browser
// is told, in both of the states that matter: a client running this bundle,
// and one that has not been reloaded since the switch.
func TestCheckoutConfigReportsTheHandoffInsteadOfAStripeKey(t *testing.T) {
	handler := newCosmeticCommerceHandlerWithStore(
		&fakeCosmeticCommerceStore{}, &fakeCosmeticPaymentProvider{}, true)
	handler.publishableKey = "pk_test_browser_key"

	withAccountsShop(t, "")
	recorder := httptest.NewRecorder()
	handler.CheckoutConfig(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	var selling map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &selling); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if selling["mode"] != "stripe" || selling["enabled"] != true || selling["publishable_key"] != "pk_test_browser_key" {
		t.Fatalf("while Arena sells, config = %v", selling)
	}

	withAccountsShop(t, testHandoffURL)
	recorder = httptest.NewRecorder()
	handler.CheckoutConfig(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	var handedOff map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &handedOff); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if handedOff["mode"] != "handoff" || handedOff["handoff_url"] != testHandoffURL {
		t.Fatalf("after the switch, config = %v", handedOff)
	}
	/*
	 * `enabled` goes false, and the browser key goes away with it. A stale
	 * bundle knows only that flag, so this is what stops it opening a checkout
	 * it can no longer complete — it shows "not available" instead of failing
	 * at the last step, and a bundle that knows `mode` sends the buyer on.
	 */
	if handedOff["enabled"] != false {
		t.Fatalf("a stale client would still have offered checkout: %v", handedOff)
	}
	if _, present := handedOff["publishable_key"]; present {
		t.Fatalf("a browser key is still published when Arena no longer sells: %v", handedOff)
	}
}

// TestPurchaseGrantsCarryRevocationsThrough is the reduction from the wire
// shape to what the reconciler acts on.
//
// The property worth a test is that a revoked purchase is *kept*, not filtered
// out. Dropping it would make a withdrawal look identical to a purchase that
// simply stopped being mentioned — and the reconciler deliberately does
// nothing about the second, because a truncated read must never strip a
// wallet.
func TestPurchaseGrantsCarryRevocationsThrough(t *testing.T) {
	snapshot := &accounts.Snapshot{Purchases: []accounts.Purchase{
		{ID: "pur_a", SKU: "pack-a", Status: accounts.PurchaseActive},
		{ID: "pur_b", SKU: "pack-b", Status: accounts.PurchaseRevoked},
		{ID: "", SKU: "pack-c", Status: accounts.PurchaseActive},
		{ID: "pur_d", SKU: "", Status: accounts.PurchaseActive},
	}}
	grants := purchaseGrants(snapshot)
	want := []db.AccountsPurchaseGrant{
		{GrantID: "pur_a", SKU: "pack-a", Revoked: false},
		{GrantID: "pur_b", SKU: "pack-b", Revoked: true},
	}
	if len(grants) != len(want) {
		t.Fatalf("grants = %+v, want the two that name both a grant and a pack", grants)
	}
	for index, grant := range grants {
		if grant != want[index] {
			t.Fatalf("grant %d = %+v, want %+v", index, grant, want[index])
		}
	}
	if purchaseGrants(nil) != nil {
		t.Fatalf("a missing snapshot produced grants")
	}
}

// TestEntitlementsSyncIsNeverOnThePathToASession states the rule that decides
// how a commerce outage is felt: signing in still works.
func TestEntitlementsSyncIsNeverOnThePathToASession(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)

	handler := &CustomerOIDCHandler{entitlements: accounts.NewClient(failing.URL, failing.Client())}
	if _, err := handler.syncEntitlementsFromAccounts(t.Context(), "account-1", "at_token"); err == nil {
		t.Fatalf("a failing entitlements service reported success")
	}

	// And with no entitlements source at all — the state this ships in, before
	// the Accounts side provisions Arena's service client — the read is a
	// no-op rather than an error a caller has to know to ignore.
	quiet := &CustomerOIDCHandler{}
	result, err := quiet.syncEntitlementsFromAccounts(t.Context(), "account-1", "at_token")
	if err != nil || result != nil {
		t.Fatalf("unconfigured sync = (%+v, %v), want a silent no-op", result, err)
	}
}
