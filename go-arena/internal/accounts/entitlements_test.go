package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// publishedResponse is the shape the Accounts `/v1/entitlements` endpoint
// publishes (apps/api/src/routes/v1.ts, `entitlementsPayload`), values and
// all. Written out in full rather than assembled from the struct so a change
// on the other side shows up here as a decode that stops matching, instead of
// as a test that agrees with whatever Arena happens to expect today.
//
// `purchases` is still on the wire for products that sell one-time items;
// Arena no longer sells any and does not read it.
const publishedResponse = `{
  "account":  { "id": "acct_1", "name": "Player", "kind": "individual" },
  "user":     { "id": "usr_1", "email": "player@example.com", "name": "Player" },
  "entitlements": [
    {
      "productId": "kynetik", "productSlug": "kynetik", "productName": "Kynetik",
      "planId": "kynetik-pro", "planSlug": "pro", "planName": "Pro",
      "status": "active", "active": true, "features": ["kynetik.publish"], "limits": {},
      "seats": 1, "seatsUsed": 1, "trialEndsAt": null, "currentPeriodEnd": "2026-10-01T00:00:00Z",
      "staffGrantExpiresAt": null, "launchUrl": "https://kynetik.dev"
    },
    {
      "productId": "arena", "productSlug": "arena", "productName": "Arena",
      "planId": "arena-all-access", "planSlug": "all-access", "planName": "Arena",
      "status": "active", "active": true,
      "features": ["arena.bot", "arena.spectate", "arena.cosmetics"], "limits": {},
      "seats": 1, "seatsUsed": 1, "trialEndsAt": null, "currentPeriodEnd": "2026-10-01T00:00:00Z",
      "staffGrantExpiresAt": null, "launchUrl": "https://arena.angel-serv.com"
    }
  ],
  "purchases": [],
  "refreshAfter": "2026-08-23T11:00:00Z"
}`

func serving(t *testing.T, status int, body string, inspect func(*http.Request)) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inspect != nil {
			inspect(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL, server.Client())
}

func TestFetchReadsThePublishedShape(t *testing.T) {
	var seenAuth string
	client := serving(t, http.StatusOK, publishedResponse, func(r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
	})
	snapshot, err := client.Fetch(context.Background(), "at_token")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if seenAuth != "Bearer at_token" {
		t.Fatalf("Authorization header = %q, want a bearer token", seenAuth)
	}
	if len(snapshot.Entitlements) != 2 {
		t.Fatalf("entitlements = %d, want every product row kept", len(snapshot.Entitlements))
	}
	arena, ok := snapshot.ArenaEntitlement()
	if !ok || arena.PlanSlug != "all-access" || arena.Status != "active" || !arena.Active {
		t.Fatalf("arena entitlement = (%+v, %v), want the active all-access row", arena, ok)
	}
	if !snapshot.ArenaSubscriptionActive() {
		t.Fatal("an active Arena entitlement did not read as a subscription")
	}
	if snapshot.RefreshAfter.IsZero() {
		t.Fatalf("refreshAfter did not decode: %+v", snapshot.RefreshAfter)
	}
}

// TestArenaSubscriptionIsDecidedByTheActiveFlagOnTheArenaRow is the whole
// consumption rule: the Arena row, and Accounts' own `active`, nothing else.
func TestArenaSubscriptionIsDecidedByTheActiveFlagOnTheArenaRow(t *testing.T) {
	for _, test := range []struct {
		name         string
		entitlements string
		wantPresent  bool
		wantActive   bool
	}{
		{name: "active by slug", entitlements: `[{"productSlug":"arena","status":"active","active":true}]`, wantPresent: true, wantActive: true},
		{name: "active by id only", entitlements: `[{"productId":"arena","status":"trialing","active":true}]`, wantPresent: true, wantActive: true},
		{name: "lapsed", entitlements: `[{"productSlug":"arena","status":"canceled","active":false}]`, wantPresent: true, wantActive: false},
		// `status` says active but Accounts says no: a suspended account, a
		// time-boxed staff grant that ran out. Accounts' answer wins.
		{name: "status without active", entitlements: `[{"productSlug":"arena","status":"active"}]`, wantPresent: true, wantActive: false},
		{name: "other product only", entitlements: `[{"productSlug":"kynetik","status":"active","active":true}]`, wantPresent: false, wantActive: false},
		{name: "nothing", entitlements: `[]`, wantPresent: false, wantActive: false},
		{name: "an active row beats a stale one", entitlements: `[{"productSlug":"arena","active":false},{"productSlug":"arena","active":true}]`, wantPresent: true, wantActive: true},
		{name: "case and whitespace do not matter", entitlements: `[{"productSlug":" Arena ","active":true}]`, wantPresent: true, wantActive: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var snapshot Snapshot
			if err := json.Unmarshal([]byte(`{"entitlements":`+test.entitlements+`}`), &snapshot); err != nil {
				t.Fatalf("decode: %v", err)
			}
			_, present := snapshot.ArenaEntitlement()
			if present != test.wantPresent || snapshot.ArenaSubscriptionActive() != test.wantActive {
				t.Fatalf("present=%v active=%v, want present=%v active=%v",
					present, snapshot.ArenaSubscriptionActive(), test.wantPresent, test.wantActive)
			}
		})
	}
	var absent *Snapshot
	if absent.ArenaSubscriptionActive() {
		t.Fatal("a missing snapshot read as subscribed")
	}
}

// TestSnapshotHasNowhereToPutAnAddress is the structural half of "Arena stores
// no email addresses". The endpoint sends one; this asserts the type Arena
// decodes into cannot receive it, which is a guarantee no reviewer has to
// re-check every time this file is edited.
func TestSnapshotHasNowhereToPutAnAddress(t *testing.T) {
	client := serving(t, http.StatusOK, publishedResponse, nil)
	snapshot, err := client.Fetch(context.Background(), "at_token")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "player@example.com") {
		t.Fatalf("the decoded snapshot carries an address: %s", encoded)
	}
	userType := reflect.TypeOf(snapshot.User)
	for index := 0; index < userType.NumField(); index++ {
		if strings.EqualFold(userType.Field(index).Name, "Email") {
			t.Fatalf("Snapshot.User has an Email field; there must be nowhere for an address to land")
		}
	}
}

// TestFetchTellsSilenceFromEmptiness is the failure that would be worst in
// production: a response that omits `entitlements` read as "subscribes to
// nothing" is a sync input that locks a paying customer's cosmetics.
func TestFetchTellsSilenceFromEmptiness(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "omitted", body: `{"account":{"id":"acct_1"}}`, wantErr: true},
		{name: "null", body: `{"account":{"id":"acct_1"},"entitlements":null}`, wantErr: true},
		{name: "empty", body: `{"account":{"id":"acct_1"},"entitlements":[]}`, wantErr: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := serving(t, http.StatusOK, test.body, nil)
			snapshot, err := client.Fetch(context.Background(), "at_token")
			if test.wantErr {
				if err == nil {
					t.Fatalf("a response with no entitlements key was accepted as subscribing to nothing")
				}
				return
			}
			if err != nil {
				t.Fatalf("an explicitly empty list is a real answer: %v", err)
			}
			if snapshot.Entitlements == nil || len(snapshot.Entitlements) != 0 || snapshot.ArenaSubscriptionActive() {
				t.Fatalf("entitlements = %+v, want an empty list and no subscription", snapshot.Entitlements)
			}
		})
	}
}

func TestFetchDistinguishesARejectedToken(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		client := serving(t, status, `{"error":"nope"}`, nil)
		if _, err := client.Fetch(context.Background(), "at_token"); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("status %d gave %v, want ErrUnauthorized", status, err)
		}
	}
	client := serving(t, http.StatusInternalServerError, `{}`, nil)
	if _, err := client.Fetch(context.Background(), "at_token"); err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a server fault gave %v, want a plain error a retry could follow", err)
	}
	// An empty token never leaves the process: there is nothing to ask with.
	if _, err := client.Fetch(context.Background(), "  "); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("an empty token gave %v, want ErrUnauthorized without a request", err)
	}
}

// TestNoEndpointMeansNoClient is the state an Arena ships in until the
// Accounts side advertises the endpoint: there is nothing to read and nothing
// must pretend otherwise.
func TestNoEndpointMeansNoClient(t *testing.T) {
	if client := NewClient("   ", nil); client != nil {
		t.Fatalf("an empty endpoint produced a client")
	}
	var absent *Client
	if _, err := absent.Fetch(context.Background(), "at_token"); err == nil {
		t.Fatalf("a nil client answered a fetch")
	}
	if absent.Endpoint() != "" {
		t.Fatalf("a nil client reported an endpoint")
	}
}
