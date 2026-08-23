// Package accounts reads what a person owns from the Angel Accounts
// application, which owns commerce for every Angel product now.
//
// The division of labour, as agreed on Support#188: **Accounts owns the
// licence** — the record that somebody bought a thing and still holds it —
// and **Arena owns only the assignment**, which bot wears which grant. So
// nothing in this package decides ownership. It reads an answer and reports
// it; everything downstream treats that answer as the authority and its own
// rows as a cache of it.
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

// PurchaseStatus values from the contract. `status` is checked rather than
// assumed, which is the whole reason the field exists before anything can
// revoke a one-time purchase: a consumer that treats presence as validity
// silently breaks on the day the first refund lands.
const (
	PurchaseActive  = "active"
	PurchaseRevoked = "revoked"
)

// ErrUnauthorized is the one failure worth distinguishing: the token was
// rejected, so a retry with the same token is pointless and a caller that
// can start a fresh sign-in should.
var ErrUnauthorized = errors.New("accounts entitlements: token rejected")

// Purchase is one thing bought once — an Arena cosmetic pack.
//
// `SKU` is Arena's own pack id, carried across the catalogue sync unchanged,
// which is what lets Arena resolve a purchase to its own items without a
// second mapping table to keep in step.
//
// `ID` is the grant id: stable, never reissued, and the reference Arena
// records against every licence it materialises from this purchase.
type Purchase struct {
	ID          string    `json:"id"`
	ProductID   string    `json:"productId"`
	ItemID      string    `json:"itemId"`
	SKU         string    `json:"sku"`
	Name        string    `json:"name"`
	PriceCents  int       `json:"priceCents"`
	Currency    string    `json:"currency"`
	PurchasedAt time.Time `json:"purchasedAt"`
	Status      string    `json:"status"`
}

// Active reports whether this grant still confers ownership.
func (p Purchase) Active() bool {
	return strings.EqualFold(strings.TrimSpace(p.Status), PurchaseActive)
}

// Snapshot is one reading of the endpoint.
//
// `Entitlements` stays raw. Subscriptions are a different shape with a
// different meaning, Arena does not consume them yet, and decoding a shape
// nothing reads would be a guess written down as a struct. It is kept so a
// caller can log or count them without this package pretending to understand
// them.
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
	Entitlements []json.RawMessage `json:"entitlements"`
	Purchases    []Purchase        `json:"purchases"`
	RefreshAfter time.Time         `json:"refreshAfter"`
}

// ActivePurchases returns the grants that still confer ownership, skipping
// any that name no SKU — a purchase Arena cannot resolve to a pack is not a
// purchase Arena can act on, and inventing a fallback for one would be worse
// than reporting nothing.
func (s *Snapshot) ActivePurchases() []Purchase {
	if s == nil {
		return nil
	}
	active := make([]Purchase, 0, len(s.Purchases))
	for _, purchase := range s.Purchases {
		if purchase.Active() && strings.TrimSpace(purchase.SKU) != "" {
			active = append(active, purchase)
		}
	}
	return active
}

// Client reads one endpoint with one bearer token.
type Client struct {
	endpoint string
	http     *http.Client
}

// maxEntitlementsBody bounds what is read from another service.
//
// 142 packs is the whole Arena catalogue and a purchase row is a few hundred
// bytes, so this is roughly two orders of magnitude of headroom over the
// largest honest answer, and a bound rather than none on a body Arena does
// not control.
const maxEntitlementsBody = 4 << 20

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
// so the answer is already scoped to Arena's purchases, and it names the user,
// so no account or user id is passed in — asking for somebody else's
// entitlements is not a request this client is able to make.
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
	 * `purchases` is always present in the contract, so its absence is a
	 * disagreement about which service is on the other end rather than a
	 * customer who has bought nothing — and treating it as "owns nothing"
	 * would revoke every licence on the next reconciliation.
	 *
	 * A null literal and a missing key both decode to a nil slice, which is
	 * why this is checked against the raw body rather than the decoded value:
	 * an empty array is a real, meaningful answer and must survive.
	 */
	if snapshot.Purchases == nil && !hasKey(body, "purchases") {
		return nil, errors.New("accounts entitlements: response omitted purchases")
	}
	if snapshot.Purchases == nil {
		snapshot.Purchases = []Purchase{}
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
