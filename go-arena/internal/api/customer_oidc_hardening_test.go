package api

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arena-server/internal/config"

	"github.com/coreos/go-oidc/v3/oidc"
)

// primeCallback puts a valid, browser-bound transaction in front of the
// callback so a test exercises the check it is about with everything else
// already satisfied.
func primeCallback(t *testing.T, handler *CustomerOIDCHandler) (state string, binding *http.Cookie) {
	t.Helper()
	state = "state-for-the-issuer-checks"
	bindingValue := "browser-binding-value"
	handler.states[state] = customerOIDCTransaction{
		ExpiresAt:            time.Now().Add(5 * time.Minute),
		BrowserBindingDigest: sha256.Sum256([]byte(bindingValue)),
		Nonce:                "nonce-value",
		PKCEVerifier:         "verifier-value",
		ReturnTo:             "/dashboard/",
	}
	handler.verifier = oidc.NewVerifier(handler.issuer, nil, &oidc.Config{ClientID: "customer-client"})
	return state, &http.Cookie{Name: customerStateCookieName, Value: bindingValue}
}

func callbackWith(t *testing.T, handler *CustomerOIDCHandler, query string, binding *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "https://arena.example/account/callback?"+query, nil)
	req.AddCookie(binding)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)
	return rec
}

// A response stamped with somebody else's issuer is not a sign-in (RFC 9207).
func TestCustomerCallbackRejectsAnUnexpectedIssuer(t *testing.T) {
	handler := newTestCustomerOIDCHandler()
	state, binding := primeCallback(t, handler)

	rec := callbackWith(t, handler, "state="+state+"&code=abc&iss=https%3A%2F%2Fevil.example", binding)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// When the provider advertises that it stamps every response, one that arrives
// without a stamp did not come from the provider.
func TestCustomerCallbackRequiresTheIssuerParameterWhenAdvertised(t *testing.T) {
	handler := newTestCustomerOIDCHandler()
	handler.issParamRequired = true
	state, binding := primeCallback(t, handler)

	rec := callbackWith(t, handler, "state="+state+"&code=abc", binding)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a missing iss; body=%s", rec.Code, rec.Body.String())
	}
}

// The control: the configured issuer passes the check and the flow moves on to
// the next stage, which is where a missing code is noticed.
func TestCustomerCallbackAcceptsTheConfiguredIssuer(t *testing.T) {
	handler := newTestCustomerOIDCHandler()
	handler.issParamRequired = true
	state, binding := primeCallback(t, handler)

	rec := callbackWith(t, handler, "state="+state+"&iss=https%3A%2F%2Fidentity.example", binding)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (past the issuer check, missing code); body=%s",
			rec.Code, rec.Body.String())
	}
}

// X-Forwarded-Proto is a claim by whoever sent the request. Only a peer inside
// ARENA_TRUSTED_PROXY_CIDRS is entitled to make it.
func TestSecureCookieBelievesForwardedProtoOnlyFromATrustedProxy(t *testing.T) {
	previous := config.C.TrustedProxyCIDRs
	t.Cleanup(func() { config.C.TrustedProxyCIDRs = previous })
	config.C.TrustedProxyCIDRs = "172.30.1.2/32"

	for _, tc := range []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{"trusted proxy is believed", "172.30.1.2:41000", true},
		{"a direct client is not", "203.0.113.9:41000", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://arena.example/account/login", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-Proto", "https")
			req.TLS = nil
			if got := secureCookie(req); got != tc.want {
				t.Fatalf("secureCookie = %v, want %v", got, tc.want)
			}
		})
	}
}
