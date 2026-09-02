package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"arena-server/internal/accounts"
	"arena-server/internal/config"
	"arena-server/internal/db"
	"arena-server/internal/platform"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// customerSessionSlideThreshold controls sliding renewal: a session whose
// remaining lifetime has dropped below this fraction of the full TTL is
// extended back out to a full TTL on next use. This is what makes "stay
// signed in" mean something beyond the raw TTL: an account used at least
// this often never has to sign in again, while a cookie that is stolen and
// never used by its real owner still expires.
const customerSessionSlideFraction = 0.5

// generateToken creates a cryptographically random hex token. It backs every
// unguessable value a sign-in produces: the OAuth state, the browser-binding
// cookie, the PKCE verifier, the nonce, the session id, and the CSRF token.
func generateToken(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func hashSessionToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

const (
	customerSessionCookieName = "arena_customer_session"
	customerStateCookieName   = "arena_customer_oauth_state"
	customerStateTTL          = 10 * time.Minute

	// The page a popup sign-in lands on. Static, same-origin with whatever
	// opened it, and does nothing but hand the news over and close.
	customerPopupLandingFile = "signed-in.html"
)

// CustomerSession is intentionally separate from OIDCSession. In particular,
// this cookie and context value are never accepted by admin middleware.
type CustomerSession struct {
	AccountID       string
	Email           string
	Name            string
	Subject         string
	EmailVerifiedAt *time.Time
	CSRFToken       string
	CreatedAt       time.Time
	ExpiresAt       time.Time
	// platformAdmin is the Angel Accounts administrator claim from the
	// sign-in that created this session, or nil for the overwhelming
	// majority of sessions. It is memory-only: nothing writes it to
	// customer_sessions, so a restart, a second replica, or simply waiting
	// it out all resolve to "not an administrator". See platform_admin.go.
	platformAdmin *platformAdminGrant
}

type customerOIDCTransaction struct {
	ExpiresAt            time.Time
	BrowserBindingDigest [sha256.Size]byte
	Nonce                string
	PKCEVerifier         string
	ReturnTo             string
	// Popup is remembered here rather than round-tripped through the
	// provider. Where the browser lands at the end of the flow is Arena's
	// decision about its own UI, and putting it in the request would let
	// anything that can craft a login URL choose it.
	Popup bool
}

type CustomerOIDCHandler struct {
	oauth2Config *oauth2.Config
	verifier     *oidc.IDTokenVerifier
	issuer       string
	// See config.CustomerLinkLegacyByEmail. False means no sign-in carries an
	// address at any point, not even in memory.
	linkLegacyByEmail bool
	// Where what-somebody-owns is read from, or nil while Accounts does not
	// own commerce here yet. See customer_entitlements.go for why this is only
	// ever used inside the callback.
	entitlements *accounts.Client
	authority    platform.IdentityAuthority
	// onSubscriptionSynced is told which linked bots a sign-in's
	// subscription sync affected, so the ones in the arena can be re-read.
	// Set by the router, which is where the engine lives.
	onSubscriptionSynced func(context.Context, []string)

	sessions map[string]*CustomerSession
	states   map[string]customerOIDCTransaction
	mu       sync.RWMutex
}

type customerSessionContextKey struct{}

// customerAccountAuthEnabled reports whether a customer can sign in at all.
//
// One way in now. Signing in is an Accounts sign-in; there is no second,
// Arena-operated path that mails somebody a link, because operating one meant
// holding the address it was mailed to.
func customerAccountAuthEnabled(handler *CustomerOIDCHandler) bool {
	return handler != nil && handler.oauth2Config != nil
}

// activeCustomerOIDC is the handler whose session cache serves requests, so
// plain handlers that change what the cache copies (the profile rename) can
// reach it without threading the handler through every route constructor.
var (
	activeCustomerOIDCMu sync.RWMutex
	activeCustomerOIDC   *CustomerOIDCHandler
)

func activeCustomerOIDCHandler() *CustomerOIDCHandler {
	activeCustomerOIDCMu.RLock()
	defer activeCustomerOIDCMu.RUnlock()
	return activeCustomerOIDC
}

func newCustomerOIDCHandlerWithAuthority(authority platform.IdentityAuthority) *CustomerOIDCHandler {
	cfg := &config.C
	if !cfg.CustomerOIDCEnabled {
		return nil
	}
	h := &CustomerOIDCHandler{
		sessions:  make(map[string]*CustomerSession),
		states:    make(map[string]customerOIDCTransaction),
		authority: authority,
	}
	activeCustomerOIDCMu.Lock()
	activeCustomerOIDC = h
	activeCustomerOIDCMu.Unlock()
	if cfg.CustomerOIDCEnabled {
		if cfg.CustomerOIDCIssuer == "" || cfg.CustomerOIDCClientID == "" ||
			cfg.CustomerOIDCClientSecret == "" || cfg.CustomerOIDCRedirectURI == "" {
			slog.Warn("customer OIDC enabled but missing required config")
			return nil
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			provider, err := oidc.NewProvider(ctx, cfg.CustomerOIDCIssuer)
			cancel()
			if err != nil {
				slog.Error("failed to initialise customer OIDC provider", "issuer", cfg.CustomerOIDCIssuer, "error", err)
				return nil
			} else {
				/*
				 * `email` is requested only while legacy accounts are still
				 * being linked, and is the first thing to go when they are
				 * not. Asking for a claim we have no intention of storing is
				 * defensible for the length of a cutover and indefensible
				 * afterwards — it shows up on the consent screen as something
				 * Arena wants, and the honest answer at that point is that it
				 * does not.
				 */
				scopes := []string{oidc.ScopeOpenID, "profile"}
				if cfg.CustomerLinkLegacyByEmail {
					scopes = append(scopes, "email")
				}
				/*
				 * `entitlements` is asked for only when there is somewhere to
				 * spend it. The endpoint is advertised in the discovery
				 * document, so this configures itself: while the Accounts side
				 * has not provisioned Arena's client, no scope is requested and
				 * no read is attempted; the day it publishes the endpoint,
				 * Arena starts asking. No flag day, and no consent screen
				 * listing a permission that would go unused.
				 */
				h.entitlements = accounts.NewClient(entitlementsEndpoint(provider), nil)
				if h.entitlements != nil {
					scopes = append(scopes, "entitlements")
					slog.Info("customer entitlements source configured", "endpoint", h.entitlements.Endpoint())
				}
				h.linkLegacyByEmail = cfg.CustomerLinkLegacyByEmail
				h.oauth2Config = &oauth2.Config{
					ClientID:     cfg.CustomerOIDCClientID,
					ClientSecret: cfg.CustomerOIDCClientSecret,
					RedirectURL:  cfg.CustomerOIDCRedirectURI,
					Endpoint:     provider.Endpoint(),
					Scopes:       scopes,
				}
				h.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.CustomerOIDCClientID})
				h.issuer = cfg.CustomerOIDCIssuer
				slog.Info("customer OIDC auth initialised", "issuer", cfg.CustomerOIDCIssuer)
			}
		}
	}
	go h.cleanupLoop()
	return h
}

// entitlementsEndpoint decides where entitlements are read from.
//
// The discovery document is the source, so Accounts moves its endpoint and
// Arena follows without a deploy. The environment override wins because a
// staging Accounts and the tests both need to point somewhere the discovery
// document does not name.
func entitlementsEndpoint(provider *oidc.Provider) string {
	if override := strings.TrimSpace(config.C.AccountsEntitlementsURL); override != "" {
		return override
	}
	if provider == nil {
		return ""
	}
	var discovery struct {
		EntitlementsEndpoint string `json:"entitlements_endpoint"`
	}
	if err := provider.Claims(&discovery); err != nil {
		slog.Warn("could not read the accounts discovery document", "error", err)
		return ""
	}
	return strings.TrimSpace(discovery.EntitlementsEndpoint)
}

func customerDashboardPath(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/arena/") {
		return "/arena/dashboard/"
	}
	return "/dashboard/"
}

func customerAPIDashboardPath(r *http.Request, suffix string) string {
	if strings.HasPrefix(r.URL.Path, "/arena/") {
		return "/arena/api/v1/dashboard" + suffix
	}
	return "/api/v1/dashboard" + suffix
}

func secureCookie(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func setCustomerNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

// customerReturnToPrefixes is the closed set of places a completed sign-in is
// allowed to land. Everything here is an app of Arena's own that a signed-in
// browser reaches with this cookie: the Dashboard, and — since the support
// desk in Angel Accounts became the only human way into administration — the
// Admin Panel, which an administrator now signs into exactly as a customer.
var customerReturnToPrefixes = []string{
	"/dashboard", "/arena/dashboard",
	"/admin", "/arena/admin",
}

func customerReturnToIsAllowed(path string) bool {
	for _, prefix := range customerReturnToPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func safeCustomerReturnTo(r *http.Request) string {
	raw := strings.TrimSpace(r.URL.Query().Get("return_to"))
	if raw == "" || len(raw) > 2048 || strings.Contains(raw, "\\") || strings.HasPrefix(raw, "//") {
		return customerDashboardPath(r)
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return customerDashboardPath(r)
	}
	if !customerReturnToIsAllowed(parsed.Path) {
		return customerDashboardPath(r)
	}
	return raw
}

func (h *CustomerOIDCHandler) LoginHandler(w http.ResponseWriter, r *http.Request) {
	setCustomerNoStore(w)
	if h == nil || h.oauth2Config == nil {
		writeError(w, http.StatusServiceUnavailable, "customer OIDC login is not configured")
		return
	}
	state := generateToken(32)
	browserBinding := generateToken(32)
	pkceVerifier := generateToken(32)
	nonce := generateToken(32)
	txn := customerOIDCTransaction{
		ExpiresAt:            time.Now().Add(customerStateTTL),
		BrowserBindingDigest: sha256.Sum256([]byte(browserBinding)),
		Nonce:                nonce,
		PKCEVerifier:         pkceVerifier,
		ReturnTo:             safeCustomerReturnTo(r),
		Popup:                r.URL.Query().Get("popup") == "1",
	}
	h.mu.Lock()
	h.states[state] = txn
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     customerStateCookieName,
		Value:    browserBinding,
		Path:     "/",
		MaxAge:   int(customerStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   secureCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
	authURL := h.oauth2Config.AuthCodeURL(state,
		oauth2.S256ChallengeOption(pkceVerifier),
		oauth2.SetAuthURLParam("nonce", nonce),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (h *CustomerOIDCHandler) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	setCustomerNoStore(w)
	if h == nil || h.oauth2Config == nil || h.verifier == nil {
		writeError(w, http.StatusServiceUnavailable, "customer OIDC login is not configured")
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	stateCookie, cookieErr := r.Cookie(customerStateCookieName)
	var txn customerOIDCTransaction
	validState := false
	if cookieErr == nil && state != "" && stateCookie.Value != "" {
		bindingDigest := sha256.Sum256([]byte(stateCookie.Value))
		h.mu.Lock()
		if candidate, exists := h.states[state]; exists && time.Now().Before(candidate.ExpiresAt) &&
			subtle.ConstantTimeCompare(bindingDigest[:], candidate.BrowserBindingDigest[:]) == 1 {
			txn = candidate
			validState = true
			delete(h.states, state)
		}
		h.mu.Unlock()
	}
	clearCustomerCookie(w, r, customerStateCookieName)
	if !validState {
		http.Error(w, "invalid or expired state parameter", http.StatusBadRequest)
		return
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		http.Error(w, "authentication failed", http.StatusForbidden)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, err := h.oauth2Config.Exchange(ctx, code, oauth2.VerifierOption(txn.PKCEVerifier))
	if err != nil {
		slog.Warn("customer OIDC token exchange failed", "error", err)
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "identity provider omitted id_token", http.StatusBadGateway)
		return
	}
	idToken, err := h.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		slog.Warn("customer OIDC id_token verification failed", "error", err)
		http.Error(w, "invalid identity token", http.StatusForbidden)
		return
	}
	if idToken.Nonce == "" || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(txn.Nonce)) != 1 {
		http.Error(w, "invalid identity token nonce", http.StatusForbidden)
		return
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		PreferredUser string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "invalid identity claims", http.StatusForbidden)
		return
	}
	/*
	 * An address is no longer required to sign in, and that is the point.
	 *
	 * Accounts has already decided who this person is; the id_token's subject
	 * is that decision, and it is what Arena binds to. Refusing a sign-in for
	 * want of an address would make Arena depend on a claim it has no reason
	 * to receive — and once the `email` scope is dropped at the end of the
	 * cutover, no sign-in would carry one and every one of them would fail.
	 *
	 * An address is still *used* when it arrives verified, for the one job
	 * described on the binder: matching a pre-cutover row so its owner keeps
	 * their bots and cosmetics. Unverified is treated as absent, because an
	 * unverified address is not evidence of anything and adopting an account
	 * on the strength of one would be an account takeover.
	 */
	linkEmail := ""
	if h.linkLegacyByEmail && claims.EmailVerified {
		linkEmail = strings.TrimSpace(claims.Email)
	}
	if claims.Name == "" {
		claims.Name = claims.PreferredUser
	}
	verifiedIssuer := strings.TrimSpace(idToken.Issuer)
	if verifiedIssuer == "" {
		verifiedIssuer = h.issuer
	}
	account, err := h.bindVerifiedIdentity(ctx, linkEmail, verifiedIssuer, idToken.Subject, claims.Name)
	if err != nil {
		slog.Warn("customer account binding failed", "error", err, "subject", idToken.Subject)
		http.Error(w, "unable to bind customer account", http.StatusConflict)
		return
	}
	if account.EmailVerifiedAt == nil {
		slog.Error("linked customer account is missing its identity-verified timestamp", "account_id", account.ID)
		http.Error(w, "unable to verify customer account", http.StatusInternalServerError)
		return
	}
	/*
	 * Whether this person administers the platform, read from the token the
	 * verifier has just accepted and from nowhere else. See platform_admin.go
	 * for the presence rule and for why the answer is not written down.
	 */
	platformAdmin := platformAdminFromIDToken(idToken)
	h.establishCustomerSession(w, r, account, idToken.Subject, platformAdmin)
	/*
	 * The one moment Arena holds a token it may read entitlements with. After
	 * this line the token goes out of scope and is never written anywhere —
	 * see customer_entitlements.go. The error is deliberately dropped: a
	 * commerce service that is unreachable must not turn a good sign-in into a
	 * failed one, and the failure is already logged where it happened.
	 */
	if _, syncErr := h.syncEntitlementsFromAccounts(ctx, account.ID, token.AccessToken); syncErr != nil {
		_ = syncErr
	}
	// The account id, and not the address. A log line is a place an address
	// outlives the database it was deleted from.
	slog.Info("customer signed in with Angel Accounts", "account_id", account.ID)
	if platformAdmin.Present {
		// Worth its own line: this is the sign-in that can reach the admin
		// panel, and an operator asking "who administered this" needs to be
		// able to find it — and to see which claim let them in.
		slog.Info("customer sign-in carries platform administrator authority",
			"account_id", account.ID, "authority", platformAdmin.authority(), "staff_role", platformAdmin.Role)
	}
	/*
	 * A popup finishes on a page of Arena's own, which tells the window that
	 * opened it and closes itself. The session cookie is already set by the
	 * time that page loads, so the opener has only to re-read it.
	 *
	 * Sending the popup to `ReturnTo` instead would leave a second, full copy
	 * of the dashboard sitting in a small window, and the person who pressed
	 * Sign in still looking at a signed-out one.
	 */
	destination := txn.ReturnTo
	if txn.Popup {
		destination = customerDashboardPath(r) + customerPopupLandingFile
	}
	http.Redirect(w, r, destination, http.StatusFound)
}

func (h *CustomerOIDCHandler) bindVerifiedIdentity(ctx context.Context, email, issuer, subject, displayName string) (*db.CustomerAccount, error) {
	return h.authority.UpsertVerifiedIdentity(ctx, email, issuer, subject, displayName)
}

func customerAccountAPIPath(r *http.Request, suffix string) string {
	if strings.HasPrefix(r.URL.Path, "/arena/") {
		return "/arena/api/v1/account" + suffix
	}
	return "/api/v1/account" + suffix
}

func (h *CustomerOIDCHandler) establishCustomerSession(w http.ResponseWriter, r *http.Request, account *db.CustomerAccount, subject string, admin verifiedPlatformAdmin) *CustomerSession {
	ttl := customerSessionTTL()
	now := time.Now().UTC()
	sessionID := generateToken(32)
	expires := now.Add(ttl)
	session := &CustomerSession{
		AccountID:       account.ID,
		Email:           account.Email,
		Name:            account.DisplayName,
		Subject:         subject,
		EmailVerifiedAt: account.EmailVerifiedAt,
		CSRFToken:       generateToken(32),
		CreatedAt:       now,
		ExpiresAt:       expires,
		platformAdmin:   newPlatformAdminGrant(admin, now, expires),
	}
	h.mu.Lock()
	h.sessions[sessionID] = session
	h.mu.Unlock()
	// Best effort: a database outage at login time just means the session
	// does not survive a restart, not that login fails.
	_ = db.InsertCustomerSession(r.Context(), hashSessionToken(sessionID), account.ID, session.CSRFToken, now, session.ExpiresAt)
	http.SetCookie(w, &http.Cookie{
		Name:     customerSessionCookieName,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   secureCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
	return session
}

// customerSessionTTL is the absolute lifetime granted to a freshly
// established or freshly slid-forward session.
func customerSessionTTL() time.Duration {
	ttl := time.Duration(config.C.CustomerOIDCSessionTTL) * time.Hour
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	return ttl
}

// refreshSessionCookie re-issues the session cookie with MaxAge recalculated
// from the session's current ExpiresAt. GetSession slides ExpiresAt forward
// server-side on an active session, but the browser cookie's own MaxAge was
// fixed at login time; without this, the cookie itself would still vanish on
// schedule even though the server considers the session renewed. Called from
// the handlers a signed-in visitor's browser hits on every page load, so an
// active user's cookie lifetime tracks the sliding server expiry.
func (h *CustomerOIDCHandler) refreshSessionCookie(w http.ResponseWriter, r *http.Request, session *CustomerSession) {
	cookie, err := r.Cookie(customerSessionCookieName)
	if err != nil || cookie.Value == "" {
		return
	}
	maxAge := int(time.Until(h.sessionExpiry(session)).Seconds())
	if maxAge <= 0 {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     customerSessionCookieName,
		Value:    cookie.Value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secureCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCustomerCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: secureCookie(r), SameSite: http.SameSiteLaxMode,
	})
}

// LogoutHandler is a mutation and is only mounted behind customer auth,
// same-origin Origin validation, and CSRF validation.
func (h *CustomerOIDCHandler) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	setCustomerNoStore(w)
	if cookie, err := r.Cookie(customerSessionCookieName); err == nil {
		h.mu.Lock()
		delete(h.sessions, cookie.Value)
		h.mu.Unlock()
		_ = db.DeleteCustomerSession(r.Context(), hashSessionToken(cookie.Value))
	}
	clearCustomerCookie(w, r, customerSessionCookieName)
	writeJSON(w, http.StatusOK, map[string]any{
		"logged_out":  true,
		"redirect_to": safeCustomerReturnTo(r),
	})
}

func (h *CustomerOIDCHandler) GetSession(r *http.Request) *CustomerSession {
	cookie, err := r.Cookie(customerSessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	h.mu.RLock()
	session := h.sessions[cookie.Value]
	h.mu.RUnlock()

	if session == nil {
		// Cache miss: either this process just restarted, or the session was
		// established by a different replica. Fall back to the durable copy
		// and, if found, rehydrate the in-memory cache so future lookups on
		// this process are fast again. A missing database (dev mode, or a
		// session that really does not exist) resolves to "not signed in".
		row, err := db.GetCustomerSessionByTokenHash(r.Context(), hashSessionToken(cookie.Value))
		if err != nil || row == nil {
			return nil
		}
		session = &CustomerSession{
			AccountID:       row.AccountID,
			Email:           row.Email,
			Name:            row.DisplayName,
			EmailVerifiedAt: row.EmailVerifiedAt,
			CSRFToken:       row.CSRFToken,
			CreatedAt:       row.CreatedAt,
			ExpiresAt:       row.ExpiresAt,
		}
		if time.Now().After(session.ExpiresAt) {
			return nil
		}
		h.mu.Lock()
		h.sessions[cookie.Value] = session
		h.mu.Unlock()
	} else if time.Now().After(h.sessionExpiry(session)) {
		return nil
	}

	h.maybeSlideSessionExpiry(r.Context(), cookie.Value, session)
	return session
}

// sessionExpiry reads ExpiresAt under the handler lock.
//
// maybeSlideSessionExpiry writes the field under h.mu while two requests
// from the same browser can be in flight together, and time.Time is two
// words: an unsynchronised read could tear and make a live session read as
// expired mid-page, or the other way round. Every reader goes through here.
func (h *CustomerOIDCHandler) sessionExpiry(session *CustomerSession) time.Time {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return session.ExpiresAt
}

// ForgetAccountSessions drops every cached session for an account, so the
// next request rehydrates it from the database.
//
// The cache holds a copy of the display name for the whole sliding thirty-day
// session, and chat derives its visible handle from that copy; a rename in
// the dashboard otherwise kept posting under the old name until the process
// restarted. The durable row is untouched: the cookie stays valid.
func (h *CustomerOIDCHandler) ForgetAccountSessions(accountID string) {
	if h == nil || accountID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, session := range h.sessions {
		if session.AccountID == accountID {
			delete(h.sessions, id)
		}
	}
}

// maybeSlideSessionExpiry extends a session that is more than halfway to
// expiry back out to a full TTL, both in memory and (best effort) in the
// database. This is what makes a returning visitor stay signed in
// indefinitely while an abandoned or stolen cookie still lapses.
func (h *CustomerOIDCHandler) maybeSlideSessionExpiry(ctx context.Context, sessionID string, session *CustomerSession) {
	ttl := customerSessionTTL()
	remaining := time.Until(h.sessionExpiry(session))
	if remaining > time.Duration(float64(ttl)*customerSessionSlideFraction) {
		return
	}
	now := time.Now().UTC()
	newExpiry := now.Add(ttl)

	h.mu.Lock()
	if current := h.sessions[sessionID]; current == session {
		current.ExpiresAt = newExpiry
	}
	h.mu.Unlock()

	_ = db.TouchCustomerSession(ctx, hashSessionToken(sessionID), now, newExpiry)
}

func (h *CustomerOIDCHandler) SessionInfoHandler(w http.ResponseWriter, r *http.Request) {
	setCustomerNoStore(w)
	session := h.GetSession(r)
	oidcEnabled := h != nil && h.oauth2Config != nil
	if session != nil {
		h.refreshSessionCookie(w, r, session)
	}
	if session == nil {
		/*
		 * `email_login_enabled` is still reported, and is always false.
		 *
		 * A deployed dashboard reads this document to decide what to draw, and
		 * a key that vanishes is a key that reads as `undefined` — which is
		 * falsey, so the old script does the right thing, but only by
		 * accident. Saying false out loud keeps a browser that has not
		 * reloaded its bundle correct on purpose, and the field can go once
		 * nothing asks for it.
		 */
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated":       false,
			"login_enabled":       oidcEnabled,
			"oidc_login_enabled":  oidcEnabled,
			"email_login_enabled": false,
			"login_url":           customerAPIDashboardPath(r, "/login"),
		})
		return
	}
	/*
	 * Whether this session administers the platform is reported so the site
	 * can offer an entry point to somebody who has one, and is recomputed on
	 * every read rather than remembered anywhere. It is a description of the
	 * session, never the thing that grants authority — the admin routes ask
	 * the same question themselves.
	 */
	platformAdminGrant, isPlatformAdmin := session.platformAdminGrantAt(time.Now())
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":  true,
		"login_enabled":  oidcEnabled,
		"platform_admin": isPlatformAdmin,
		// Which claim admitted the sign-in ("staff" or "product_admin"), and
		// the desk role when Accounts named one. Descriptive only.
		"platform_admin_authority": platformAdminGrant.Authority,
		"platform_admin_role":      platformAdminGrant.Role,
		"oidc_login_enabled":       oidcEnabled,
		"email_login_enabled":      false,
		"account": map[string]any{
			"id": session.AccountID,
			// Empty for a linked account, which is every account after the
			// cutover. The key stays so a dashboard that has not reloaded
			// still finds what it reads; there is simply nothing in it.
			"email":             session.Email,
			"display_name":      session.Name,
			"name":              session.Name,
			"email_verified":    session.EmailVerifiedAt != nil,
			"email_verified_at": session.EmailVerifiedAt,
		},
		"csrf_token":       session.CSRFToken,
		"created_at":       session.CreatedAt,
		"expires_at":       h.sessionExpiry(session),
		"login_url":        customerAPIDashboardPath(r, "/login"),
		"logout_url":       customerAPIDashboardPath(r, "/logout"),
		"email_start_url":  customerAccountAPIPath(r, "/email/start"),
		"email_verify_url": customerAccountAPIPath(r, "/email/verify"),
	})
}

func CustomerSessionUnavailableHandler(w http.ResponseWriter, r *http.Request) {
	setCustomerNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":       false,
		"login_enabled":       false,
		"oidc_login_enabled":  false,
		"email_login_enabled": false,
	})
}

func CustomerLoginUnavailableHandler(w http.ResponseWriter, _ *http.Request) {
	setCustomerNoStore(w)
	writeError(w, http.StatusServiceUnavailable, "customer login is not configured")
}

func CustomerSessionFromContext(ctx context.Context) *CustomerSession {
	session, _ := ctx.Value(customerSessionContextKey{}).(*CustomerSession)
	return session
}

func withCustomerSession(ctx context.Context, session *CustomerSession) context.Context {
	return context.WithValue(ctx, customerSessionContextKey{}, session)
}

func normalizedOriginPort(scheme, port string) string {
	if port != "" {
		return port
	}
	if strings.EqualFold(scheme, "https") {
		return "443"
	}
	return "80"
}

func customerMutationHasSameOrigin(r *http.Request) bool {
	rawOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	if rawOrigin == "" || strings.Contains(rawOrigin, ",") || strings.EqualFold(rawOrigin, "null") {
		return false
	}
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.User != nil || origin.Hostname() == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	scheme := "http"
	if secureCookie(r) {
		scheme = "https"
	}
	expected, err := url.Parse(scheme + "://" + r.Host)
	if err != nil || expected.Hostname() == "" {
		return false
	}
	return strings.EqualFold(origin.Scheme, scheme) &&
		strings.EqualFold(origin.Hostname(), expected.Hostname()) &&
		normalizedOriginPort(origin.Scheme, origin.Port()) == normalizedOriginPort(scheme, expected.Port())
}

func MakeCustomerAuthMiddleware(handler *CustomerOIDCHandler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			setCustomerNoStore(w)
			if handler == nil {
				writeError(w, http.StatusServiceUnavailable, "customer login is not configured")
				return
			}
			session := handler.GetSession(r)
			if session == nil {
				writeError(w, http.StatusUnauthorized, "customer authentication required")
				return
			}
			handler.refreshSessionCookie(w, r, session)
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				if !customerMutationHasSameOrigin(r) {
					writeError(w, http.StatusForbidden, "cross-origin customer mutation rejected")
					return
				}
				provided := r.Header.Get("X-CSRF-Token")
				if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(session.CSRFToken)) != 1 {
					writeError(w, http.StatusForbidden, "invalid CSRF token")
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(withCustomerSession(r.Context(), session)))
		})
	}
}

func (h *CustomerOIDCHandler) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		h.mu.Lock()
		for id, session := range h.sessions {
			if now.After(session.ExpiresAt) {
				delete(h.sessions, id)
			}
		}
		for state, txn := range h.states {
			if now.After(txn.ExpiresAt) {
				delete(h.states, state)
			}
		}
		h.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := db.DeleteExpiredCustomerSessions(ctx); err != nil && !errors.Is(err, db.ErrNoDatabase) {
			slog.Warn("failed to purge expired customer sessions", "error", err)
		}
		cancel()
	}
}
