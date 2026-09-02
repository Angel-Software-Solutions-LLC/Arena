package game

import (
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
)

// ServiceNotice is a public, plain-text operational notice. Combat clients may
// use maintenance retry hints, but no field in this structure affects gameplay.
type ServiceNotice struct {
	ID                       int64      `json:"id"`
	Severity                 string     `json:"severity"`
	Message                  string     `json:"message"`
	Phase                    string     `json:"phase,omitempty"`
	TargetCommit             string     `json:"target_commit,omitempty"`
	EstimatedDowntimeSeconds int        `json:"estimated_downtime_seconds,omitempty"`
	RetryAfterSeconds        int        `json:"retry_after_seconds,omitempty"`
	ExpiresAt                *time.Time `json:"expires_at,omitempty"`
	Source                   string     `json:"source,omitempty"`
	PublishedAt              time.Time  `json:"published_at"`
}

// ServiceStatus is the full replacement snapshot sent over REST and both
// WebSocket streams. Durable transitions advance Revision. A final in-memory
// shutdown warning can reuse the durable revision, so clients also compare the
// semantic notice content when revisions are equal.
type ServiceStatus struct {
	Type        string         `json:"type"`
	Revision    int64          `json:"revision"`
	ServerTime  time.Time      `json:"server_time"`
	Broadcast   *ServiceNotice `json:"broadcast"`
	Maintenance *ServiceNotice `json:"maintenance"`
}

func cloneServiceNotice(in *ServiceNotice) *ServiceNotice {
	if in == nil {
		return nil
	}
	out := *in
	if in.ExpiresAt != nil {
		expires := *in.ExpiresAt
		out.ExpiresAt = &expires
	}
	return &out
}

func visibleServiceNotice(in *ServiceNotice, now time.Time) *ServiceNotice {
	out := cloneServiceNotice(in)
	if out != nil && out.ExpiresAt != nil && !out.ExpiresAt.After(now) {
		return nil
	}
	return out
}

// RestoreServiceStatus installs durable state during startup without emitting
// a redundant live event before the HTTP and WebSocket listeners exist.
func (e *GameEngine) RestoreServiceStatus(status ServiceStatus) {
	e.serviceStatusMu.Lock()
	e.serviceStatus = status
	e.serviceStatusMu.Unlock()
}

// SetServiceStatus installs and broadcasts a full replacement snapshot.
func (e *GameEngine) SetServiceStatus(status ServiceStatus) {
	e.RestoreServiceStatus(status)
	e.BroadcastServiceStatus()
}

// GetServiceStatus returns a defensive copy and removes expired current
// notices. Revision remains unchanged so an older notice can never reappear.
func (e *GameEngine) GetServiceStatus() ServiceStatus {
	now := time.Now().UTC()
	e.serviceStatusMu.RLock()
	status := ServiceStatus{
		Type:        "service_status",
		Revision:    e.serviceStatus.Revision,
		ServerTime:  now,
		Broadcast:   visibleServiceNotice(e.serviceStatus.Broadcast, now),
		Maintenance: visibleServiceNotice(e.serviceStatus.Maintenance, now),
	}
	e.serviceStatusMu.RUnlock()
	return status
}

// BroadcastServiceStatus pushes the current snapshot to active and queued bots
// plus every spectator. REST polling and repeated maintenance tick data remain
// the authoritative fallback if a slow client's normal send queue is full.
func (e *GameEngine) BroadcastServiceStatus() {
	status := e.GetServiceStatus()

	data, err := marshalJSON(status)
	if err != nil {
		slog.Error("failed to marshal service status", "error", err)
		return
	}
	// The channels are captured while the engine lock is held: DetachBotSession
	// nils SendChan under the same lock, and reading it afterwards raced with
	// a bot dropping mid-round. One marshal serves every recipient.
	e.mu.RLock()
	channels := make([]chan []byte, 0, len(e.Bots)+len(e.WaitingBots))
	for _, bot := range e.Bots {
		if bot.SendChan != nil {
			channels = append(channels, bot.SendChan)
		}
	}
	for _, bot := range e.WaitingBots {
		if bot.SendChan != nil {
			channels = append(channels, bot.SendChan)
		}
	}
	e.mu.RUnlock()
	for _, ch := range channels {
		safeSend(ch, data)
	}
	e.spectatorsMu.RLock()
	spectators := append([]*SpectatorConn(nil), e.Spectators...)
	e.spectatorsMu.RUnlock()
	BroadcastToSpectators(spectators, data)
}

// SendServiceStatusToSpectator gives a newly connected visitor the current
// control state immediately instead of waiting for a future change or poll.
func (e *GameEngine) SendServiceStatusToSpectator(spec *SpectatorConn) {
	if spec == nil || spec.SendChan == nil {
		return
	}
	data, err := marshalJSON(e.GetServiceStatus())
	if err != nil {
		return
	}
	message, err := newSpectatorMessage(data)
	if err != nil {
		return
	}
	safeSendSpectator(spec.SendChan, message)
}

// NotifyServiceRestart emits an in-memory final warning for manual restarts or
// SIGTERM. Scheduled updates already carry durable maintenance state, which is
// retained and moved to the restarting phase here.
func (e *GameEngine) NotifyServiceRestart(retryAfterSeconds int) {
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 60
	}
	status := e.GetServiceStatus()
	now := time.Now().UTC()
	if status.Maintenance == nil {
		expires := now.Add(time.Duration(retryAfterSeconds+60) * time.Second)
		status.Maintenance = &ServiceNotice{
			Severity:                 "warning",
			Message:                  "Arena is restarting. Connections will return automatically.",
			Phase:                    "restarting",
			EstimatedDowntimeSeconds: retryAfterSeconds,
			RetryAfterSeconds:        retryAfterSeconds,
			ExpiresAt:                &expires,
			Source:                   "shutdown",
			PublishedAt:              now,
		}
	} else {
		status.Maintenance.Phase = "restarting"
		status.Maintenance.Message = "Arena is restarting. Connections will return automatically."
		status.Maintenance.RetryAfterSeconds = retryAfterSeconds
		status.Maintenance.EstimatedDowntimeSeconds = retryAfterSeconds
	}
	e.SetServiceStatus(status)
}

// CloseAllWebSockets closes hijacked bot and spectator connections with the
// standard Service Restart code. Gorilla permits WriteControl concurrently
// with the single normal writer goroutine used by each connection.
func (e *GameEngine) CloseAllWebSockets(reason string) {
	deadline := time.Now().Add(2 * time.Second)
	closeReason := boundedWebSocketCloseReason(reason)
	payload := websocket.FormatCloseMessage(websocket.CloseServiceRestart, closeReason)

	type botConnection struct {
		bot  *BotState
		conn *websocket.Conn
	}
	e.mu.RLock()
	botConns := make([]botConnection, 0, len(e.Bots)+len(e.WaitingBots))
	for _, bot := range e.Bots {
		if bot.Conn != nil {
			botConns = append(botConns, botConnection{bot: bot, conn: bot.Conn})
		}
	}
	for _, bot := range e.WaitingBots {
		if bot.Conn != nil {
			botConns = append(botConns, botConnection{bot: bot, conn: bot.Conn})
		}
	}
	e.mu.RUnlock()

	e.spectatorsMu.RLock()
	specConns := make([]*websocket.Conn, 0, len(e.Spectators))
	for _, spec := range e.Spectators {
		if spec.Conn != nil {
			specConns = append(specConns, spec.Conn)
		}
	}
	e.spectatorsMu.RUnlock()

	for _, current := range botConns {
		current.bot.SignalTransportClose(BotTransportCloseCause{
			Source: "service_restart", CloseCode: websocket.CloseServiceRestart, CloseReason: closeReason,
		})
		_ = current.conn.WriteControl(websocket.CloseMessage, payload, deadline)
		_ = current.conn.Close()
	}
	for _, conn := range specConns {
		_ = conn.WriteControl(websocket.CloseMessage, payload, deadline)
		_ = conn.Close()
	}
}
