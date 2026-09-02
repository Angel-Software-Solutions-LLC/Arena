package api

// Self-update: version comparison + browser-triggered "update to latest".
//
// The running server never holds Docker-socket access. Instead, a separate
// arena-updater sidecar (updater/, holding /var/run/docker.sock) does the
// fetch+build+recreate. These admin endpoints (1) report the running commit vs.
// the latest commit on the release branch (from GitHub's public API), and (2)
// forward an operator's "update to <sha>" request to the sidecar over the
// internal compose network, authenticated with a shared secret. All three live
// under the admin router, so they inherit its X-Admin-Token / OIDC auth.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"arena-server/internal/config"
	"arena-server/internal/version"
)

var fullSHARe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// githubHTTPClient is a shared client with a sane timeout for GitHub API calls.
var githubHTTPClient = &http.Client{Timeout: 12 * time.Second}

// updaterHTTPClient is intentionally separate from githubHTTPClient because the
// sidecar is a local control-plane dependency with different semantics.
var updaterHTTPClient = &http.Client{Timeout: 15 * time.Second}

// versionInfoCache memoizes the GitHub-derived comparison for a short window so
// every Admin-Panel load does not hit the GitHub API (and its 60/hr anonymous
// rate limit).
var versionInfoCache struct {
	mu        sync.Mutex
	payload   map[string]interface{}
	expiresAt time.Time
}

const versionCacheTTL = 60 * time.Second

// splitRepo parses "owner/repo" into its parts, falling back to the canonical Arena repository.
func splitRepo(repo string) (owner, name string) {
	parts := strings.SplitN(strings.TrimSpace(repo), "/", 2)
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1]
	}
	return "Angel-Software-Solutions-LLC", "Arena"
}

// adminVersionInfo reports the running commit alongside the latest commit on the
// configured release branch and how many commits behind the running build is.
func adminVersionInfo(w http.ResponseWriter, r *http.Request) {
	versionInfoCache.mu.Lock()
	if versionInfoCache.payload != nil && time.Now().Before(versionInfoCache.expiresAt) {
		payload := versionInfoCache.payload
		versionInfoCache.mu.Unlock()
		writeJSON(w, http.StatusOK, payload)
		return
	}
	versionInfoCache.mu.Unlock()

	owner, repo := splitRepo(config.C.UpdateRepo)
	branch := config.C.UpdateBranch
	if branch == "" {
		branch = "main"
	}
	running := version.ResolvedCommit()

	payload := map[string]interface{}{
		"running": map[string]interface{}{
			"commit":    running,
			"short":     version.ShortCommit(),
			"buildTime": version.BuildTime,
		},
		"repo":              owner + "/" + repo,
		"branch":            branch,
		"updaterConfigured": config.C.UpdaterURL != "" && config.C.UpdaterSharedSecret != "",
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	latest, err := githubLatestCommit(ctx, owner, repo, branch, config.C.UpdateGitHubToken)
	if err != nil {
		// Degrade gracefully: still report the running commit so the card renders,
		// just without the "latest / N behind" comparison.
		slog.Warn("admin version: failed to fetch latest commit", "error", err)
		payload["latestError"] = err.Error()
		writeJSON(w, http.StatusOK, payload)
		return
	}
	payload["latest"] = latest

	latestSHA, _ := latest["commit"].(string)
	if running != "" && running != "unknown" && latestSHA != "" {
		if running == latestSHA {
			payload["commitsBehind"] = 0
		} else if behind, compareURL, err := githubCompare(ctx, owner, repo, running, latestSHA, config.C.UpdateGitHubToken); err == nil {
			payload["commitsBehind"] = behind
			payload["compareUrl"] = compareURL
		} else {
			slog.Warn("admin version: compare failed", "error", err)
		}
	}

	versionInfoCache.mu.Lock()
	versionInfoCache.payload = payload
	versionInfoCache.expiresAt = time.Now().Add(versionCacheTTL)
	versionInfoCache.mu.Unlock()

	writeJSON(w, http.StatusOK, payload)
}

// githubLatestCommit fetches the tip commit of a branch on a public repo.
func githubLatestCommit(ctx context.Context, owner, repo, branch, token string) (map[string]interface{}, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", owner, repo, branch)
	var body struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message   string `json:"message"`
			Committer struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := githubGetJSON(ctx, url, token, &body); err != nil {
		return nil, err
	}
	short := body.SHA
	if len(short) > 7 {
		short = short[:7]
	}
	message := body.Commit.Message
	if i := strings.IndexByte(message, '\n'); i >= 0 {
		message = message[:i]
	}
	return map[string]interface{}{
		"commit":      body.SHA,
		"short":       short,
		"committedAt": body.Commit.Committer.Date,
		"htmlUrl":     body.HTMLURL,
		"message":     message,
	}, nil
}

// githubCompare returns how many commits `base` is behind `head` on a public
// repo, plus the compare URL.
func githubCompare(ctx context.Context, owner, repo, base, head, token string) (int, string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/compare/%s...%s", owner, repo, base, head)
	var body struct {
		BehindBy int    `json:"behind_by"`
		HTMLURL  string `json:"html_url"`
	}
	if err := githubGetJSON(ctx, url, token, &body); err != nil {
		return 0, "", err
	}
	return body.BehindBy, body.HTMLURL, nil
}

func githubGetJSON(ctx context.Context, url, token string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "arena-server")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("github %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func updaterStatusURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(u.Path, "/update") {
		u.Path = strings.TrimSuffix(u.Path, "/update")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/status"
	} else {
		u.Path += "/status"
	}
	return u.String(), nil
}

// triggerUpdate forwards an "update to <commitSha>" request to the arena-updater
// sidecar. It returns as soon as the sidecar accepts the job (202); progress is
// polled via updateStatus, since arena-server itself gets recreated as the last
// step and cannot deliver a response to a request it is holding.
func (h *AdminHandler) triggerUpdate(w http.ResponseWriter, r *http.Request) {
	if config.C.UpdaterURL == "" || config.C.UpdaterSharedSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "updater not configured (ARENA_UPDATER_URL / ARENA_UPDATER_SHARED_SECRET unset)")
		return
	}

	var reqBody struct {
		CommitSha string `json:"commitSha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !fullSHARe.MatchString(reqBody.CommitSha) {
		writeError(w, http.StatusBadRequest, "commitSha must be a full 40-character lowercase commit SHA")
		return
	}

	// Serialize checking the active notice, best-effort notice publication, and
	// submission to the sidecar. Otherwise two concurrent
	// requests can each publish and the losing 409 can clear the winner's UI.
	h.updateMu.Lock()
	defer h.updateMu.Unlock()
	statusEngine := h.Engine
	if statusEngine == nil && h.ServiceStatus != nil {
		statusEngine = h.ServiceStatus.engine
	}
	if h.ServiceStatus != nil && statusEngine != nil {
		if active := statusEngine.GetServiceStatus().Maintenance; active != nil {
			writeError(w, http.StatusConflict, "an update or restart is already in progress")
			return
		}
	}

	maintenancePublished := false
	if h != nil && h.ServiceStatus != nil {
		if _, err := h.ServiceStatus.SetMaintenance(
			r.Context(),
			reqBody.CommitSha,
			"preparing",
			"Arena update is being prepared. A brief restart of up to 1 minute is expected.",
			"admin-update",
		); err != nil {
			slog.Warn("failed to publish update notice; continuing with update", "error", err, "commit", reqBody.CommitSha)
		} else {
			maintenancePublished = true
		}
	}
	clearMaintenance := func(source string) {
		if !maintenancePublished || h == nil || h.ServiceStatus == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := h.ServiceStatus.ClearMaintenance(ctx, reqBody.CommitSha, source); err != nil && !errors.Is(err, errStaleMaintenance) {
			slog.Warn("failed to clear maintenance notice after update rejection", "error", err, "commit", reqBody.CommitSha)
		}
	}

	payload, _ := json.Marshal(map[string]string{
		"commitSha":   reqBody.CommitSha,
		"githubToken": config.C.UpdateGitHubToken, // optional; empty is fine for a public repo
	})

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.C.UpdaterURL, bytes.NewReader(payload))
	if err != nil {
		clearMaintenance("update-request-error")
		writeError(w, http.StatusInternalServerError, "failed to build updater request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.C.UpdaterSharedSecret)

	resp, err := updaterHTTPClient.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		// A transport error is ambiguous: the sidecar may have accepted the POST
		// and lost the response. Reconcile against its authenticated status
		// endpoint; if that is also unavailable, preserving the notice is safer
		// than falsely telling every client the update was cancelled.
		status, statusErr := fetchUpdaterStatus(context.Background())
		state := updaterTargetAmbiguous
		if statusErr == nil {
			state = classifyUpdaterTarget(status, reqBody.CommitSha)
		}
		switch state {
		case updaterTargetRejected:
			clearMaintenance("updater-not-accepted")
			writeError(w, http.StatusBadGateway, "updater did not accept the update: "+err.Error())
			return
		case updaterTargetComplete:
			clearMaintenance("updater-reconciled-done")
			writeJSON(w, http.StatusAccepted, map[string]interface{}{
				"ok": true, "commitSha": reqBody.CommitSha, "acceptance": "reconciled", "complete": true,
			})
			return
		case updaterTargetActive:
			if maintenancePublished {
				go h.watchUpdateFailure(reqBody.CommitSha)
			}
			writeJSON(w, http.StatusAccepted, map[string]interface{}{
				"ok": true, "commitSha": reqBody.CommitSha, "acceptance": "reconciled",
			})
			return
		default:
			if maintenancePublished {
				go h.watchUpdateFailure(reqBody.CommitSha)
			}
			writeJSON(w, http.StatusAccepted, map[string]interface{}{
				"ok": true, "commitSha": reqBody.CommitSha, "acceptance": "unknown",
				"message": "the updater may have accepted the request; maintenance status is being preserved while it is reconciled",
			})
			return
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		clearMaintenance("updater-rejected")
		writeError(w, http.StatusBadGateway, fmt.Sprintf("updater responded %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet))))
		return
	}

	slog.Warn("admin triggered self-update", "commit", reqBody.CommitSha)
	if maintenancePublished {
		go h.watchUpdateFailure(reqBody.CommitSha)
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"ok": true, "commitSha": reqBody.CommitSha})
}

type updaterTargetState int

const (
	updaterTargetAmbiguous updaterTargetState = iota
	updaterTargetActive
	updaterTargetComplete
	updaterTargetRejected
)

func updaterStatusString(status map[string]interface{}, key string) string {
	value, _ := status[key].(string)
	return strings.TrimSpace(value)
}

// classifyUpdaterTarget only rejects when a reachable sidecar definitively
// reports that another/no target is active. Unknown or incomplete status data
// stays ambiguous so a potentially accepted update keeps its warning visible.
func classifyUpdaterTarget(status map[string]interface{}, targetCommit string) updaterTargetState {
	target := updaterStatusString(status, "targetCommit")
	completed := updaterStatusString(status, "lastCompletedCommit")
	phase := strings.ToLower(updaterStatusString(status, "phase"))
	inProgress, hasInProgress := status["inProgress"].(bool)

	if completed == targetCommit || (target == targetCommit && phase == "done") {
		return updaterTargetComplete
	}
	if target == targetCommit {
		if phase == "failed" || status["lastError"] != nil {
			return updaterTargetRejected
		}
		if inProgress || phase != "" {
			return updaterTargetActive
		}
		return updaterTargetAmbiguous
	}
	if inProgress {
		if target != "" {
			return updaterTargetRejected
		}
		return updaterTargetAmbiguous
	}
	if hasInProgress && !inProgress {
		// The real sidecar retains targetCommit for the life of the job. A
		// reachable, idle status with no matching target is definitive rejection.
		return updaterTargetRejected
	}
	return updaterTargetAmbiguous
}

// triggerUpdate retains the package-level entrypoint used by focused legacy
// unit tests. Production routes call AdminHandler.triggerUpdate so a durable
// maintenance notice participates in the operation when persistence is healthy.
func triggerUpdate(w http.ResponseWriter, r *http.Request) {
	(&AdminHandler{}).triggerUpdate(w, r)
}

// watchUpdateFailure observes the sidecar while the old app process is still
// alive. It clears a notice when fetch/build fails asynchronously. Successful
// recreates are reconciled by the target build on startup, and newer sidecars
// also send the internal done callback.
func (h *AdminHandler) watchUpdateFailure(targetCommit string) {
	if h == nil || h.ServiceStatus == nil {
		return
	}
	statusEngine := h.Engine
	if statusEngine == nil {
		statusEngine = h.ServiceStatus.engine
	}
	if statusEngine == nil {
		return
	}
	deadline := time.NewTimer(maintenanceFallbackTTL)
	defer deadline.Stop()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	lastPublicPhase := ""
	for {
		select {
		case <-deadline.C:
			return
		case <-ticker.C:
			maintenance := statusEngine.GetServiceStatus().Maintenance
			if maintenance == nil || maintenance.TargetCommit != targetCommit {
				return
			}
			status, err := fetchUpdaterStatus(context.Background())
			if err != nil {
				continue
			}
			phase, _ := status["phase"].(string)
			switch classifyUpdaterTarget(status, targetCommit) {
			case updaterTargetComplete:
				return
			case updaterTargetRejected:
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_, clearErr := h.ServiceStatus.ClearMaintenance(ctx, targetCommit, "updater-failed")
				cancel()
				if clearErr != nil && !errors.Is(clearErr, errStaleMaintenance) {
					slog.Warn("failed to clear failed-update notice", "error", clearErr)
				}
				return
			}
			if phase == "recreating" && phase != lastPublicPhase {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_, phaseErr := h.ServiceStatus.UpdateMaintenancePhase(ctx, targetCommit, "restarting", "Arena is restarting. Connections will return automatically.")
				cancel()
				if phaseErr == nil {
					lastPublicPhase = phase
				}
			}
		}
	}
}

func fetchUpdaterStatus(ctx context.Context) (map[string]interface{}, error) {
	statusURL, err := updaterStatusURL(config.C.UpdaterURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+config.C.UpdaterSharedSecret)
	resp, err := updaterHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("updater status returned %d", resp.StatusCode)
	}
	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	return status, nil
}

// updateStatus proxies the sidecar's in-memory progress. It always returns 200:
// an unreachable updater is itself meaningful ("likely mid recreate"), not an
// error, so the Admin-Panel overlay can keep polling through the brief window
// where arena-server recreates its own container.
func updateStatus(w http.ResponseWriter, r *http.Request) {
	unreachable := map[string]interface{}{"reachable": false, "inProgress": false}

	if config.C.UpdaterURL == "" || config.C.UpdaterSharedSecret == "" {
		writeJSON(w, http.StatusOK, unreachable)
		return
	}
	statusURL, err := updaterStatusURL(config.C.UpdaterURL)
	if err != nil {
		writeJSON(w, http.StatusOK, unreachable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, unreachable)
		return
	}
	req.Header.Set("Authorization", "Bearer "+config.C.UpdaterSharedSecret)

	resp, err := updaterHTTPClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, unreachable)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusOK, unreachable)
		return
	}

	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		writeJSON(w, http.StatusOK, unreachable)
		return
	}
	status["reachable"] = true
	writeJSON(w, http.StatusOK, status)
}
