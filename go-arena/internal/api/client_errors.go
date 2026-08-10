package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Client-error intake.
//
// The spectator frontend previously had no error reporting of any kind: an
// uncaught exception, a dead render loop, or a failed asset produced console
// output on a machine nobody was watching. A total rendering outage therefore
// ran for four weeks before a human reported it (see the vendored-Babylon
// side-effect regression).
//
// This endpoint is a deliberately small, unauthenticated sink for those
// reports. It writes nothing to the database: reports land in the existing
// bounded in-memory ErrorAggregator (capped at maxDistinctErrors) and show up
// in the admin panel's Errors tab alongside server-side errors. Every field is
// length-capped and the whole route is rate limited, so a hostile client can
// waste its own quota and nothing else.
const (
	maxClientErrorMessage = 500
	maxClientErrorStack   = 2000
	maxClientErrorContext = 200
)

type clientErrorReport struct {
	Message string `json:"message"`
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Stack   string `json:"stack"`
	Page    string `json:"page"`
	Build   string `json:"build"`
}

// truncateRunes bounds a string by runes (not bytes) so multi-byte input
// cannot be cut mid-character, and strips control characters that would
// corrupt the admin log rendering.
func truncateRunes(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "..."
}

// ClientErrorHandler accepts a browser error report and records it on the
// dashboard event bus. It always answers 204 so a reporting failure can never
// cascade into more client-side errors.
func ClientErrorHandler(bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var report clientErrorReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		message := truncateRunes(strings.TrimSpace(report.Message), maxClientErrorMessage)
		if message == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		kind := truncateRunes(strings.TrimSpace(report.Kind), 40)
		if kind == "" {
			kind = "error"
		}
		if bus != nil {
			bus.Emit(DashboardEvent{
				Type: EventError,
				Data: map[string]interface{}{
					"message": message,
					// A distinct code keeps browser reports separable from
					// server-side errors in the admin Errors tab.
					"code":    "client_" + kind,
					"stack":   truncateRunes(report.Stack, maxClientErrorStack),
					"source":  truncateRunes(report.Source, maxClientErrorContext),
					"page":    truncateRunes(report.Page, maxClientErrorContext),
					"build":   truncateRunes(report.Build, maxClientErrorContext),
					"ip":      extractIP(r),
					"origin":  "browser",
				},
			})
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
