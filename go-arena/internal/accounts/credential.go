package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CredentialOutcome is what a credential probe concluded. The strings are
// what `arena-server check-oidc` prints, so they are stable.
type CredentialOutcome string

const (
	// CredentialAccepted: Accounts holds this secret for this client id.
	CredentialAccepted CredentialOutcome = "accepted"
	// CredentialAcceptedWithoutGrant: the credential is right; only the
	// client_credentials grant is switched off, which browser sign-in never
	// uses.
	CredentialAcceptedWithoutGrant CredentialOutcome = "accepted-without-grant"
	// CredentialRejected: revoked, deleted, or a secret Accounts does not hold.
	CredentialRejected CredentialOutcome = "rejected"
	// CredentialDiscoveryFailed: the issuer answered, but not as an Accounts.
	CredentialDiscoveryFailed CredentialOutcome = "discovery-failed"
	// CredentialUnreachable: no answer at all.
	CredentialUnreachable CredentialOutcome = "unreachable"
	// CredentialUnexpected: an answer that says nothing about the credential.
	CredentialUnexpected CredentialOutcome = "unexpected"
)

// CredentialVerdict is the whole answer. It never carries the secret, or
// anything derived from it, because it is printed.
type CredentialVerdict struct {
	OK       bool
	Outcome  CredentialOutcome
	Issuer   string
	ClientID string
	// Product is what Accounts says the client is bound to, when it answered
	// with a token. Informational: Arena does not validate a product claim.
	Product string
	Scope   string
	// Message is one operator-facing sentence with the next step in it.
	Message string
}

const consoleHint = "In the Accounts console, Settings → API clients: reinstate the client if it is revoked, " +
	"restore it with this client id and secret if it was deleted, or rotate it and update ARENA_CUSTOMER_OIDC_CLIENT_SECRET."

// maxCredentialBody bounds what is read from the issuer. A discovery document
// or a token response is a few kilobytes; this is headroom, not a limit.
const maxCredentialBody = 256 << 10

// VerifyClientCredential asks the issuer whether it accepts this client id
// and secret, from this process and this environment.
//
// Until now the only way to learn that Arena's client had been revoked,
// deleted, or restored with a typo on the Accounts side was to ask a customer
// to sign in and watch it fail — after they had already signed in at Accounts,
// which is the most expensive place to find out. This asks directly: it
// discovers the issuer, then posts a `client_credentials` grant at the
// discovered token endpoint authenticated exactly as the code exchange is.
// Accounts checks the secret before it checks whether the grant is enabled,
// so `unauthorized_client` still means the credential is right.
func VerifyClientCredential(ctx context.Context, issuer, clientID, clientSecret string, httpClient *http.Client) CredentialVerdict {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	clientID = strings.TrimSpace(clientID)
	verdict := CredentialVerdict{Issuer: issuer, ClientID: clientID}
	fail := func(outcome CredentialOutcome, format string, args ...any) CredentialVerdict {
		verdict.Outcome = outcome
		verdict.Message = fmt.Sprintf(format, args...)
		return verdict
	}
	if issuer == "" || clientID == "" || strings.TrimSpace(clientSecret) == "" {
		return fail(CredentialDiscoveryFailed, "customer OIDC is not fully configured: ARENA_CUSTOMER_OIDC_ISSUER, _CLIENT_ID and _CLIENT_SECRET are all required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}

	tokenEndpoint, err := discoverTokenEndpoint(ctx, httpClient, issuer)
	var unreachable *unreachableError
	switch {
	case errors.As(err, &unreachable):
		return fail(CredentialUnreachable, "could not reach Accounts discovery at %s: %v", issuer, unreachable.err)
	case err != nil:
		return fail(CredentialDiscoveryFailed, "Accounts discovery at %s is not usable: %v", issuer, err)
	}

	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fail(CredentialUnexpected, "could not build the token request: %v", err)
	}
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fail(CredentialUnreachable, "could not reach the Accounts token endpoint: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxCredentialBody))
	var answer struct {
		Error       string `json:"error"`
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	}
	_ = json.Unmarshal(body, &answer)

	switch {
	case resp.StatusCode == http.StatusOK:
		verdict.Product = productFromUnverifiedJWT(answer.AccessToken)
		verdict.Scope = answer.Scope
		verdict.OK = true
		verdict.Outcome = CredentialAccepted
		bound := ""
		if verdict.Product != "" {
			bound = " (product " + verdict.Product + ")"
		}
		verdict.Message = fmt.Sprintf("Accounts accepts the credential for client %s%s.", clientID, bound)
		return verdict
	case resp.StatusCode == http.StatusBadRequest && answer.Error == "unauthorized_client":
		verdict.OK = true
		verdict.Outcome = CredentialAcceptedWithoutGrant
		verdict.Message = fmt.Sprintf("Accounts accepts the credential for client %s; the client_credentials grant is not enabled on it, which browser sign-in does not need.", clientID)
		return verdict
	case resp.StatusCode == http.StatusUnauthorized:
		return fail(CredentialRejected, "Accounts rejects the credential for client %s: the client is revoked, deleted, or ARENA_CUSTOMER_OIDC_CLIENT_SECRET is not the secret Accounts holds. %s", clientID, consoleHint)
	}
	detail := ""
	if answer.Error != "" {
		detail = " (" + answer.Error + ")"
	}
	return fail(CredentialUnexpected, "the Accounts token endpoint answered %d%s, which says nothing about the credential; try again, then check the Accounts side", resp.StatusCode, detail)
}

// unreachableError separates "nothing answered" from "something answered
// wrongly", which are different next steps for the operator.
type unreachableError struct{ err error }

func (e *unreachableError) Error() string { return e.err.Error() }

// discoverTokenEndpoint reads the discovery document and returns the token
// endpoint, refusing one that is not on the issuer's own origin — the same
// trust boundary the sign-in path pins, so the probe cannot be talked into
// posting the credential somewhere else.
func discoverTokenEndpoint(ctx context.Context, httpClient *http.Client, issuer string) (string, error) {
	issuerURL, err := url.Parse(issuer)
	if err != nil || issuerURL.Host == "" {
		return "", fmt.Errorf("issuer %q is not an absolute URL", issuer)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", &unreachableError{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discovery answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCredentialBody))
	if err != nil {
		return "", &unreachableError{err: err}
	}
	var document struct {
		Issuer        string `json:"issuer"`
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return "", fmt.Errorf("discovery is not JSON: %v", err)
	}
	if strings.TrimRight(document.Issuer, "/") != issuer {
		return "", fmt.Errorf("discovery names issuer %q, not the configured %q", document.Issuer, issuer)
	}
	endpoint, err := url.Parse(document.TokenEndpoint)
	if err != nil || endpoint.Scheme != issuerURL.Scheme || endpoint.Host != issuerURL.Host ||
		endpoint.User != nil || endpoint.Fragment != "" {
		return "", fmt.Errorf("discovery names a token endpoint off the issuer origin: %q", document.TokenEndpoint)
	}
	return endpoint.String(), nil
}

// productFromUnverifiedJWT reads the `product` claim of a JWT without
// verifying it. Fine for a diagnostic that has just authenticated to the
// issuer over its own origin and makes no authority decision from the value;
// nothing on the sign-in path calls this.
func productFromUnverifiedJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Product string `json:"product"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Product
}
