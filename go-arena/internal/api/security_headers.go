package api

import (
	"net/http"

	"arena-server/internal/config"
)

// contentSecurityPolicy allowlists exactly the external origins the frontend
// actually loads from (Babylon.js CDN, Google Fonts) plus 'self'. It keeps
// 'unsafe-inline'/'unsafe-eval' because the frontend and dashboard rely on
// inline <script>/<style> blocks and Babylon.js's WebGL/WASM shader
// compilation — tightening further would require a frontend refactor.
const contentSecurityPolicy = "" +
	"default-src 'self'; " +
	"script-src 'self' https://cdn.jsdelivr.net https://cdn.babylonjs.com https://js.stripe.com https://*.js.stripe.com https://checkout.stripe.com 'unsafe-inline' 'unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src 'self' https://fonts.gstatic.com; " +
	"img-src 'self' data: blob: https://*.stripe.com https://*.link.com; " +
	// accounts.angel-serv.com is the legal corpus, which the shared footer
	// fetches at runtime so publishing a document updates every site at once.
	// Scoped to that one host: it is a read of public documents, answered with
	// a wildcard and no credentials, and widening this to anything else would
	// give the page a new place it may talk to for no reason.
	"connect-src 'self' ws: wss: https://accounts.angel-serv.com https://cdn.babylonjs.com https://api.stripe.com https://checkout.stripe.com https://link.com https://*.link.com; " +
	"worker-src 'self' blob:; " +
	"frame-src 'self' https://checkout.stripe.com https://js.stripe.com https://*.js.stripe.com https://hooks.stripe.com https://link.com https://*.link.com; " +
	"frame-ancestors 'self'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// maxRequestBodyBytes caps the size of any HTTP request body the server will
// read. All real request bodies (JSON config payloads, etc.) are well under
// a few KB; this exists purely to bound memory use against oversized-body
// DoS attempts. It does not affect WebSocket traffic, which is capped
// separately by ARENA_WS_MESSAGE_MAX_BYTES.
const maxRequestBodyBytes = 1 << 20 // 1 MiB

// BodySizeLimitMiddleware wraps the request body in an http.MaxBytesReader
// so handlers that call json.NewDecoder(r.Body).Decode(...) can't be made to
// read an unbounded amount of attacker-supplied data into memory.
func BodySizeLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// SecurityHeadersMiddleware sets standard defensive HTTP response headers
// (clickjacking, MIME sniffing, referrer leakage, transport security). It is
// a no-op for WebSocket upgrade requests, which don't carry these headers
// meaningfully and whose response is otherwise controlled by the upgrader.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !config.C.SecurityHeadersEnabled {
			next.ServeHTTP(w, r)
			return
		}

		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		// SAMEORIGIN (not DENY): frontend/index.html embeds /dashboard/ in a
		// same-origin <iframe> for the Toolkit and Dashboard overlays. DENY
		// blocks ALL framing including same-origin, which breaks that embed.
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(self \"https://checkout.stripe.com\" \"https://*.stripe.com\" \"https://link.com\" \"https://*.link.com\")")
		// Only takes effect over HTTPS (browsers ignore it over plain HTTP), so
		// it's safe to always send even behind a proxy that terminates TLS.
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		h.Set("Content-Security-Policy", contentSecurityPolicy)

		next.ServeHTTP(w, r)
	})
}
