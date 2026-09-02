package api

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"arena-server/internal/config"
	"arena-server/internal/db"
)

/*
 * These tests drive the real sign-in: a fake Angel Accounts that publishes a
 * discovery document and a JWKS, an id_token this process actually signs, and
 * Arena's own LoginHandler/CallbackHandler and admin middleware in between.
 *
 * A hand-built CustomerSession would prove nothing here. The whole question is
 * whether a claim that arrives on a verified token reaches the route guard, so
 * the token has to be verified for real.
 */

const (
	testAngelClientID    = "arena-customer-client"
	testAngelSigningKey  = "angel-test-key"
	testAngelSubject     = "angel-subject-1"
	testAngelDisplayName = "Platform Owner"
)

type angelAccounts struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	mu     sync.Mutex
	nonce  string
	claims map[string]any
	// entitlements is the body /entitlements answers with for the access
	// token the token endpoint minted, or "" for an Accounts that has not
	// provisioned the endpoint for this client.
	entitlements string
}

// serveEntitlements arms the entitlements endpoint with one published body.
func (a *angelAccounts) serveEntitlements(body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entitlements = body
}

// expect arms the next token response: the nonce Arena just generated, and
// whatever the desk role currently says about this identity.
func (a *angelAccounts) expect(nonce string, claims map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nonce, a.claims = nonce, claims
}

func base64URL(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

func signRS256(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	encode := func(value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal jwt segment: %v", err)
		}
		return base64URL(raw)
	}
	input := encode(map[string]any{"alg": "RS256", "typ": "JWT", "kid": testAngelSigningKey}) + "." + encode(claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign id_token: %v", err)
	}
	return input + "." + base64URL(signature)
}

// The fake Accounts signs with one 2048-bit RSA key per test binary. Every
// sign-in test builds its own Accounts, and generating a fresh key each time
// was the single largest cost in this package; nothing here depends on two
// Accounts holding different keys (the kid is a constant already), so sharing
// it changes nothing about what the tests prove.
var (
	testAngelKeyOnce sync.Once
	testAngelKey     *rsa.PrivateKey
	testAngelKeyErr  error
)

func angelSigningKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testAngelKeyOnce.Do(func() {
		testAngelKey, testAngelKeyErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	if testAngelKeyErr != nil {
		t.Fatalf("generate signing key: %v", testAngelKeyErr)
	}
	return testAngelKey
}

func newAngelAccounts(t *testing.T) *angelAccounts {
	t.Helper()
	key := angelSigningKey(t)
	accounts := &angelAccounts{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		issuer := accounts.server.URL
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"userinfo_endpoint":                     issuer + "/userinfo",
			"jwks_uri":                              issuer + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": testAngelSigningKey, "alg": "RS256", "use": "sig",
			"n": base64URL(key.N.Bytes()),
			"e": base64URL(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		accounts.mu.Lock()
		nonce, extra := accounts.nonce, accounts.claims
		accounts.mu.Unlock()
		now := time.Now()
		claims := map[string]any{
			"iss":   accounts.server.URL,
			"sub":   testAngelSubject,
			"aud":   testAngelClientID,
			"iat":   now.Unix(),
			"exp":   now.Add(time.Hour).Unix(),
			"nonce": nonce,
			"name":  testAngelDisplayName,
		}
		for name, value := range extra {
			claims[name] = value
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "angel-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     signRS256(t, key, claims),
		})
	})
	mux.HandleFunc("/entitlements", func(w http.ResponseWriter, r *http.Request) {
		accounts.mu.Lock()
		body := accounts.entitlements
		accounts.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer angel-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if body == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	accounts.server = httptest.NewServer(mux)
	t.Cleanup(accounts.server.Close)
	return accounts
}

// newArenaSignedInWithAngel builds the customer OIDC handler the way a
// deployment does, through the real constructor and a real discovery read.
func newArenaSignedInWithAngel(t *testing.T, accounts *angelAccounts) (*CustomerOIDCHandler, *fakeIdentityAuthority) {
	t.Helper()
	previous := config.C
	t.Cleanup(func() { config.C = previous })
	config.C.CustomerOIDCEnabled = true
	config.C.CustomerOIDCIssuer = accounts.server.URL
	config.C.CustomerOIDCClientID = testAngelClientID
	config.C.CustomerOIDCClientSecret = "arena-customer-secret"
	config.C.CustomerOIDCRedirectURI = "https://arena.example/api/v1/dashboard/callback"
	config.C.CustomerOIDCSessionTTL = 720
	config.C.CustomerLinkLegacyByEmail = false
	config.C.AccountsEntitlementsURL = ""
	config.C.OIDCSessionTTL = 8
	config.C.AdminLocalhostBypass = false
	config.C.AdminToken = "admin-secret"

	verifiedAt := time.Now().UTC().Add(-time.Hour)
	authority := &fakeIdentityAuthority{account: &db.CustomerAccount{
		ID: "account-1", DisplayName: testAngelDisplayName, EmailVerifiedAt: &verifiedAt,
	}}
	handler := newCustomerOIDCHandlerWithAuthority(authority)
	if handler == nil {
		t.Fatal("customer OIDC handler did not initialise against the test Accounts")
	}
	return handler, authority
}

// signInThroughAngel runs one complete sign-in and returns the session it
// created together with the cookie a browser would now be carrying.
func signInThroughAngel(t *testing.T, handler *CustomerOIDCHandler, accounts *angelAccounts, claims map[string]any) (*CustomerSession, *http.Cookie) {
	t.Helper()

	login := httptest.NewRecorder()
	handler.LoginHandler(login, httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/dashboard/login", nil))
	if login.Code != http.StatusFound {
		t.Fatalf("login status = %d, want a redirect to Accounts", login.Code)
	}
	authURL, err := url.Parse(login.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse authorization redirect: %v", err)
	}
	accounts.expect(authURL.Query().Get("nonce"), claims)

	var stateCookie *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == customerStateCookieName {
			stateCookie = cookie
		}
	}
	if stateCookie == nil {
		t.Fatal("login set no browser-binding cookie")
	}

	callback := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet,
		"https://arena.example/api/v1/dashboard/callback?code=angel-code&state="+url.QueryEscape(authURL.Query().Get("state")), nil)
	request.AddCookie(stateCookie)
	handler.CallbackHandler(callback, request)
	if callback.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want a redirect; body = %s", callback.Code, callback.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == customerSessionCookieName && cookie.Value != "" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("a completed sign-in established no customer session")
	}
	handler.mu.RLock()
	session := handler.sessions[sessionCookie.Value]
	handler.mu.RUnlock()
	if session == nil {
		t.Fatal("the session cookie names no session")
	}
	return session, sessionCookie
}

// callAdminRoute puts one request through the real admin guard with only the
// customer cookie to go on, and reports what the guard decided.
func callAdminRoute(t *testing.T, handler *CustomerOIDCHandler, cookie *http.Cookie, method string, decorate func(*http.Request)) (int, string) {
	t.Helper()
	principal := ""
	guarded := MakeAdminAuthMiddlewareWithPlatformAdmins(nil, handler)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal = adminPrincipalFromContext(r.Context())
			w.WriteHeader(http.StatusNoContent)
		}))
	request := httptest.NewRequest(method, "https://arena.example/api/v1/admin/chat/enabled", nil)
	request.RemoteAddr = "198.51.100.10:4444"
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if decorate != nil {
		decorate(request)
	}
	recorder := httptest.NewRecorder()
	guarded.ServeHTTP(recorder, request)
	return recorder.Code, principal
}

// TestAngelPlatformAdminClaimDecidesArenaAdminAuthority is the contract, read
// off a real sign-in each time: `staff: true` OR `product_admin: true`
// admits, on presence, and nothing else does.
func TestAngelPlatformAdminClaimDecidesArenaAdminAuthority(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		claims        map[string]any
		wantAdmin     bool
		wantAuthority string
		wantRole      string
	}{
		{
			name:      "a support-desk owner administers the platform",
			claims:    map[string]any{"staff": true, "staff_role": "owner"},
			wantAdmin: true, wantAuthority: "staff", wantRole: "owner",
		},
		{
			name:      "a support-desk admin administers the platform",
			claims:    map[string]any{"staff": true, "staff_role": "admin"},
			wantAdmin: true, wantAuthority: "staff", wantRole: "admin",
		},
		{
			// The shape every ordinary customer arrives in. There is no
			// staff claim at all, and a missing claim is the answer "no".
			name:      "no staff claim at all is not an administrator",
			claims:    nil,
			wantAdmin: false,
		},
		{
			// Accounts does not send this, but Arena must not be the thing
			// that decides on truthiness if anything ever does. The role is
			// present and says owner; the answer is still no.
			name:      "an explicit staff:false is not an administrator",
			claims:    map[string]any{"staff": false, "staff_role": "owner"},
			wantAdmin: false,
		},
		{
			// An open vocabulary. Arena is not entitled to read a role it
			// has never heard of as "not an administrator" — nor to invent
			// what it means beyond that.
			name:      "an unrecognised role is an administrator, nothing finer",
			claims:    map[string]any{"staff": true, "staff_role": "incident-commander"},
			wantAdmin: true, wantAuthority: "staff", wantRole: "incident-commander",
		},
		{
			name:      "staff with no role named is still an administrator",
			claims:    map[string]any{"staff": true},
			wantAdmin: true, wantAuthority: "staff", wantRole: "",
		},
		{
			// The per-product grant: somebody the desk made an administrator
			// of Arena specifically. Same routes, its own principal.
			name:      "a product administrator grant administers Arena",
			claims:    map[string]any{"product_admin": true},
			wantAdmin: true, wantAuthority: "product_admin", wantRole: "",
		},
		{
			// A grant is decided on presence too. Accounts never emits false,
			// and if anything ever does it must not be read as truthy.
			name:      "an explicit product_admin:false is not an administrator",
			claims:    map[string]any{"product_admin": false},
			wantAdmin: false,
		},
		{
			name:      "a product_admin string is not an administrator",
			claims:    map[string]any{"product_admin": "true"},
			wantAdmin: false,
		},
		{
			// A desk owner who also holds a grant is reported as the desk:
			// it is the wider authority and the grant adds nothing to it.
			name:      "a desk owner who also holds a product grant is reported as staff",
			claims:    map[string]any{"staff": true, "staff_role": "owner", "product_admin": true},
			wantAdmin: true, wantAuthority: "staff", wantRole: "owner",
		},
		{
			// A role is only meaningful beside `staff`. On its own it is
			// not a claim Arena reads, and it must not leak onto a grant.
			name:      "a product grant does not pick up a stray staff_role",
			claims:    map[string]any{"product_admin": true, "staff_role": "owner"},
			wantAdmin: true, wantAuthority: "product_admin", wantRole: "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			accounts := newAngelAccounts(t)
			handler, _ := newArenaSignedInWithAngel(t, accounts)
			session, cookie := signInThroughAngel(t, handler, accounts, testCase.claims)

			grant, isAdmin := session.platformAdminGrantAt(time.Now())
			if isAdmin != testCase.wantAdmin || grant.Role != testCase.wantRole || grant.Authority != testCase.wantAuthority {
				t.Fatalf("session authority = (%q, %q, %v), want (%q, %q, %v)",
					grant.Authority, grant.Role, isAdmin, testCase.wantAuthority, testCase.wantRole, testCase.wantAdmin)
			}

			status, principal := callAdminRoute(t, handler, cookie, http.MethodGet, nil)
			if testCase.wantAdmin {
				if status != http.StatusNoContent {
					t.Fatalf("admin route status = %d, want the administrator through", status)
				}
				if want := expectedPlatformAdminPrincipal(session.AccountID, testCase.wantAuthority, testCase.wantRole); principal != want {
					t.Fatalf("admin principal = %q, want %q", principal, want)
				}
			} else if status != http.StatusUnauthorized {
				t.Fatalf("admin route status = %d, want 401 for a non-administrator", status)
			}

			// And whatever the answer was, it was reached without writing
			// anything down about this account.
			if session.EmailVerifiedAt == nil {
				t.Fatal("sign-in lost its verified-identity timestamp")
			}
		})
	}
}

func expectedPlatformAdminPrincipal(accountID, authority, role string) string {
	if authority == "product_admin" {
		return "accounts-product-admin:" + accountID
	}
	if role == "" {
		return "accounts-staff:" + accountID
	}
	return "accounts-staff:" + accountID + ":" + role
}

// TestPlatformAdminAuthorityIsGoneAtTheNextSignInAfterRevocation is the
// revocation path, and the reason nothing is persisted.
//
// Nobody clears a flag in Arena between these two sign-ins. The desk role is
// withdrawn in Accounts, the next token simply stops carrying the claim, and
// the authority is gone.
func TestPlatformAdminAuthorityIsGoneAtTheNextSignInAfterRevocation(t *testing.T) {
	accounts := newAngelAccounts(t)
	handler, _ := newArenaSignedInWithAngel(t, accounts)

	granted, grantedCookie := signInThroughAngel(t, handler, accounts, map[string]any{"staff": true, "staff_role": "admin"})
	if _, isAdmin := granted.platformAdminAt(time.Now()); !isAdmin {
		t.Fatal("the administrator sign-in carried no authority")
	}
	if status, _ := callAdminRoute(t, handler, grantedCookie, http.MethodGet, nil); status != http.StatusNoContent {
		t.Fatalf("administrator was refused the admin route: status = %d", status)
	}

	revoked, revokedCookie := signInThroughAngel(t, handler, accounts, nil)
	if revoked.AccountID != granted.AccountID {
		t.Fatalf("second sign-in bound a different account: %q vs %q", revoked.AccountID, granted.AccountID)
	}
	if role, isAdmin := revoked.platformAdminAt(time.Now()); isAdmin {
		t.Fatalf("authority survived revocation: role = %q", role)
	}
	if status, _ := callAdminRoute(t, handler, revokedCookie, http.MethodGet, nil); status != http.StatusUnauthorized {
		t.Fatalf("admin route status after revocation = %d, want 401", status)
	}
}

// TestPlatformAdminGrantOutlivesNeitherTheWindowNorTheSession pins the two
// bounds that keep a 30-day customer session from becoming a 30-day admin
// session.
func TestPlatformAdminGrantOutlivesNeitherTheWindowNorTheSession(t *testing.T) {
	accounts := newAngelAccounts(t)
	handler, _ := newArenaSignedInWithAngel(t, accounts)
	session, cookie := signInThroughAngel(t, handler, accounts, map[string]any{"staff": true, "staff_role": "owner"})

	if session.platformAdmin == nil {
		t.Fatal("no grant was recorded for an administrator sign-in")
	}
	if !session.platformAdmin.ExpiresAt.Before(session.ExpiresAt) {
		t.Fatalf("admin authority runs as long as the customer session: grant %v, session %v",
			session.platformAdmin.ExpiresAt, session.ExpiresAt)
	}
	if window := time.Until(session.platformAdmin.ExpiresAt); window > platformAdminGrantTTL()+time.Minute {
		t.Fatalf("grant window = %v, want at most %v", window, platformAdminGrantTTL())
	}

	// Once the window closes the person is still signed in; only the admin
	// authority is gone, and nothing had to be cleaned up for that.
	session.platformAdmin.ExpiresAt = time.Now().Add(-time.Second)
	if status, _ := callAdminRoute(t, handler, cookie, http.MethodGet, nil); status != http.StatusUnauthorized {
		t.Fatalf("a lapsed grant still opened the admin route: status = %d", status)
	}
	stillSignedIn := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/dashboard/session", nil)
	stillSignedIn.AddCookie(cookie)
	if handler.GetSession(stillSignedIn) == nil {
		t.Fatal("a lapsed admin grant signed the customer out")
	}
}

// TestPlatformAdminMutationsAreHeldToOriginAndCSRF covers the one thing that
// changes by letting a cookie a browser sends everywhere reach admin routes.
func TestPlatformAdminMutationsAreHeldToOriginAndCSRF(t *testing.T) {
	accounts := newAngelAccounts(t)
	handler, _ := newArenaSignedInWithAngel(t, accounts)
	session, cookie := signInThroughAngel(t, handler, accounts, map[string]any{"staff": true, "staff_role": "owner"})

	if status, _ := callAdminRoute(t, handler, cookie, http.MethodPut, nil); status != http.StatusForbidden {
		t.Fatalf("a mutation with no Origin was allowed: status = %d", status)
	}
	crossOrigin := func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("X-CSRF-Token", session.CSRFToken)
	}
	if status, _ := callAdminRoute(t, handler, cookie, http.MethodPut, crossOrigin); status != http.StatusForbidden {
		t.Fatalf("a cross-origin mutation was allowed: status = %d", status)
	}
	noToken := func(r *http.Request) { r.Header.Set("Origin", "https://arena.example") }
	if status, _ := callAdminRoute(t, handler, cookie, http.MethodPut, noToken); status != http.StatusForbidden {
		t.Fatalf("a mutation with no CSRF token was allowed: status = %d", status)
	}
	valid := func(r *http.Request) {
		r.Header.Set("Origin", "https://arena.example")
		r.Header.Set("X-CSRF-Token", session.CSRFToken)
	}
	if status, _ := callAdminRoute(t, handler, cookie, http.MethodPut, valid); status != http.StatusNoContent {
		t.Fatalf("a same-origin mutation with a valid CSRF token was refused: status = %d", status)
	}

	/*
	 * And the path that must not have changed at all: a client authenticating
	 * with an admin token still gets in, even while carrying an administrator
	 * cookie that would have failed the browser checks. The token is what it
	 * asked to be judged on.
	 */
	byToken := func(r *http.Request) { r.Header.Set("X-Admin-Token", config.C.AdminToken) }
	status, principal := callAdminRoute(t, handler, cookie, http.MethodPut, byToken)
	if status != http.StatusNoContent || principal != "admin-token:environment" {
		t.Fatalf("an admin-token mutation was disturbed by the cookie: status = %d principal = %q", status, principal)
	}
}

// TestPlatformAdminClaimDecidesOnPresenceOfTrue is the decoder on its own,
// over shapes a live token is unlikely to carry and must not be fooled by.
func TestPlatformAdminClaimDecidesOnPresenceOfTrue(t *testing.T) {
	for _, testCase := range []struct {
		payload       string
		wantAdmin     bool
		wantAuthority string
		wantRole      string
	}{
		{payload: `{}`, wantAdmin: false},
		{payload: `{"staff_role":"owner"}`, wantAdmin: false},
		{payload: `{"staff":true,"staff_role":"owner"}`, wantAdmin: true, wantAuthority: "staff", wantRole: "owner"},
		{payload: `{"staff":true}`, wantAdmin: true, wantAuthority: "staff"},
		{payload: `{"staff":false}`, wantAdmin: false},
		{payload: `{"staff":null}`, wantAdmin: false},
		{payload: `{"staff":"true"}`, wantAdmin: false},
		{payload: `{"staff":1}`, wantAdmin: false},
		{payload: `{"staff":true,"staff_role":123}`, wantAdmin: true, wantAuthority: "staff"},
		{payload: `{"staff":true,"staff_role":"  owner  "}`, wantAdmin: true, wantAuthority: "staff", wantRole: "owner"},
		{payload: `{"product_admin":true}`, wantAdmin: true, wantAuthority: "product_admin"},
		{payload: `{"product_admin":false}`, wantAdmin: false},
		{payload: `{"product_admin":null}`, wantAdmin: false},
		{payload: `{"product_admin":"true"}`, wantAdmin: false},
		{payload: `{"product_admin":1}`, wantAdmin: false},
		{payload: `{"product_admin":true,"staff_role":"owner"}`, wantAdmin: true, wantAuthority: "product_admin"},
		{payload: `{"staff":false,"product_admin":true}`, wantAdmin: true, wantAuthority: "product_admin"},
		{payload: `{"staff":true,"product_admin":true,"staff_role":"admin"}`, wantAdmin: true, wantAuthority: "staff", wantRole: "admin"},
		{payload: `{"staff":true,"product_admin":false}`, wantAdmin: true, wantAuthority: "staff"},
	} {
		t.Run(testCase.payload, func(t *testing.T) {
			var claims map[string]json.RawMessage
			if err := json.Unmarshal([]byte(testCase.payload), &claims); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			admin := platformAdminFromVerifiedClaims(claims)
			if admin.Present != testCase.wantAdmin || admin.Role != testCase.wantRole || admin.authority() != testCase.wantAuthority {
				t.Fatalf("claim = %+v, want present=%v authority=%q role=%q",
					admin, testCase.wantAdmin, testCase.wantAuthority, testCase.wantRole)
			}
		})
	}
}

// TestPlatformAdminIsReportedToTheSignedInBrowser is how the site learns it
// has somewhere to offer.
func TestPlatformAdminIsReportedToTheSignedInBrowser(t *testing.T) {
	accounts := newAngelAccounts(t)
	handler, _ := newArenaSignedInWithAngel(t, accounts)
	_, cookie := signInThroughAngel(t, handler, accounts, map[string]any{"staff": true, "staff_role": "owner"})

	read := func() map[string]any {
		request := httptest.NewRequest(http.MethodGet, "https://arena.example/api/v1/dashboard/session", nil)
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		handler.SessionInfoHandler(recorder, request)
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode session info: %v (%s)", err, recorder.Body.String())
		}
		return body
	}

	body := read()
	if body["platform_admin"] != true || body["platform_admin_role"] != "owner" || body["platform_admin_authority"] != "staff" {
		t.Fatalf("session info = %v, want it to report the desk authority and role", body)
	}

	_, grantCookie := signInThroughAngel(t, handler, accounts, map[string]any{"product_admin": true})
	cookie = grantCookie
	if body := read(); body["platform_admin"] != true || body["platform_admin_authority"] != "product_admin" || body["platform_admin_role"] != "" {
		t.Fatalf("session info = %v, want it to report the product grant", body)
	}

	_, ordinaryCookie := signInThroughAngel(t, handler, accounts, nil)
	cookie = ordinaryCookie
	if body := read(); body["platform_admin"] != false || body["platform_admin_role"] != "" || body["platform_admin_authority"] != "" {
		t.Fatalf("an ordinary session was reported as an administrator: %v", body)
	}
}

// TestProductAdminGrantIsGoneAtTheNextSignInAfterRevocation is the revocation
// path for the per-product grant, and the reason it is not persisted either:
// Accounts stops emitting the claim, the next token has no grant, and there is
// nothing in Arena to clean up.
func TestProductAdminGrantIsGoneAtTheNextSignInAfterRevocation(t *testing.T) {
	accounts := newAngelAccounts(t)
	handler, _ := newArenaSignedInWithAngel(t, accounts)

	granted, grantedCookie := signInThroughAngel(t, handler, accounts, map[string]any{"product_admin": true})
	if grant, isAdmin := granted.platformAdminGrantAt(time.Now()); !isAdmin || grant.Authority != "product_admin" {
		t.Fatalf("the product-admin sign-in carried no authority: %+v %v", grant, isAdmin)
	}
	if !granted.platformAdmin.ExpiresAt.Before(granted.ExpiresAt) {
		t.Fatalf("a product grant runs as long as the customer session: grant %v, session %v",
			granted.platformAdmin.ExpiresAt, granted.ExpiresAt)
	}
	status, principal := callAdminRoute(t, handler, grantedCookie, http.MethodGet, nil)
	if status != http.StatusNoContent || principal != "accounts-product-admin:"+granted.AccountID {
		t.Fatalf("product administrator was refused or misnamed: status = %d principal = %q", status, principal)
	}

	revoked, revokedCookie := signInThroughAngel(t, handler, accounts, nil)
	if revoked.AccountID != granted.AccountID {
		t.Fatalf("second sign-in bound a different account: %q vs %q", revoked.AccountID, granted.AccountID)
	}
	if _, isAdmin := revoked.platformAdminAt(time.Now()); isAdmin {
		t.Fatal("product-admin authority survived revocation")
	}
	if status, _ := callAdminRoute(t, handler, revokedCookie, http.MethodGet, nil); status != http.StatusUnauthorized {
		t.Fatalf("admin route status after revocation = %d, want 401", status)
	}
}
