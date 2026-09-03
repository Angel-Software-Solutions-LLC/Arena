package security

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"arena-server/internal/config"

	"github.com/redis/go-redis/v9"
)

// RedisClient is the package-level Redis client used for rate limiting. It is
// nil if Redis is unavailable. General endpoints degrade gracefully, while
// side-effecting routes protected by FailClosedRateLimitMiddleware return 503.
var RedisClient *redis.Client

type rateLimitEvaluator interface {
	Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd
}

const rateLimitLua = `
local count = redis.call('INCR', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
if ttl < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
  ttl = tonumber(ARGV[1])
end
return {count, ttl}
`

// InitRedis connects to Redis using the host and port from the loaded config.
// If the connection fails, it logs a warning and leaves RedisClient nil.
// Individual middleware selects graceful degradation or fail-closed behavior.
func InitRedis() error {
	addr := fmt.Sprintf("%s:%d", config.C.RedisHost, config.C.RedisPort)

	client := redis.NewClient(&redis.Options{
		Addr: addr,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		slog.Warn("redis connection failed; general limits degraded and protected side effects unavailable", "addr", addr, "error", err)
		return nil
	}

	RedisClient = client
	slog.Info("redis connected", "addr", addr)
	return nil
}

// CheckRateLimit checks whether a request identified by key is within the
// allowed rate. It uses a fixed Redis window that starts with the first request
// and expires windowSecs later; later requests increment the count without
// extending the expiry. Like any fixed-window limiter, it can allow one full
// window's quota immediately before reset and another immediately after it.
//
// Returns:
//   - allowed:   true if the request is within the limit
//   - remaining: how many requests are left in the current window
//   - resetAt:   when the current window expires
//   - err:       any Redis communication error
func CheckRateLimit(ctx context.Context, key string, maxPerWindow int, windowSecs int) (allowed bool, remaining int, resetAt time.Time, err error) {
	if RedisClient == nil {
		// Redis unavailable -- allow everything.
		return true, maxPerWindow, time.Now().Add(time.Duration(windowSecs) * time.Second), nil
	}
	return checkRateLimitWithEvaluator(ctx, RedisClient, key, maxPerWindow, windowSecs, time.Now())
}

func checkRateLimitWithEvaluator(ctx context.Context, evaluator rateLimitEvaluator, key string, maxPerWindow int, windowSecs int, now time.Time) (allowed bool, remaining int, resetAt time.Time, err error) {
	redisKey := "ratelimit:" + key
	windowMillis := int64(windowSecs) * int64(time.Second/time.Millisecond)
	result, err := evaluator.Eval(ctx, rateLimitLua, []string{redisKey}, windowMillis).Int64Slice()
	if err != nil {
		return true, maxPerWindow, now.Add(time.Duration(windowSecs) * time.Second), fmt.Errorf("redis rate limit script failed: %w", err)
	}
	if len(result) != 2 {
		return true, maxPerWindow, now.Add(time.Duration(windowSecs) * time.Second), fmt.Errorf("redis rate limit script returned %d values", len(result))
	}

	count, ttlMillis := result[0], result[1]
	if ttlMillis < 0 {
		ttlMillis = windowMillis
	}
	resetAt = now.Add(time.Duration(ttlMillis) * time.Millisecond)

	if count > int64(maxPerWindow) {
		return false, 0, resetAt, nil
	}

	return true, maxPerWindow - int(count), resetAt, nil
}

// rateLimitResponse is the JSON body returned when a client exceeds the rate limit.
type rateLimitResponse struct {
	Error     string `json:"error"`
	Remaining int    `json:"remaining"`
	ResetAt   int64  `json:"reset_at"`
}

// RateLimitMiddleware returns an HTTP middleware that enforces a per-IP rate
// limit of rpm requests per fixed 60-second window. If Redis is unavailable,
// all requests are allowed through. Each endpoint gets its own rate-limit
// bucket based on the request path, so different endpoints don't share
// counters.
func RateLimitMiddleware(rpm int) func(http.Handler) http.Handler {
	return rateLimitMiddleware(rpm, false)
}

// FailClosedRateLimitMiddleware is for side-effecting endpoints, such as email
// delivery, that must not become unbounded when Redis is unavailable. It
// returns 503 without invoking the protected handler if the limiter cannot
// make a reliable decision.
func FailClosedRateLimitMiddleware(rpm int) func(http.Handler) http.Handler {
	return rateLimitMiddleware(rpm, true)
}

func rateLimitMiddleware(rpm int, failClosed bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if failClosed && RedisClient == nil {
				writeRateLimitUnavailable(w)
				return
			}

			ip := ExtractClientIP(r)
			key := "api:" + r.URL.Path + ":" + ip

			allowed, remaining, resetAt, err := CheckRateLimit(r.Context(), key, rpm, 60)
			if err != nil {
				if failClosed {
					slog.Warn("rate limit check error, rejecting side-effecting request", "error", err)
					writeRateLimitUnavailable(w)
					return
				}
				slog.Warn("rate limit check error, allowing request",
					"error", err, "ip", ip)
			}

			if !allowed {
				retryAfter := int(time.Until(resetAt).Seconds()) + 1
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				w.WriteHeader(http.StatusTooManyRequests)

				resp := map[string]interface{}{
					"error": "rate limit exceeded",
					"code":  "RATE_LIMITED",
					"details": map[string]interface{}{
						"remaining":   remaining,
						"reset_at":    resetAt.Unix(),
						"retry_after": retryAfter,
						"limit":       rpm,
						"window":      "60s",
						"path":        r.URL.Path,
					},
				}
				json.NewEncoder(w).Encode(resp)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func writeRateLimitUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "60")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": "request rate limiting is temporarily unavailable",
		"code":  "RATE_LIMIT_UNAVAILABLE",
	})
}

type trustedProxySet struct {
	raw      string
	prefixes []netip.Prefix
}

var trustedProxyCache atomic.Pointer[trustedProxySet]
var trustedCloudflareProxyCache atomic.Pointer[trustedProxySet]

// ExtractClientIP returns the validated client IP for rate limits and access
// controls. Forwarded headers are accepted only when the immediate network
// peer is inside ARENA_TRUSTED_PROXY_CIDRS; direct clients cannot select their
// own identity by supplying proxy headers.
func ExtractClientIP(r *http.Request) string {
	remote, remoteValid := parseRemoteIP(r.RemoteAddr)
	clientIP := strings.TrimSpace(r.RemoteAddr)
	if remoteValid {
		clientIP = remote.String()
	}

	trusted := configuredTrustedProxies()
	if !remoteValid || !isTrustedProxy(remote, trusted) {
		return clientIP
	}

	// Only a peer explicitly identified as Cloudflare-controlled can make the
	// vendor header authoritative. A generic Caddy/nginx proxy may preserve a
	// client-supplied CF-Connecting-IP and must fall through to its XFF chain.
	if raw := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); raw != "" &&
		isTrustedProxy(remote, configuredTrustedCloudflareProxies()) {
		if forwarded, ok := parseIP(raw); ok {
			return forwarded.String()
		}
		return clientIP
	}

	if raw := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); raw != "" {
		forwarded, ok := parseForwardedFor(raw)
		if !ok {
			return clientIP
		}

		// Walk toward the originating client, discarding only configured proxy
		// hops. This avoids trusting a leftmost value injected before a proxy
		// appends its own forwarding information.
		for i := len(forwarded) - 1; i >= 0; i-- {
			if !isTrustedProxy(forwarded[i], trusted) {
				return forwarded[i].String()
			}
		}
	}

	return clientIP
}

// RequestFromTrustedProxy reports whether the immediate network peer is inside
// ARENA_TRUSTED_PROXY_CIDRS.
//
// Callers use it to decide whether a forwarding header may be believed at all.
// ExtractClientIP already applies this to the client address; anything else
// that reads an X-Forwarded-* header needs the same gate, or a direct client
// gets to describe its own connection.
func RequestFromTrustedProxy(r *http.Request) bool {
	if r == nil {
		return false
	}
	remote, ok := parseRemoteIP(r.RemoteAddr)
	return ok && isTrustedProxy(remote, configuredTrustedProxies())
}

func configuredTrustedProxies() []netip.Prefix {
	return configuredProxyPrefixes(config.C.TrustedProxyCIDRs, &trustedProxyCache)
}

func configuredTrustedCloudflareProxies() []netip.Prefix {
	return configuredProxyPrefixes(config.C.TrustedCloudflareProxyCIDRs, &trustedCloudflareProxyCache)
}

func configuredProxyPrefixes(raw string, cache *atomic.Pointer[trustedProxySet]) []netip.Prefix {
	raw = strings.TrimSpace(raw)
	if cached := cache.Load(); cached != nil && cached.raw == raw {
		return cached.prefixes
	}

	prefixes := make([]netip.Prefix, 0, strings.Count(raw, ",")+1)
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			slog.Warn("ignoring invalid trusted proxy CIDR", "cidr", value)
			continue
		}
		prefixes = append(prefixes, prefix.Masked())
	}

	parsed := &trustedProxySet{raw: raw, prefixes: prefixes}
	cache.Store(parsed)
	return parsed.prefixes
}

func parseRemoteIP(remoteAddr string) (netip.Addr, bool) {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if addrPort, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return normalizeIP(addrPort.Addr()), true
	}
	return parseIP(remoteAddr)
}

func parseIP(value string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return netip.Addr{}, false
	}
	return normalizeIP(addr), true
}

func normalizeIP(addr netip.Addr) netip.Addr {
	addr = addr.Unmap()
	if addr.Zone() != "" {
		addr = addr.WithZone("")
	}
	return addr
}

func parseForwardedFor(value string) ([]netip.Addr, bool) {
	parts := strings.Split(value, ",")
	addresses := make([]netip.Addr, 0, len(parts))
	for _, part := range parts {
		addr, ok := parseIP(part)
		if !ok {
			return nil, false
		}
		addresses = append(addresses, addr)
	}
	return addresses, len(addresses) > 0
}

func isTrustedProxy(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
