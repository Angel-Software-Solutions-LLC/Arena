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

// publishedResponse is the exact shape documented on Support#188, values and
// all. Written out in full rather than assembled from the struct so a change
// on the other side shows up here as a decode that stops matching, instead of
// as a test that agrees with whatever Arena happens to expect today.
const publishedResponse = `{
  "account":  { "id": "acct_1", "name": "Player", "kind": "individual" },
  "user":     { "id": "usr_1", "email": "player@example.com", "name": "Player" },
  "entitlements": [ { "productId": "arena", "plan": "all-access" } ],
  "purchases": [
    {
      "id":          "pur_abc",
      "productId":   "arena",
      "itemId":      "arena:neon-signal-pack",
      "sku":         "neon-signal-pack",
      "name":        "Neon Signal Pack",
      "priceCents":  199,
      "currency":    "USD",
      "purchasedAt": "2026-08-23T10:00:00Z",
      "status":      "active"
    },
    {
      "id":          "pur_def",
      "productId":   "arena",
      "itemId":      "arena:ember-vanguard-pack",
      "sku":         "ember-vanguard-pack",
      "name":        "Ember Vanguard Pack",
      "priceCents":  199,
      "currency":    "USD",
      "purchasedAt": "2026-08-01T10:00:00Z",
      "status":      "revoked"
    }
  ],
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
	if len(snapshot.Purchases) != 2 {
		t.Fatalf("purchases = %d, want both rows kept", len(snapshot.Purchases))
	}
	/*
	 * A revoked purchase must survive the read. Dropping it here would make a
	 * withdrawal indistinguishable from a purchase that simply stopped being
	 * mentioned, and the reconciler acts on exactly that difference.
	 */
	if snapshot.Purchases[1].Active() {
		t.Fatalf("a revoked purchase read as active: %+v", snapshot.Purchases[1])
	}
	active := snapshot.ActivePurchases()
	if len(active) != 1 || active[0].ID != "pur_abc" || active[0].SKU != "neon-signal-pack" {
		t.Fatalf("active purchases = %+v, want only the active grant", active)
	}
	if snapshot.RefreshAfter.IsZero() {
		t.Fatalf("refreshAfter did not decode: %+v", snapshot.RefreshAfter)
	}
	if len(snapshot.Entitlements) != 1 {
		t.Fatalf("subscriptions = %d, want them carried without being interpreted", len(snapshot.Entitlements))
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

// TestFetchTellsSilenceFromEmptiness is the failure that would have been worst
// in production: a response that omits `purchases` read as "owns nothing" is a
// reconciliation input that says withdraw everything.
func TestFetchTellsSilenceFromEmptiness(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "omitted", body: `{"account":{"id":"acct_1"}}`, wantErr: true},
		{name: "null", body: `{"account":{"id":"acct_1"},"purchases":null}`, wantErr: true},
		{name: "empty", body: `{"account":{"id":"acct_1"},"purchases":[]}`, wantErr: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := serving(t, http.StatusOK, test.body, nil)
			snapshot, err := client.Fetch(context.Background(), "at_token")
			if test.wantErr {
				if err == nil {
					t.Fatalf("a response with no purchases key was accepted as owning nothing")
				}
				return
			}
			if err != nil {
				t.Fatalf("an explicitly empty list is a real answer: %v", err)
			}
			if snapshot.Purchases == nil || len(snapshot.Purchases) != 0 {
				t.Fatalf("purchases = %+v, want an empty list", snapshot.Purchases)
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

// TestNoEndpointMeansNoClient is the state this ships in: until the Accounts
// side provisions Arena's service client and advertises the endpoint, there is
// nothing to read and nothing must pretend otherwise.
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
