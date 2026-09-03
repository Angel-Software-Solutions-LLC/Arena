package accounts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func catalogStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/catalog" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPlanSourceQuotesTheProductsPublicPaidPlan(t *testing.T) {
	server := catalogStub(t, http.StatusOK, `{"products":[
		{"id":"reef","slug":"agentreef","plans":[{"slug":"pro","priceCents":14900,"interval":"month","public":true}]},
		{"id":"arena","slug":"arena","plans":[{"slug":"all-access","priceCents":999,"interval":"month","public":true}]}
	]}`)
	source := NewPlanSource(server.URL, "arena", server.Client())
	if err := source.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	plan, ok := source.Get()
	if !ok {
		t.Fatal("the plan should be known after a successful read")
	}
	if plan.PriceCents != 999 || plan.Interval != "month" || plan.Slug != "all-access" {
		t.Fatalf("plan = %+v", plan)
	}
	// Another product's plan must never be quoted as Arena's.
	if plan.PriceCents == 14900 {
		t.Fatal("quoted a different product's price")
	}
	if plan.Currency != "USD" {
		t.Fatalf("currency = %q, want the USD default when the catalog names none", plan.Currency)
	}
}

// A free tier is not what "subscribe" costs, and a private plan is not on
// offer to the person reading the Shop.
func TestPlanSourceIgnoresFreeAndNonPublicPlans(t *testing.T) {
	server := catalogStub(t, http.StatusOK, `{"products":[{"id":"arena","plans":[
		{"slug":"free","priceCents":0,"interval":"month","public":true},
		{"slug":"secret","priceCents":100,"interval":"month","public":false},
		{"slug":"all-access","priceCents":999,"interval":"month","public":true},
		{"slug":"premium","priceCents":4900,"interval":"month","public":true}
	]}]}`)
	source := NewPlanSource(server.URL, "arena", server.Client())
	if err := source.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	plan, _ := source.Get()
	if plan.Slug != "all-access" || plan.PriceCents != 999 {
		t.Fatalf("plan = %+v, want the cheapest public paid plan", plan)
	}
}

// The Shop quotes nothing rather than guessing, and the catalog it serves must
// not fail because another service did.
func TestPlanSourceUnknownUntilAReadSucceeds(t *testing.T) {
	server := catalogStub(t, http.StatusInternalServerError, `nope`)
	source := NewPlanSource(server.URL, "arena", server.Client())
	if err := source.Refresh(context.Background()); err == nil {
		t.Fatal("a 500 should be reported to the caller")
	}
	if _, ok := source.Get(); ok {
		t.Fatal("no price may be reported when no read has succeeded")
	}
}

func TestPlanSourceKeepsTheLastGoodAnswerWhenAReadFails(t *testing.T) {
	good := catalogStub(t, http.StatusOK,
		`{"products":[{"id":"arena","plans":[{"slug":"all-access","priceCents":999,"interval":"month","public":true}]}]}`)
	source := NewPlanSource(good.URL, "arena", good.Client())
	if err := source.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// Point it at nothing and fail; a price that was right a minute ago beats
	// removing the figure from the Shop.
	source.catalogURL = good.URL + "/gone"
	_ = source.Refresh(context.Background())
	plan, ok := source.Get()
	if !ok || plan.PriceCents != 999 {
		t.Fatalf("plan = %+v ok=%v, want the previous answer retained", plan, ok)
	}
}

func TestPlanSourceIsInertWithoutAnIssuerOrProduct(t *testing.T) {
	for _, tc := range []struct{ issuer, product string }{
		{"", "arena"}, {"https://accounts.example", ""}, {"  ", "  "},
	} {
		source := NewPlanSource(tc.issuer, tc.product, nil)
		if source != nil {
			t.Fatalf("NewPlanSource(%q,%q) should be nil", tc.issuer, tc.product)
		}
		// And a nil source answers, rather than panicking, on every path.
		if _, ok := source.Get(); ok {
			t.Fatal("a nil source must report no price")
		}
		if err := source.Refresh(context.Background()); err != nil {
			t.Fatalf("a nil source must refresh to nothing, got %v", err)
		}
	}
}

func TestPlanSourceReportsAMissingProduct(t *testing.T) {
	server := catalogStub(t, http.StatusOK, `{"products":[{"id":"reef","plans":[]}]}`)
	source := NewPlanSource(server.URL, "arena", server.Client())
	if err := source.Refresh(context.Background()); err == nil {
		t.Fatal("a catalog without the product should be reported")
	}
	if _, ok := source.Get(); ok {
		t.Fatal("and must not leave a price behind")
	}
}
