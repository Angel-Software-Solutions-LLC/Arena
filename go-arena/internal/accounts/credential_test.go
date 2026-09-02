package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testClientID = "arena"
	testSecret   = "a-secret-arena-holds-in-its-store-0123456789"
)

type issuerStub struct {
	t             *testing.T
	tokenStatus   int
	tokenBody     map[string]any
	discovery     map[string]any
	discoveryCode int
	observed      []*http.Request
	server        *httptest.Server
}

func unsignedJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func newIssuer(t *testing.T) *issuerStub {
	t.Helper()
	stub := &issuerStub{t: t, tokenStatus: http.StatusOK, discoveryCode: http.StatusOK}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			document := stub.discovery
			if document == nil {
				document = map[string]any{"issuer": stub.server.URL, "token_endpoint": stub.server.URL + "/api/v1/token"}
			}
			w.WriteHeader(stub.discoveryCode)
			_ = json.NewEncoder(w).Encode(document)
		case "/api/v1/token":
			_ = r.ParseForm()
			stub.observed = append(stub.observed, r)
			w.WriteHeader(stub.tokenStatus)
			_ = json.NewEncoder(w).Encode(stub.tokenBody)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *issuerStub) verify(secret string) CredentialVerdict {
	return VerifyClientCredential(context.Background(), s.server.URL, testClientID, secret, s.server.Client())
}

func TestVerifyClientCredentialAcceptedAuthenticatesLikeTheCodeExchange(t *testing.T) {
	stub := newIssuer(t)
	stub.tokenBody = map[string]any{
		"access_token": unsignedJWT(t, map[string]any{"product": "arena", "typ": "client"}),
		"token_type":   "Bearer",
		"scope":        "entitlements:read",
	}
	verdict := stub.verify(testSecret)
	if !verdict.OK || verdict.Outcome != CredentialAccepted {
		t.Fatalf("verdict = %+v", verdict)
	}
	if verdict.Product != "arena" || verdict.Scope != "entitlements:read" {
		t.Fatalf("product/scope = %q/%q", verdict.Product, verdict.Scope)
	}
	if len(stub.observed) != 1 {
		t.Fatalf("token endpoint called %d times", len(stub.observed))
	}
	req := stub.observed[0]
	user, pass, ok := req.BasicAuth()
	if !ok || user != testClientID || pass != testSecret {
		t.Fatalf("basic auth = %q/%v", user, ok)
	}
	if req.PostForm.Get("grant_type") != "client_credentials" {
		t.Fatalf("grant_type = %q", req.PostForm.Get("grant_type"))
	}
}

func TestVerifyClientCredentialRejectedNamesTheConsoleAndNeverTheSecret(t *testing.T) {
	stub := newIssuer(t)
	stub.tokenStatus = http.StatusUnauthorized
	stub.tokenBody = map[string]any{"error": "invalid_client"}
	verdict := stub.verify(testSecret)
	if verdict.OK || verdict.Outcome != CredentialRejected {
		t.Fatalf("verdict = %+v", verdict)
	}
	if !strings.Contains(verdict.Message, "revoked, deleted, or ARENA_CUSTOMER_OIDC_CLIENT_SECRET") {
		t.Fatalf("message = %q", verdict.Message)
	}
	if !strings.Contains(verdict.Message, "reinstate") {
		t.Fatalf("message should point at the console: %q", verdict.Message)
	}
	if strings.Contains(verdict.Message, testSecret) {
		t.Fatal("the secret must never be printed")
	}
}

func TestVerifyClientCredentialUnauthorizedClientIsStillAccepted(t *testing.T) {
	stub := newIssuer(t)
	stub.tokenStatus = http.StatusBadRequest
	stub.tokenBody = map[string]any{"error": "unauthorized_client"}
	verdict := stub.verify(testSecret)
	if !verdict.OK || verdict.Outcome != CredentialAcceptedWithoutGrant {
		t.Fatalf("verdict = %+v", verdict)
	}
}

func TestVerifyClientCredentialDiscoveryProblemsAreNotCredentialProblems(t *testing.T) {
	down := newIssuer(t)
	down.discoveryCode = http.StatusServiceUnavailable
	if v := down.verify(testSecret); v.Outcome != CredentialDiscoveryFailed {
		t.Fatalf("down: %+v", v)
	}

	wrongIssuer := newIssuer(t)
	wrongIssuer.discovery = map[string]any{"issuer": "https://elsewhere.example", "token_endpoint": wrongIssuer.server.URL + "/api/v1/token"}
	if v := wrongIssuer.verify(testSecret); v.Outcome != CredentialDiscoveryFailed {
		t.Fatalf("wrong issuer: %+v", v)
	}

	offOrigin := newIssuer(t)
	offOrigin.discovery = map[string]any{"issuer": offOrigin.server.URL, "token_endpoint": "https://evil.example/token"}
	v := offOrigin.verify(testSecret)
	if v.Outcome != CredentialDiscoveryFailed {
		t.Fatalf("off-origin endpoint: %+v", v)
	}
	if len(offOrigin.observed) != 0 {
		t.Fatal("the credential must not be posted to an endpoint off the issuer origin")
	}

	unreachable := VerifyClientCredential(context.Background(), "http://127.0.0.1:1", testClientID, testSecret, nil)
	if unreachable.Outcome != CredentialUnreachable {
		t.Fatalf("unreachable: %+v", unreachable)
	}

	unconfigured := VerifyClientCredential(context.Background(), "", "", "", nil)
	if unconfigured.OK || unconfigured.Outcome != CredentialDiscoveryFailed {
		t.Fatalf("unconfigured: %+v", unconfigured)
	}
}

func TestVerifyClientCredentialUnexpectedAnswerIsNotAVerdict(t *testing.T) {
	stub := newIssuer(t)
	stub.tokenStatus = http.StatusBadGateway
	stub.tokenBody = map[string]any{"error": "server_error"}
	verdict := stub.verify(testSecret)
	if verdict.OK || verdict.Outcome != CredentialUnexpected {
		t.Fatalf("verdict = %+v", verdict)
	}
	if !strings.Contains(verdict.Message, "502") || !strings.Contains(verdict.Message, "server_error") {
		t.Fatalf("message = %q", verdict.Message)
	}
}
