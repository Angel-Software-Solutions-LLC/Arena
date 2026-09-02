package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"arena-server/internal/security"
)

// EventType categorises dashboard events.
type EventType string

const (
	EventConnection  EventType = "connection"
	EventAuthFailure EventType = "auth_failure"
	EventHTTPRequest EventType = "http_request"
	EventGameEvent   EventType = "game_event"
	EventError       EventType = "error"
	EventWSMessage   EventType = "ws_message"
	EventRateLimit   EventType = "rate_limit"
)

// DashboardEvent is a single event stored in the ring buffer.
type DashboardEvent struct {
	ID        int64                  `json:"id"`
	Type      EventType              `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data"`
}

// RingBuffer is a fixed-size circular buffer for events.
type RingBuffer struct {
	mu     sync.RWMutex
	events []DashboardEvent
	size   int
	head   int // next write position
	count  int
	nextID int64
}

// NewRingBuffer creates a ring buffer with the given capacity.
func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		events: make([]DashboardEvent, size),
		size:   size,
	}
}

// Add inserts an event into the ring buffer.
func (rb *RingBuffer) Add(evt DashboardEvent) int64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	// EventBus assigns one global ID before copying an event into its
	// type-specific and unified buffers. Standalone RingBuffer callers still
	// get the original local auto-increment behaviour.
	if evt.ID <= 0 {
		rb.nextID++
		evt.ID = rb.nextID
	} else if evt.ID > rb.nextID {
		rb.nextID = evt.ID
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}

	rb.events[rb.head] = evt
	rb.head = (rb.head + 1) % rb.size
	if rb.count < rb.size {
		rb.count++
	}

	return evt.ID
}

// GetAll returns all events in chronological order.
func (rb *RingBuffer) GetAll() []DashboardEvent {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	result := make([]DashboardEvent, 0, rb.count)
	start := (rb.head - rb.count + rb.size) % rb.size
	for i := 0; i < rb.count; i++ {
		idx := (start + i) % rb.size
		result = append(result, rb.events[idx])
	}
	return result
}

// GetSince returns events with ID > sinceID.
func (rb *RingBuffer) GetSince(sinceID int64) []DashboardEvent {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	result := make([]DashboardEvent, 0)
	start := (rb.head - rb.count + rb.size) % rb.size
	for i := 0; i < rb.count; i++ {
		idx := (start + i) % rb.size
		if rb.events[idx].ID > sinceID {
			result = append(result, rb.events[idx])
		}
	}
	return result
}

// Count returns the number of stored events.
func (rb *RingBuffer) Count() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.count
}

// ErrorAggregate tracks error frequency.
type ErrorAggregate struct {
	Message    string    `json:"message"`
	Code       string    `json:"code"`
	Count      int       `json:"count"`
	LastSeen   time.Time `json:"last_seen"`
	FirstSeen  time.Time `json:"first_seen"`
	StackTrace string    `json:"stack_trace,omitempty"`
}

// ErrorAggregator collects and deduplicates errors.
type ErrorAggregator struct {
	mu     sync.RWMutex
	errors map[string]*ErrorAggregate
}

// NewErrorAggregator creates a new aggregator.
func NewErrorAggregator() *ErrorAggregator {
	return &ErrorAggregator{
		errors: make(map[string]*ErrorAggregate),
	}
}

// maxDistinctErrors bounds ErrorAggregator's map so a source that keeps
// producing new distinct (code, message) pairs can't grow it unboundedly --
// every other event store in this package (RingBuffer) is already
// fixed-size, and this map was the one exception.
const maxDistinctErrors = 1000

// Record adds or increments an error.
func (ea *ErrorAggregator) Record(message, code, stack string) {
	ea.mu.Lock()
	defer ea.mu.Unlock()

	key := code + ":" + message
	now := time.Now()
	if agg, ok := ea.errors[key]; ok {
		agg.Count++
		agg.LastSeen = now
		if stack != "" {
			agg.StackTrace = stack
		}
		return
	}

	if len(ea.errors) >= maxDistinctErrors {
		ea.evictOldestLocked()
	}
	ea.errors[key] = &ErrorAggregate{
		Message:    message,
		Code:       code,
		Count:      1,
		LastSeen:   now,
		FirstSeen:  now,
		StackTrace: stack,
	}
}

// evictOldestLocked removes the least-recently-seen error. Callers must hold ea.mu.
func (ea *ErrorAggregator) evictOldestLocked() {
	var oldestKey string
	var oldestSeen time.Time
	for key, agg := range ea.errors {
		if oldestKey == "" || agg.LastSeen.Before(oldestSeen) {
			oldestKey = key
			oldestSeen = agg.LastSeen
		}
	}
	delete(ea.errors, oldestKey)
}

// GetAll returns all aggregated errors.
func (ea *ErrorAggregator) GetAll() []ErrorAggregate {
	ea.mu.RLock()
	defer ea.mu.RUnlock()

	result := make([]ErrorAggregate, 0, len(ea.errors))
	for _, agg := range ea.errors {
		result = append(result, *agg)
	}
	return result
}

// SSESubscriber represents a connected SSE client.
type SSESubscriber struct {
	Events chan DashboardEvent
	Done   chan struct{}
	Filter EventType // empty = all events
}

// EventBus is the central event distribution hub for the dashboard.
type EventBus struct {
	// Ring buffers per event type.
	Connections *RingBuffer
	HTTPLog     *RingBuffer
	GameEvents  *RingBuffer
	WSMessages  *RingBuffer
	RateLimits  *RingBuffer
	AllEvents   *RingBuffer

	// Error aggregation.
	Errors *ErrorAggregator

	// SSE subscribers.
	subscribers   []*SSESubscriber
	subscribersMu sync.RWMutex

	// Emit is serialized so the ID stored in history is the same ID delivered
	// live, and concurrent event producers cannot fan out IDs out of order.
	emitMu      sync.Mutex
	nextEventID int64
}

// NewEventBus creates the event bus with default buffer sizes.
func NewEventBus() *EventBus {
	return &EventBus{
		Connections: NewRingBuffer(500),
		HTTPLog:     NewRingBuffer(1000),
		GameEvents:  NewRingBuffer(1000),
		WSMessages:  NewRingBuffer(500),
		RateLimits:  NewRingBuffer(200),
		AllEvents:   NewRingBuffer(2000),
		Errors:      NewErrorAggregator(),
	}
}

// Emit publishes an event to the appropriate ring buffer and all SSE subscribers.
func (eb *EventBus) Emit(evt DashboardEvent) {
	eb.emitMu.Lock()
	defer eb.emitMu.Unlock()

	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	eb.nextEventID++
	evt.ID = eb.nextEventID

	// Store in type-specific buffer.
	switch evt.Type {
	case EventConnection, EventAuthFailure:
		eb.Connections.Add(evt)
	case EventHTTPRequest:
		eb.HTTPLog.Add(evt)
	case EventGameEvent:
		eb.GameEvents.Add(evt)
	case EventWSMessage:
		eb.WSMessages.Add(evt)
	case EventRateLimit:
		eb.RateLimits.Add(evt)
	case EventError:
		msg, _ := evt.Data["message"].(string)
		code, _ := evt.Data["code"].(string)
		stack, _ := evt.Data["stack"].(string)
		eb.Errors.Record(msg, code, stack)
	}

	// Every event is stored once in the unified buffer with the exact same ID
	// used by type history and live SSE delivery.
	eb.AllEvents.Add(evt)

	// Fan out to SSE subscribers.
	eb.subscribersMu.RLock()
	subs := make([]*SSESubscriber, len(eb.subscribers))
	copy(subs, eb.subscribers)
	eb.subscribersMu.RUnlock()

	for _, sub := range subs {
		if sub.Filter != "" && sub.Filter != evt.Type {
			continue
		}
		select {
		case sub.Events <- evt:
		default:
			// Drop if subscriber is slow.
		}
	}
}

// Subscribe adds an SSE subscriber.
func (eb *EventBus) Subscribe(filter EventType) *SSESubscriber {
	sub := &SSESubscriber{
		Events: make(chan DashboardEvent, 64),
		Done:   make(chan struct{}),
		Filter: filter,
	}
	eb.subscribersMu.Lock()
	eb.subscribers = append(eb.subscribers, sub)
	eb.subscribersMu.Unlock()
	return sub
}

// Unsubscribe removes an SSE subscriber.
func (eb *EventBus) Unsubscribe(sub *SSESubscriber) {
	eb.subscribersMu.Lock()
	defer eb.subscribersMu.Unlock()

	for i, s := range eb.subscribers {
		if s == sub {
			eb.subscribers = append(eb.subscribers[:i], eb.subscribers[i+1:]...)
			close(sub.Done)
			return
		}
	}
}

// DashboardLogMiddleware creates an HTTP middleware that logs requests to the event bus.
func DashboardLogMiddleware(bus *EventBus) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Skip logging for SSE streams and WebSocket upgrades (they're long-lived).
			isStream := strings.Contains(r.URL.Path, "/stream/")
			isWS := strings.HasPrefix(r.URL.Path, "/ws/") || strings.Contains(r.URL.Path, "/ws/")

			if isStream || isWS {
				next.ServeHTTP(w, r)
				return
			}

			// Wrap the response writer to capture status code.
			wrapped := &statusWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)

			bus.Emit(DashboardEvent{
				Type:      EventHTTPRequest,
				Timestamp: start,
				Data: map[string]interface{}{
					"method":      r.Method,
					"path":        r.URL.Path,
					"status":      wrapped.status,
					"duration_ms": float64(duration.Microseconds()) / 1000.0,
					"ip":          extractIP(r),
					"user_agent":  r.UserAgent(),
					"bytes":       wrapped.bytes,
				},
			})
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Hijack implements http.Hijacker so WebSocket upgrades work through this wrapper.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// Flush implements http.Flusher for SSE support.
func (w *statusWriter) Flush() {
	if fl, ok := w.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// extractIP is the client address as the rate limiter sees it: forwarded
// headers are honoured only from a trusted proxy. This used to believe
// CF-Connecting-IP and X-Forwarded-For from any peer, so a caller abusing the
// public client-error endpoint could have the admin log attribute it to an
// address of their choosing.
func extractIP(r *http.Request) string {
	return security.ExtractClientIP(r)
}

// StructuredError is the enhanced error response format.
type StructuredError struct {
	Error   string                 `json:"error"`
	Code    string                 `json:"code"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// writeStructuredError writes a structured error response and logs it to the event bus.
func writeStructuredError(w http.ResponseWriter, bus *EventBus, status int, message, code string, details map[string]interface{}) {
	resp := StructuredError{
		Error:   message,
		Code:    code,
		Details: details,
	}
	writeJSON(w, status, resp)

	if bus != nil {
		bus.Emit(DashboardEvent{
			Type: EventError,
			Data: map[string]interface{}{
				"message": message,
				"code":    code,
				"status":  status,
				"details": details,
			},
		})
	}
}

// EmitConnection logs a connection event with optional transport diagnostics.
func EmitConnection(bus *EventBus, action, botName, botID, ip, apiKeyID string, err string, details map[string]interface{}) {
	if bus == nil {
		return
	}
	evtType := EventConnection
	if err != "" {
		evtType = EventAuthFailure
	}
	data := make(map[string]interface{}, len(details)+6)
	for key, value := range details {
		data[key] = value
	}
	data["action"] = action
	data["bot_name"] = botName
	data["bot_id"] = botID
	data["ip"] = ip
	data["api_key_id"] = apiKeyID
	data["error"] = err
	bus.Emit(DashboardEvent{
		Type: evtType,
		Data: data,
	})
}

// EmitGameEvent logs a game event (kill, death, spawn, damage).
func EmitGameEvent(bus *EventBus, eventName string, data map[string]interface{}) {
	if bus == nil {
		return
	}
	if data == nil {
		data = make(map[string]interface{})
	}
	data["event"] = eventName
	bus.Emit(DashboardEvent{
		Type: EventGameEvent,
		Data: data,
	})
}

// EmitWSMessage logs a WebSocket message.
func EmitWSMessage(bus *EventBus, botID, botName, action string, data map[string]interface{}) {
	if bus == nil {
		return
	}
	if data == nil {
		data = make(map[string]interface{})
	}
	data["bot_id"] = botID
	data["bot_name"] = botName
	data["action"] = action
	bus.Emit(DashboardEvent{
		Type: EventWSMessage,
		Data: data,
	})
}

// EmitRateLimit logs a rate limit event.
func EmitRateLimit(bus *EventBus, key, ip string, count, limit int, retryAfter float64) {
	if bus == nil {
		return
	}
	bus.Emit(DashboardEvent{
		Type: EventRateLimit,
		Data: map[string]interface{}{
			"key":         key,
			"ip":          ip,
			"count":       count,
			"limit":       limit,
			"retry_after": fmt.Sprintf("%.0fs", retryAfter),
		},
	})
}
