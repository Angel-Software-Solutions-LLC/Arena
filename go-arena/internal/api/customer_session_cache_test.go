package api

import (
	"sync"
	"testing"
	"time"
)

func TestForgetAccountSessionsDropsOnlyThatAccount(t *testing.T) {
	h := &CustomerOIDCHandler{sessions: map[string]*CustomerSession{
		"a1": {AccountID: "acct-a", Name: "old"},
		"a2": {AccountID: "acct-a", Name: "old"},
		"b1": {AccountID: "acct-b", Name: "other"},
	}}
	h.ForgetAccountSessions("acct-a")
	if _, ok := h.sessions["a1"]; ok {
		t.Fatal("a1 should be forgotten")
	}
	if _, ok := h.sessions["a2"]; ok {
		t.Fatal("a2 should be forgotten")
	}
	if _, ok := h.sessions["b1"]; !ok {
		t.Fatal("b1 belongs to another account and must stay")
	}
	// A nil handler (customer OIDC not configured) is a no-op, not a panic.
	var none *CustomerOIDCHandler
	none.ForgetAccountSessions("acct-a")
}

func TestSessionExpiryReadsAreSynchronisedWithSlides(t *testing.T) {
	h := &CustomerOIDCHandler{sessions: map[string]*CustomerSession{}}
	session := &CustomerSession{AccountID: "acct", ExpiresAt: time.Now().Add(time.Hour)}
	h.sessions["s"] = session
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				h.mu.Lock()
				session.ExpiresAt = time.Now().Add(2 * time.Hour)
				h.mu.Unlock()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if h.sessionExpiry(session).IsZero() {
					t.Error("expiry read as zero")
				}
			}
		}()
	}
	wg.Wait()
}
