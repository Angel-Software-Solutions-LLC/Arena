package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"arena-server/internal/config"
	"arena-server/internal/game"
)

/*
 * The support-desk role in Angel Accounts is the single source of HUMAN admin
 * authority. Arena used to keep a second one: an admin-SSO application with
 * its own email allowlist (ARENA_OIDC_ADMIN_EMAILS) and its own
 * arena_admin_session cookie. These tests pin the retirement.
 *
 * They are deliberately written against environment variables and routes
 * rather than Go fields, so they say the same thing before and after the
 * allowlist stopped existing in code: setting it changes nothing.
 */

// loadConfigWithRetiredAdminSSO sets every variable the old admin-SSO
// application needed — pointed at an issuer that really answers discovery, so
// that "the handler could not reach its IdP" is not what makes this pass — and
// loads the configuration the way a deployment does.
func loadConfigWithRetiredAdminSSO(t *testing.T, issuer string) {
	t.Helper()
	previous := config.C
	t.Cleanup(func() { config.C = previous })
	t.Setenv("ARENA_OIDC_ENABLED", "true")
	t.Setenv("ARENA_OIDC_ISSUER", issuer)
	t.Setenv("ARENA_OIDC_CLIENT_ID", "arena-admin")
	t.Setenv("ARENA_OIDC_CLIENT_SECRET", "admin-secret")
	t.Setenv("ARENA_OIDC_REDIRECT_URI", "https://arena.example/admin/callback")
	t.Setenv("ARENA_OIDC_ADMIN_EMAILS", "owner@example.com,operator@example.com")
	t.Setenv("ARENA_CUSTOMER_OIDC_ENABLED", "false")
	config.Load()
}

// TestRetiredAdminEmailAllowlistOpensNoAdminSSOFlow is the retirement itself:
// an address on the old allowlist has nowhere to sign in.
func TestRetiredAdminEmailAllowlistOpensNoAdminSSOFlow(t *testing.T) {
	accounts := newAngelAccounts(t)
	loadConfigWithRetiredAdminSSO(t, accounts.server.URL)
	router := NewRouter(game.NewGameEngine())

	for _, path := range []string{
		"/admin/login", "/admin/callback", "/admin/logout",
		"/arena/admin/login", "/arena/admin/callback", "/arena/admin/logout",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404 — the admin SSO flow is retired", path, recorder.Code)
		}
	}
}

// TestRetiredAdminSessionCookieAuthorizesNothing covers the other half: the
// cookie that flow used to mint carries no authority any more, whatever it
// holds.
func TestRetiredAdminSessionCookieAuthorizesNothing(t *testing.T) {
	accounts := newAngelAccounts(t)
	loadConfigWithRetiredAdminSSO(t, accounts.server.URL)
	config.C.AdminLocalhostBypass = false
	config.C.AdminToken = "admin-secret"
	router := NewRouter(game.NewGameEngine())

	request := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/admin/dashboard/overview", nil)
	request.RemoteAddr = "198.51.100.10:4444"
	request.AddCookie(&http.Cookie{Name: "arena_admin_session", Value: "a-session-the-old-flow-would-have-minted"})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("admin route status with an arena_admin_session cookie = %d, want 401", recorder.Code)
	}
}

// TestAdminSessionEndpointReportsNoAdminSSOLogin keeps the admin panel's
// bootstrap honest: there is no SSO button to draw.
func TestAdminSessionEndpointReportsNoAdminSSOLogin(t *testing.T) {
	accounts := newAngelAccounts(t)
	loadConfigWithRetiredAdminSSO(t, accounts.server.URL)
	router := NewRouter(game.NewGameEngine())

	for _, path := range []string{"/api/v1/admin/session", "/arena/api/v1/admin/session"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, recorder.Code)
		}
		var payload struct {
			Authenticated bool `json:"authenticated"`
			LoginEnabled  bool `json:"login_enabled"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("GET %s response: %v", path, err)
		}
		if payload.Authenticated || payload.LoginEnabled {
			t.Fatalf("GET %s payload = %+v, want no admin SSO session and no login offer", path, payload)
		}
	}
}

/*
 * Everything below is the other half of the retirement: what must NOT have
 * changed. The machine paths authenticate automation, not people, and they
 * are also the break-glass path if Accounts is unreachable — so they are
 * exercised here through the same guard, in the same request shapes, as
 * before.
 */

// TestMachineAdminPathsAuthorizeExactlyAsBefore covers all three at once, and
// pins the principal each one records.
func TestMachineAdminPathsAuthorizeExactlyAsBefore(t *testing.T) {
	previous := config.C
	t.Cleanup(func() { config.C = previous })
	config.C.AdminToken = "env-admin-token"
	config.C.AdminLocalhostBypass = false

	databaseIssued := "database-issued-admin-token"
	adminHandler := &AdminHandler{tokenHashes: []string{hashToken(databaseIssued)}}

	call := func(t *testing.T, decorate func(*http.Request)) (int, string) {
		t.Helper()
		principal := ""
		guarded := MakeAdminAuthMiddlewareWithPlatformAdmins(adminHandler, nil)(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				principal = adminPrincipalFromContext(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))
		request := httptest.NewRequest(http.MethodPut, "https://arena.example/api/v1/admin/chat/enabled", nil)
		request.RemoteAddr = "198.51.100.10:4444"
		decorate(request)
		recorder := httptest.NewRecorder()
		guarded.ServeHTTP(recorder, request)
		return recorder.Code, principal
	}

	t.Run("ARENA_ADMIN_TOKEN", func(t *testing.T) {
		status, principal := call(t, func(r *http.Request) {
			r.Header.Set("X-Admin-Token", "env-admin-token")
		})
		if status != http.StatusNoContent || principal != "admin-token:environment" {
			t.Fatalf("status = %d principal = %q", status, principal)
		}
	})

	t.Run("database-issued admin token", func(t *testing.T) {
		status, principal := call(t, func(r *http.Request) {
			r.Header.Set("X-Admin-Token", databaseIssued)
		})
		wantPrincipal := "admin-token:" + hashToken(databaseIssued)[:12]
		if status != http.StatusNoContent || principal != wantPrincipal {
			t.Fatalf("status = %d principal = %q, want %q", status, principal, wantPrincipal)
		}
	})

	t.Run("a wrong X-Admin-Token is still refused", func(t *testing.T) {
		if status, _ := call(t, func(r *http.Request) {
			r.Header.Set("X-Admin-Token", "not-the-token")
		}); status != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", status)
		}
	})

	t.Run("no credential at all is still refused", func(t *testing.T) {
		if status, _ := call(t, func(*http.Request) {}); status != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", status)
		}
	})

	t.Run("localhost bypass", func(t *testing.T) {
		config.C.AdminLocalhostBypass = true
		t.Cleanup(func() { config.C.AdminLocalhostBypass = false })
		status, principal := call(t, func(r *http.Request) { r.RemoteAddr = "127.0.0.1:5555" })
		if status != http.StatusNoContent || principal != "localhost-bypass" {
			t.Fatalf("status = %d principal = %q", status, principal)
		}
	})
}

// TestAdminPanelReachesTheAdminAppOnADeskClaim is the browser question the
// retirement had to answer before it could ship: with no admin SSO cookie left
// to hold, can a support-desk sign-in actually open /admin/ and act there?
//
// It walks the three requests the panel makes, in order: the bootstrap read
// that decides whether to draw the panel or the sign-in button, a read, and a
// mutation carrying the CSRF token the bootstrap handed over.
func TestAdminPanelReachesTheAdminAppOnADeskClaim(t *testing.T) {
	accounts := newAngelAccounts(t)
	handler, _ := newArenaSignedInWithAngel(t, accounts)
	_, cookie := signInThroughAngel(t, handler, accounts, map[string]any{"staff": true, "staff_role": "owner"})

	bootstrap := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/admin/session", nil)
	bootstrap.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.AdminSessionInfoHandler(recorder, bootstrap)
	var panel struct {
		Authenticated bool   `json:"authenticated"`
		Role          string `json:"role"`
		CSRFToken     string `json:"csrf_token"`
		LogoutURL     string `json:"logout_url"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &panel); err != nil {
		t.Fatalf("decode admin session: %v (%s)", err, recorder.Body.String())
	}
	if !panel.Authenticated || panel.Role != "owner" {
		t.Fatalf("admin session = %+v, want the panel drawn for a desk owner", panel)
	}
	if panel.CSRFToken == "" || panel.LogoutURL == "" {
		t.Fatalf("admin session = %+v, want the panel given what its mutations need", panel)
	}

	if status, _ := callAdminRoute(t, handler, cookie, http.MethodGet, nil); status != http.StatusNoContent {
		t.Fatalf("panel read status = %d, want the administrator through", status)
	}
	mutate := func(r *http.Request) {
		r.Header.Set("Origin", "https://arena.example")
		r.Header.Set("X-CSRF-Token", panel.CSRFToken)
	}
	if status, _ := callAdminRoute(t, handler, cookie, http.MethodPut, mutate); status != http.StatusNoContent {
		t.Fatalf("panel mutation status = %d, want it accepted on the bootstrap's CSRF token", status)
	}
}

// TestAdminPanelStaysShutForAnOrdinaryCustomer is the same bootstrap read from
// a session with no desk role: the panel is simply not drawn.
func TestAdminPanelStaysShutForAnOrdinaryCustomer(t *testing.T) {
	accounts := newAngelAccounts(t)
	handler, _ := newArenaSignedInWithAngel(t, accounts)
	_, cookie := signInThroughAngel(t, handler, accounts, nil)

	request := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/admin/session", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.AdminSessionInfoHandler(recorder, request)

	var panel map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &panel); err != nil {
		t.Fatalf("decode admin session: %v (%s)", err, recorder.Body.String())
	}
	if panel["authenticated"] != false {
		t.Fatalf("admin session = %v, want an ordinary customer kept out of the panel", panel)
	}
	if _, leaked := panel["csrf_token"]; leaked {
		t.Fatalf("admin session handed a CSRF token to a non-administrator: %v", panel)
	}
	if status, _ := callAdminRoute(t, handler, cookie, http.MethodGet, nil); status != http.StatusUnauthorized {
		t.Fatalf("admin route status = %d, want 401 for an ordinary customer", status)
	}
}

// TestSignInReturnsToTheAdminPanel is the last step of reaching /admin/ in a
// browser: the panel sends the administrator to Accounts and asks to be
// returned to itself, and nothing wider than Arena's own apps is accepted.
func TestSignInReturnsToTheAdminPanel(t *testing.T) {
	for _, testCase := range []struct{ returnTo, want string }{
		{"/admin/", "/admin/"},
		{"/admin/?tab=chatmod", "/admin/?tab=chatmod"},
		{"/arena/admin/", "/arena/admin/"},
		{"/dashboard/", "/dashboard/"},
		{"https://evil.example/admin/", "/dashboard/"},
		{"//evil.example/admin/", "/dashboard/"},
		{"/adminevil", "/dashboard/"},
		{"/etc/passwd", "/dashboard/"},
	} {
		t.Run(testCase.returnTo, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet,
				"https://arena.example/api/v1/dashboard/login?return_to="+url.QueryEscape(testCase.returnTo), nil)
			if got := safeCustomerReturnTo(request); got != testCase.want {
				t.Fatalf("return_to %q resolved to %q, want %q", testCase.returnTo, got, testCase.want)
			}
		})
	}
}
