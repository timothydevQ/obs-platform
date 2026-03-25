package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ── Domain ────────────────────────────────────────────────────────────────────

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
}

type MetricPoint struct {
	Name      string            `json:"name"`
	Value     float64           `json:"value"`
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

type Deployment struct {
	Service    string    `json:"service"`
	Version    string    `json:"version"`
	CommitSHA  string    `json:"commit_sha"`
	DeployedAt time.Time `json:"deployed_at"`
}

// ── Service Map ───────────────────────────────────────────────────────────────

type ServiceEdge struct {
	Source      string  `json:"source"`
	Target      string  `json:"target"`
	RequestRate float64 `json:"request_rate"`
	ErrorRate   float64 `json:"error_rate"`
	P99Ms       float64 `json:"p99_ms"`
	CallCount   int64   `json:"call_count"`
}

type ServiceNode struct {
	Name        string  `json:"name"`
	HealthScore float64 `json:"health_score"` // 0-100
	ErrorRate   float64 `json:"error_rate"`
	P99Ms       float64 `json:"p99_ms"`
	RequestRate float64 `json:"request_rate"`
}

type ServiceMap struct {
	Nodes     []*ServiceNode `json:"nodes"`
	Edges     []*ServiceEdge `json:"edges"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// ── In-Memory Store ───────────────────────────────────────────────────────────

type Store struct {
	mu          sync.RWMutex
	spans       []*Span
	metrics     []*MetricPoint
	logs        []*LogEntry
	deployments []*Deployment
	maxSize     int
}

func NewStore(maxSize int) *Store {
	return &Store{maxSize: maxSize}
}

func (s *Store) AddSpans(spans []*Span) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spans = append(s.spans, spans...)
	if len(s.spans) > s.maxSize {
		s.spans = s.spans[len(s.spans)-s.maxSize:]
	}
}

func (s *Store) AddMetrics(metrics []*MetricPoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = append(s.metrics, metrics...)
	if len(s.metrics) > s.maxSize {
		s.metrics = s.metrics[len(s.metrics)-s.maxSize:]
	}
}

func (s *Store) AddLogs(logs []*LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, logs...)
	if len(s.logs) > s.maxSize {
		s.logs = s.logs[len(s.logs)-s.maxSize:]
	}
}

func (s *Store) AddDeployment(d *Deployment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deployments = append(s.deployments, d)
}

// ── Query Service ─────────────────────────────────────────────────────────────

type QueryService struct{ store *Store }

// GetTrace returns all spans for a trace plus correlated logs
type TraceView struct {
	TraceID  string      `json:"trace_id"`
	Spans    []*Span     `json:"spans"`
	Logs     []*LogEntry `json:"logs"`
	DurationMs float64   `json:"duration_ms"`
	HasError bool        `json:"has_error"`
	Services []string    `json:"services"`
}

func (q *QueryService) GetTrace(traceID string) *TraceView {
	q.store.mu.RLock()
	defer q.store.mu.RUnlock()

	var spans []*Span
	for _, s := range q.store.spans {
		if s.TraceID == traceID {
			spans = append(spans, s)
		}
	}

	var logs []*LogEntry
	for _, l := range q.store.logs {
		if l.TraceID == traceID {
			logs = append(logs, l)
		}
	}

	if len(spans) == 0 {
		return nil
	}

	var minStart, maxEnd int64
	var hasError bool
	serviceSet := make(map[string]bool)
	for _, s := range spans {
		if minStart == 0 || s.StartTime < minStart {
			minStart = s.StartTime
		}
		end := s.StartTime + int64(s.DurationMs)
		if end > maxEnd {
			maxEnd = end
		}
		if s.Error {
			hasError = true
		}
		serviceSet[s.Service] = true
	}

	services := make([]string, 0, len(serviceSet))
	for svc := range serviceSet {
		services = append(services, svc)
	}
	sort.Strings(services)

	return &TraceView{
		TraceID:    traceID,
		Spans:      spans,
		Logs:       logs,
		DurationMs: float64(maxEnd - minStart),
		HasError:   hasError,
		Services:   services,
	}
}

// SearchTraces finds traces matching filters
type TraceSearchResult struct {
	TraceID    string   `json:"trace_id"`
	Services   []string `json:"services"`
	DurationMs float64  `json:"duration_ms"`
	HasError   bool     `json:"has_error"`
	StartTime  int64    `json:"start_time_ms"`
}

func (q *QueryService) SearchTraces(service string, minDurationMs float64, errorsOnly bool, limit int) []*TraceSearchResult {
	q.store.mu.RLock()
	defer q.store.mu.RUnlock()

	// Group spans by trace
	byTrace := make(map[string][]*Span)
	for _, s := range q.store.spans {
		byTrace[s.TraceID] = append(byTrace[s.TraceID], s)
	}

	var results []*TraceSearchResult
	for traceID, spans := range byTrace {
		var hasError bool
		var minStart, maxEnd int64
		serviceSet := make(map[string]bool)
		matchesService := service == ""

		for _, s := range spans {
			if s.Error {
				hasError = true
			}
			if minStart == 0 || s.StartTime < minStart {
				minStart = s.StartTime
			}
			end := s.StartTime + int64(s.DurationMs)
			if end > maxEnd {
				maxEnd = end
			}
			serviceSet[s.Service] = true
			if s.Service == service {
				matchesService = true
			}
		}

		if !matchesService {
			continue
		}
		if errorsOnly && !hasError {
			continue
		}
		totalDuration := float64(maxEnd - minStart)
		if totalDuration < minDurationMs {
			continue
		}

		svcs := make([]string, 0, len(serviceSet))
		for svc := range serviceSet {
			svcs = append(svcs, svc)
		}
		sort.Strings(svcs)

		results = append(results, &TraceSearchResult{
			TraceID:    traceID,
			Services:   svcs,
			DurationMs: totalDuration,
			HasError:   hasError,
			StartTime:  minStart,
		})
	}

	// Sort by start time descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].StartTime > results[j].StartTime
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// GetLatencyPercentiles computes p50/p95/p99 for a service
type LatencyStats struct {
	Service string  `json:"service"`
	P50Ms   float64 `json:"p50_ms"`
	P95Ms   float64 `json:"p95_ms"`
	P99Ms   float64 `json:"p99_ms"`
	Count   int     `json:"count"`
}

func (q *QueryService) GetLatencyStats(service string, windowMs int64) *LatencyStats {
	q.store.mu.RLock()
	defer q.store.mu.RUnlock()

	cutoff := time.Now().UnixMilli() - windowMs
	var durations []float64
	for _, s := range q.store.spans {
		if s.Service != service && service != "" {
			continue
		}
		if s.StartTime < cutoff {
			continue
		}
		if s.ParentID == "" { // root spans only
			durations = append(durations, s.DurationMs)
		}
	}

	if len(durations) == 0 {
		return &LatencyStats{Service: service, Count: 0}
	}

	sort.Float64s(durations)
	return &LatencyStats{
		Service: service,
		P50Ms:   percentile(durations, 50),
		P95Ms:   percentile(durations, 95),
		P99Ms:   percentile(durations, 99),
		Count:   len(durations),
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(len(sorted))*p/100.0)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// BuildServiceMap generates real-time service dependency graph
func (q *QueryService) BuildServiceMap() *ServiceMap {
	q.store.mu.RLock()
	defer q.store.mu.RUnlock()

	type edgeKey struct{ src, dst string }
	type edgeStats struct {
		calls     int64
		errors    int64
		durations []float64
	}

	edges := make(map[edgeKey]*edgeStats)
	nodeStats := make(map[string]*edgeStats)

	// Build parent-child service relationships from spans
	spanByID := make(map[string]*Span)
	for _, s := range q.store.spans {
		spanByID[s.SpanID] = s
	}

	for _, s := range q.store.spans {
		// Track node stats
		if _, ok := nodeStats[s.Service]; !ok {
			nodeStats[s.Service] = &edgeStats{}
		}
		ns := nodeStats[s.Service]
		ns.calls++
		if s.Error {
			ns.errors++
		}
		ns.durations = append(ns.durations, s.DurationMs)

		// Track edge if parent is different service
		if s.ParentID != "" {
			if parent, ok := spanByID[s.ParentID]; ok {
				if parent.Service != s.Service {
					key := edgeKey{src: parent.Service, dst: s.Service}
					if _, ok := edges[key]; !ok {
						edges[key] = &edgeStats{}
					}
					es := edges[key]
					es.calls++
					if s.Error {
						es.errors++
					}
					es.durations = append(es.durations, s.DurationMs)
				}
			}
		}
	}

	var nodes []*ServiceNode
	for name, stats := range nodeStats {
		sort.Float64s(stats.durations)
		errorRate := 0.0
		if stats.calls > 0 {
			errorRate = float64(stats.errors) / float64(stats.calls)
		}
		p99 := percentile(stats.durations, 99)
		healthScore := 100.0 - (errorRate*50.0) - math.Min(p99/10.0, 50.0)
		if healthScore < 0 {
			healthScore = 0
		}
		nodes = append(nodes, &ServiceNode{
			Name:        name,
			HealthScore: healthScore,
			ErrorRate:   errorRate,
			P99Ms:       p99,
			RequestRate: float64(stats.calls),
		})
	}

	var edgeList []*ServiceEdge
	for key, stats := range edges {
		sort.Float64s(stats.durations)
		errorRate := 0.0
		if stats.calls > 0 {
			errorRate = float64(stats.errors) / float64(stats.calls)
		}
		edgeList = append(edgeList, &ServiceEdge{
			Source:      key.src,
			Target:      key.dst,
			RequestRate: float64(stats.calls),
			ErrorRate:   errorRate,
			P99Ms:       percentile(stats.durations, 99),
			CallCount:   stats.calls,
		})
	}

	return &ServiceMap{Nodes: nodes, Edges: edgeList, UpdatedAt: time.Now()}
}

// CorrelateDeployment finds traces that spiked after a deployment
type DeploymentCorrelation struct {
	Deployment    *Deployment  `json:"deployment"`
	PreP99Ms      float64      `json:"pre_p99_ms"`
	PostP99Ms     float64      `json:"post_p99_ms"`
	LatencyChange float64      `json:"latency_change_pct"`
	PreErrorRate  float64      `json:"pre_error_rate"`
	PostErrorRate float64      `json:"post_error_rate"`
	Anomaly       bool         `json:"anomaly"`
}

func (q *QueryService) CorrelateDeployment(d *Deployment) *DeploymentCorrelation {
	window := int64(5 * time.Minute / time.Millisecond)
	deployMs := d.DeployedAt.UnixMilli()

	q.store.mu.RLock()
	defer q.store.mu.RUnlock()

	var preDurations, postDurations []float64
	var preErrors, postErrors int64
	var preCalls, postCalls int64

	for _, s := range q.store.spans {
		if s.Service != d.Service || s.ParentID != "" {
			continue
		}
		if s.StartTime >= deployMs-window && s.StartTime < deployMs {
			preDurations = append(preDurations, s.DurationMs)
			preCalls++
			if s.Error {
				preErrors++
			}
		} else if s.StartTime >= deployMs && s.StartTime < deployMs+window {
			postDurations = append(postDurations, s.DurationMs)
			postCalls++
			if s.Error {
				postErrors++
			}
		}
	}

	sort.Float64s(preDurations)
	sort.Float64s(postDurations)

	preP99 := percentile(preDurations, 99)
	postP99 := percentile(postDurations, 99)

	preErrRate := 0.0
	if preCalls > 0 {
		preErrRate = float64(preErrors) / float64(preCalls)
	}
	postErrRate := 0.0
	if postCalls > 0 {
		postErrRate = float64(postErrors) / float64(postCalls)
	}

	latencyChange := 0.0
	if preP99 > 0 {
		latencyChange = (postP99 - preP99) / preP99 * 100
	}

	anomaly := latencyChange > 20 || (postErrRate-preErrRate) > 0.05

	return &DeploymentCorrelation{
		Deployment:    d,
		PreP99Ms:      preP99,
		PostP99Ms:     postP99,
		LatencyChange: latencyChange,
		PreErrorRate:  preErrRate,
		PostErrorRate: postErrRate,
		Anomaly:       anomaly,
	}
}

// ── HTTP Handler ──────────────────────────────────────────────────────────────

type handler struct{ svc *QueryService }

func (h *handler) getTrace(w http.ResponseWriter, r *http.Request) {
	traceID := strings.TrimPrefix(r.URL.Path, "/v1/traces/")
	if traceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace_id required"})
		return
	}
	tv := h.svc.GetTrace(traceID)
	if tv == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "trace not found"})
		return
	}
	writeJSON(w, http.StatusOK, tv)
}

func (h *handler) searchTraces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := q.Get("service")
	minDuration, _ := strconv.ParseFloat(q.Get("min_duration_ms"), 64)
	errorsOnly := q.Get("errors_only") == "true"
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	results := h.svc.SearchTraces(service, minDuration, errorsOnly, limit)
	if results == nil {
		results = []*TraceSearchResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"traces": results, "count": len(results)})
}

func (h *handler) getLatency(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	windowMs := int64(60_000) // default 1 minute
	if v := r.URL.Query().Get("window_ms"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			windowMs = parsed
		}
	}
	stats := h.svc.GetLatencyStats(service, windowMs)
	writeJSON(w, http.StatusOK, stats)
}

func (h *handler) getServiceMap(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.BuildServiceMap())
}

func (h *handler) ingestData(w http.ResponseWriter, r *http.Request) {
	// Accept spans/metrics/logs pushed from collector
	var payload struct {
		Spans   []*Span        `json:"spans,omitempty"`
		Metrics []*MetricPoint `json:"metrics,omitempty"`
		Logs    []*LogEntry    `json:"logs,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	if len(payload.Spans) > 0 {
		h.svc.store.AddSpans(payload.Spans)
	}
	if len(payload.Metrics) > 0 {
		h.svc.store.AddMetrics(payload.Metrics)
	}
	if len(payload.Logs) > 0 {
		h.svc.store.AddLogs(payload.Logs)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "ingested"})
}

func (h *handler) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (h *handler) readiness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *handler) metricsHandler(w http.ResponseWriter, _ *http.Request) {
	stats := h.svc.store.Stats()
	for k, v := range stats {
		fmt.Fprintf(w, "query_api_%s %d\n", k, v)
	}
}

// ── Store stats ───────────────────────────────────────────────────────────────

func (s *Store) Stats() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]int{
		"spans":   len(s.spans),
		"metrics": len(s.metrics),
		"logs":    len(s.logs),
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

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
	store := NewStore(500_000)
	svc := &QueryService{store: store}
	h := &handler{svc: svc}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces/", methodHandler(map[string]http.HandlerFunc{"GET": h.getTrace}))
	mux.HandleFunc("/v1/traces", methodHandler(map[string]http.HandlerFunc{"GET": h.searchTraces}))
	mux.HandleFunc("/v1/latency", methodHandler(map[string]http.HandlerFunc{"GET": h.getLatency}))
	mux.HandleFunc("/v1/service-map", methodHandler(map[string]http.HandlerFunc{"GET": h.getServiceMap}))
	mux.HandleFunc("/v1/ingest", methodHandler(map[string]http.HandlerFunc{"POST": h.ingestData}))
	mux.HandleFunc("/healthz/live", h.liveness)
	mux.HandleFunc("/healthz/ready", h.readiness)
	mux.HandleFunc("/metrics", h.metricsHandler)

	port := getEnv("HTTP_PORT", "8090")
	srv := &http.Server{
		Addr:         net.JoinHostPort("", port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		slog.Info("Query API started", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
// scaffold
// span domain
// metric point
// log entry
// deployment domain
// service edge
// service node
