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
	"strings"
	"sync"
	"syscall"
	"time"
)

// ── Domain ────────────────────────────────────────────────────────────────────

type CallRecord struct {
	Source      string  `json:"source"`
	Target      string  `json:"target"`
	DurationMs  float64 `json:"duration_ms"`
	Error       bool    `json:"error"`
	Timestamp   int64   `json:"timestamp_ms"`
	Operation   string  `json:"operation,omitempty"`
	TraceID     string  `json:"trace_id,omitempty"`
}

type ServiceStats struct {
	Name        string  `json:"name"`
	CallsIn     int64   `json:"calls_in"`
	CallsOut    int64   `json:"calls_out"`
	Errors      int64   `json:"errors"`
	P50Ms       float64 `json:"p50_ms"`
	P95Ms       float64 `json:"p95_ms"`
	P99Ms       float64 `json:"p99_ms"`
	ErrorRate   float64 `json:"error_rate"`
	HealthScore float64 `json:"health_score"` // 0-100
	LastSeenMs  int64   `json:"last_seen_ms"`
}

type EdgeStats struct {
	Source      string  `json:"source"`
	Target      string  `json:"target"`
	CallCount   int64   `json:"call_count"`
	ErrorCount  int64   `json:"error_count"`
	ErrorRate   float64 `json:"error_rate"`
	P50Ms       float64 `json:"p50_ms"`
	P95Ms       float64 `json:"p95_ms"`
	P99Ms       float64 `json:"p99_ms"`
	RPS         float64 `json:"requests_per_second"`
	IsSlow      bool    `json:"is_slow"`
	HasErrors   bool    `json:"has_errors"`
}

type ServiceGraph struct {
	Nodes     []*ServiceStats `json:"nodes"`
	Edges     []*EdgeStats    `json:"edges"`
	UpdatedAt time.Time       `json:"updated_at"`
	WindowMs  int64           `json:"window_ms"`
}

// ── Graph Store ───────────────────────────────────────────────────────────────

type GraphStore struct {
	mu      sync.RWMutex
	records []*CallRecord
	maxSize int
}

func NewGraphStore(maxSize int) *GraphStore {
	return &GraphStore{maxSize: maxSize}
}

func (s *GraphStore) Add(records []*CallRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, records...)
	if len(s.records) > s.maxSize {
		s.records = s.records[len(s.records)-s.maxSize:]
	}
}

func (s *GraphStore) GetSince(sinceMs int64) []*CallRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*CallRecord
	for _, r := range s.records {
		if r.Timestamp >= sinceMs {
			out = append(out, r)
		}
	}
	return out
}

func (s *GraphStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// ── Graph Builder ─────────────────────────────────────────────────────────────

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

func healthScore(errorRate, p99Ms float64) float64 {
	score := 100.0
	score -= errorRate * 100 * 0.5     // errors cost up to 50 points
	score -= math.Min(p99Ms/20.0, 50.0) // high latency costs up to 50 points
	if score < 0 {
		score = 0
	}
	return score
}

type GraphBuilder struct {
	store *GraphStore
}

func NewGraphBuilder(store *GraphStore) *GraphBuilder {
	return &GraphBuilder{store: store}
}

func (b *GraphBuilder) Build(windowMs int64) *ServiceGraph {
	since := time.Now().UnixMilli() - windowMs
	records := b.store.GetSince(since)

	type edgeKey struct{ src, dst string }
	type edgeData struct {
		calls     int64
		errors    int64
		durations []float64
		firstSeen int64
		lastSeen  int64
	}

	edges := make(map[edgeKey]*edgeData)
	nodes := make(map[string]*edgeData) // per-service incoming stats

	windowSecs := float64(windowMs) / 1000.0

	for _, r := range records {
		// Node stats (incoming calls)
		if _, ok := nodes[r.Target]; !ok {
			nodes[r.Target] = &edgeData{}
		}
		nd := nodes[r.Target]
		nd.calls++
		if r.Error {
			nd.errors++
		}
		nd.durations = append(nd.durations, r.DurationMs)
		if nd.lastSeen < r.Timestamp {
			nd.lastSeen = r.Timestamp
		}

		// Ensure source node exists
		if _, ok := nodes[r.Source]; !ok {
			nodes[r.Source] = &edgeData{}
		}

		// Edge stats
		key := edgeKey{src: r.Source, dst: r.Target}
		if _, ok := edges[key]; !ok {
			edges[key] = &edgeData{firstSeen: r.Timestamp}
		}
		ed := edges[key]
		ed.calls++
		if r.Error {
			ed.errors++
		}
		ed.durations = append(ed.durations, r.DurationMs)
		if ed.lastSeen < r.Timestamp {
			ed.lastSeen = r.Timestamp
		}
	}

	// Build node list
	var nodeList []*ServiceStats
	for name, data := range nodes {
		sort.Float64s(data.durations)
		errRate := 0.0
		if data.calls > 0 {
			errRate = float64(data.errors) / float64(data.calls)
		}
		p99 := percentile(data.durations, 99)
		nodeList = append(nodeList, &ServiceStats{
			Name:        name,
			CallsIn:     data.calls,
			Errors:      data.errors,
			P50Ms:       percentile(data.durations, 50),
			P95Ms:       percentile(data.durations, 95),
			P99Ms:       p99,
			ErrorRate:   errRate,
			HealthScore: healthScore(errRate, p99),
			LastSeenMs:  data.lastSeen,
		})
	}
	sort.Slice(nodeList, func(i, j int) bool { return nodeList[i].Name < nodeList[j].Name })

	// Build edge list
	var edgeList []*EdgeStats
	for key, data := range edges {
		sort.Float64s(data.durations)
		errRate := 0.0
		if data.calls > 0 {
			errRate = float64(data.errors) / float64(data.calls)
		}
		p99 := percentile(data.durations, 99)
		rps := float64(data.calls) / windowSecs

		edgeList = append(edgeList, &EdgeStats{
			Source:     key.src,
			Target:     key.dst,
			CallCount:  data.calls,
			ErrorCount: data.errors,
			ErrorRate:  errRate,
			P50Ms:      percentile(data.durations, 50),
			P95Ms:      percentile(data.durations, 95),
			P99Ms:      p99,
			RPS:        rps,
			IsSlow:     p99 > 500,
			HasErrors:  errRate > 0.01,
		})
	}
	sort.Slice(edgeList, func(i, j int) bool {
		return edgeList[i].Source+edgeList[i].Target < edgeList[j].Source+edgeList[j].Target
	})

	return &ServiceGraph{
		Nodes:     nodeList,
		Edges:     edgeList,
		UpdatedAt: time.Now(),
		WindowMs:  windowMs,
	}
}

// ErrorPropagationPath traces how errors spread across services
type PropagationPath struct {
	Service     string             `json:"service"`
	ErrorRate   float64            `json:"error_rate"`
	Downstream  []*PropagationPath `json:"downstream,omitempty"`
}

func (b *GraphBuilder) TraceErrorPropagation(rootService string, windowMs int64) *PropagationPath {
	graph := b.Build(windowMs)

	// Build adjacency map
	adj := make(map[string][]string)
	errRates := make(map[string]float64)

	for _, node := range graph.Nodes {
		errRates[node.Name] = node.ErrorRate
	}
	for _, edge := range graph.Edges {
		adj[edge.Source] = append(adj[edge.Source], edge.Target)
	}

	var buildPath func(service string, visited map[string]bool, depth int) *PropagationPath
	buildPath = func(service string, visited map[string]bool, depth int) *PropagationPath {
		if visited[service] || depth > 5 {
			return nil
		}
		visited[service] = true
		path := &PropagationPath{
			Service:   service,
			ErrorRate: errRates[service],
		}
		for _, downstream := range adj[service] {
			if child := buildPath(downstream, visited, depth+1); child != nil {
				path.Downstream = append(path.Downstream, child)
			}
		}
		return path
	}

	return buildPath(rootService, make(map[string]bool), 0)
}

// ── HTTP Handler ──────────────────────────────────────────────────────────────

type handler struct {
	builder *GraphBuilder
	store   *GraphStore
}

func (h *handler) recordCalls(w http.ResponseWriter, r *http.Request) {
	var records []*CallRecord
	if err := json.NewDecoder(r.Body).Decode(&records); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	now := time.Now().UnixMilli()
	for _, rec := range records {
		if rec.Timestamp == 0 {
			rec.Timestamp = now
		}
	}
	h.store.Add(records)
	writeJSON(w, http.StatusAccepted, map[string]int{"recorded": len(records)})
}

func (h *handler) getGraph(w http.ResponseWriter, r *http.Request) {
	windowMs := int64(5 * 60 * 1000) // default 5 minutes
	graph := h.builder.Build(windowMs)
	writeJSON(w, http.StatusOK, graph)
}

func (h *handler) getErrorPropagation(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	if service == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "service required"})
		return
	}
	windowMs := int64(5 * 60 * 1000)
	path := h.builder.TraceErrorPropagation(service, windowMs)
	if path == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "service not found in graph"})
		return
	}
	writeJSON(w, http.StatusOK, path)
}

func (h *handler) getStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"total_records": h.store.Count(),
		"updated_at":    time.Now(),
	})
}

func (h *handler) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (h *handler) readiness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *handler) metricsEndpoint(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintf(w, "service_mapper_records_total %d\n", h.store.Count())
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
	store := NewGraphStore(500_000)
	builder := NewGraphBuilder(store)
	h := &handler{builder: builder, store: store}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/calls", methodHandler(map[string]http.HandlerFunc{"POST": h.recordCalls}))
	mux.HandleFunc("/v1/graph", methodHandler(map[string]http.HandlerFunc{"GET": h.getGraph}))
	mux.HandleFunc("/v1/propagation", methodHandler(map[string]http.HandlerFunc{"GET": h.getErrorPropagation}))
	mux.HandleFunc("/v1/stats", methodHandler(map[string]http.HandlerFunc{"GET": h.getStats}))
	mux.HandleFunc("/healthz/live", h.liveness)
	mux.HandleFunc("/healthz/ready", h.readiness)
	mux.HandleFunc("/metrics", h.metricsEndpoint)

	port := getEnv("HTTP_PORT", "8093")
	srv := &http.Server{
		Addr:         net.JoinHostPort("", port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		slog.Info("Service Mapper started", "port", port)
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
// call record
// service stats
// edge stats
// service graph
// graph store
// store add
// store get since
// store count
// percentile
// health score
// graph builder
// build nodes
// build edges
// slow edge
// error edge
// rps calc
// propagation
// cycle guard
// depth limit
// record handler
// graph handler
// propagation handler
// stats handler
// health
// routes
// server
// log graph
// writeJSON
// net join
// signal
// slog
// getenv
// sort nodes
// sort edges
// window secs
// first seen
// last seen
// slog startup
// v2 window param
// v2 node filter
// v2 edge filter
// v2 slow threshold
// v2 error threshold
// v2 graph summary
// v2 service count
// v2 edge count
// extra24
// extra25
// extra26
// extra27
// extra28
