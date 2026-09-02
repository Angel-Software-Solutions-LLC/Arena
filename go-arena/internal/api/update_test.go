package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arena-server/internal/config"
)

func TestSplitRepo(t *testing.T) {
	cases := []struct{ in, owner, name string }{
		{"Angel-Software-Solutions-LLC/Arena", "Angel-Software-Solutions-LLC", "Arena"},
		{" foo/bar ", "foo", "bar"},
		{"", "Angel-Software-Solutions-LLC", "Arena"},
		{"nowhere", "Angel-Software-Solutions-LLC", "Arena"},
		{"a/b/c", "a", "b/c"},
	}
	for _, c := range cases {
		o, n := splitRepo(c.in)
		if o != c.owner || n != c.name {
			t.Errorf("splitRepo(%q) = %q/%q, want %q/%q", c.in, o, n, c.owner, c.name)
		}
	}
}

// withUpdaterConfig temporarily sets the updater config and restores it.
func withUpdaterConfig(t *testing.T, url, secret string) {
	t.Helper()
	prevURL, prevSecret := config.C.UpdaterURL, config.C.UpdaterSharedSecret
	config.C.UpdaterURL = url
	config.C.UpdaterSharedSecret = secret
	t.Cleanup(func() {
		config.C.UpdaterURL = prevURL
		config.C.UpdaterSharedSecret = prevSecret
	})
}

func TestTriggerUpdate_Unconfigured(t *testing.T) {
	withUpdaterConfig(t, "", "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/update", strings.NewReader(`{"commitSha":"`+strings.Repeat("a", 40)+`"}`))
	triggerUpdate(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when updater unconfigured, got %d", rec.Code)
	}
}

func TestTriggerUpdate_BadSHA(t *testing.T) {
	withUpdaterConfig(t, "http://arena-updater:8090/update", "secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/update", strings.NewReader(`{"commitSha":"not-a-sha"}`))
	triggerUpdate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a malformed SHA, got %d", rec.Code)
	}
}

func TestTriggerUpdate_ForwardsToSidecar(t *testing.T) {
	var gotAuth, gotBody string
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"ok":true,"accepted":true}`))
	}))
	defer sidecar.Close()

	withUpdaterConfig(t, sidecar.URL, "the-secret")
	sha := strings.Repeat("a", 40)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/update", strings.NewReader(`{"commitSha":"`+sha+`"}`))
	triggerUpdate(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202 on accepted update, got %d (%s)", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer the-secret" {
		t.Errorf("sidecar got Authorization %q, want Bearer the-secret", gotAuth)
	}
	if !strings.Contains(gotBody, sha) {
		t.Errorf("sidecar body %q did not carry the commit SHA", gotBody)
	}
}

func TestUpdateStatus_Unconfigured(t *testing.T) {
	withUpdaterConfig(t, "", "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/update/status", nil)
	updateStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status must always be 200, got %d", rec.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["reachable"] != false {
		t.Errorf("unconfigured updater should report reachable=false, got %v", body["reachable"])
	}
}

func TestUpdateStatus_ProxiesSidecar(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/status") {
			t.Errorf("expected /status path, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true,"inProgress":true,"phase":"building"}`))
	}))
	defer sidecar.Close()

	withUpdaterConfig(t, sidecar.URL+"/update", "secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/update/status", nil)
	updateStatus(rec, req)

	var body map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["reachable"] != true {
		t.Errorf("reachable should be true when the sidecar answers, got %v", body["reachable"])
	}
	if body["phase"] != "building" {
		t.Errorf("phase should be proxied through, got %v", body["phase"])
	}
}

func TestUpdateStatus_HandlesUpdaterURLWithTrailingSlash(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Fatalf("expected normalized /status path, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true,"inProgress":false,"phase":"idle"}`))
	}))
	defer sidecar.Close()

	withUpdaterConfig(t, sidecar.URL+"/update/", "secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/update/status", nil)
	updateStatus(rec, req)

	var body map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["reachable"] != true {
		t.Fatalf("reachable should be true for a normalized trailing-slash updater URL, got %v", body["reachable"])
	}
}
