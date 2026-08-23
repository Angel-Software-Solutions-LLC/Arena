package api

import (
	"arena-server/internal/db"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

type cosmeticSubscriptionStore interface {
	Reserve(context.Context, string, CosmeticCheckoutPresentation) (*db.CosmeticSubscription, bool, error)
	Attach(context.Context, string, string, string) (*db.CosmeticSubscription, error)
	ExpireCheckout(context.Context, string, string, string) (*db.CosmeticSubscription, error)
	Process(context.Context, db.CosmeticSubscriptionEventInput) (*db.CosmeticSubscriptionEventResult, error)
	Current(context.Context, string) (*db.CosmeticSubscription, error)
}

type databaseCosmeticSubscriptionStore struct{}

func (databaseCosmeticSubscriptionStore) Reserve(ctx context.Context, accountID string, presentation CosmeticCheckoutPresentation) (*db.CosmeticSubscription, bool, error) {
	return db.ReserveCosmeticSubscriptionCheckout(ctx, accountID, string(presentation))
}

func (databaseCosmeticSubscriptionStore) Attach(ctx context.Context, accountID, subscriptionID, checkoutSessionID string) (*db.CosmeticSubscription, error) {
	return db.AttachCosmeticSubscriptionCheckout(ctx, accountID, subscriptionID, checkoutSessionID)
}

func (databaseCosmeticSubscriptionStore) ExpireCheckout(ctx context.Context, accountID, subscriptionID, checkoutSessionID string) (*db.CosmeticSubscription, error) {
	return db.ExpireCosmeticSubscriptionCheckout(ctx, accountID, subscriptionID, checkoutSessionID)
}

func (databaseCosmeticSubscriptionStore) Process(ctx context.Context, event db.CosmeticSubscriptionEventInput) (*db.CosmeticSubscriptionEventResult, error) {
	return db.ProcessCosmeticSubscriptionEvent(ctx, event)
}

func (databaseCosmeticSubscriptionStore) Current(ctx context.Context, accountID string) (*db.CosmeticSubscription, error) {
	return db.GetCustomerCosmeticSubscription(ctx, accountID)
}

type cosmeticSubscriptionCheckoutProvider interface {
	CreateSubscriptionCheckoutSession(context.Context, CosmeticSubscriptionCheckoutRequest) (*CosmeticCheckoutSession, error)
}

type cosmeticSubscriptionPortalProvider interface {
	CreateBillingPortalSession(context.Context, string) (*CosmeticPortalSession, error)
}

type cosmeticSubscriptionCheckoutRetriever interface {
	RetrieveSubscriptionCheckoutSession(context.Context, string) (*CosmeticCheckoutSession, error)
}

type cosmeticSubscriptionStateRetriever interface {
	RetrieveCosmeticSubscription(context.Context, string) (*CosmeticSubscriptionProviderState, error)
}

type cosmeticSubscriptionCheckoutBody struct {
	Presentation CosmeticCheckoutPresentation `json:"presentation"`
}

func newCosmeticCommerceHandlerWithSubscriptionStore(
	store cosmeticCommerceStore,
	subscriptions cosmeticSubscriptionStore,
	provider CosmeticPaymentProvider,
	enabled bool,
) *CosmeticCommerceHandler {
	handler := newCosmeticCommerceHandlerWithStore(store, provider, enabled)
	handler.subscriptionStore = subscriptions
	return handler
}

func (h *CosmeticCommerceHandler) subscriptionReady() bool {
	if h == nil || !h.Enabled() || h.subscriptionStore == nil {
		return false
	}
	_, checkoutOK := h.provider.(cosmeticSubscriptionCheckoutProvider)
	return checkoutOK
}

func (h *CosmeticCommerceHandler) SubscriptionCheckout(w http.ResponseWriter, r *http.Request) {
	// Starting a subscription. Refused before any state is read or written, so
	// nothing here half-happens on the way to a handoff.
	if refuseWhenPurchasingMoved(w) {
		return
	}
	setCustomerNoStore(w)
	if !h.subscriptionReady() {
		writeError(w, http.StatusServiceUnavailable, "cosmetic subscription checkout is not available")
		return
	}
	session, ok := customerSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "customer authentication required")
		return
	}
	presentation := CosmeticCheckoutPresentationEmbedded
	if r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var request cosmeticSubscriptionCheckoutBody
		if err := decodeStrictCosmeticAdminJSON(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid cosmetic subscription checkout request")
			return
		}
		var valid bool
		presentation, valid = normalizeCosmeticCheckoutPresentation(request.Presentation)
		if !valid {
			writeError(w, http.StatusBadRequest, "presentation must be embedded or hosted")
			return
		}
	}
	subscription, reservedNew, ok := h.createCosmeticSubscriptionCheckoutRecord(w, r, session.AccountID, presentation)
	if !ok {
		return
	}
	presentation, valid := storedCosmeticCheckoutPresentation(subscription.CheckoutPresentation)
	if !valid {
		slog.Error("cosmetic subscription has an invalid persisted checkout presentation", "subscription_id", subscription.ID)
		writeError(w, http.StatusInternalServerError, "failed to create cosmetic subscription")
		return
	}
	provider := h.provider.(cosmeticSubscriptionCheckoutProvider)
	if subscription.Status == db.CosmeticSubscriptionStatusCheckoutPending {
		retriever, supportsRetrieval := h.provider.(cosmeticSubscriptionCheckoutRetriever)
		if !supportsRetrieval || subscription.CheckoutSessionID == "" {
			writeError(w, http.StatusServiceUnavailable, "cosmetic subscription checkout cannot be resumed")
			return
		}
		providerCtx, cancel := context.WithTimeout(r.Context(), cosmeticCheckoutTimeout)
		checkout, err := retriever.RetrieveSubscriptionCheckoutSession(providerCtx, subscription.CheckoutSessionID)
		cancel()
		if err != nil || !validRetrievedCosmeticSubscriptionCheckout(checkout, subscription) {
			slog.Error("failed to retrieve cosmetic subscription checkout", "subscription_id", subscription.ID, "error", err)
			writeError(w, http.StatusBadGateway, "failed to resume cosmetic subscription checkout")
			return
		}
		switch checkout.Status {
		case CosmeticCheckoutSessionStatusOpen:
			if !validCosmeticCheckoutSession(checkout) {
				writeError(w, http.StatusBadGateway, "failed to resume cosmetic subscription checkout")
				return
			}
			writeCosmeticSubscriptionCheckoutResponse(w, http.StatusOK, subscription.ID, checkout, true)
			return
		case CosmeticCheckoutSessionStatusComplete:
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error": "This checkout is already complete. Refresh the account before trying again.",
				"code":  "CHECKOUT_COMPLETE",
			})
			return
		case CosmeticCheckoutSessionStatusExpired:
			if _, err := h.subscriptionStore.ExpireCheckout(r.Context(), subscription.AccountID, subscription.ID, checkout.ID); err != nil {
				slog.Error("failed to expire provider-confirmed cosmetic subscription checkout", "subscription_id", subscription.ID, "error", err)
				writeError(w, http.StatusInternalServerError, "failed to replace expired cosmetic subscription checkout")
				return
			}
			subscription, reservedNew, ok = h.createCosmeticSubscriptionCheckoutRecord(w, r, session.AccountID, presentation)
			if !ok {
				return
			}
			presentation, valid = storedCosmeticCheckoutPresentation(subscription.CheckoutPresentation)
			if !valid {
				writeError(w, http.StatusInternalServerError, "failed to create cosmetic subscription")
				return
			}
		default:
			writeError(w, http.StatusBadGateway, "cosmetic subscription checkout has an unknown provider state")
			return
		}
	}
	if subscription.Status != db.CosmeticSubscriptionStatusCreated {
		writeError(w, http.StatusConflict, "an existing cosmetic subscription must be managed instead")
		return
	}
	providerCtx, cancel := context.WithTimeout(r.Context(), cosmeticCheckoutTimeout)
	checkout, err := provider.CreateSubscriptionCheckoutSession(providerCtx, CosmeticSubscriptionCheckoutRequest{
		SubscriptionID: subscription.ID,
		AccountID:      subscription.AccountID,
		CustomerEmail:  subscription.AccountEmail,
		CustomerID:     subscription.CustomerID,
		Presentation:   presentation,
	})
	cancel()
	if err != nil || !validCosmeticCheckoutSession(checkout) || checkout.Presentation != presentation {
		// A provider timeout can be an ambiguous success. Keep the local record
		// retryable so the subscription-ID idempotency key recovers the same
		// Checkout Session instead of creating a parallel payable session.
		slog.Error("failed to create hosted cosmetic subscription checkout", "subscription_id", subscription.ID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to open cosmetic subscription checkout")
		return
	}
	if _, err := h.subscriptionStore.Attach(r.Context(), subscription.AccountID, subscription.ID, checkout.ID); err != nil {
		// The remote session is real. Leave this record retryable; the next call
		// uses the same idempotency key, receives the same session, and retries
		// the local attach.
		slog.Error("failed to attach cosmetic subscription checkout", "subscription_id", subscription.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save cosmetic subscription checkout")
		return
	}
	status := http.StatusCreated
	if !reservedNew {
		status = http.StatusOK
	}
	writeCosmeticSubscriptionCheckoutResponse(w, status, subscription.ID, checkout, !reservedNew)
}

func validRetrievedCosmeticSubscriptionCheckout(checkout *CosmeticCheckoutSession, subscription *db.CosmeticSubscription) bool {
	if checkout == nil || subscription == nil {
		return false
	}
	presentation, ok := storedCosmeticCheckoutPresentation(subscription.CheckoutPresentation)
	return ok && checkout.ID == subscription.CheckoutSessionID &&
		checkout.Mode == "subscription" && checkout.SubscriptionID == subscription.ID &&
		checkout.AccountID == subscription.AccountID && checkout.Presentation == presentation
}

func (h *CosmeticCommerceHandler) createCosmeticSubscriptionCheckoutRecord(w http.ResponseWriter, r *http.Request, accountID string, presentation CosmeticCheckoutPresentation) (*db.CosmeticSubscription, bool, bool) {
	subscription, created, err := h.subscriptionStore.Reserve(r.Context(), accountID, presentation)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrCosmeticSubscriptionExists):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, db.ErrCustomerAPIKeyLimit):
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error":  "This account has more than 5 active API keys. Deactivate keys until 5 or fewer remain before subscribing.",
				"code":   "API_KEY_LIMIT",
				"action": "Deactivate API keys from the account dashboard, then retry subscription checkout.",
				"limit":  db.CosmeticSubscriptionMaxAPIKeys,
			})
		case errors.Is(err, db.ErrCustomerAccountUnverified):
			writeError(w, http.StatusForbidden, "a verified customer email is required")
		case errors.Is(err, db.ErrNoDatabase):
			writeError(w, http.StatusServiceUnavailable, "cosmetic subscriptions are unavailable")
		default:
			slog.Error("failed to create cosmetic subscription", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to create cosmetic subscription")
		}
		return nil, false, false
	}
	if subscription == nil || subscription.ID == "" || subscription.AccountID != accountID ||
		subscription.PriceCents != db.CosmeticSubscriptionPriceCents ||
		subscription.Currency != db.CosmeticSubscriptionCurrency ||
		subscription.Interval != db.CosmeticSubscriptionInterval {
		writeError(w, http.StatusInternalServerError, "failed to create cosmetic subscription")
		return nil, false, false
	}
	return subscription, created, true
}

func writeCosmeticSubscriptionCheckoutResponse(w http.ResponseWriter, status int, subscriptionID string, checkout *CosmeticCheckoutSession, resumed bool) {
	response := map[string]interface{}{"subscription_id": subscriptionID}
	addCosmeticCheckoutResponseFields(response, checkout)
	if resumed {
		response["resumed"] = true
	}
	if !checkout.ExpiresAt.IsZero() {
		response["expires_at"] = checkout.ExpiresAt
	}
	writeJSON(w, status, response)
}

func (h *CosmeticCommerceHandler) SubscriptionPortal(w http.ResponseWriter, r *http.Request) {
	/*
	 * Deliberately NOT gated on the handoff, unlike every other endpoint here.
	 *
	 * A subscription started before the switch is live money in Arena's own
	 * Stripe account, and this portal is the only place its holder can pause
	 * or cancel it. Redirecting them to an Angel account that does not hold
	 * that subscription would leave a recurring charge nobody can reach.
	 *
	 * The repository already decided this, and a test already asserts it:
	 * TestCosmeticSubscriptionPortalRemainsAvailableWhenNewSalesArePaused.
	 * Managing an existing subscription is not a sale, so it does not stop
	 * when sales do. Starting a new one is, and SubscriptionCheckout above is
	 * gated accordingly.
	 */
	setCustomerNoStore(w)
	if h == nil || h.provider == nil || h.subscriptionStore == nil {
		writeError(w, http.StatusServiceUnavailable, "cosmetic subscription billing portal is not available")
		return
	}
	provider, ok := h.provider.(cosmeticSubscriptionPortalProvider)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "cosmetic subscription billing portal is not available")
		return
	}
	session, authenticated := customerSession(r)
	if !authenticated {
		writeError(w, http.StatusUnauthorized, "customer authentication required")
		return
	}
	subscription, err := h.subscriptionStore.Current(r.Context(), session.AccountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load cosmetic subscription")
		return
	}
	if subscription == nil || subscription.AccountID != session.AccountID || !subscription.CanManage || subscription.CustomerID == "" {
		writeError(w, http.StatusConflict, "no manageable cosmetic subscription was found")
		return
	}
	providerCtx, cancel := context.WithTimeout(r.Context(), cosmeticCheckoutTimeout)
	portal, err := provider.CreateBillingPortalSession(providerCtx, subscription.CustomerID)
	cancel()
	if err != nil || portal == nil || !validHostedHTTPSURL(portal.URL) {
		writeError(w, http.StatusBadGateway, "failed to open cosmetic subscription billing portal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"portal_url": portal.URL})
}

func databaseCosmeticSubscriptionEvent(event CosmeticPaymentEvent) (db.CosmeticSubscriptionEventInput, error) {
	eventType := ""
	switch event.Type {
	case CosmeticPaymentEventSubscriptionCheckoutCompleted:
		eventType = db.CosmeticStripeSubscriptionCheckoutCompleted
	case CosmeticPaymentEventSubscriptionCheckoutExpired:
		eventType = db.CosmeticStripeSubscriptionCheckoutExpired
	case CosmeticPaymentEventSubscriptionCreated:
		eventType = db.CosmeticStripeSubscriptionCreated
	case CosmeticPaymentEventSubscriptionUpdated:
		eventType = db.CosmeticStripeSubscriptionUpdated
	case CosmeticPaymentEventSubscriptionDeleted:
		eventType = db.CosmeticStripeSubscriptionDeleted
	default:
		return db.CosmeticSubscriptionEventInput{}, ErrUnsupportedCosmeticPaymentEvent
	}
	return db.CosmeticSubscriptionEventInput{
		Provider:                "stripe",
		EventID:                 strings.TrimSpace(event.ID),
		EventType:               eventType,
		PayloadHash:             strings.ToLower(strings.TrimSpace(event.PayloadSHA256)),
		SubscriptionID:          strings.TrimSpace(event.SubscriptionID),
		AccountID:               strings.TrimSpace(event.AccountID),
		CheckoutSessionID:       strings.TrimSpace(event.CheckoutSessionID),
		ProviderSubscriptionID:  strings.TrimSpace(event.ProviderSubscriptionID),
		CustomerID:              strings.TrimSpace(event.CustomerID),
		Status:                  strings.ToLower(strings.TrimSpace(event.SubscriptionStatus)),
		CancelAtPeriodEnd:       event.CancelAtPeriodEnd,
		CurrentPeriodEnd:        event.CurrentPeriodEnd,
		ProviderCreatedAt:       event.ProviderCreatedAt,
		ProviderStateObservedAt: event.ProviderStateObservedAt,
		Terminal:                event.Terminal,
	}, nil
}
