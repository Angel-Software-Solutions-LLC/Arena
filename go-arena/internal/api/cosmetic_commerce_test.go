package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arena-server/internal/config"
	"arena-server/internal/db"
	"arena-server/internal/game"
	"arena-server/internal/security"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	stripe "github.com/stripe/stripe-go/v86"
)

type fakeCosmeticCommerceStore struct {
	order               *db.CosmeticOrder
	getOrder            *db.CosmeticOrder
	orders              []db.CosmeticOrder
	result              *db.CosmeticPaymentEventResult
	createErr           error
	reserveReused       bool
	attachErr           error
	processErr          error
	listErr             error
	getErr              error
	createdAccount      string
	createdPack         string
	createdQty          int
	createdPresentation CosmeticCheckoutPresentation
	attachedID          string
	attachedCS          string
	failedID            string
	failedMessage       string
	processed           db.CosmeticPaymentEventInput
	listAccount         string
	getAccount          string
	getID               string
	adminQuery          string
	adminStatus         string
	listLimit           int
	equipped            map[string]map[string]string
	equippedErr         error
	equippedBots        []string
	equippedBatchCalls  int
}

func (s *fakeCosmeticCommerceStore) ReserveOrder(_ context.Context, accountID, packID string, quantity int, presentation CosmeticCheckoutPresentation) (*db.CosmeticOrder, bool, error) {
	s.createdAccount, s.createdPack, s.createdQty, s.createdPresentation = accountID, packID, quantity, presentation
	return s.order, !s.reserveReused, s.createErr
}

func (s *fakeCosmeticCommerceStore) AttachCheckout(_ context.Context, accountID, orderID, checkoutSessionID string) (*db.CosmeticOrder, error) {
	s.createdAccount, s.attachedID, s.attachedCS = accountID, orderID, checkoutSessionID
	return s.order, s.attachErr
}

func (s *fakeCosmeticCommerceStore) MarkCheckoutFailed(_ context.Context, accountID, orderID, message string) (*db.CosmeticOrder, error) {
	s.createdAccount, s.failedID, s.failedMessage = accountID, orderID, message
	return s.order, nil
}

func (s *fakeCosmeticCommerceStore) ProcessEvent(_ context.Context, event db.CosmeticPaymentEventInput) (*db.CosmeticPaymentEventResult, error) {
	s.processed = event
	return s.result, s.processErr
}

func (s *fakeCosmeticCommerceStore) ListCustomerOrders(_ context.Context, accountID string, limit int) ([]db.CosmeticOrder, error) {
	s.listAccount, s.listLimit = accountID, limit
	return s.orders, s.listErr
}

func (s *fakeCosmeticCommerceStore) GetCustomerOrder(_ context.Context, accountID, orderID string) (*db.CosmeticOrder, error) {
	s.getAccount, s.getID = accountID, orderID
	if s.getOrder != nil {
		return s.getOrder, s.getErr
	}
	return s.order, s.getErr
}

func (s *fakeCosmeticCommerceStore) ListAdminOrders(_ context.Context, query, status string, limit int) ([]db.CosmeticOrder, error) {
	s.adminQuery, s.adminStatus, s.listLimit = query, status, limit
	return s.orders, s.listErr
}

func (s *fakeCosmeticCommerceStore) EquippedForBots(_ context.Context, botIDs []string) (map[string]map[string]string, error) {
	s.equippedBatchCalls++
	s.equippedBots = append([]string(nil), botIDs...)
	if s.equippedErr != nil {
		return nil, s.equippedErr
	}
	result := make(map[string]map[string]string, len(botIDs))
	for _, botID := range botIDs {
		result[botID] = make(map[string]string)
		for slot, assetKey := range s.equipped[botID] {
			result[botID][slot] = assetKey
		}
	}
	return result, nil
}

type fakeCosmeticPaymentProvider struct {
	request             CosmeticCheckoutRequest
	subscriptionRequest CosmeticSubscriptionCheckoutRequest
	portalCustomer      string
	session             *CosmeticCheckoutSession
	retrievedSession    *CosmeticCheckoutSession
	retrievedState      *CosmeticSubscriptionProviderState
	portal              *CosmeticPortalSession
	createErr           error
	retrieveSessionErr  error
	retrieveStateErr    error
	deadline            time.Time
	event               *CosmeticPaymentEvent
	parseErr            error
	payload             []byte
	signature           string
	retrievedSessionID  string
	retrievedStateID    string
}

func (p *fakeCosmeticPaymentProvider) CreateCheckoutSession(ctx context.Context, request CosmeticCheckoutRequest) (*CosmeticCheckoutSession, error) {
	p.request = request
	p.deadline, _ = ctx.Deadline()
	return p.session, p.createErr
}

func (p *fakeCosmeticPaymentProvider) ParseWebhook(payload []byte, signature string) (*CosmeticPaymentEvent, error) {
	p.payload = append([]byte(nil), payload...)
	p.signature = signature
	return p.event, p.parseErr
}

func (p *fakeCosmeticPaymentProvider) CreateSubscriptionCheckoutSession(ctx context.Context, request CosmeticSubscriptionCheckoutRequest) (*CosmeticCheckoutSession, error) {
	p.subscriptionRequest = request
	p.deadline, _ = ctx.Deadline()
	return p.session, p.createErr
}

func (p *fakeCosmeticPaymentProvider) RetrieveSubscriptionCheckoutSession(_ context.Context, checkoutSessionID string) (*CosmeticCheckoutSession, error) {
	p.retrievedSessionID = checkoutSessionID
	return p.retrievedSession, p.retrieveSessionErr
}

func (p *fakeCosmeticPaymentProvider) RetrieveCheckoutSession(_ context.Context, checkoutSessionID string) (*CosmeticCheckoutSession, error) {
	p.retrievedSessionID = checkoutSessionID
	return p.retrievedSession, p.retrieveSessionErr
}

func (p *fakeCosmeticPaymentProvider) RetrieveCosmeticSubscription(_ context.Context, providerSubscriptionID string) (*CosmeticSubscriptionProviderState, error) {
	p.retrievedStateID = providerSubscriptionID
	return p.retrievedState, p.retrieveStateErr
}

func (p *fakeCosmeticPaymentProvider) CreateBillingPortalSession(_ context.Context, customerID string) (*CosmeticPortalSession, error) {
	p.portalCustomer = customerID
	return p.portal, p.createErr
}

type fakeCosmeticSubscriptionStore struct {
	subscription          *db.CosmeticSubscription
	createResults         []*db.CosmeticSubscription
	reserveCreatedResults []bool
	result                *db.CosmeticSubscriptionEventResult
	createErr             error
	reserveReused         bool
	attachErr             error
	processErr            error
	createdFor            string
	createCalls           int
	reservedPresentation  CosmeticCheckoutPresentation
	attachedID            string
	attachedCS            string
	expiredID             string
	expiredCS             string
	processed             db.CosmeticSubscriptionEventInput
	processedEvents       []db.CosmeticSubscriptionEventInput
}

func (s *fakeCosmeticSubscriptionStore) Reserve(_ context.Context, accountID string, presentation CosmeticCheckoutPresentation) (*db.CosmeticSubscription, bool, error) {
	s.createdFor = accountID
	s.reservedPresentation = presentation
	s.createCalls++
	created := !s.reserveReused
	if len(s.createResults) > 0 {
		index := s.createCalls - 1
		if index >= len(s.createResults) {
			index = len(s.createResults) - 1
		}
		if index < len(s.reserveCreatedResults) {
			created = s.reserveCreatedResults[index]
		}
		return s.createResults[index], created, s.createErr
	}
	return s.subscription, created, s.createErr
}

func (s *fakeCosmeticSubscriptionStore) Attach(_ context.Context, accountID, subscriptionID, checkoutSessionID string) (*db.CosmeticSubscription, error) {
	s.createdFor, s.attachedID, s.attachedCS = accountID, subscriptionID, checkoutSessionID
	return s.subscription, s.attachErr
}

func (s *fakeCosmeticSubscriptionStore) ExpireCheckout(_ context.Context, accountID, subscriptionID, checkoutSessionID string) (*db.CosmeticSubscription, error) {
	s.createdFor, s.expiredID, s.expiredCS = accountID, subscriptionID, checkoutSessionID
	return s.subscription, nil
}

func (s *fakeCosmeticSubscriptionStore) Process(_ context.Context, event db.CosmeticSubscriptionEventInput) (*db.CosmeticSubscriptionEventResult, error) {
	s.processed = event
	s.processedEvents = append(s.processedEvents, event)
	return s.result, s.processErr
}

func (s *fakeCosmeticSubscriptionStore) Current(_ context.Context, _ string) (*db.CosmeticSubscription, error) {
	return s.subscription, s.createErr
}

func commerceTestOrder() *db.CosmeticOrder {
	return &db.CosmeticOrder{
		ID: "order-1", AccountID: "account-1", AccountEmail: "owner@example.com",
		PackID: "arena-set-003-ember-vanguard-pack", PackName: "Ember Vanguard Set",
		UnitPriceCents: 199, Quantity: 2, ExpectedSubtotalCents: 398, Currency: "USD",
		Status: db.CosmeticOrderStatusCreated, CheckoutPresentation: db.CosmeticCheckoutPresentationEmbedded,
		Items: []db.CosmeticOrderItem{{ID: "skin-1"}, {ID: "weapon-1"}, {ID: "attachment-1"}},
	}
}

func commerceCustomerRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	return req.WithContext(withCustomerSession(req.Context(), &CustomerSession{AccountID: "account-1", Email: "owner@example.com"}))
}

func TestCosmeticCommerceCheckoutUsesOnlyServerOrderSnapshot(t *testing.T) {
	order := commerceTestOrder()
	store := &fakeCosmeticCommerceStore{order: order}
	provider := &fakeCosmeticPaymentProvider{session: &CosmeticCheckoutSession{
		ID: "cs_test_1", Presentation: CosmeticCheckoutPresentationEmbedded,
		ClientSecret: "cs_test_1_secret_browser", ExpiresAt: time.Unix(1_900_000_000, 0).UTC(),
	}}
	handler := newCosmeticCommerceHandlerWithStore(store, provider, true)
	recorder := httptest.NewRecorder()
	handler.Checkout(recorder, commerceCustomerRequest(http.MethodPost, "/api/v1/account/cosmetics/checkout", `{
		"pack_id":"arena-set-003-ember-vanguard-pack", "quantity":2
	}`))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if store.createdAccount != "account-1" || store.createdPack != order.PackID || store.createdQty != 2 ||
		store.attachedID != order.ID || store.attachedCS != provider.session.ID {
		t.Fatalf("store calls create=%q/%q/%d attach=%q/%q", store.createdAccount, store.createdPack, store.createdQty, store.attachedID, store.attachedCS)
	}
	request := provider.request
	if request.OrderID != order.ID || request.AccountID != order.AccountID || request.CustomerEmail != order.AccountEmail ||
		request.PackID != order.PackID || request.PackName != order.PackName || request.UnitAmount != order.UnitPriceCents ||
		request.Currency != order.Currency || request.Quantity != int64(order.Quantity) ||
		request.Presentation != CosmeticCheckoutPresentationEmbedded {
		t.Fatalf("provider received non-snapshot checkout data: %+v", request)
	}
	if !strings.Contains(recorder.Body.String(), `"order_id":"order-1"`) ||
		!strings.Contains(recorder.Body.String(), `"client_secret":"cs_test_1_secret_browser"`) ||
		!strings.Contains(recorder.Body.String(), `"presentation":"embedded"`) {
		t.Fatalf("checkout response=%s", recorder.Body.String())
	}
	remaining := time.Until(provider.deadline)
	if provider.deadline.IsZero() || remaining < 14*time.Second || remaining > cosmeticCheckoutTimeout {
		t.Fatalf("provider deadline remaining=%v, want a bounded %v timeout", remaining, cosmeticCheckoutTimeout)
	}
}

func TestCosmeticCommerceCheckoutUsesReservedPresentationInsteadOfRetryRequest(t *testing.T) {
	order := commerceTestOrder()
	order.Status = db.CosmeticOrderStatusPaymentFailed
	order.CheckoutPresentation = db.CosmeticCheckoutPresentationHosted
	store := &fakeCosmeticCommerceStore{order: order, reserveReused: true}
	provider := &fakeCosmeticPaymentProvider{session: &CosmeticCheckoutSession{
		ID: "cs_reserved_hosted", Presentation: CosmeticCheckoutPresentationHosted,
		URL: "https://checkout.stripe.com/c/pay/cs_reserved_hosted",
	}}
	recorder := httptest.NewRecorder()
	newCosmeticCommerceHandlerWithStore(store, provider, true).Checkout(
		recorder,
		commerceCustomerRequest(http.MethodPost, "/", `{
			"pack_id":"arena-set-003-ember-vanguard-pack", "quantity":2, "presentation":"embedded"
		}`),
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if provider.request.OrderID != order.ID || provider.request.Presentation != CosmeticCheckoutPresentationHosted {
		t.Fatalf("provider request = %+v, want same order with persisted hosted presentation", provider.request)
	}
	if !strings.Contains(recorder.Body.String(), `"resumed":true`) ||
		!strings.Contains(recorder.Body.String(), `"presentation":"hosted"`) {
		t.Fatalf("retry response=%s", recorder.Body.String())
	}
}

func TestCosmeticSubscriptionCheckoutAndPortalUseServerAccountSnapshot(t *testing.T) {
	subscription := &db.CosmeticSubscription{
		ID: "subscription-record", AccountID: "account-1", AccountEmail: "owner@example.com",
		Status: db.CosmeticSubscriptionStatusCreated, CheckoutPresentation: db.CosmeticCheckoutPresentationEmbedded,
		PriceCents: 1999, Currency: "USD", Interval: "month", CustomerID: "cus_owner", CanManage: true,
	}
	store := &fakeCosmeticSubscriptionStore{subscription: subscription}
	provider := &fakeCosmeticPaymentProvider{
		session: &CosmeticCheckoutSession{ID: "cs_subscription", Presentation: CosmeticCheckoutPresentationEmbedded, ClientSecret: "cs_subscription_secret_browser"},
		portal:  &CosmeticPortalSession{URL: "https://billing.stripe.com/p/session/test"},
	}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(
		&fakeCosmeticCommerceStore{}, store, provider, true,
	)

	checkout := httptest.NewRecorder()
	handler.SubscriptionCheckout(checkout, commerceCustomerRequest(http.MethodPost, "/api/v1/account/cosmetics/subscription/checkout", ""))
	if checkout.Code != http.StatusCreated || store.createdFor != "account-1" || store.attachedID != subscription.ID ||
		store.attachedCS != provider.session.ID {
		t.Fatalf("subscription checkout status=%d store=%+v body=%s", checkout.Code, store, checkout.Body.String())
	}
	if provider.subscriptionRequest.SubscriptionID != subscription.ID || provider.subscriptionRequest.AccountID != "account-1" ||
		provider.subscriptionRequest.CustomerEmail != "owner@example.com" ||
		provider.subscriptionRequest.CustomerID != "cus_owner" ||
		provider.subscriptionRequest.Presentation != CosmeticCheckoutPresentationEmbedded {
		t.Fatalf("subscription provider request = %+v", provider.subscriptionRequest)
	}
	var checkoutBody struct {
		SubscriptionID string `json:"subscription_id"`
		ClientSecret   string `json:"client_secret"`
		Presentation   string `json:"presentation"`
	}
	if err := json.Unmarshal(checkout.Body.Bytes(), &checkoutBody); err != nil || checkoutBody.SubscriptionID != subscription.ID ||
		checkoutBody.ClientSecret != provider.session.ClientSecret || checkoutBody.Presentation != "embedded" {
		t.Fatalf("subscription checkout response = (%+v, %v)", checkoutBody, err)
	}

	portal := httptest.NewRecorder()
	handler.SubscriptionPortal(portal, commerceCustomerRequest(http.MethodPost, "/api/v1/account/cosmetics/subscription/portal", ""))
	if portal.Code != http.StatusOK || provider.portalCustomer != "cus_owner" ||
		!strings.Contains(portal.Body.String(), `"portal_url":"https://billing.stripe.com/p/session/test"`) {
		t.Fatalf("portal status=%d customer=%q body=%s", portal.Code, provider.portalCustomer, portal.Body.String())
	}
}

func TestCosmeticSubscriptionCheckoutUsesPersistedPresentationAcrossRetry(t *testing.T) {
	subscription := &db.CosmeticSubscription{
		ID: "subscription-retry", AccountID: "account-1", AccountEmail: "owner@example.com",
		Status: db.CosmeticSubscriptionStatusCreated, PriceCents: db.CosmeticSubscriptionPriceCents,
		Currency: db.CosmeticSubscriptionCurrency, Interval: db.CosmeticSubscriptionInterval,
		CheckoutPresentation: db.CosmeticCheckoutPresentationHosted,
	}
	store := &fakeCosmeticSubscriptionStore{subscription: subscription, reserveReused: true}
	provider := &fakeCosmeticPaymentProvider{session: &CosmeticCheckoutSession{
		ID: "cs_subscription_retry", Presentation: CosmeticCheckoutPresentationHosted,
		URL: "https://checkout.stripe.com/c/pay/cs_subscription_retry",
	}}
	recorder := httptest.NewRecorder()
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(&fakeCosmeticCommerceStore{}, store, provider, true)
	handler.SubscriptionCheckout(recorder, commerceCustomerRequest(
		http.MethodPost, "/api/v1/account/cosmetics/subscription/checkout", `{"presentation":"embedded"}`,
	))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if provider.subscriptionRequest.SubscriptionID != subscription.ID ||
		provider.subscriptionRequest.Presentation != CosmeticCheckoutPresentationHosted {
		t.Fatalf("subscription provider request = %+v", provider.subscriptionRequest)
	}
	if !strings.Contains(recorder.Body.String(), `"resumed":true`) ||
		!strings.Contains(recorder.Body.String(), `"presentation":"hosted"`) {
		t.Fatalf("subscription retry response=%s", recorder.Body.String())
	}
}

func TestCosmeticSubscriptionPortalRemainsAvailableWhenNewSalesArePaused(t *testing.T) {
	subscription := &db.CosmeticSubscription{
		ID: "subscription-record", AccountID: "account-1", CustomerID: "cus_owner",
		Status: db.CosmeticSubscriptionStatusActive, CanManage: true,
	}
	store := &fakeCosmeticSubscriptionStore{subscription: subscription}
	provider := &fakeCosmeticPaymentProvider{portal: &CosmeticPortalSession{URL: "https://billing.stripe.com/p/session/sales_pause"}}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(&fakeCosmeticCommerceStore{}, store, provider, false)
	checkout := httptest.NewRecorder()
	handler.SubscriptionCheckout(checkout, commerceCustomerRequest(http.MethodPost, "/", ""))
	if checkout.Code != http.StatusServiceUnavailable || store.createCalls != 0 {
		t.Fatalf("sales-pause checkout status=%d creates=%d body=%s", checkout.Code, store.createCalls, checkout.Body.String())
	}
	recorder := httptest.NewRecorder()
	handler.SubscriptionPortal(recorder, commerceCustomerRequest(http.MethodPost, "/", ""))
	if recorder.Code != http.StatusOK || provider.portalCustomer != "cus_owner" {
		t.Fatalf("sales-pause portal status=%d customer=%q body=%s", recorder.Code, provider.portalCustomer, recorder.Body.String())
	}
}

func pendingCommerceTestSubscription() *db.CosmeticSubscription {
	return &db.CosmeticSubscription{
		ID: "subscription-pending", AccountID: "account-1", AccountEmail: "owner@example.com",
		Status: db.CosmeticSubscriptionStatusCheckoutPending, CheckoutSessionID: "cs_pending",
		CheckoutPresentation: db.CosmeticCheckoutPresentationHosted,
		PriceCents:           db.CosmeticSubscriptionPriceCents, Currency: db.CosmeticSubscriptionCurrency,
		Interval: db.CosmeticSubscriptionInterval,
	}
}

func TestCosmeticSubscriptionCheckoutResumesAuthoritativeOpenSession(t *testing.T) {
	subscription := pendingCommerceTestSubscription()
	store := &fakeCosmeticSubscriptionStore{subscription: subscription}
	provider := &fakeCosmeticPaymentProvider{retrievedSession: &CosmeticCheckoutSession{
		ID: subscription.CheckoutSessionID, URL: "https://checkout.stripe.com/c/pay/cs_pending",
		Presentation: CosmeticCheckoutPresentationHosted,
		Status:       CosmeticCheckoutSessionStatusOpen, ExpiresAt: time.Unix(1_900_000_000, 0).UTC(),
		Mode: "subscription", SubscriptionID: subscription.ID, AccountID: subscription.AccountID,
	}}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(&fakeCosmeticCommerceStore{}, store, provider, true)
	recorder := httptest.NewRecorder()
	handler.SubscriptionCheckout(recorder, commerceCustomerRequest(http.MethodPost, "/api/v1/account/cosmetics/subscription/checkout", ""))

	if recorder.Code != http.StatusOK || provider.retrievedSessionID != subscription.CheckoutSessionID ||
		provider.subscriptionRequest.SubscriptionID != "" || store.attachedID != "" || store.expiredID != "" {
		t.Fatalf("resume status=%d provider=%+v store=%+v body=%s", recorder.Code, provider, store, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"checkout_url":"https://checkout.stripe.com/c/pay/cs_pending"`) ||
		!strings.Contains(recorder.Body.String(), `"resumed":true`) {
		t.Fatalf("resume response=%s", recorder.Body.String())
	}
}

func TestCosmeticSubscriptionCheckoutReplacesOnlyAuthoritativelyExpiredSession(t *testing.T) {
	pending := pendingCommerceTestSubscription()
	replacement := &db.CosmeticSubscription{
		ID: "subscription-replacement", AccountID: pending.AccountID, AccountEmail: pending.AccountEmail,
		Status: db.CosmeticSubscriptionStatusCreated, PriceCents: db.CosmeticSubscriptionPriceCents,
		Currency: db.CosmeticSubscriptionCurrency, Interval: db.CosmeticSubscriptionInterval,
		CheckoutPresentation: db.CosmeticCheckoutPresentationEmbedded,
	}
	store := &fakeCosmeticSubscriptionStore{
		subscription: pending, createResults: []*db.CosmeticSubscription{pending, replacement},
	}
	provider := &fakeCosmeticPaymentProvider{
		retrievedSession: &CosmeticCheckoutSession{
			ID: pending.CheckoutSessionID, Status: CosmeticCheckoutSessionStatusExpired,
			Presentation: CosmeticCheckoutPresentationHosted,
			Mode:         "subscription", SubscriptionID: pending.ID, AccountID: pending.AccountID,
		},
		session: &CosmeticCheckoutSession{
			ID: "cs_replacement", Presentation: CosmeticCheckoutPresentationEmbedded,
			ClientSecret: "cs_replacement_secret_browser",
			Status:       CosmeticCheckoutSessionStatusOpen,
		},
	}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(&fakeCosmeticCommerceStore{}, store, provider, true)
	recorder := httptest.NewRecorder()
	handler.SubscriptionCheckout(recorder, commerceCustomerRequest(http.MethodPost, "/api/v1/account/cosmetics/subscription/checkout", ""))

	if recorder.Code != http.StatusCreated || store.createCalls != 2 || store.expiredID != pending.ID ||
		store.expiredCS != pending.CheckoutSessionID || provider.subscriptionRequest.SubscriptionID != replacement.ID ||
		store.attachedID != replacement.ID || store.attachedCS != "cs_replacement" {
		t.Fatalf("replacement status=%d provider=%+v store=%+v body=%s", recorder.Code, provider, store, recorder.Body.String())
	}
}

func TestCosmeticSubscriptionCheckoutDoesNotReplaceCompletedSession(t *testing.T) {
	pending := pendingCommerceTestSubscription()
	store := &fakeCosmeticSubscriptionStore{subscription: pending}
	provider := &fakeCosmeticPaymentProvider{retrievedSession: &CosmeticCheckoutSession{
		ID: pending.CheckoutSessionID, Status: CosmeticCheckoutSessionStatusComplete,
		Presentation: CosmeticCheckoutPresentationHosted,
		Mode:         "subscription", SubscriptionID: pending.ID, AccountID: pending.AccountID,
	}}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(&fakeCosmeticCommerceStore{}, store, provider, true)
	recorder := httptest.NewRecorder()
	handler.SubscriptionCheckout(recorder, commerceCustomerRequest(http.MethodPost, "/api/v1/account/cosmetics/subscription/checkout", ""))

	if recorder.Code != http.StatusConflict || store.expiredID != "" || provider.subscriptionRequest.SubscriptionID != "" || store.attachedID != "" {
		t.Fatalf("completed status=%d provider=%+v store=%+v body=%s", recorder.Code, provider, store, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"CHECKOUT_COMPLETE"`) {
		t.Fatalf("completed response=%s", recorder.Body.String())
	}
}

func TestCosmeticSubscriptionCheckoutRejectsRetrievedSessionForAnotherAccount(t *testing.T) {
	pending := pendingCommerceTestSubscription()
	store := &fakeCosmeticSubscriptionStore{subscription: pending}
	provider := &fakeCosmeticPaymentProvider{retrievedSession: &CosmeticCheckoutSession{
		ID: pending.CheckoutSessionID, Status: CosmeticCheckoutSessionStatusOpen,
		URL: "https://checkout.stripe.com/c/pay/cs_pending", Mode: "subscription", Presentation: CosmeticCheckoutPresentationHosted,
		SubscriptionID: pending.ID, AccountID: "another-account",
	}}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(&fakeCosmeticCommerceStore{}, store, provider, true)
	recorder := httptest.NewRecorder()
	handler.SubscriptionCheckout(recorder, commerceCustomerRequest(http.MethodPost, "/", ""))
	if recorder.Code != http.StatusBadGateway || store.expiredID != "" || provider.subscriptionRequest.SubscriptionID != "" {
		t.Fatalf("mismatched resume status=%d provider=%+v store=%+v body=%s", recorder.Code, provider, store, recorder.Body.String())
	}
}

func TestCosmeticSubscriptionCheckoutKeepsAmbiguousProviderAndAttachFailuresRetryable(t *testing.T) {
	for _, test := range []struct {
		name       string
		provider   *fakeCosmeticPaymentProvider
		attachErr  error
		wantStatus int
	}{
		{name: "provider timeout", provider: &fakeCosmeticPaymentProvider{createErr: errors.New("timeout")}, wantStatus: http.StatusBadGateway},
		{name: "local attach failure", provider: &fakeCosmeticPaymentProvider{session: &CosmeticCheckoutSession{
			ID: "cs_ambiguous", Presentation: CosmeticCheckoutPresentationEmbedded,
			ClientSecret: "cs_ambiguous_secret_browser",
		}}, attachErr: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			subscription := &db.CosmeticSubscription{
				ID: "subscription-retryable", AccountID: "account-1", AccountEmail: "owner@example.com",
				Status: db.CosmeticSubscriptionStatusCreated, PriceCents: db.CosmeticSubscriptionPriceCents,
				Currency: db.CosmeticSubscriptionCurrency, Interval: db.CosmeticSubscriptionInterval,
				CheckoutPresentation: db.CosmeticCheckoutPresentationEmbedded,
			}
			store := &fakeCosmeticSubscriptionStore{subscription: subscription, attachErr: test.attachErr}
			handler := newCosmeticCommerceHandlerWithSubscriptionStore(&fakeCosmeticCommerceStore{}, store, test.provider, true)
			recorder := httptest.NewRecorder()
			handler.SubscriptionCheckout(recorder, commerceCustomerRequest(http.MethodPost, "/", ""))
			if recorder.Code != test.wantStatus || store.expiredID != "" {
				t.Fatalf("status=%d expired=%q body=%s", recorder.Code, store.expiredID, recorder.Body.String())
			}
			if subscription.Status != db.CosmeticSubscriptionStatusCreated {
				t.Fatalf("ambiguous failure mutated local status to %q", subscription.Status)
			}
		})
	}
}

func TestCosmeticSubscriptionCheckoutMapsLegacyAPIKeyOverflowToCleanupConflict(t *testing.T) {
	store := &fakeCosmeticSubscriptionStore{createErr: db.ErrCustomerAPIKeyLimit}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(
		&fakeCosmeticCommerceStore{}, store, &fakeCosmeticPaymentProvider{}, true,
	)
	recorder := httptest.NewRecorder()
	handler.SubscriptionCheckout(recorder, commerceCustomerRequest(
		http.MethodPost, "/api/v1/account/cosmetics/subscription/checkout", "",
	))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Error  string `json:"error"`
		Code   string `json:"code"`
		Action string `json:"action"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode conflict response: %v", err)
	}
	if response.Code != "API_KEY_LIMIT" || response.Limit != db.CosmeticSubscriptionMaxAPIKeys ||
		!strings.Contains(strings.ToLower(response.Action), "deactivate") ||
		!strings.Contains(strings.ToLower(response.Error), "5") {
		t.Fatalf("API key cleanup response = %+v", response)
	}
}

func TestCosmeticCommerceWebhookDispatchesSubscriptionLifecycle(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	periodEnd := now.Add(30 * 24 * time.Hour)
	subscription := &db.CosmeticSubscription{
		ID: "subscription-record", AccountID: "account-1", Status: db.CosmeticSubscriptionStatusActive,
		HasAccess: true, PriceCents: 1999, Currency: "USD", Interval: "month",
	}
	subscriptionStore := &fakeCosmeticSubscriptionStore{result: &db.CosmeticSubscriptionEventResult{
		Subscription: subscription, Applied: true, LicensesCreated: 300,
	}}
	provider := &fakeCosmeticPaymentProvider{event: &CosmeticPaymentEvent{
		ID: "evt_subscription", Kind: CosmeticPaymentKindSubscription, Type: CosmeticPaymentEventSubscriptionUpdated,
		PayloadSHA256: strings.Repeat("f", 64), SubscriptionID: subscription.ID, AccountID: "account-1",
		ProviderSubscriptionID: "sub_provider", CustomerID: "cus_provider", SubscriptionStatus: "active",
		CancelAtPeriodEnd: true, CurrentPeriodEnd: &periodEnd, ProviderCreatedAt: now,
	}, retrievedState: &CosmeticSubscriptionProviderState{
		ID: "sub_provider", CustomerID: "cus_provider", Status: db.CosmeticSubscriptionStatusActive,
		CancelAtPeriodEnd: true, CurrentPeriodEnd: &periodEnd,
	}}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(
		&fakeCosmeticCommerceStore{}, subscriptionStore, provider, true,
	)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/cosmetics/webhooks/stripe", strings.NewReader(`{"object":"event"}`))
	request.Header.Set("Stripe-Signature", "signed")
	handler.StripeWebhook(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"licenses_created":300`) {
		t.Fatalf("subscription webhook status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	input := subscriptionStore.processed
	if input.EventID != "evt_subscription" || input.EventType != db.CosmeticStripeSubscriptionUpdated ||
		input.SubscriptionID != subscription.ID || input.AccountID != "account-1" ||
		input.ProviderSubscriptionID != "sub_provider" || input.CustomerID != "cus_provider" ||
		input.Status != "active" || !input.CancelAtPeriodEnd || input.CurrentPeriodEnd == nil ||
		!input.CurrentPeriodEnd.Equal(periodEnd) || !input.ProviderCreatedAt.Equal(now) || input.ProviderStateObservedAt.IsZero() ||
		provider.retrievedStateID != "sub_provider" {
		t.Fatalf("subscription event input = %+v", input)
	}
}

func TestCosmeticSubscriptionWebhookReconcilesReverseSameSecondEventsFromAuthoritativeState(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	subscription := &db.CosmeticSubscription{
		ID: "subscription-record", AccountID: "account-1", Status: db.CosmeticSubscriptionStatusPastDue,
		PriceCents: 1999, Currency: "USD", Interval: "month", CustomerID: "cus_provider", CanManage: true,
	}
	store := &fakeCosmeticSubscriptionStore{result: &db.CosmeticSubscriptionEventResult{Subscription: subscription, Applied: true}}
	provider := &fakeCosmeticPaymentProvider{retrievedState: &CosmeticSubscriptionProviderState{
		ID: "sub_provider", CustomerID: "cus_provider", Status: db.CosmeticSubscriptionStatusPastDue,
	}}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(&fakeCosmeticCommerceStore{}, store, provider, true)

	for index, payloadStatus := range []string{db.CosmeticSubscriptionStatusPastDue, db.CosmeticSubscriptionStatusActive} {
		provider.event = &CosmeticPaymentEvent{
			ID: fmt.Sprintf("evt_same_second_%d", index), Kind: CosmeticPaymentKindSubscription,
			Type: CosmeticPaymentEventSubscriptionUpdated, PayloadSHA256: strings.Repeat(fmt.Sprint(index+1), 64),
			SubscriptionID: subscription.ID, AccountID: subscription.AccountID,
			ProviderSubscriptionID: "sub_provider", CustomerID: "cus_payload",
			SubscriptionStatus: payloadStatus, ProviderCreatedAt: created,
		}
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/cosmetics/webhooks/stripe", strings.NewReader(`{"object":"event"}`))
		handler.StripeWebhook(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("reverse event %d status=%d body=%s", index, recorder.Code, recorder.Body.String())
		}
	}
	if len(store.processedEvents) != 2 {
		t.Fatalf("processed events = %d, want 2", len(store.processedEvents))
	}
	for index, input := range store.processedEvents {
		if input.Status != db.CosmeticSubscriptionStatusPastDue || input.CustomerID != "cus_provider" ||
			input.EventID != fmt.Sprintf("evt_same_second_%d", index) || !input.ProviderCreatedAt.Equal(created) ||
			input.ProviderStateObservedAt.IsZero() {
			t.Fatalf("reconciled reverse event %d = %+v", index, input)
		}
	}
	if !store.processedEvents[1].ProviderStateObservedAt.After(store.processedEvents[0].ProviderStateObservedAt) {
		t.Fatalf("observation timestamps are not ordered: %v then %v",
			store.processedEvents[0].ProviderStateObservedAt, store.processedEvents[1].ProviderStateObservedAt)
	}
	if delta := store.processedEvents[1].ProviderStateObservedAt.Sub(store.processedEvents[0].ProviderStateObservedAt); delta < time.Microsecond {
		t.Fatalf("observation delta %v is below PostgreSQL timestamp precision", delta)
	}
}

func TestCosmeticSubscriptionWebhookTerminalPayloadDoesNotRequireStripeRetrieval(t *testing.T) {
	subscription := &db.CosmeticSubscription{
		ID: "subscription-record", AccountID: "account-1", Status: db.CosmeticSubscriptionStatusCanceled,
		Terminal: true, PriceCents: 1999, Currency: "USD", Interval: "month",
	}
	store := &fakeCosmeticSubscriptionStore{result: &db.CosmeticSubscriptionEventResult{Subscription: subscription, Applied: true}}
	provider := &fakeCosmeticPaymentProvider{
		retrieveStateErr: errors.New("Stripe API unavailable"),
		event: &CosmeticPaymentEvent{
			ID: "evt_deleted_offline", Kind: CosmeticPaymentKindSubscription, Type: CosmeticPaymentEventSubscriptionDeleted,
			PayloadSHA256: strings.Repeat("d", 64), SubscriptionID: subscription.ID, AccountID: subscription.AccountID,
			ProviderSubscriptionID: "sub_provider", CustomerID: "cus_provider",
			SubscriptionStatus: db.CosmeticSubscriptionStatusCanceled, ProviderCreatedAt: time.Now().UTC(), Terminal: true,
		},
	}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(&fakeCosmeticCommerceStore{}, store, provider, true)
	recorder := httptest.NewRecorder()
	handler.StripeWebhook(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusOK || provider.retrievedStateID != "" || len(store.processedEvents) != 1 {
		t.Fatalf("terminal offline status=%d retrieved=%q processed=%d body=%s",
			recorder.Code, provider.retrievedStateID, len(store.processedEvents), recorder.Body.String())
	}
}

func TestCosmeticSubscriptionWebhookNonterminalRetrievalFailureRetriesWithoutMutation(t *testing.T) {
	store := &fakeCosmeticSubscriptionStore{}
	provider := &fakeCosmeticPaymentProvider{
		retrieveStateErr: errors.New("Stripe API unavailable"),
		event: &CosmeticPaymentEvent{
			ID: "evt_active_offline", Kind: CosmeticPaymentKindSubscription, Type: CosmeticPaymentEventSubscriptionUpdated,
			PayloadSHA256: strings.Repeat("a", 64), SubscriptionID: "subscription-record", AccountID: "account-1",
			ProviderSubscriptionID: "sub_provider", SubscriptionStatus: db.CosmeticSubscriptionStatusActive,
			ProviderCreatedAt: time.Now().UTC(),
		},
	}
	handler := newCosmeticCommerceHandlerWithSubscriptionStore(&fakeCosmeticCommerceStore{}, store, provider, true)
	recorder := httptest.NewRecorder()
	handler.StripeWebhook(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
	if recorder.Code < 500 || recorder.Code > 599 || len(store.processedEvents) != 0 || provider.retrievedStateID != "sub_provider" {
		t.Fatalf("nonterminal offline status=%d retrieved=%q processed=%d body=%s",
			recorder.Code, provider.retrievedStateID, len(store.processedEvents), recorder.Body.String())
	}
}

func TestCosmeticCommerceCheckoutFailsClosed(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		newCosmeticCommerceHandlerWithStore(&fakeCosmeticCommerceStore{}, nil, false).Checkout(
			recorder, commerceCustomerRequest(http.MethodPost, "/", `{"pack_id":"pack","quantity":1}`),
		)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"pack_id":"pack","quantity":1,"price_cents":1}`},
		{name: "quantity zero", body: `{"pack_id":"pack","quantity":0}`},
		{name: "quantity high", body: `{"pack_id":"pack","quantity":11}`},
		{name: "invalid pack", body: `{"pack_id":"../pack","quantity":1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeCosmeticCommerceStore{order: commerceTestOrder()}
			recorder := httptest.NewRecorder()
			newCosmeticCommerceHandlerWithStore(store, &fakeCosmeticPaymentProvider{}, true).Checkout(
				recorder, commerceCustomerRequest(http.MethodPost, "/", test.body),
			)
			if recorder.Code != http.StatusBadRequest || store.createdAccount != "" {
				t.Fatalf("status=%d createAccount=%q body=%s", recorder.Code, store.createdAccount, recorder.Body.String())
			}
		})
	}

	t.Run("provider error records order failure", func(t *testing.T) {
		store := &fakeCosmeticCommerceStore{order: commerceTestOrder()}
		provider := &fakeCosmeticPaymentProvider{createErr: errors.New("provider down")}
		recorder := httptest.NewRecorder()
		newCosmeticCommerceHandlerWithStore(store, provider, true).Checkout(
			recorder, commerceCustomerRequest(http.MethodPost, "/", `{"pack_id":"arena-set-003-ember-vanguard-pack","quantity":2}`),
		)
		if recorder.Code != http.StatusBadGateway || store.failedID != "order-1" || store.failedMessage == "" {
			t.Fatalf("status=%d failed=%q/%q body=%s", recorder.Code, store.failedID, store.failedMessage, recorder.Body.String())
		}
	})

	for name, redirectURL := range map[string]string{
		"script scheme": "javascript:alert(1)",
		"plain HTTP":    "http://checkout.stripe.example/session",
		"URL userinfo":  "https://user:password@checkout.stripe.example/session",
	} {
		t.Run("unsafe provider redirect "+name, func(t *testing.T) {
			store := &fakeCosmeticCommerceStore{order: commerceTestOrder()}
			provider := &fakeCosmeticPaymentProvider{session: &CosmeticCheckoutSession{
				ID: "cs_bad", Presentation: CosmeticCheckoutPresentationHosted, URL: redirectURL,
			}}
			recorder := httptest.NewRecorder()
			newCosmeticCommerceHandlerWithStore(store, provider, true).Checkout(
				recorder, commerceCustomerRequest(http.MethodPost, "/", `{"pack_id":"arena-set-003-ember-vanguard-pack","quantity":2}`),
			)
			if recorder.Code != http.StatusBadGateway || store.attachedID != "" || store.failedID != "order-1" {
				t.Fatalf("status=%d attached=%q failed=%q body=%s", recorder.Code, store.attachedID, store.failedID, recorder.Body.String())
			}
		})
	}
}

func TestCosmeticCommerceWebhookVerifiesRawBodyAndMapsEvents(t *testing.T) {
	store := &fakeCosmeticCommerceStore{result: &db.CosmeticPaymentEventResult{Order: commerceTestOrder(), Applied: true, LicensesCreated: 6}}
	provider := &fakeCosmeticPaymentProvider{event: &CosmeticPaymentEvent{
		ID: "evt_paid", Type: CosmeticPaymentEventCheckoutCompleted, PayloadSHA256: strings.Repeat("a", 64),
		OrderID: "order-1", AccountID: "account-1", CheckoutSessionID: "cs_test_1", PaymentIntentID: "pi_test_1",
		AmountTotal: 418, Currency: "usd", PaymentStatus: "paid",
	}}
	handler := newCosmeticCommerceHandlerWithStore(store, provider, true)
	body := []byte(`{"id":"evt_paid","object":"event"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/cosmetics/webhooks/stripe", bytes.NewReader(body))
	request.Header.Set("Stripe-Signature", "t=1,v1=signed")
	handler.StripeWebhook(recorder, request)

	if recorder.Code != http.StatusOK || !bytes.Equal(provider.payload, body) || provider.signature != "t=1,v1=signed" {
		t.Fatalf("status=%d parsed=%q signature=%q body=%s", recorder.Code, provider.payload, provider.signature, recorder.Body.String())
	}
	input := store.processed
	if input.Provider != "stripe" || input.EventID != "evt_paid" || input.EventType != db.CosmeticStripeCheckoutCompleted ||
		input.PayloadHash != strings.Repeat("a", 64) || input.OrderID != "order-1" || input.AccountID != "account-1" ||
		input.CheckoutSessionID != "cs_test_1" || input.PaymentIntentID != "pi_test_1" || input.Currency != "USD" ||
		!input.Paid || input.AmountReceivedCents != 418 {
		t.Fatalf("processed input=%+v", input)
	}
}

func TestCosmeticCommerceTerminalReversalRefreshesConnectedAndWaitingBots(t *testing.T) {
	order := commerceTestOrder()
	order.Status = db.CosmeticOrderStatusDisputed
	store := &fakeCosmeticCommerceStore{
		result: &db.CosmeticPaymentEventResult{Order: order, Applied: true},
		equipped: map[string]map[string]string{
			"bot-active":  {},
			"bot-waiting": {"bot_skin": "starter_blue"},
		},
	}
	provider := &fakeCosmeticPaymentProvider{event: &CosmeticPaymentEvent{
		ID: "evt_dispute_refresh", Type: CosmeticPaymentEventDisputeCreated, PayloadSHA256: strings.Repeat("d", 64),
		OrderID: order.ID, AccountID: order.AccountID, PaymentIntentID: "pi_test_1", Currency: "usd",
	}}
	engine := game.NewGameEngine()
	engine.Bots["bot-active"] = &game.BotState{BotID: "bot-active", Cosmetics: map[string]string{"bot_skin": "refunded_skin"}}
	engine.WaitingBots["bot-waiting"] = &game.BotState{BotID: "bot-waiting", Cosmetics: map[string]string{"bot_skin": "refunded_skin"}}
	handler := newCosmeticCommerceHandlerWithStore(store, provider, true)
	handler.engine = engine

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/cosmetics/webhooks/stripe", strings.NewReader(`{"object":"event"}`))
	request.Header.Set("Stripe-Signature", "signed")
	handler.StripeWebhook(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"live_refreshed":2`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(engine.Bots["bot-active"].Cosmetics) != 0 {
		t.Fatalf("active bot retained reversed cosmetics: %+v", engine.Bots["bot-active"].Cosmetics)
	}
	if got := engine.WaitingBots["bot-waiting"].Cosmetics["bot_skin"]; got != "starter_blue" {
		t.Fatalf("waiting bot cosmetics = %q, want starter_blue", got)
	}
	if strings.Join(store.equippedBots, ",") != "bot-active,bot-waiting" {
		t.Fatalf("refreshed bot IDs = %v", store.equippedBots)
	}
	if store.equippedBatchCalls != 1 {
		t.Fatalf("loadout batch queries = %d, want 1", store.equippedBatchCalls)
	}
}

func TestCosmeticCommerceTerminalRefreshFailureReturnsRetryableFiveHundred(t *testing.T) {
	order := commerceTestOrder()
	order.Status = db.CosmeticOrderStatusRefunded
	store := &fakeCosmeticCommerceStore{
		result:      &db.CosmeticPaymentEventResult{Order: order, Duplicate: true},
		equippedErr: errors.New("temporary loadout read failure"),
		equipped: map[string]map[string]string{
			"bot-active": {},
		},
	}
	provider := &fakeCosmeticPaymentProvider{event: &CosmeticPaymentEvent{
		ID: "evt_refund_refresh_retry", Type: CosmeticPaymentEventRefundUpdated, PayloadSHA256: strings.Repeat("e", 64),
		OrderID: order.ID, AccountID: order.AccountID, PaymentIntentID: "pi_test_1", Currency: "usd",
		RefundID: "re_retry", RefundStatus: "succeeded", AmountRefunded: order.AmountReceivedCents,
	}}
	engine := game.NewGameEngine()
	engine.Bots["bot-active"] = &game.BotState{BotID: "bot-active", Cosmetics: map[string]string{"bot_skin": "refunded_skin"}}
	handler := newCosmeticCommerceHandlerWithStore(store, provider, true)
	handler.engine = engine

	deliver := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/cosmetics/webhooks/stripe", strings.NewReader(`{"object":"event"}`))
		request.Header.Set("Stripe-Signature", "signed")
		handler.StripeWebhook(recorder, request)
		return recorder
	}

	failed := deliver()
	if failed.Code != http.StatusInternalServerError || strings.Contains(failed.Body.String(), `"received":true`) {
		t.Fatalf("failed refresh status=%d body=%s", failed.Code, failed.Body.String())
	}
	if got := engine.Bots["bot-active"].Cosmetics["bot_skin"]; got != "refunded_skin" {
		t.Fatalf("failed refresh changed stale cache to %q", got)
	}

	store.equippedErr = nil
	retried := deliver()
	if retried.Code != http.StatusOK || !strings.Contains(retried.Body.String(), `"duplicate":true`) ||
		!strings.Contains(retried.Body.String(), `"live_refreshed":1`) {
		t.Fatalf("retry status=%d body=%s", retried.Code, retried.Body.String())
	}
	if len(engine.Bots["bot-active"].Cosmetics) != 0 {
		t.Fatalf("duplicate retry did not clear stale cache: %+v", engine.Bots["bot-active"].Cosmetics)
	}
	if store.equippedBatchCalls != 2 {
		t.Fatalf("loadout batch calls = %d, want one per delivery", store.equippedBatchCalls)
	}
}

func TestCosmeticCommerceWebhookAllowsMetadataFreeReversalByPaymentIntent(t *testing.T) {
	store := &fakeCosmeticCommerceStore{result: &db.CosmeticPaymentEventResult{Order: commerceTestOrder(), Applied: true}}
	provider := &fakeCosmeticPaymentProvider{event: &CosmeticPaymentEvent{
		ID: "evt_refund", Type: CosmeticPaymentEventRefundUpdated, PayloadSHA256: strings.Repeat("b", 64),
		PaymentIntentID: "pi_test_1", Currency: "usd", RefundID: "re_test_1", RefundStatus: "succeeded", AmountRefunded: 199,
	}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"object":"event"}`))
	request.Header.Set("Stripe-Signature", "signed")
	newCosmeticCommerceHandlerWithStore(store, provider, true).StripeWebhook(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if store.processed.OrderID != "" || store.processed.AccountID != "" || store.processed.PaymentIntentID != "pi_test_1" ||
		store.processed.EventType != db.CosmeticStripeRefundUpdated || store.processed.RefundAmountCents != 199 {
		t.Fatalf("metadata-free refund mapping=%+v", store.processed)
	}
}

func TestCosmeticCommerceWebhookKeepsRetryableOrderingFailuresOnFiveHundredPath(t *testing.T) {
	store := &fakeCosmeticCommerceStore{processErr: db.ErrCosmeticPaymentEventRetryable}
	provider := &fakeCosmeticPaymentProvider{event: &CosmeticPaymentEvent{
		ID: "evt_retryable", Type: CosmeticPaymentEventRefundUpdated, PayloadSHA256: strings.Repeat("c", 64),
		PaymentIntentID: "pi_test_1", Currency: "usd", RefundID: "re_test_1", RefundStatus: "succeeded", AmountRefunded: 199,
	}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/cosmetics/webhooks/stripe", strings.NewReader(`{"object":"event"}`))
	request.Header.Set("Stripe-Signature", "signed")
	newCosmeticCommerceHandlerWithStore(store, provider, true).StripeWebhook(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want retryable 5xx", recorder.Code, recorder.Body.String())
	}
}

func TestCosmeticCommerceWebhookAcknowledgesSignedUnsupportedEventsWithoutDatabaseWork(t *testing.T) {
	const secret = "whsec_unsupported"
	store := &fakeCosmeticCommerceStore{}
	provider := newStripeCosmeticPaymentProviderWithCreator(nil, []string{secret}, "", "", false)
	payload := stripeEventPayload(t, "evt_unsupported", stripe.EventType("customer.created"), map[string]interface{}{"id": "cus_test"})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/cosmetics/webhooks/stripe", bytes.NewReader(payload))
	request.Header.Set("Stripe-Signature", signedStripePayload(payload, secret, time.Now()))
	newCosmeticCommerceHandlerWithStore(store, provider, true).StripeWebhook(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"ignored":true`) {
		t.Fatalf("status=%d body=%s, want signed unsupported event acknowledged", recorder.Code, recorder.Body.String())
	}
	if store.processed.EventID != "" {
		t.Fatalf("unsupported event reached database store: %+v", store.processed)
	}
}

func TestCosmeticCommerceWebhookRejectsInvalidOrOversizedPayload(t *testing.T) {
	t.Run("invalid signature", func(t *testing.T) {
		store := &fakeCosmeticCommerceStore{}
		provider := &fakeCosmeticPaymentProvider{parseErr: errors.New("bad signature")}
		recorder := httptest.NewRecorder()
		newCosmeticCommerceHandlerWithStore(store, provider, true).StripeWebhook(
			recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)),
		)
		if recorder.Code != http.StatusBadRequest || store.processed.EventID != "" {
			t.Fatalf("status=%d processed=%+v", recorder.Code, store.processed)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		store := &fakeCosmeticCommerceStore{}
		provider := &fakeCosmeticPaymentProvider{}
		recorder := httptest.NewRecorder()
		newCosmeticCommerceHandlerWithStore(store, provider, true).StripeWebhook(
			recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", cosmeticWebhookMaxBytes+1))),
		)
		if recorder.Code != http.StatusRequestEntityTooLarge || provider.payload != nil {
			t.Fatalf("status=%d parsedBytes=%d", recorder.Code, len(provider.payload))
		}
	})
}

func TestCosmeticCommerceOrderHistoryIsScopedAndBounded(t *testing.T) {
	store := &fakeCosmeticCommerceStore{orders: []db.CosmeticOrder{*commerceTestOrder()}}
	handler := newCosmeticCommerceHandlerWithStore(store, &fakeCosmeticPaymentProvider{}, true)

	customer := httptest.NewRecorder()
	handler.CustomerOrders(customer, commerceCustomerRequest(http.MethodGet, "/api/v1/account/cosmetics/orders?limit=500", ""))
	if customer.Code != http.StatusOK || store.listAccount != "account-1" || store.listLimit != 100 {
		t.Fatalf("customer status=%d account=%q limit=%d body=%s", customer.Code, store.listAccount, store.listLimit, customer.Body.String())
	}

	admin := httptest.NewRecorder()
	handler.AdminOrders(admin, httptest.NewRequest(http.MethodGet, "/api/v1/admin/cosmetics/orders?query=owner%40example.com&status=paid&limit=999", nil))
	if admin.Code != http.StatusOK || store.adminQuery != "owner@example.com" || store.adminStatus != "paid" || store.listLimit != 100 {
		t.Fatalf("admin status=%d query=%q filter=%q limit=%d body=%s", admin.Code, store.adminQuery, store.adminStatus, store.listLimit, admin.Body.String())
	}
}

func TestCosmeticCommerceRoutesExistAtRootAndArenaPrefixes(t *testing.T) {
	previous := config.C
	t.Cleanup(func() { config.C = previous })
	config.C.OIDCEnabled = false
	config.C.CustomerOIDCEnabled = false
	config.C.AdminLocalhostBypass = false
	config.C.AdminToken = ""
	config.C.CosmeticsCheckoutEnabled = false
	config.C.StripeSecretKey = ""
	config.C.StripeWebhookSecrets = ""
	router := NewRouter(game.NewGameEngine())

	for _, prefix := range []string{"/api/v1", "/arena/api/v1"} {
		tests := []struct {
			method string
			path   string
			want   int
		}{
			{method: http.MethodPost, path: "/cosmetics/webhooks/stripe", want: http.StatusServiceUnavailable},
			{method: http.MethodGet, path: "/account/cosmetics/orders", want: http.StatusServiceUnavailable},
			{method: http.MethodPost, path: "/account/cosmetics/checkout", want: http.StatusServiceUnavailable},
			{method: http.MethodPost, path: "/account/cosmetics/orders/order-1/checkout", want: http.StatusServiceUnavailable},
			{method: http.MethodPost, path: "/account/cosmetics/subscription/checkout", want: http.StatusServiceUnavailable},
			{method: http.MethodPost, path: "/account/cosmetics/subscription/portal", want: http.StatusServiceUnavailable},
			{method: http.MethodGet, path: "/admin/cosmetics/orders", want: http.StatusUnauthorized},
		}
		for _, test := range tests {
			target := prefix + test.path
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, target, nil)
			request.RemoteAddr = "198.51.100.10:4444"
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Errorf("%s %s status=%d body=%s, want %d", test.method, target, recorder.Code, recorder.Body.String(), test.want)
			}
		}
	}
}

func TestCosmeticCheckoutRouteFailsClosedAtBothPrefixesWithoutRedis(t *testing.T) {
	previousConfig := config.C
	previousRedis := security.RedisClient
	t.Cleanup(func() {
		config.C = previousConfig
		security.RedisClient = previousRedis
	})
	config.C.CosmeticsCheckoutRPM = 3
	security.RedisClient = nil

	store := &fakeCosmeticCommerceStore{order: commerceTestOrder()}
	provider := &fakeCosmeticPaymentProvider{}
	handler := newCosmeticCommerceHandlerWithStore(store, provider, true)
	router := chi.NewRouter()
	for _, prefix := range []string{"/api/v1/account", "/arena/api/v1/account"} {
		router.Route(prefix, func(account chi.Router) {
			registerCustomerCosmeticCommerceRoutes(account, handler)
		})
	}

	for _, target := range []string{
		"/api/v1/account/cosmetics/checkout",
		"/arena/api/v1/account/cosmetics/checkout",
		"/api/v1/account/cosmetics/orders/order-1/checkout",
		"/arena/api/v1/account/cosmetics/orders/order-1/checkout",
		"/api/v1/account/cosmetics/subscription/checkout",
		"/arena/api/v1/account/cosmetics/subscription/checkout",
		"/api/v1/account/cosmetics/subscription/portal",
		"/arena/api/v1/account/cosmetics/subscription/portal",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"pack_id":"arena-set-003-ember-vanguard-pack","quantity":1}`))
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"code":"RATE_LIMIT_UNAVAILABLE"`) {
			t.Fatalf("%s status=%d body=%s", target, recorder.Code, recorder.Body.String())
		}
	}
	if store.createdAccount != "" || store.getID != "" || provider.request.OrderID != "" || provider.retrievedSessionID != "" {
		t.Fatalf("checkout side effects ran without limiter: store=%q/%q provider=%+v retrieve=%q", store.createdAccount, store.getID, provider.request, provider.retrievedSessionID)
	}
}

func TestCosmeticCatalogAdvertisesCheckoutOnlyWithUsableAuthAndRedis(t *testing.T) {
	previousConfig := config.C
	previousRedis := security.RedisClient
	redisClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() {
		config.C = previousConfig
		security.RedisClient = previousRedis
		_ = redisClient.Close()
	})

	config.C.CosmeticsCheckoutEnabled = true
	config.C.CosmeticsCheckoutRPM = 10
	config.C.StripeSecretKey = "sk_test_catalog_readiness"
	config.C.StripeWebhookSecrets = "whsec_catalog_readiness"
	config.C.StripeSuccessURL = "https://arena.example/dashboard/?checkout=success"
	config.C.StripeCancelURL = "https://arena.example/dashboard/?checkout=cancel"
	/*
	 * A real discovery document, served locally.
	 *
	 * This used to prove the positive case with the native email sign-in,
	 * which no longer exists — Accounts is the only way in. Rather than delete
	 * the assertion, it now stands up something `oidc.NewProvider` will
	 * actually accept, so "customer auth initialised" is demonstrated by the
	 * provider handshake succeeding rather than asserted about a config
	 * struct.
	 */
	issuer := newStubAccountsIssuer(t)
	config.C.CustomerOIDCEnabled = true
	config.C.CustomerOIDCIssuer = issuer
	config.C.CustomerOIDCClientID = "arena-dashboard"
	config.C.CustomerOIDCClientSecret = "arena-dashboard-secret"
	config.C.CustomerOIDCRedirectURI = "https://arena.example/api/v1/dashboard/callback"
	config.C.CustomerOIDCSessionTTL = 720
	security.RedisClient = redisClient

	checkoutEnabled := func() bool {
		router := NewRouter(game.NewGameEngine())
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/cosmetics/catalog", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("catalog status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			CheckoutEnabled bool `json:"checkout_enabled"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.CheckoutEnabled
	}

	if !checkoutEnabled() {
		t.Fatal("working Accounts sign-in with Redis did not advertise checkout")
	}

	config.C.CustomerOIDCIssuer = ""
	config.C.CustomerOIDCClientID = ""
	config.C.CustomerOIDCClientSecret = ""
	config.C.CustomerOIDCRedirectURI = ""
	if checkoutEnabled() {
		t.Fatal("checkout was advertised after customer auth failed to initialise")
	}

	config.C.CustomerOIDCIssuer = issuer
	config.C.CustomerOIDCClientID = "arena-dashboard"
	config.C.CustomerOIDCClientSecret = "arena-dashboard-secret"
	config.C.CustomerOIDCRedirectURI = "https://arena.example/api/v1/dashboard/callback"
	security.RedisClient = nil
	if checkoutEnabled() {
		t.Fatal("checkout was advertised without the required Redis quota")
	}
}

// newStubAccountsIssuer serves the smallest discovery document
// `oidc.NewProvider` will accept, and returns its issuer URL.
//
// Enough to prove the handshake happens; it issues no tokens and is never
// asked to. The endpoints it advertises are the real Accounts shapes so that a
// reader can see what Arena is talking to.
func newStubAccountsIssuer(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/connect",
			"token_endpoint":                        base + "/api/v1/token",
			"jwks_uri":                              base + "/.well-known/jwks.json",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"code_challenge_methods_supported":      []string{"S256"},
		})
	})
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	base = server.URL
	return base
}
