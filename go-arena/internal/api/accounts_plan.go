package api

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"arena-server/internal/accounts"
)

// arenaProductID is Arena's product in the Angel Accounts catalog. The public
// catalog already publishes this same string as the subscription's product.
const arenaProductID = "arena"

// accountsPlanRefreshInterval is how often the published price is re-read.
// A price changes about never; this is a floor under how long a change in
// Accounts can go unquoted here, not a poll anything depends on.
const accountsPlanRefreshInterval = 15 * time.Minute

// accountsPlan holds the last known Arena plan, or nothing.
//
// Read on the catalog request path, written only by the refresher below, so
// serving the catalog never touches the network and never blocks.
var accountsPlan atomic.Pointer[accounts.PlanSource]

// startAccountsPlanRefresh begins reading Arena's plan from the Accounts
// catalog. Safe to call when Accounts is not configured: NewPlanSource returns
// nil for a blank issuer, and every read of a nil source reports "no price".
func startAccountsPlanRefresh(issuer, productID string) {
	source := accounts.NewPlanSource(issuer, productID, nil)
	if source == nil {
		return
	}
	accountsPlan.Store(source)
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := source.Refresh(ctx)
			cancel()
			if err != nil {
				// Not an error worth alarming about: the Shop simply quotes no
				// figure until a read succeeds, which is the honest thing to
				// show when the price is not known.
				slog.Debug("could not read the Arena plan from Accounts", "error", err)
			} else if plan, ok := source.Get(); ok {
				slog.Info("arena subscription price read from accounts",
					"price_cents", plan.PriceCents, "interval", plan.Interval, "plan", plan.Slug)
			}
			time.Sleep(accountsPlanRefreshInterval)
		}
	}()
}

// arenaPlanForCatalog reports the plan the Shop should quote, if it is known.
func arenaPlanForCatalog() (accounts.Plan, bool) {
	return accountsPlan.Load().Get()
}
