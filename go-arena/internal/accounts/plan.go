package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

/*
 * What Accounts charges for the Arena subscription, so the Shop can quote it.
 *
 * Arena sells nothing and holds no price. Quoting one from its own source
 * would put a second number in the world that can disagree with the card
 * charge, which is exactly what the subscription rewrite removed. So the Shop
 * says what Accounts says, read from the same public catalog a customer sees,
 * and says nothing at all when it does not know.
 *
 * The read is deliberately off the request path: the catalog endpoint answers
 * from a value refreshed in the background, so a slow or unreachable Accounts
 * costs the Shop nothing and an unknown price is simply a Shop that quotes no
 * figure — never a Shop that fails to load.
 */

// Plan is one product's public subscription plan, as Accounts sells it.
type Plan struct {
	Slug       string
	PriceCents int
	Currency   string
	Interval   string
}

// PlanSource remembers one product's plan from the Accounts catalog.
type PlanSource struct {
	catalogURL string
	productID  string
	http       *http.Client

	mu     sync.RWMutex
	plan   Plan
	known  bool
	lastAt time.Time
}

// NewPlanSource reads productID's plan from the catalog the issuer publishes.
// A blank issuer or product yields nil, which Get reports as "no price".
func NewPlanSource(issuer, productID string, httpClient *http.Client) *PlanSource {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	productID = strings.TrimSpace(productID)
	if issuer == "" || productID == "" {
		return nil
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &PlanSource{
		catalogURL: issuer + "/api/v1/catalog",
		productID:  productID,
		http:       httpClient,
	}
}

// Get returns the last known plan without blocking. A nil source, or one that
// has never had a successful read, reports false — and the caller quotes
// nothing rather than guessing.
func (s *PlanSource) Get() (Plan, bool) {
	if s == nil {
		return Plan{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.plan, s.known
}

// Refresh reads the catalog once. A failure leaves the previous answer in
// place: a price that was right a minute ago is a better thing to show than
// nothing, and a price that has genuinely changed is picked up on the next
// successful read.
func (s *PlanSource) Refresh(ctx context.Context) error {
	if s == nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.catalogURL, nil)
	if err != nil {
		return fmt.Errorf("accounts catalog request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	res, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("accounts catalog fetch: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("accounts catalog: HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("accounts catalog read: %w", err)
	}

	var doc struct {
		Products []struct {
			ID    string `json:"id"`
			Slug  string `json:"slug"`
			Plans []struct {
				Slug       string `json:"slug"`
				PriceCents int    `json:"priceCents"`
				Currency   string `json:"currency"`
				Interval   string `json:"interval"`
				Public     bool   `json:"public"`
			} `json:"plans"`
		} `json:"products"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("accounts catalog decode: %w", err)
	}

	for _, product := range doc.Products {
		if product.ID != s.productID && product.Slug != s.productID {
			continue
		}
		/*
		 * The cheapest public paid plan is what a Shop quotes: a free tier is
		 * not what "subscribe" costs, and a plan that is not public is not on
		 * offer to the person reading. Arena has exactly one today; choosing
		 * deliberately means a second one added in Accounts does not silently
		 * change the figure to whichever happens to be listed first.
		 */
		var best Plan
		found := false
		for _, plan := range product.Plans {
			if !plan.Public || plan.PriceCents <= 0 {
				continue
			}
			if found && plan.PriceCents >= best.PriceCents {
				continue
			}
			currency := strings.TrimSpace(plan.Currency)
			if currency == "" {
				currency = "USD"
			}
			best = Plan{
				Slug:       plan.Slug,
				PriceCents: plan.PriceCents,
				Currency:   currency,
				Interval:   strings.TrimSpace(plan.Interval),
			}
			found = true
		}
		if !found {
			return fmt.Errorf("accounts catalog: %s publishes no public paid plan", s.productID)
		}
		s.mu.Lock()
		s.plan = best
		s.known = true
		s.lastAt = time.Now()
		s.mu.Unlock()
		return nil
	}
	return fmt.Errorf("accounts catalog: no product %q", s.productID)
}
