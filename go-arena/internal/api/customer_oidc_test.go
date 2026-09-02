package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"arena-server/internal/accounts"
	"arena-server/internal/config"
	"arena-server/internal/db"
	"arena-server/internal/game"

	"golang.org/x/oauth2"
)

type fakeIdentityAuthority struct {
	account     *db.CustomerAccount
	email       string
	issuer      string
	subject     string
	displayName string

	// What the subscription sync recorded, in order. The fake plays the
	// account row: a sync that repeats the current flag is not a change.
	subscriptionCalls []fakeSubscriptionCall
	subscriptionBots  []string
}

type fakeSubscriptionCall struct {
	accountID string
	active    bool
}

func (f *fakeIdentityAuthority) UpsertVerifiedIdentity(_ context.Context, email, issuer, subject, displayName string) (*db.CustomerAccount, error) {
	f.email, f.issuer, f.subject, f.displayName = email, issuer, subject, displayName
	return f.account, nil
}

func (f *fakeIdentityAuthority) SetSubscription(_ context.Context, accountID string, active bool, _ time.Time) (*db.SubscriptionSyncChange, error) {
	f.subscriptionCalls = append(f.subscriptionCalls, fakeSubscriptionCall{accountID: accountID, active: active})
	changed := f.account == nil || f.account.SubscriptionActive != active
	if f.account != nil {
		f.account.SubscriptionActive = active
	}
	change := &db.SubscriptionSyncChange{AccountID: accountID, Active: active, Changed: changed}
	if changed {
		change.BotIDs = append([]string(nil), f.subscriptionBots...)
	}
	return change, nil
}

func newTestCustomerOIDCHandler() *CustomerOIDCHandler {
	return &CustomerOIDCHandler{
		oauth2Config: &oauth2.Config{
			ClientID:    "customer-client",
			RedirectURL: "https://arena.example/account/callback",
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://identity.example/authorize",
				TokenURL: "https://identity.example/token",
			},
		},
		issuer:   "https://identity.example",
		sessions: make(map[string]*CustomerSession),
		states:   make(map[string]customerOIDCTransaction),
	}
}

func addTestCustomerSession(t *testing.T, handler *CustomerOIDCHandler) (*CustomerSession, *http.Cookie) {
	t.Helper()
	verifiedAt := time.Now().UTC().Add(-time.Minute)
	session := &CustomerSession{
		AccountID:       "account-1",
		Email:           "owner@example.com",
		Name:            "Owner",
		Subject:         "subject-1",
		EmailVerifiedAt: &verifiedAt,
		CSRFToken:       "csrf-secret",
		CreatedAt:       time.Now().UTC().Add(-time.Minute),
		ExpiresAt:       time.Now().UTC().Add(time.Hour),
	}
	handler.sessions["session-secret"] = session
	return session, &http.Cookie{Name: customerSessionCookieName, Value: "session-secret"}
}

func TestCustomerLoginUsesBrowserBindingNonceAndPKCE(t *testing.T) {
	handler := newTestCustomerOIDCHandler()
	req := httptest.NewRequest(http.MethodGet,
		"https://arena.example/api/v1/dashboard/login?return_to=%2Fdashboard%2F%3Ftab%3Dcosmetics", nil)
	rec := httptest.NewRecorder()
	handler.LoginHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := location.Query()
	state := query.Get("state")
	if state == "" || query.Get("nonce") == "" || query.Get("code_challenge") == "" {
		t.Fatalf("authorization URL missing state/nonce/PKCE: %s", location.String())
	}
	if query.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", query.Get("code_challenge_method"))
	}

	var bindingCookie *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == customerStateCookieName {
			bindingCookie = cookie
			break
		}
	}
	if bindingCookie == nil || bindingCookie.Value == "" {
		t.Fatal("browser-binding cookie was not set")
	}
	if bindingCookie.Value == state {
		t.Fatal("browser binding must be distinct from the state in the redirect URL")
	}
	txn, ok := handler.states[state]
	if !ok {
		t.Fatal("server-side OIDC transaction was not recorded")
	}
	digest := sha256.Sum256([]byte(bindingCookie.Value))
	if digest != txn.BrowserBindingDigest {
		t.Fatal("state transaction is not bound to the browser cookie")
	}
	if query.Get("nonce") != txn.Nonce {
		t.Fatal("authorization nonce does not match the server-side transaction")
	}
	if query.Get("code_challenge") != oauth2.S256ChallengeFromVerifier(txn.PKCEVerifier) {
		t.Fatal("PKCE challenge does not match the stored verifier")
	}
	if txn.ReturnTo != "/dashboard/?tab=cosmetics" {
		t.Fatalf("return_to = %q", txn.ReturnTo)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestCustomerVerifiedIdentityUsesPlatformAuthority(t *testing.T) {
	verifiedAt := time.Now().UTC()
	authority := &fakeIdentityAuthority{account: &db.CustomerAccount{
		ID:              "account-platform",
		Email:           "owner@example.com",
		EmailVerifiedAt: &verifiedAt,
	}}
	handler := newTestCustomerOIDCHandler()
	handler.authority = authority

	account, err := handler.bindVerifiedIdentity(t.Context(), "owner@example.com", "https://identity.example", "subject-1", "Owner")
	if err != nil {
		t.Fatalf("bind verified identity: %v", err)
	}
	if account.ID != "account-platform" || authority.email != "owner@example.com" ||
		authority.issuer != "https://identity.example" || authority.subject != "subject-1" || authority.displayName != "Owner" {
		t.Fatalf("authority result=%+v call=%q/%q/%q/%q", account, authority.email, authority.issuer, authority.subject, authority.displayName)
	}
}

func TestCustomerSessionInfoHasNestedVerifiedAccountAndCSRF(t *testing.T) {
	handler := newTestCustomerOIDCHandler()
	session, cookie := addTestCustomerSession(t, handler)
	req := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/account/session", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.SessionInfoHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Authenticated bool `json:"authenticated"`
		LoginEnabled  bool `json:"login_enabled"`
		Account       struct {
			ID              string     `json:"id"`
			Email           string     `json:"email"`
			EmailVerified   bool       `json:"email_verified"`
			EmailVerifiedAt *time.Time `json:"email_verified_at"`
		} `json:"account"`
		CSRFToken string `json:"csrf_token"`
		LoginURL  string `json:"login_url"`
		LogoutURL string `json:"logout_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Authenticated || !payload.LoginEnabled || payload.Account.ID != session.AccountID ||
		payload.Account.Email != session.Email || !payload.Account.EmailVerified ||
		payload.Account.EmailVerifiedAt == nil || payload.CSRFToken != session.CSRFToken {
		t.Fatalf("unexpected session payload: %+v", payload)
	}
	if payload.LoginURL != "/api/v1/dashboard/login" || payload.LogoutURL != "/api/v1/dashboard/logout" {
		t.Fatalf("unexpected auth routes: login=%q logout=%q", payload.LoginURL, payload.LogoutURL)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestCustomerAuthMiddlewareRequiresSameOriginAndCSRFForMutations(t *testing.T) {
	handler := newTestCustomerOIDCHandler()
	_, cookie := addTestCustomerSession(t, handler)
	protected := MakeCustomerAuthMiddleware(handler)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if CustomerSessionFromContext(r.Context()) == nil {
			t.Error("customer session was not added to request context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name   string
		method string
		origin string
		csrf   string
		want   int
	}{
		{name: "safe read", method: http.MethodGet, want: http.StatusNoContent},
		{name: "missing origin", method: http.MethodPut, csrf: "csrf-secret", want: http.StatusForbidden},
		{name: "cross origin", method: http.MethodPut, origin: "https://evil.example", csrf: "csrf-secret", want: http.StatusForbidden},
		{name: "missing csrf", method: http.MethodPut, origin: "https://arena.example", want: http.StatusForbidden},
		{name: "bad csrf", method: http.MethodPut, origin: "https://arena.example", csrf: "wrong", want: http.StatusForbidden},
		{name: "valid mutation", method: http.MethodPut, origin: "https://arena.example", csrf: "csrf-secret", want: http.StatusNoContent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "https://arena.example/api/v1/account/cosmetics", strings.NewReader("{}"))
			req.AddCookie(cookie)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.csrf != "" {
				req.Header.Set("X-CSRF-Token", tc.csrf)
			}
			rec := httptest.NewRecorder()
			protected.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestCustomerLogoutIsProtectedMutationAndDeletesOnlyCustomerSession(t *testing.T) {
	handler := newTestCustomerOIDCHandler()
	_, cookie := addTestCustomerSession(t, handler)
	protected := MakeCustomerAuthMiddleware(handler)(http.HandlerFunc(handler.LogoutHandler))
	req := httptest.NewRequest(http.MethodPost,
		"https://arena.example/api/v1/dashboard/logout?return_to=%2Fdashboard%2F", nil)
	req.AddCookie(cookie)
	req.Header.Set("Origin", "https://arena.example")
	req.Header.Set("X-CSRF-Token", "csrf-secret")
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if handler.sessions["session-secret"] != nil {
		t.Fatal("customer session was not deleted")
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), customerSessionCookieName+"=") {
		t.Fatal("customer session cookie was not cleared")
	}
}

func TestCustomerCookieNeverAuthorizesAdmin(t *testing.T) {
	previous := config.C
	t.Cleanup(func() { config.C = previous })
	config.C.AdminLocalhostBypass = false
	config.C.AdminToken = "admin-secret"

	called := false
	protected := MakeAdminAuthMiddlewareWithPlatformAdmins(nil, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/admin/status", nil)
	req.RemoteAddr = "198.51.100.10:4444"
	req.AddCookie(&http.Cookie{Name: customerSessionCookieName, Value: "customer-only"})
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if called || rec.Code != http.StatusUnauthorized {
		t.Fatalf("customer cookie admin authorization: called=%v status=%d", called, rec.Code)
	}
}

func TestCustomerDashboardAuthRoutesExistWhenLoginDisabled(t *testing.T) {
	previous := config.C
	t.Cleanup(func() { config.C = previous })
	config.C.CustomerOIDCEnabled = false
	router := NewRouter(game.NewGameEngine())

	for _, path := range []string{"/api/v1/dashboard/login", "/arena/api/v1/dashboard/login"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s status = %d, want 503", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", path, got)
		}
	}
	for _, path := range []string{"/api/v1/dashboard/logout", "/arena/api/v1/dashboard/logout"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("POST %s status = %d, want 503", path, rec.Code)
		}
	}
}

func newTestEntitlementsClient(server *httptest.Server) *accounts.Client {
	return accounts.NewClient(server.URL, server.Client())
}

func accountsEntitlementsClientAt(endpoint string) *accounts.Client {
	return accounts.NewClient(endpoint, nil)
}
