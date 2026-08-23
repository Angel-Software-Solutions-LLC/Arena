package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"arena-server/internal/config"
	"arena-server/internal/db"
	"arena-server/internal/game"

	"github.com/go-chi/chi/v5"
)

const (
	cosmeticWebhookMaxBytes = 1 << 20
	cosmeticCheckoutTimeout = 15 * time.Second
)

var cosmeticSubscriptionObservationNanos atomic.Int64

func nextCosmeticSubscriptionObservationTime() time.Time {
	// PostgreSQL timestamptz persists microseconds. Generate monotonic values at
	// that same resolution so distinct local observations cannot collapse to an
	// equal stored timestamp and be misclassified after a concurrent commit.
	now := time.Now().UTC().Truncate(time.Microsecond).UnixNano()
	for {
		previous := cosmeticSubscriptionObservationNanos.Load()
		if now <= previous {
			now = previous + int64(time.Microsecond)
		}
		if cosmeticSubscriptionObservationNanos.CompareAndSwap(previous, now) {
			return time.Unix(0, now).UTC()
		}
	}
}

var cosmeticCheckoutPackIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

type cosmeticCommerceStore interface {
	ReserveOrder(context.Context, string, string, int, CosmeticCheckoutPresentation) (*db.CosmeticOrder, bool, error)
	AttachCheckout(context.Context, string, string, string) (*db.CosmeticOrder, error)
	MarkCheckoutFailed(context.Context, string, string, string) (*db.CosmeticOrder, error)
	ProcessEvent(context.Context, db.CosmeticPaymentEventInput) (*db.CosmeticPaymentEventResult, error)
	GetCustomerOrder(context.Context, string, string) (*db.CosmeticOrder, error)
	ListCustomerOrders(context.Context, string, int) ([]db.CosmeticOrder, error)
	ListAdminOrders(context.Context, string, string, int) ([]db.CosmeticOrder, error)
	EquippedForBots(context.Context, []string) (map[string]map[string]string, error)
}

type databaseCosmeticCommerceStore struct{}

func (databaseCosmeticCommerceStore) ReserveOrder(ctx context.Context, accountID, packID string, quantity int, presentation CosmeticCheckoutPresentation) (*db.CosmeticOrder, bool, error) {
	return db.ReserveCosmeticOrderCheckout(ctx, accountID, packID, quantity, string(presentation))
}

func (databaseCosmeticCommerceStore) AttachCheckout(ctx context.Context, accountID, orderID, checkoutSessionID string) (*db.CosmeticOrder, error) {
	return db.AttachCosmeticOrderCheckout(ctx, accountID, orderID, checkoutSessionID)
}

func (databaseCosmeticCommerceStore) MarkCheckoutFailed(ctx context.Context, accountID, orderID, message string) (*db.CosmeticOrder, error) {
	return db.MarkCosmeticOrderCheckoutFailed(ctx, accountID, orderID, message)
}

func (databaseCosmeticCommerceStore) ProcessEvent(ctx context.Context, event db.CosmeticPaymentEventInput) (*db.CosmeticPaymentEventResult, error) {
	return db.ProcessCosmeticPaymentEvent(ctx, event)
}

func (databaseCosmeticCommerceStore) GetCustomerOrder(ctx context.Context, accountID, orderID string) (*db.CosmeticOrder, error) {
	return db.GetCustomerCosmeticOrder(ctx, accountID, orderID)
}

func (databaseCosmeticCommerceStore) ListCustomerOrders(ctx context.Context, accountID string, limit int) ([]db.CosmeticOrder, error) {
	return db.ListCustomerCosmeticOrders(ctx, accountID, limit)
}

func (databaseCosmeticCommerceStore) ListAdminOrders(ctx context.Context, query, status string, limit int) ([]db.CosmeticOrder, error) {
	return db.ListAdminCosmeticOrders(ctx, query, status, limit)
}

func (databaseCosmeticCommerceStore) EquippedForBots(ctx context.Context, botIDs []string) (map[string]map[string]string, error) {
	return db.GetEquippedCosmeticsForBots(ctx, botIDs)
}

// CosmeticCommerceHandler keeps authenticated order creation, provider IO, and
// signature-verified fulfillment separate from the catalog/equip handler.
// Webhook reconciliation and the billing portal remain available when new
// sales are paused, provided the operator retains the webhook secret, Stripe
// API key, and portal return URL required by config validation.
type CosmeticCommerceHandler struct {
	store             cosmeticCommerceStore
	subscriptionStore cosmeticSubscriptionStore
	provider          CosmeticPaymentProvider
	checkoutEnabled   bool
	publishableKey    string
	engine            *game.GameEngine
}

func NewCosmeticCommerceHandler(engine *game.GameEngine) *CosmeticCommerceHandler {
	secrets := config.ParseStripeWebhookSecrets(config.C.StripeWebhookSecrets)
	var provider CosmeticPaymentProvider
	if len(secrets) > 0 {
		provider = NewStripeCosmeticPaymentProvider(
			config.C.StripeSecretKey,
			secrets,
			config.C.StripeSuccessURL,
			config.C.StripeCancelURL,
			config.C.StripeReturnURL,
			config.C.StripePortalReturnURL,
			config.C.StripeAutomaticTax,
		)
	}
	return &CosmeticCommerceHandler{
		store:             databaseCosmeticCommerceStore{},
		subscriptionStore: databaseCosmeticSubscriptionStore{},
		provider:          provider,
		checkoutEnabled:   config.C.CosmeticsCheckoutEnabled && provider != nil,
		publishableKey:    strings.TrimSpace(config.C.StripePublishableKey),
		engine:            engine,
	}
}

func newCosmeticCommerceHandlerWithStore(store cosmeticCommerceStore, provider CosmeticPaymentProvider, enabled bool) *CosmeticCommerceHandler {
	return &CosmeticCommerceHandler{store: store, provider: provider, checkoutEnabled: enabled && provider != nil}
}

func (h *CosmeticCommerceHandler) Enabled() bool {
	return h != nil && h.checkoutEnabled && h.provider != nil
}

type cosmeticCheckoutBody struct {
	PackID       string                       `json:"pack_id"`
	Quantity     int                          `json:"quantity"`
	Presentation CosmeticCheckoutPresentation `json:"presentation"`
}

// accountsShopHandoff returns where buying happens, or "" while Arena still
// sells for itself.
//
// One reading of one setting, used by the config document the shop reads and
// by every checkout endpoint below, so the answer the browser is given and the
// answer the server enforces cannot come apart.
func accountsShopHandoff() string {
	return strings.TrimSpace(config.C.AccountsShopURL)
}

// refuseWhenPurchasingMoved answers a checkout attempt that should have been a
// handoff, and reports whether it did.
//
// 409 rather than 503: nothing is broken or temporarily away — this endpoint is
// no longer where the act happens, and the reply says where it does. A client
// that has not been reloaded since the switch gets a destination rather than a
// dead end.
func refuseWhenPurchasingMoved(w http.ResponseWriter) bool {
	handoff := accountsShopHandoff()
	if handoff == "" {
		return false
	}
	setCustomerNoStore(w)
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":       "purchases are completed in your Angel account",
		"handoff_url": handoff,
	})
	return true
}

// CheckoutConfig tells the shop how buying works here.
//
// Two shapes. While Arena sells, it exposes Stripe's intentionally public
// browser key and the presentation mode. Once ARENA_ACCOUNTS_SHOP_URL is set,
// it says so instead and names the destination — and `enabled` goes false, so
// a browser running the previous bundle stops offering a checkout it can no
// longer complete rather than failing at the last step.
func (h *CosmeticCommerceHandler) CheckoutConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	if handoff := accountsShopHandoff(); handoff != "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled":                 false,
			"mode":                    "handoff",
			"handoff_url":             handoff,
			"default_presentation":    CosmeticCheckoutPresentationEmbedded,
			"hosted_fallback_enabled": false,
		})
		return
	}
	enabled := h != nil && h.Enabled() && strings.TrimSpace(h.publishableKey) != ""
	response := map[string]interface{}{
		"enabled":                 enabled,
		"mode":                    "stripe",
		"default_presentation":    CosmeticCheckoutPresentationEmbedded,
		"hosted_fallback_enabled": enabled,
	}
	if enabled {
		response["publishable_key"] = strings.TrimSpace(h.publishableKey)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *CosmeticCommerceHandler) Checkout(w http.ResponseWriter, r *http.Request) {
	// Buying a pack. Refused before any state is read or written, so nothing
	// here half-happens on the way to a handoff.
	if refuseWhenPurchasingMoved(w) {
		return
	}
	setCustomerNoStore(w)
	if h == nil || !h.Enabled() || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "cosmetic checkout is not available")
		return
	}
	session, ok := customerSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "customer authentication required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var request cosmeticCheckoutBody
	if err := decodeStrictCosmeticAdminJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid cosmetic checkout request")
		return
	}
	request.PackID = strings.ToLower(strings.TrimSpace(request.PackID))
	presentation, validPresentation := normalizeCosmeticCheckoutPresentation(request.Presentation)
	if !validPresentation {
		writeError(w, http.StatusBadRequest, "presentation must be embedded or hosted")
		return
	}
	if !cosmeticCheckoutPackIDPattern.MatchString(request.PackID) || request.Quantity < 1 || request.Quantity > 10 {
		writeError(w, http.StatusBadRequest, "pack_id and quantity between 1 and 10 are required")
		return
	}

	order, reservedNew, err := h.store.ReserveOrder(r.Context(), session.AccountID, request.PackID, request.Quantity, presentation)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrCosmeticOrderQuantity):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, db.ErrCosmeticOrderPackUnavailable):
			writeError(w, http.StatusConflict, "cosmetic pack is not available for purchase")
		case errors.Is(err, db.ErrCustomerAccountNotFound):
			writeError(w, http.StatusUnauthorized, "customer account is unavailable")
		case errors.Is(err, db.ErrCustomerAccountUnverified):
			writeError(w, http.StatusForbidden, "a verified customer email is required")
		case errors.Is(err, db.ErrNoDatabase):
			writeError(w, http.StatusServiceUnavailable, "cosmetic orders are unavailable")
		default:
			slog.Error("failed to create cosmetic order", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to create cosmetic order")
		}
		return
	}
	if order == nil || order.ID == "" || order.AccountID != session.AccountID || order.PackID != request.PackID || order.Quantity != request.Quantity {
		writeError(w, http.StatusInternalServerError, "failed to create cosmetic order")
		return
	}
	if _, ok := storedCosmeticCheckoutPresentation(order.CheckoutPresentation); !ok {
		slog.Error("cosmetic order has an invalid persisted checkout presentation", "order_id", order.ID)
		writeError(w, http.StatusInternalServerError, "failed to create cosmetic order")
		return
	}
	if order.Status == db.CosmeticOrderStatusCheckout && strings.TrimSpace(order.CheckoutSessionID) != "" {
		h.resumeAttachedCosmeticOrderCheckout(w, r, order)
		return
	}
	if (order.Status != db.CosmeticOrderStatusCreated && order.Status != db.CosmeticOrderStatusPaymentFailed) ||
		strings.TrimSpace(order.CheckoutSessionID) != "" {
		writeError(w, http.StatusConflict, "cosmetic checkout is not retryable")
		return
	}
	checkout, attachedOrder, ok := h.createAndAttachCosmeticOrderCheckout(w, r, order)
	if !ok {
		return
	}
	status := http.StatusCreated
	if !reservedNew {
		status = http.StatusOK
	}
	writeCosmeticOrderCheckoutResponse(w, status, attachedOrder, checkout, !reservedNew)
}

func (h *CosmeticCommerceHandler) markCheckoutFailed(ctx context.Context, order *db.CosmeticOrder, message string) {
	if h == nil || h.store == nil || order == nil {
		return
	}
	if _, err := h.store.MarkCheckoutFailed(ctx, order.AccountID, order.ID, message); err != nil {
		slog.Error("failed to persist cosmetic checkout failure", "order_id", order.ID, "error", err)
	}
}

// ResumeCheckout returns the same still-open provider session previously
// attached to an account-owned order. For a retryable unattached order it
// replays provider creation with the same order-derived idempotency key and the
// presentation committed before the original provider call.
func (h *CosmeticCommerceHandler) ResumeCheckout(w http.ResponseWriter, r *http.Request) {
	// An order started before the switch finishes in the Accounts app too, so
	// the receipt lands where the licence now lives. Refused before any state
	// is read or written, so nothing here half-happens on the way to a handoff.
	if refuseWhenPurchasingMoved(w) {
		return
	}
	setCustomerNoStore(w)
	if h == nil || !h.Enabled() || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "cosmetic checkout is not available")
		return
	}
	session, ok := customerSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "customer authentication required")
		return
	}
	orderID := strings.TrimSpace(chi.URLParam(r, "order_id"))
	if orderID == "" || len(orderID) > 255 {
		writeError(w, http.StatusNotFound, "cosmetic order was not found")
		return
	}
	order, err := h.store.GetCustomerOrder(r.Context(), session.AccountID, orderID)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrCosmeticOrderNotFound), errors.Is(err, db.ErrCosmeticOrderMismatch):
			writeError(w, http.StatusNotFound, "cosmetic order was not found")
		case errors.Is(err, db.ErrNoDatabase):
			writeError(w, http.StatusServiceUnavailable, "cosmetic orders are unavailable")
		default:
			slog.Error("failed to load cosmetic order for checkout resume", "order_id", orderID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load cosmetic order")
		}
		return
	}
	if order == nil || order.ID != orderID || order.AccountID != session.AccountID {
		writeError(w, http.StatusNotFound, "cosmetic order was not found")
		return
	}
	if _, ok := storedCosmeticCheckoutPresentation(order.CheckoutPresentation); !ok {
		slog.Error("cosmetic order has an invalid persisted checkout presentation", "order_id", order.ID)
		writeError(w, http.StatusInternalServerError, "failed to resume cosmetic checkout")
		return
	}
	if (order.Status == db.CosmeticOrderStatusCreated || order.Status == db.CosmeticOrderStatusPaymentFailed) &&
		strings.TrimSpace(order.CheckoutSessionID) == "" {
		checkout, attachedOrder, ok := h.createAndAttachCosmeticOrderCheckout(w, r, order)
		if !ok {
			return
		}
		writeCosmeticOrderCheckoutResponse(w, http.StatusOK, attachedOrder, checkout, true)
		return
	}
	if order.Status != db.CosmeticOrderStatusCheckout || strings.TrimSpace(order.CheckoutSessionID) == "" {
		writeError(w, http.StatusConflict, "cosmetic checkout is not resumable")
		return
	}
	h.resumeAttachedCosmeticOrderCheckout(w, r, order)
}

func (h *CosmeticCommerceHandler) createAndAttachCosmeticOrderCheckout(w http.ResponseWriter, r *http.Request, order *db.CosmeticOrder) (*CosmeticCheckoutSession, *db.CosmeticOrder, bool) {
	presentation, validPresentation := storedCosmeticCheckoutPresentation(order.CheckoutPresentation)
	if !validPresentation {
		writeError(w, http.StatusInternalServerError, "failed to open cosmetic checkout")
		return nil, nil, false
	}
	providerCtx, cancel := context.WithTimeout(r.Context(), cosmeticCheckoutTimeout)
	checkout, err := h.provider.CreateCheckoutSession(providerCtx, CosmeticCheckoutRequest{
		OrderID:         order.ID,
		AccountID:       order.AccountID,
		CustomerEmail:   order.AccountEmail,
		PackID:          order.PackID,
		PackName:        order.PackName,
		PackDescription: order.PackDescription,
		UnitAmount:      order.UnitPriceCents,
		Currency:        order.Currency,
		Quantity:        int64(order.Quantity),
		Presentation:    presentation,
	})
	cancel()
	if err != nil {
		slog.Error("failed to create cosmetic checkout", "order_id", order.ID, "error", err)
		h.markCheckoutFailed(r.Context(), order, "checkout provider unavailable")
		writeError(w, http.StatusBadGateway, "failed to open cosmetic checkout")
		return nil, nil, false
	}
	if !validCosmeticCheckoutSession(checkout) || checkout.Presentation != presentation {
		slog.Error("cosmetic checkout provider returned an invalid session", "order_id", order.ID)
		h.markCheckoutFailed(r.Context(), order, "checkout provider returned an invalid session")
		writeError(w, http.StatusBadGateway, "failed to open cosmetic checkout")
		return nil, nil, false
	}
	attachedOrder, err := h.store.AttachCheckout(r.Context(), order.AccountID, order.ID, checkout.ID)
	if err != nil {
		// Provider creation and local attachment can both succeed even when this
		// request observes a timeout. Leave the reserved record unchanged so the
		// next request replays the same order-derived idempotency key.
		slog.Error("failed to attach cosmetic checkout session", "order_id", order.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save cosmetic checkout")
		return nil, nil, false
	}
	if attachedOrder == nil {
		attachedOrder = order
	}
	return checkout, attachedOrder, true
}

func (h *CosmeticCommerceHandler) resumeAttachedCosmeticOrderCheckout(w http.ResponseWriter, r *http.Request, order *db.CosmeticOrder) {

	providerCtx, cancel := context.WithTimeout(r.Context(), cosmeticCheckoutTimeout)
	checkout, err := h.provider.RetrieveCheckoutSession(providerCtx, order.CheckoutSessionID)
	cancel()
	if err != nil {
		slog.Error("failed to retrieve cosmetic checkout", "order_id", order.ID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to resume cosmetic checkout")
		return
	}
	if !validRetrievedCosmeticOrderCheckout(checkout, order) {
		slog.Error("cosmetic checkout retrieval did not match the owned order", "order_id", order.ID)
		writeError(w, http.StatusBadGateway, "failed to resume cosmetic checkout")
		return
	}

	response := map[string]interface{}{
		"order_id": order.ID, "order_status": order.Status,
		"checkout_status": checkout.Status, "resumable": checkout.Status == CosmeticCheckoutSessionStatusOpen,
	}
	switch checkout.Status {
	case CosmeticCheckoutSessionStatusOpen:
		if !validCosmeticCheckoutSession(checkout) || (!checkout.ExpiresAt.IsZero() && !time.Now().Before(checkout.ExpiresAt)) {
			writeError(w, http.StatusBadGateway, "failed to resume cosmetic checkout")
			return
		}
		response["resumed"] = true
		addCosmeticCheckoutResponseFields(response, checkout)
		if !checkout.ExpiresAt.IsZero() {
			response["expires_at"] = checkout.ExpiresAt
		}
	case CosmeticCheckoutSessionStatusComplete, CosmeticCheckoutSessionStatusExpired:
		response["resumed"] = false
	default:
		writeError(w, http.StatusBadGateway, "failed to resume cosmetic checkout")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func validRetrievedCosmeticOrderCheckout(checkout *CosmeticCheckoutSession, order *db.CosmeticOrder) bool {
	if checkout == nil || order == nil || checkout.ID != order.CheckoutSessionID || checkout.Mode != "payment" ||
		checkout.OrderID != order.ID || checkout.AccountID != order.AccountID || checkout.PackID != order.PackID {
		return false
	}
	presentation, ok := storedCosmeticCheckoutPresentation(order.CheckoutPresentation)
	return ok && checkout.Presentation == presentation
}

func storedCosmeticCheckoutPresentation(value string) (CosmeticCheckoutPresentation, bool) {
	switch CosmeticCheckoutPresentation(strings.ToLower(strings.TrimSpace(value))) {
	case CosmeticCheckoutPresentationEmbedded:
		return CosmeticCheckoutPresentationEmbedded, true
	case CosmeticCheckoutPresentationHosted:
		return CosmeticCheckoutPresentationHosted, true
	default:
		return "", false
	}
}

func validCosmeticCheckoutSession(session *CosmeticCheckoutSession) bool {
	if session == nil || strings.TrimSpace(session.ID) == "" || len(session.ID) > 255 {
		return false
	}
	switch session.Presentation {
	case CosmeticCheckoutPresentationEmbedded:
		secret := strings.TrimSpace(session.ClientSecret)
		return secret != "" && len(secret) <= 2048 && strings.TrimSpace(session.URL) == ""
	case CosmeticCheckoutPresentationHosted:
		urlValue := strings.TrimSpace(session.URL)
		return urlValue != "" && len(urlValue) <= 2048 && strings.TrimSpace(session.ClientSecret) == "" && validHostedHTTPSURL(urlValue)
	default:
		return false
	}
}

func addCosmeticCheckoutResponseFields(response map[string]interface{}, checkout *CosmeticCheckoutSession) {
	if response == nil || checkout == nil {
		return
	}
	response["presentation"] = checkout.Presentation
	response["session_id"] = checkout.ID
	if checkout.Presentation == CosmeticCheckoutPresentationEmbedded {
		response["client_secret"] = checkout.ClientSecret
	} else {
		response["checkout_url"] = checkout.URL
	}
}

func writeCosmeticOrderCheckoutResponse(w http.ResponseWriter, status int, order *db.CosmeticOrder, checkout *CosmeticCheckoutSession, resumed bool) {
	if order == nil || checkout == nil {
		writeError(w, http.StatusInternalServerError, "failed to render cosmetic checkout")
		return
	}
	response := map[string]interface{}{"order_id": order.ID}
	if order.Status != "" {
		response["order_status"] = order.Status
	}
	if resumed {
		response["resumed"] = true
		response["resumable"] = true
		response["checkout_status"] = CosmeticCheckoutSessionStatusOpen
	}
	addCosmeticCheckoutResponseFields(response, checkout)
	if !checkout.ExpiresAt.IsZero() {
		response["expires_at"] = checkout.ExpiresAt
	}
	writeJSON(w, status, response)
}

func validHostedHTTPSURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && strings.EqualFold(parsed.Scheme, "https") && parsed.Hostname() != "" && parsed.User == nil
}

func (h *CosmeticCommerceHandler) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h == nil || h.provider == nil || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "cosmetic payment webhooks are not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, cosmeticWebhookMaxBytes)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payment webhook is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read payment webhook")
		return
	}
	event, err := h.provider.ParseWebhook(payload, r.Header.Get("Stripe-Signature"))
	if err != nil {
		if errors.Is(err, ErrUnsupportedCosmeticPaymentEvent) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"received": true, "ignored": true})
			return
		}
		writeError(w, http.StatusBadRequest, "invalid payment webhook")
		return
	}
	if event == nil {
		writeError(w, http.StatusBadRequest, "invalid payment webhook")
		return
	}
	if event.Kind == CosmeticPaymentKindSubscription {
		h.subscriptionWebhook(w, r, *event)
		return
	}
	input, err := databaseCosmeticPaymentEvent(*event)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unsupported payment webhook")
		return
	}
	result, err := h.store.ProcessEvent(r.Context(), input)
	if err != nil {
		if errors.Is(err, db.ErrCosmeticOrderTerminal) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"received": true, "ignored": true, "terminal": true})
			return
		}
		switch {
		case errors.Is(err, db.ErrCosmeticOrderMismatch),
			errors.Is(err, db.ErrCosmeticPaymentEventInvalid),
			errors.Is(err, db.ErrCosmeticPaymentEventConflict),
			errors.Is(err, db.ErrCosmeticPaymentEventRejected):
			slog.Warn("rejected cosmetic payment event", "event_id", event.ID, "event_type", event.Type, "error", err)
			writeError(w, http.StatusUnprocessableEntity, "payment event did not match an order")
		case errors.Is(err, db.ErrNoDatabase):
			writeError(w, http.StatusServiceUnavailable, "cosmetic order ledger is unavailable")
		default:
			slog.Error("failed to process cosmetic payment event", "event_id", event.ID, "event_type", event.Type, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to process payment event")
		}
		return
	}
	if result == nil {
		writeError(w, http.StatusInternalServerError, "failed to process payment event")
		return
	}
	liveRefreshed, err := h.refreshTerminalBotVisuals(r.Context(), result.Order)
	if err != nil {
		// The payment mutation is already committed and idempotent. A non-2xx
		// response makes Stripe retry the same event; duplicate processing then
		// re-attempts this authoritative cache repair without reapplying money.
		slog.Error("failed to refresh connected bot cosmetics after payment reversal", "event_id", event.ID, "event_type", event.Type)
		writeError(w, http.StatusInternalServerError, "payment event requires retry")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"received": true, "applied": result.Applied, "duplicate": result.Duplicate,
		"licenses_created": result.LicensesCreated, "live_refreshed": liveRefreshed, "order": result.Order,
	})
}

func (h *CosmeticCommerceHandler) subscriptionWebhook(w http.ResponseWriter, r *http.Request, event CosmeticPaymentEvent) {
	if h.subscriptionStore == nil {
		writeError(w, http.StatusServiceUnavailable, "cosmetic subscription ledger is unavailable")
		return
	}
	if cosmeticSubscriptionEventNeedsProviderReconciliation(event) {
		retriever, ok := h.provider.(cosmeticSubscriptionStateRetriever)
		if !ok || strings.TrimSpace(event.ProviderSubscriptionID) == "" {
			writeError(w, http.StatusServiceUnavailable, "subscription provider reconciliation is unavailable")
			return
		}
		// Capture before Retrieve so concurrent requests have a stable local
		// observation order even if the earlier request returns more slowly.
		observedAt := nextCosmeticSubscriptionObservationTime()
		providerCtx, cancel := context.WithTimeout(r.Context(), cosmeticCheckoutTimeout)
		state, retrieveErr := retriever.RetrieveCosmeticSubscription(providerCtx, event.ProviderSubscriptionID)
		cancel()
		if retrieveErr != nil || state == nil || state.ID != event.ProviderSubscriptionID || state.CustomerID == "" {
			slog.Error("failed to reconcile cosmetic subscription from provider", "event_id", event.ID,
				"provider_subscription_id", event.ProviderSubscriptionID, "error", retrieveErr)
			writeError(w, http.StatusServiceUnavailable, "subscription provider reconciliation failed")
			return
		}
		event.CustomerID = state.CustomerID
		event.SubscriptionStatus = state.Status
		event.CancelAtPeriodEnd = state.CancelAtPeriodEnd
		event.CurrentPeriodEnd = state.CurrentPeriodEnd
		event.Terminal = state.Terminal
		event.ProviderStateObservedAt = observedAt
	}
	input, err := databaseCosmeticSubscriptionEvent(event)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unsupported subscription webhook")
		return
	}
	result, err := h.subscriptionStore.Process(r.Context(), input)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrCosmeticSubscriptionMismatch),
			errors.Is(err, db.ErrCosmeticSubscriptionEventInvalid),
			errors.Is(err, db.ErrCosmeticSubscriptionEventConflict),
			errors.Is(err, db.ErrCosmeticSubscriptionNotFound):
			writeError(w, http.StatusUnprocessableEntity, "payment event did not match a cosmetic subscription")
		case errors.Is(err, db.ErrNoDatabase):
			writeError(w, http.StatusServiceUnavailable, "cosmetic subscription ledger is unavailable")
		default:
			slog.Error("failed to process cosmetic subscription event", "event_id", event.ID, "event_type", event.Type, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to process subscription event")
		}
		return
	}
	if result == nil || result.Subscription == nil {
		writeError(w, http.StatusInternalServerError, "failed to process subscription event")
		return
	}
	liveRefreshed := 0
	if !result.Subscription.HasAccess {
		liveRefreshed, err = h.refreshConnectedBotVisuals(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "subscription event requires retry")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"received": true, "applied": result.Applied, "duplicate": result.Duplicate,
		"licenses_created": result.LicensesCreated, "licenses_revoked": result.LicensesRevoked,
		"live_refreshed": liveRefreshed, "subscription": result.Subscription,
	})
}

func cosmeticSubscriptionEventNeedsProviderReconciliation(event CosmeticPaymentEvent) bool {
	if event.Type != CosmeticPaymentEventSubscriptionCreated && event.Type != CosmeticPaymentEventSubscriptionUpdated {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(event.SubscriptionStatus))
	return !event.Terminal && status != db.CosmeticSubscriptionStatusCanceled &&
		status != db.CosmeticSubscriptionStatusExpired && status != "incomplete_expired"
}

// refreshTerminalBotVisuals repairs the engine's presentation-only cache after
// the committed order transaction deletes refunded/chargeback loadouts. The
// connected set is bounded and includes waiting bots; refreshing every member
// also makes an idempotent webhook retry able to repair a prior transient read
// failure without retaining deleted assignment metadata.
func (h *CosmeticCommerceHandler) refreshTerminalBotVisuals(ctx context.Context, order *db.CosmeticOrder) (int, error) {
	if h == nil || h.engine == nil || h.store == nil || order == nil ||
		(order.Status != db.CosmeticOrderStatusRefunded && order.Status != db.CosmeticOrderStatusDisputed) {
		return 0, nil
	}
	return h.refreshConnectedBotVisuals(ctx)
}

func (h *CosmeticCommerceHandler) refreshConnectedBotVisuals(ctx context.Context) (int, error) {
	if h == nil || h.engine == nil || h.store == nil {
		return 0, nil
	}
	botIDs := h.engine.ConnectedBotIDs()
	if len(botIDs) == 0 {
		return 0, nil
	}
	equippedByBot, err := h.store.EquippedForBots(ctx, botIDs)
	if err != nil {
		return 0, err
	}
	refreshed := 0
	for _, botID := range botIDs {
		if h.engine.UpdateBotCosmetics(botID, equippedByBot[botID]) {
			refreshed++
		}
	}
	return refreshed, nil
}

func databaseCosmeticPaymentEvent(event CosmeticPaymentEvent) (db.CosmeticPaymentEventInput, error) {
	eventType, err := databaseCosmeticPaymentEventType(event.Type)
	if err != nil {
		return db.CosmeticPaymentEventInput{}, err
	}
	input := db.CosmeticPaymentEventInput{
		Provider: "stripe", EventID: strings.TrimSpace(event.ID), EventType: eventType,
		PayloadHash: strings.ToLower(strings.TrimSpace(event.PayloadSHA256)),
		OrderID:     strings.TrimSpace(event.OrderID), AccountID: strings.TrimSpace(event.AccountID),
		CheckoutSessionID: strings.TrimSpace(event.CheckoutSessionID), PaymentIntentID: strings.TrimSpace(event.PaymentIntentID),
		Currency:            strings.ToUpper(strings.TrimSpace(event.Currency)),
		Paid:                strings.EqualFold(strings.TrimSpace(event.PaymentStatus), "paid"),
		AmountReceivedCents: event.AmountTotal,
		RefundID:            strings.TrimSpace(event.RefundID), RefundStatus: strings.ToLower(strings.TrimSpace(event.RefundStatus)),
	}
	switch event.Type {
	case CosmeticPaymentEventRefundCreated, CosmeticPaymentEventRefundUpdated, CosmeticPaymentEventRefundFailed:
		input.RefundAmountCents = event.AmountRefunded
	case CosmeticPaymentEventChargeRefunded:
		input.CumulativeRefundedCents = event.AmountRefunded
	case CosmeticPaymentEventCheckoutAsyncFailed:
		input.FailureMessage = "asynchronous checkout payment failed"
	case CosmeticPaymentEventCheckoutExpired:
		input.FailureMessage = "checkout session expired"
	}
	return input, nil
}

func databaseCosmeticPaymentEventType(eventType CosmeticPaymentEventType) (string, error) {
	switch eventType {
	case CosmeticPaymentEventCheckoutCompleted:
		return db.CosmeticStripeCheckoutCompleted, nil
	case CosmeticPaymentEventCheckoutAsyncSucceeded:
		return db.CosmeticStripeCheckoutAsyncPaymentSucceeded, nil
	case CosmeticPaymentEventCheckoutAsyncFailed:
		return db.CosmeticStripeCheckoutAsyncPaymentFailed, nil
	case CosmeticPaymentEventCheckoutExpired:
		return db.CosmeticStripeCheckoutExpired, nil
	case CosmeticPaymentEventRefundCreated:
		return db.CosmeticStripeRefundCreated, nil
	case CosmeticPaymentEventRefundUpdated:
		return db.CosmeticStripeRefundUpdated, nil
	case CosmeticPaymentEventRefundFailed:
		return db.CosmeticStripeRefundFailed, nil
	case CosmeticPaymentEventChargeRefunded:
		return db.CosmeticStripeChargeRefunded, nil
	case CosmeticPaymentEventDisputeCreated:
		return db.CosmeticStripeDisputeCreated, nil
	default:
		return "", ErrUnsupportedCosmeticPaymentEvent
	}
}

func (h *CosmeticCommerceHandler) CustomerOrders(w http.ResponseWriter, r *http.Request) {
	setCustomerNoStore(w)
	session, ok := customerSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "customer authentication required")
		return
	}
	limit, ok := cosmeticOrderLimit(w, r)
	if !ok {
		return
	}
	orders, err := h.store.ListCustomerOrders(r.Context(), session.AccountID, limit)
	if err != nil {
		writeCosmeticOrdersListError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"orders": orders, "count": len(orders)})
}

func (h *CosmeticCommerceHandler) AdminOrders(w http.ResponseWriter, r *http.Request) {
	setAdminCatalogNoStore(w)
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	status := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("status")))
	if len(query) > 120 || !validCosmeticOrderStatusFilter(status) {
		writeError(w, http.StatusBadRequest, "invalid cosmetic order filter")
		return
	}
	limit, ok := cosmeticOrderLimit(w, r)
	if !ok {
		return
	}
	orders, err := h.store.ListAdminOrders(r.Context(), query, status, limit)
	if err != nil {
		writeCosmeticOrdersListError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"orders": orders, "count": len(orders)})
}

func cosmeticOrderLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return 0, false
		}
		limit = parsed
	}
	if limit > 100 {
		limit = 100
	}
	return limit, true
}

func validCosmeticOrderStatusFilter(status string) bool {
	switch status {
	case "", db.CosmeticOrderStatusCreated, db.CosmeticOrderStatusCheckout, db.CosmeticOrderStatusProcessing,
		db.CosmeticOrderStatusPaid, db.CosmeticOrderStatusPaymentFailed, db.CosmeticOrderStatusExpired,
		db.CosmeticOrderStatusRefundReview, db.CosmeticOrderStatusRefunded, db.CosmeticOrderStatusDisputed:
		return true
	default:
		return false
	}
}

func writeCosmeticOrdersListError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNoDatabase) {
		writeError(w, http.StatusServiceUnavailable, "cosmetic order ledger is unavailable")
		return
	}
	slog.Error("failed to list cosmetic orders", "error", err)
	writeError(w, http.StatusInternalServerError, "failed to load cosmetic orders")
}
