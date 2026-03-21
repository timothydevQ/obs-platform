package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ── Domain ────────────────────────────────────────────────────────────────────

type TelemetryType string

const (
	TypeTrace  TelemetryType = "trace"
	TypeMetric TelemetryType = "metric"
	TypeLog    TelemetryType = "log"
)

type Span struct {
	TraceID    string            `json:"trace_id"`
	SpanID     string            `json:"span_id"`
	ParentID   string            `json:"parent_id,omitempty"`
	Service    string            `json:"service"`
	Operation  string            `json:"operation"`
	StartTime  int64             `json:"start_time_ms"`
	DurationMs float64           `json:"duration_ms"`
	StatusCode int               `json:"status_code"`
	Error      bool              `json:"error"`
	Tags       map[string]string `json:"tags,omitempty"`
	Logs       []SpanLog         `json:"logs,omitempty"`
}

type SpanLog struct {
	TimestampMs int64             `json:"timestamp_ms"`
	Fields      map[string]string `json:"fields"`
}

type Metric struct {
	Name      string            `json:"name"`
	Value     float64           `json:"value"`
	Type      string            `json:"type"` // counter, gauge, histogram
	Labels    map[string]string `json:"labels,omitempty"`
	Timestamp int64             `json:"timestamp_ms"`
	Service   string            `json:"service"`
}

type LogEntry struct {
	TraceID     string            `json:"trace_id,omitempty"`
	SpanID      string            `json:"span_id,omitempty"`
	Service     string            `json:"service"`
	Level       string            `json:"level"`
	Message     string            `json:"message"`
	TimestampMs int64             `json:"timestamp_ms"`
	Fields      map[string]string `json:"fields,omitempty"`
}

// ── Tail-Based Sampler ────────────────────────────────────────────────────────

type SamplingDecision int

const (
	SampleKeep SamplingDecision = iota
	SampleDrop
	SamplePending
)

type TraceBuffer struct {
	spans     []*Span
	decision  SamplingDecision
	createdAt time.Time
}

type TailSampler struct {
	mu       sync.Mutex
	buffers  map[string]*TraceBuffer // traceID → buffer
	keepRate float64                  // base keep rate 0-1
}

func NewTailSampler(keepRate float64) *TailSampler {
	ts := &TailSampler{
		buffers:  make(map[string]*TraceBuffer),
		keepRate: keepRate,
	}
	go ts.flushExpired()
	return ts
}

// ShouldKeep applies tail-based sampling rules:
// Always keep: errors, slow requests (>500ms), rare operations
// Sample down: healthy traffic below threshold
func (ts *TailSampler) ShouldKeep(trace []*Span) bool {
	for _, s := range trace {
		if s.Error {
			return true // always keep errors
		}
		if s.DurationMs > 500 {
			return true // always keep slow requests
		}
		if s.StatusCode >= 500 {
			return true // always keep server errors
		}
	}
	// Sample healthy traffic at base rate
	b := make([]byte, 1)
	rand.Read(b)
	return float64(b[0])/255.0 < ts.keepRate
}

func (ts *TailSampler) AddSpan(span *Span) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	buf, ok := ts.buffers[span.TraceID]
	if !ok {
		buf = &TraceBuffer{createdAt: time.Now(), decision: SamplePending}
		ts.buffers[span.TraceID] = buf
	}
	buf.spans = append(buf.spans, span)
}

func (ts *TailSampler) FlushTrace(traceID string) ([]*Span, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	buf, ok := ts.buffers[traceID]
	if !ok {
		return nil, false
	}
	keep := ts.ShouldKeep(buf.spans)
	delete(ts.buffers, traceID)
	if keep {
		return buf.spans, true
	}
	return nil, false
}

func (ts *TailSampler) flushExpired() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		ts.mu.Lock()
		cutoff := time.Now().Add(-60 * time.Second)
		for id, buf := range ts.buffers {
			if buf.createdAt.Before(cutoff) {
				delete(ts.buffers, id)
			}
		}
		ts.mu.Unlock()
	}
}

func (ts *TailSampler) Stats() map[string]int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return map[string]int{"pending_traces": len(ts.buffers)}
}

// ── In-Memory Storage ─────────────────────────────────────────────────────────

type Storage struct {
	mu      sync.RWMutex
	spans   []*Span
	metrics []*Metric
	logs    []*LogEntry
	maxSize int
}

func NewStorage(maxSize int) *Storage {
	return &Storage{maxSize: maxSize}
}

func (s *Storage) StoreSpans(spans []*Span) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spans = append(s.spans, spans...)
	if len(s.spans) > s.maxSize {
		s.spans = s.spans[len(s.spans)-s.maxSize:]
	}
}

func (s *Storage) StoreMetric(m *Metric) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = append(s.metrics, m)
	if len(s.metrics) > s.maxSize {
		s.metrics = s.metrics[len(s.metrics)-s.maxSize:]
	}
}

func (s *Storage) StoreLog(l *LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, l)
	if len(s.logs) > s.maxSize {
		s.logs = s.logs[len(s.logs)-s.maxSize:]
	}
}

func (s *Storage) GetSpansByTrace(traceID string) []*Span {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Span
	for _, sp := range s.spans {
		if sp.TraceID == traceID {
			out = append(out, sp)
		}
	}
	return out
}

func (s *Storage) GetRecentSpans(limit int) []*Span {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.spans) <= limit {
		cp := make([]*Span, len(s.spans))
		copy(cp, s.spans)
		return cp
	}
	cp := make([]*Span, limit)
	copy(cp, s.spans[len(s.spans)-limit:])
	return cp
}

func (s *Storage) GetMetricsByName(name string, limit int) []*Metric {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Metric
	for _, m := range s.metrics {
		if m.Name == name {
			out = append(out, m)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (s *Storage) GetLogsByTrace(traceID string) []*LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*LogEntry
	for _, l := range s.logs {
		if l.TraceID == traceID {
			out = append(out, l)
		}
	}
	return out
}

func (s *Storage) Stats() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]int{
		"spans":   len(s.spans),
		"metrics": len(s.metrics),
		"logs":    len(s.logs),
	}
}

// ── Deployment Tracker ────────────────────────────────────────────────────────

type Deployment struct {
	Service     string    `json:"service"`
	Version     string    `json:"version"`
	CommitSHA   string    `json:"commit_sha"`
	DeployedAt  time.Time `json:"deployed_at"`
	Environment string    `json:"environment"`
}

type DeploymentTracker struct {
	mu          sync.RWMutex
	deployments []*Deployment
}

func NewDeploymentTracker() *DeploymentTracker {
	return &DeploymentTracker{}
}

func (dt *DeploymentTracker) Record(d *Deployment) {
	dt.mu.Lock()
	dt.deployments = append(dt.deployments, d)
	dt.mu.Unlock()
	slog.Info("Deployment recorded", "service", d.Service, "version", d.Version, "sha", d.CommitSHA)
}

func (dt *DeploymentTracker) GetRecent(limit int) []*Deployment {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	if len(dt.deployments) <= limit {
		cp := make([]*Deployment, len(dt.deployments))
		copy(cp, dt.deployments)
		return cp
	}
	cp := make([]*Deployment, limit)
	copy(cp, dt.deployments[len(dt.deployments)-limit:])
	return cp
}

// ── Stats accumulator ─────────────────────────────────────────────────────────

type CollectorStats struct {
	mu            sync.Mutex
	SpansReceived int64
	SpansDropped  int64
	SpansKept     int64
	MetricsRecv   int64
	LogsReceived  int64
	BytesReceived int64
}

func (cs *CollectorStats) snapshot() map[string]int64 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return map[string]int64{
		"spans_received": cs.SpansReceived,
		"spans_dropped":  cs.SpansDropped,
		"spans_kept":     cs.SpansKept,
		"metrics_recv":   cs.MetricsRecv,
		"logs_received":  cs.LogsReceived,
		"bytes_received": cs.BytesReceived,
	}
}

// ── HTTP Handlers ─────────────────────────────────────────────────────────────

type Handler struct {
	sampler    *TailSampler
	storage    *Storage
	tracker    *DeploymentTracker
	stats      *CollectorStats
}

func NewHandler(sampler *TailSampler, storage *Storage, tracker *DeploymentTracker) *Handler {
	return &Handler{
		sampler: sampler,
		storage: storage,
		tracker: tracker,
		stats:   &CollectorStats{},
	}
}

func (h *Handler) receiveTraces(w http.ResponseWriter, r *http.Request) {
	var spans []*Span
	if err := json.NewDecoder(r.Body).Decode(&spans); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	h.stats.mu.Lock()
	h.stats.SpansReceived += int64(len(spans))
	h.stats.mu.Unlock()

	// Group by trace ID and buffer for tail sampling
	byTrace := make(map[string][]*Span)
	for _, s := range spans {
		if s.TraceID == "" {
			s.TraceID = newID()
		}
		if s.SpanID == "" {
			s.SpanID = newID()
		}
		byTrace[s.TraceID] = append(byTrace[s.TraceID], s)
		h.sampler.AddSpan(s)
	}

	// Flush completed traces
	var kept int
	for traceID := range byTrace {
		if sampledSpans, ok := h.sampler.FlushTrace(traceID); ok {
			h.storage.StoreSpans(sampledSpans)
			kept += len(sampledSpans)
		}
	}

	h.stats.mu.Lock()
	h.stats.SpansKept += int64(kept)
	h.stats.SpansDropped += int64(len(spans) - kept)
	h.stats.mu.Unlock()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"received": len(spans),
		"kept":     kept,
		"dropped":  len(spans) - kept,
	})
}

func (h *Handler) receiveMetrics(w http.ResponseWriter, r *http.Request) {
	var metrics []*Metric
	if err := json.NewDecoder(r.Body).Decode(&metrics); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	for _, m := range metrics {
		if m.Timestamp == 0 {
			m.Timestamp = time.Now().UnixMilli()
		}
		h.storage.StoreMetric(m)
	}
	h.stats.mu.Lock()
	h.stats.MetricsRecv += int64(len(metrics))
	h.stats.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]int{"received": len(metrics)})
}

func (h *Handler) receiveLogs(w http.ResponseWriter, r *http.Request) {
	var logs []*LogEntry
	if err := json.NewDecoder(r.Body).Decode(&logs); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	for _, l := range logs {
		if l.TimestampMs == 0 {
			l.TimestampMs = time.Now().UnixMilli()
		}
		h.storage.StoreLog(l)
	}
	h.stats.mu.Lock()
	h.stats.LogsReceived += int64(len(logs))
	h.stats.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]int{"received": len(logs)})
}

func (h *Handler) recordDeployment(w http.ResponseWriter, r *http.Request) {
	var d Deployment
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	if d.DeployedAt.IsZero() {
		d.DeployedAt = time.Now()
	}
	h.tracker.Record(&d)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "recorded"})
}

func (h *Handler) getStats(w http.ResponseWriter, r *http.Request) {
	stats := h.stats.snapshot()
	storageStats := h.storage.Stats()
	samplerStats := h.sampler.Stats()
	result := map[string]any{
		"collector": stats,
		"storage":   storageStats,
		"sampler":   samplerStats,
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) liveness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (h *Handler) readiness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) {
	snap := h.stats.snapshot()
	fmt.Fprintf(w, "# HELP collector_spans_received_total Total spans received\n")
	fmt.Fprintf(w, "collector_spans_received_total %d\n", snap["spans_received"])
	fmt.Fprintf(w, "# HELP collector_spans_kept_total Total spans kept after sampling\n")
	fmt.Fprintf(w, "collector_spans_kept_total %d\n", snap["spans_kept"])
	fmt.Fprintf(w, "# HELP collector_spans_dropped_total Total spans dropped by sampler\n")
	fmt.Fprintf(w, "collector_spans_dropped_total %d\n", snap["spans_dropped"])
	fmt.Fprintf(w, "# HELP collector_metrics_received_total Total metrics received\n")
	fmt.Fprintf(w, "collector_metrics_received_total %d\n", snap["metrics_recv"])
	fmt.Fprintf(w, "# HELP collector_logs_received_total Total log entries received\n")
	fmt.Fprintf(w, "collector_logs_received_total %d\n", snap["logs_received"])
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func methodHandler(handlers map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handlers[strings.ToUpper(r.Method)]; ok {
			h(w, r)
			return
		}
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	keepRate := 0.1 // keep 10% of healthy traffic, 100% of errors/slow
	sampler := NewTailSampler(keepRate)
	storage := NewStorage(100_000)
	tracker := NewDeploymentTracker()
	h := NewHandler(sampler, storage, tracker)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", methodHandler(map[string]http.HandlerFunc{"POST": h.receiveTraces}))
	mux.HandleFunc("/v1/metrics", methodHandler(map[string]http.HandlerFunc{"POST": h.receiveMetrics}))
	mux.HandleFunc("/v1/logs", methodHandler(map[string]http.HandlerFunc{"POST": h.receiveLogs}))
	mux.HandleFunc("/v1/deployments", methodHandler(map[string]http.HandlerFunc{"POST": h.recordDeployment}))
	mux.HandleFunc("/v1/stats", methodHandler(map[string]http.HandlerFunc{"GET": h.getStats}))
	mux.HandleFunc("/healthz/live", h.liveness)
	mux.HandleFunc("/healthz/ready", h.readiness)
	mux.HandleFunc("/metrics", h.metrics)

	port := getEnv("HTTP_PORT", "4318")
	srv := &http.Server{
		Addr:         net.JoinHostPort("", port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		slog.Info("Collector started", "port", port, "sample_rate", keepRate)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("Shutting down collector...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	slog.Info("Collector stopped")
}

// Ensure math is used
var _ = math.Pi
// scaffold
// span domain
// metric domain
// log domain
// span log
// tail sampler struct
// trace buffer
// sampler new
