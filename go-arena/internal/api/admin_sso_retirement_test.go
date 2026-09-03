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

// TestAdminPanelStaysShutForAnUnprovisionedDeskIdentity is the whole of the
// change that made the per-product grant the only key, read from the browser.
//
// The desk claim used to open this panel, which meant every owner and admin of
// the support desk administered Arena — and every other Angel product — with
// nothing provisioned and nothing to revoke per product. Working the desk now
// admits nobody: the bootstrap read draws the sign-in button rather than the
// panel, hands over no CSRF token, and the admin subtree answers a desk owner
// exactly as it answers a stranger.
func TestAdminPanelStaysShutForAnUnprovisionedDeskIdentity(t *testing.T) {
	accounts := newAngelAccounts(t)
	handler, _ := newArenaSignedInWithAngel(t, accounts)

	for _, role := range []string{"owner", "admin", "incident-commander", ""} {
		t.Run("staff_role="+role, func(t *testing.T) {
			claims := map[string]any{"staff": true}
			if role != "" {
				claims["staff_role"] = role
			}
			session, cookie := signInThroughAngel(t, handler, accounts, claims)

			bootstrap := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/admin/session", nil)
			bootstrap.AddCookie(cookie)
			recorder := httptest.NewRecorder()
			handler.AdminSessionInfoHandler(recorder, bootstrap)
			var panel map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &panel); err != nil {
				t.Fatalf("decode admin session: %v (%s)", err, recorder.Body.String())
			}
			if panel["authenticated"] != false {
				t.Fatalf("admin session = %v, want the panel kept shut for an unprovisioned desk identity", panel)
			}
			if _, leaked := panel["csrf_token"]; leaked {
				t.Fatalf("admin session handed a CSRF token to a desk identity with no grant: %v", panel)
			}

			// A read, and a mutation carrying everything a mutation could
			// carry. Neither is a way in.
			if status, principal := callAdminRoute(t, handler, cookie, http.MethodGet, nil); status != http.StatusUnauthorized || principal != "" {
				t.Fatalf("panel read = %d %q, want 401 and no principal", status, principal)
			}
			mutate := func(r *http.Request) {
				r.Header.Set("Origin", "https://arena.example")
				r.Header.Set("X-CSRF-Token", session.CSRFToken)
			}
			if status, principal := callAdminRoute(t, handler, cookie, http.MethodPut, mutate); status != http.StatusUnauthorized || principal != "" {
				t.Fatalf("panel mutation = %d %q, want 401 and no principal", status, principal)
			}

			// And the sign-in itself still worked: this is a customer, kept
			// signed in, simply not an administrator.
			signedIn := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/account/session", nil)
			signedIn.AddCookie(cookie)
			if handler.GetSession(signedIn) == nil {
				t.Fatal("refusing the panel signed the desk identity out of Arena")
			}
		})
	}
}

// TestAdminPanelTellsASignedInVisitorWhatIsMissing is the difference between
// "nobody is here" and "somebody is here who may not come in".
//
// Both are `authenticated: false`, and offering the same sign-in button to
// each is how a desk owner ends up pressing it forever: the sign-in already
// worked, and the grant is what is absent. `signed_in` is what lets the panel
// say so, and it is never true for a browser holding no session.
func TestAdminPanelTellsASignedInVisitorWhatIsMissing(t *testing.T) {
	accounts := newAngelAccounts(t)
	handler, _ := newArenaSignedInWithAngel(t, accounts)

	read := func(cookie *http.Cookie) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/admin/session", nil)
		if cookie != nil {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		handler.AdminSessionInfoHandler(recorder, request)
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode admin session: %v (%s)", err, recorder.Body.String())
		}
		return body
	}

	if body := read(nil); body["authenticated"] != false || body["signed_in"] != false {
		t.Fatalf("admin session for no cookie at all = %v, want signed_in false", body)
	}

	// A desk owner and an ordinary customer are the same answer here, which
	// is the point: neither has been granted Arena.
	for _, claims := range []map[string]any{nil, {"staff": true, "staff_role": "owner"}} {
		_, cookie := signInThroughAngel(t, handler, accounts, claims)
		body := read(cookie)
		if body["authenticated"] != false || body["signed_in"] != true {
			t.Fatalf("admin session for %v = %v, want authenticated false and signed_in true", claims, body)
		}
		if _, leaked := body["csrf_token"]; leaked {
			t.Fatalf("admin session handed a CSRF token to a non-administrator: %v", body)
		}
	}

	_, adminCookie := signInThroughAngel(t, handler, accounts, map[string]any{"product_admin": true})
	if body := read(adminCookie); body["authenticated"] != true || body["signed_in"] != true {
		t.Fatalf("admin session for an administrator = %v, want both true", body)
	}

	// The no-Accounts bootstrap says the same thing about a browser it knows
	// nothing about, so the panel reads one shape everywhere.
	recorder := httptest.NewRecorder()
	AdminSessionUnavailableHandler(recorder, httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/admin/session", nil))
	var unavailable map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &unavailable); err != nil {
		t.Fatalf("decode admin session: %v (%s)", err, recorder.Body.String())
	}
	if unavailable["authenticated"] != false || unavailable["signed_in"] != false || unavailable["login_enabled"] != false {
		t.Fatalf("unconfigured admin session = %v, want every door reported shut", unavailable)
	}
}

// TestAdminPanelReachesTheAdminAppOnAProductGrant is the same walk for the
// per-product grant: the panel bootstraps, says which claim opened it, and
// the customer cookie plus the bootstrap's CSRF token carry a mutation.
func TestAdminPanelReachesTheAdminAppOnAProductGrant(t *testing.T) {
	accounts := newAngelAccounts(t)
	handler, _ := newArenaSignedInWithAngel(t, accounts)
	session, cookie := signInThroughAngel(t, handler, accounts, map[string]any{"product_admin": true})

	bootstrap := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/admin/session", nil)
	bootstrap.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.AdminSessionInfoHandler(recorder, bootstrap)
	var panel struct {
		Authenticated bool   `json:"authenticated"`
		Authority     string `json:"authority"`
		Role          string `json:"role"`
		CSRFToken     string `json:"csrf_token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &panel); err != nil {
		t.Fatalf("decode admin session: %v (%s)", err, recorder.Body.String())
	}
	if !panel.Authenticated || panel.Authority != "product_admin" || panel.Role != "" || panel.CSRFToken == "" {
		t.Fatalf("admin session = %+v, want the panel drawn for a product administrator", panel)
	}

	mutate := func(r *http.Request) {
		r.Header.Set("Origin", "https://arena.example")
		r.Header.Set("X-CSRF-Token", panel.CSRFToken)
	}
	status, principal := callAdminRoute(t, handler, cookie, http.MethodPut, mutate)
	if status != http.StatusNoContent || principal != "accounts-product-admin:"+session.AccountID {
		t.Fatalf("panel mutation = %d %q, want it accepted under the product-admin principal", status, principal)
	}

	// The same walk for somebody who works the desk *and* has been granted
	// Arena. The grant is what admits them; the desk role rides along in the
	// principal and the panel so an audit line can say who acted.
	deskSession, deskCookie := signInThroughAngel(t, handler, accounts,
		map[string]any{"staff": true, "staff_role": "owner", "product_admin": true})
	deskBootstrap := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/admin/session", nil)
	deskBootstrap.AddCookie(deskCookie)
	deskRecorder := httptest.NewRecorder()
	handler.AdminSessionInfoHandler(deskRecorder, deskBootstrap)
	if err := json.Unmarshal(deskRecorder.Body.Bytes(), &panel); err != nil {
		t.Fatalf("decode admin session: %v (%s)", err, deskRecorder.Body.String())
	}
	if !panel.Authenticated || panel.Authority != "product_admin" || panel.Role != "owner" {
		t.Fatalf("admin session = %+v, want the grant reported with the desk role beside it", panel)
	}
	deskMutate := func(r *http.Request) {
		r.Header.Set("Origin", "https://arena.example")
		r.Header.Set("X-CSRF-Token", panel.CSRFToken)
	}
	status, principal = callAdminRoute(t, handler, deskCookie, http.MethodPut, deskMutate)
	if want := "accounts-product-admin:" + deskSession.AccountID + ":owner"; status != http.StatusNoContent || principal != want {
		t.Fatalf("panel mutation = %d %q, want %q", status, principal, want)
	}
}

// TestCustomerCookieAloneNeverAuthorisesAdmin pins the other side of both
// claims: a perfectly good customer session that carries neither is a
// customer, and the admin subtree answers it the way it answers nobody.
func TestCustomerCookieAloneNeverAuthorisesAdmin(t *testing.T) {
	accounts := newAngelAccounts(t)
	handler, _ := newArenaSignedInWithAngel(t, accounts)
	session, cookie := signInThroughAngel(t, handler, accounts, nil)
	if handler.GetSession(func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/account/session", nil)
		r.AddCookie(cookie)
		return r
	}()) == nil {
		t.Fatal("the customer sign-in established no session")
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		status, principal := callAdminRoute(t, handler, cookie, method, func(r *http.Request) {
			r.Header.Set("Origin", "https://arena.example")
			r.Header.Set("X-CSRF-Token", session.CSRFToken)
		})
		if status != http.StatusUnauthorized || principal != "" {
			t.Fatalf("%s with a plain customer cookie = %d %q, want 401 and no principal", method, status, principal)
		}
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
