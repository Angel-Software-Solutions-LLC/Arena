package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"arena-server/internal/config"
	"arena-server/internal/security"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

// TestAdminRateLimitAppliesToFailedAuthRequests guards against the admin
// route composition regressing to auth-then-rate-limit ordering. chi's
// Use() wraps in registration order (first = outermost), and every
// rejection branch in MakeAdminAuthMiddlewareWithPlatformAdmins returns without
// calling next — so if the auth middleware is registered before the rate
// limiter, every failed X-Admin-Token guess skips the limiter entirely and
// brute-forcing the token is unthrottled. This mirrors router.go's actual
// mount order for both /api/v1/admin and /api/v1/arena/admin.
func TestAdminRateLimitAppliesToFailedAuthRequests(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("ARENA_TEST_REDIS_ADDR"))
	if addr == "" {
		t.Skip("set ARENA_TEST_REDIS_ADDR to run the admin rate-limit ordering test")
	}

	client := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("ping test Redis: %v", err)
	}
	previousRedis := security.RedisClient
	security.RedisClient = client
	key := "ratelimit:api:/probe:192.0.2.1"
	t.Cleanup(func() {
		_ = client.Del(ctx, key).Err()
		_ = client.Close()
		security.RedisClient = previousRedis
	})

	previousToken := config.C.AdminToken
	previousBypass := config.C.AdminLocalhostBypass
	config.C.AdminToken = "the-real-admin-token"
	config.C.AdminLocalhostBypass = false
	t.Cleanup(func() {
		config.C.AdminToken = previousToken
		config.C.AdminLocalhostBypass = previousBypass
	})

	const rpm = 2 // small limit so the test doesn't need many requests

	r := chi.NewRouter()
	r.Route("/probe", func(admin chi.Router) {
		// Same order as router.go's fixed /admin mounts: rate limit first.
		admin.Use(security.RateLimitMiddleware(rpm))
		admin.Use(MakeAdminAuthMiddlewareWithPlatformAdmins(nil, nil))
		admin.Get("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	doRequest := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/probe/", nil)
		req.RemoteAddr = "192.0.2.1:4242"
		if token != "" {
			req.Header.Set("X-Admin-Token", token)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	// Every one of these uses a WRONG token, so auth always rejects. If the
	// limiter were mounted inside auth (the bug), all rpm+1 of these would
	// return 403/401 and the limiter would never see them.
	var codes []int
	for i := 0; i < rpm+1; i++ {
		codes = append(codes, doRequest("wrong-token"))
	}

	sawRateLimited := false
	for _, code := range codes {
		if code == http.StatusTooManyRequests {
			sawRateLimited = true
		} else if code != http.StatusForbidden {
			t.Fatalf("unexpected status among failed-auth attempts: %d (all codes: %v)", code, codes)
		}
	}
	if !sawRateLimited {
		t.Fatalf("rate limiter never triggered across %d failed-auth requests (rpm=%d): codes=%v — the limiter is being skipped on auth failure", rpm+1, rpm, codes)
	}

	// A subsequent request with the CORRECT token must also be throttled:
	// the limit is per IP+path regardless of auth outcome.
	if code := doRequest("the-real-admin-token"); code != http.StatusTooManyRequests {
		t.Fatalf("valid-token request after exhausting the limit = %d, want 429 (rate limit is per IP+path, not per auth outcome)", code)
	}
}
