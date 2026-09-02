// Package accounts reads what a person is entitled to from the Angel Accounts
// application, which owns commerce for every Angel product.
//
// Arena sells one thing, and Accounts sells it: the Arena subscription. So the
// only question this package answers is whether the person signing in holds
// an ACTIVE entitlement for the Arena product. It reads an answer and reports
// it; everything downstream treats that answer as the authority and its own
// column as a cache of it.
//
// One thing this package deliberately cannot do is learn an address. The
// contract's `user` object carries `email`, and Arena stopped storing email
// addresses in #248. The struct below has no field for it, which is a
// stronger guarantee than a rule about not writing it down: there is nowhere
// for it to go after the decode.
package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ArenaProductSlug is how the Accounts catalogue names this product. An
// entitlement is Arena's when either its `productSlug` or its `productId`
// says so; the seed uses the same string for both.
const ArenaProductSlug = "arena"

// ErrUnauthorized is the one failure worth distinguishing: the token was
// rejected, so a retry with the same token is pointless and a caller that
// can start a fresh sign-in should.
var ErrUnauthorized = errors.New("accounts entitlements: token rejected")

// Entitlement is one row of the contract's `entitlements[]`: the account's
// standing on one product.
//
// `Active` is the whole answer. Accounts computes it from the subscription
// status, the trial and paid periods, a staff time-box and the account's own
// standing, and Arena has no business re-deriving any of that from `Status`
// — the day the rule on the Accounts side changes, a consumer that recomputed
// it would silently disagree. `Status` is carried for logs only.
type Entitlement struct {
	ProductID   string `json:"productId"`
	ProductSlug string `json:"productSlug"`
	PlanSlug    string `json:"planSlug"`
	Status      string `json:"status"`
	Active      bool   `json:"active"`
}

// IsArena reports whether this entitlement is for the Arena product.
func (e Entitlement) IsArena() bool {
	return strings.EqualFold(strings.TrimSpace(e.ProductSlug), ArenaProductSlug) ||
		strings.EqualFold(strings.TrimSpace(e.ProductID), ArenaProductSlug)
}

// Snapshot is one reading of the endpoint.
type Snapshot struct {
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"account"`
	// No Email. See the package comment: there is nowhere to put one.
	User struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"user"`
	Entitlements []Entitlement `json:"entitlements"`
	RefreshAfter time.Time     `json:"refreshAfter"`
}

// ArenaEntitlement returns the Arena row, if the snapshot carries one.
//
// The endpoint returns inactive rows too (`includeInactive` on the Accounts
// side, so a lapsed subscriber is listed as lapsed rather than absent). If
// more than one row names Arena — which the catalogue does not allow, but a
// consumer should not fall over if it ever does — an active one wins.
func (s *Snapshot) ArenaEntitlement() (Entitlement, bool) {
	if s == nil {
		return Entitlement{}, false
	}
	var found Entitlement
	present := false
	for _, entitlement := range s.Entitlements {
		if !entitlement.IsArena() {
			continue
		}
		if !present || (entitlement.Active && !found.Active) {
			found, present = entitlement, true
		}
	}
	return found, present
}

// ArenaSubscriptionActive is the one bit the rest of Arena acts on: an
// Arena entitlement is present and Accounts says it is active right now.
func (s *Snapshot) ArenaSubscriptionActive() bool {
	entitlement, ok := s.ArenaEntitlement()
	return ok && entitlement.Active
}

// Client reads one endpoint with one bearer token.
type Client struct {
	endpoint string
	http     *http.Client
}

// maxEntitlementsBody bounds what is read from another service.
//
// An account holds at most one entitlement per product and there are a
// handful of products, so this is several orders of magnitude of headroom
// over the largest honest answer, and a bound rather than none on a body
// Arena does not control.
const maxEntitlementsBody = 1 << 20

// NewClient returns a reader for the given endpoint, or nil when there is no
// endpoint to read — which is the state before the service client is
// provisioned, and is a configuration Arena runs in rather than an error.
func NewClient(endpoint string, httpClient *http.Client) *Client {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{endpoint: endpoint, http: httpClient}
}

// Endpoint reports where this client reads from. For logging and for the
// service-status surface, so an operator can see which side is configured.
func (c *Client) Endpoint() string {
	if c == nil {
		return ""
	}
	return c.endpoint
}

// Fetch reads the snapshot for whoever the token names.
//
// The token is the whole of the authorization: it is bound to Arena's client,
// so the answer is already scoped to Arena, and it names the user, so no
// account or user id is passed in — asking for somebody else's entitlements
// is not a request this client is able to make.
func (c *Client) Fetch(ctx context.Context, accessToken string) (*Snapshot, error) {
	if c == nil {
		return nil, errors.New("accounts entitlements: not configured")
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, ErrUnauthorized
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("accounts entitlements: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("accounts entitlements: fetch: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxEntitlementsBody))
		resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("accounts entitlements: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEntitlementsBody))
	if err != nil {
		return nil, fmt.Errorf("accounts entitlements: read: %w", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return nil, fmt.Errorf("accounts entitlements: decode: %w", err)
	}
	/*
	 * `entitlements` is always present in the contract, so its absence is a
	 * disagreement about which service is on the other end rather than a
	 * customer who subscribes to nothing — and treating it as "not
	 * subscribed" would lock a paying customer's cosmetics on the next
	 * sign-in.
	 *
	 * A null literal and a missing key both decode to a nil slice, which is
	 * why this is checked against the raw body rather than the decoded value:
	 * an empty array is a real, meaningful answer and must survive.
	 */
	if snapshot.Entitlements == nil && !hasKey(body, "entitlements") {
		return nil, errors.New("accounts entitlements: response omitted entitlements")
	}
	if snapshot.Entitlements == nil {
		snapshot.Entitlements = []Entitlement{}
	}
	return &snapshot, nil
}

// hasKey reports whether a top-level key is present in a JSON object, without
// caring what it holds. Used to tell "said nothing" from "said empty".
func hasKey(body []byte, key string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	raw, ok := probe[key]
	if !ok {
		return false
	}
	return string(raw) != "null"
}
