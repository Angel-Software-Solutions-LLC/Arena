package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arena-server/internal/config"
)

// TestSecurityHeadersMiddleware_AllowsSameOriginFraming is the regression
// test for the 2026-07 Toolkit/Dashboard outage: frontend/index.html embeds
// /dashboard/?view=public and /dashboard/?view=private in same-origin
// <iframe>s (the Toolkit and Dashboard nav overlays). X-Frame-Options: DENY
// and CSP frame-ancestors 'none' block ALL framing, including same-origin,
// so Chrome refused to render the iframe response (net::ERR_BLOCKED_BY_RESPONSE)
// and both overlays rendered as an empty drawer. The fix must still block
// third-party (cross-origin) framing to preserve the original clickjacking
// protection.
func TestSecurityHeadersMiddleware_AllowsSameOriginFraming(t *testing.T) {
	config.C.SecurityHeadersEnabled = true

	handler := SecurityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/dashboard/?view=public", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if xfo := rec.Header().Get("X-Frame-Options"); xfo != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN (DENY blocks the same-origin dashboard iframe)", xfo)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP contains frame-ancestors 'none', which blocks the same-origin dashboard iframe: %q", csp)
	} else if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("CSP missing frame-ancestors 'self': %q", csp)
	} else if !strings.Contains(csp, "frame-src 'self'") {
		t.Errorf("CSP frame-src must allow the same-origin dashboard iframe: %q", csp)
	}
}

func TestSecurityHeadersMiddleware_AllowsStripeEmbeddedCheckout(t *testing.T) {
	config.C.SecurityHeadersEnabled = true
	handler := SecurityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	/*
	 * Checked origin by origin, within the directive that must carry it,
	 * rather than by matching a whole directive verbatim.
	 *
	 * The verbatim form asserted more than it meant: adding any unrelated
	 * origin to connect-src — the legal corpus, say — failed a test named for
	 * Stripe, while saying nothing about whether Stripe still worked. This
	 * still fails if any of these is dropped, which is the thing worth
	 * protecting, and stays silent about origins it has no opinion on.
	 */
	for _, required := range []struct{ directive, origin string }{
		{"script-src", "https://js.stripe.com"},
		{"script-src", "https://*.js.stripe.com"},
		{"script-src", "https://checkout.stripe.com"},
		{"frame-src", "https://checkout.stripe.com"},
		{"frame-src", "https://js.stripe.com"},
		{"frame-src", "https://hooks.stripe.com"},
		{"frame-src", "https://*.link.com"},
		{"connect-src", "https://api.stripe.com"},
		{"connect-src", "https://checkout.stripe.com"},
		{"connect-src", "https://*.link.com"},
		{"img-src", "https://*.stripe.com"},
	} {
		if !cspDirectiveAllows(csp, required.directive, required.origin) {
			t.Errorf("CSP %s must allow %s for Stripe Embedded Checkout: %q", required.directive, required.origin, csp)
		}
	}
	if policy := rec.Header().Get("Permissions-Policy"); !strings.Contains(policy, `payment=(self "https://checkout.stripe.com"`) {
		t.Errorf("Permissions-Policy blocks Stripe wallet payments: %q", policy)
	}
}

// Cloudflare Browser Insights is disabled for arena.angel-serv.com with a
// hostname-scoped Configuration Rule. Keep its executable origin out of the
// CSP: if edge injection returns, the deployment is misconfigured and should
// be fixed at Cloudflare instead of widening Arena's script policy.
func TestSecurityHeadersMiddleware_RejectsCloudflareInsightsInjection(t *testing.T) {
	config.C.SecurityHeadersEnabled = true
	handler := SecurityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	for _, forbidden := range []string{"cloudflareinsights.com", "static.cloudflareinsights.com"} {
		if strings.Contains(csp, forbidden) {
			t.Errorf("CSP must not trust Cloudflare Browser Insights origin %q: %q", forbidden, csp)
		}
	}
}

// cspDirectiveAllows reports whether one CSP directive lists one source.
//
// Split on the directive rather than searched across the whole policy, so an
// origin permitted for scripts is not mistaken for one permitted to be
// connected to. Sources are compared whole, so `https://link.com` never
// matches because `https://*.link.com` happens to contain it.
func cspDirectiveAllows(policy, directive, source string) bool {
	for _, section := range strings.Split(policy, ";") {
		fields := strings.Fields(strings.TrimSpace(section))
		if len(fields) == 0 || fields[0] != directive {
			continue
		}
		for _, candidate := range fields[1:] {
			if candidate == source {
				return true
			}
		}
	}
	return false
}
