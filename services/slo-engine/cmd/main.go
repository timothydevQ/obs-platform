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
	"strings"
	"sync"
	"syscall"
	"time"
)

// ── SLO Domain ────────────────────────────────────────────────────────────────

type SLOType string

const (
	SLOLatency    SLOType = "latency"
	SLOErrorRate  SLOType = "error_rate"
	SLOThroughput SLOType = "throughput"
)

type SLO struct {
	ID          string  `json:"id"`
	Service     string  `json:"service"`
	Name        string  `json:"name"`
	Type        SLOType `json:"type"`
	Target      float64 `json:"target"`      // e.g. 99.9 for 99.9%
	Window      string  `json:"window"`      // "30d", "7d", "1d"
	ThresholdMs float64 `json:"threshold_ms,omitempty"` // for latency SLOs
	Enabled     bool    `json:"enabled"`
	CreatedAt   int64   `json:"created_at"`
}

type SLOStatus struct {
	SLO           *SLO    `json:"slo"`
	CurrentValue  float64 `json:"current_value"`
	ErrorBudget   float64 `json:"error_budget_remaining_pct"`
	BurnRate1h    float64 `json:"burn_rate_1h"`
	BurnRate6h    float64 `json:"burn_rate_6h"`
	BurnRate24h   float64 `json:"burn_rate_24h"`
	Status        string  `json:"status"` // ok, warning, critical, breached
	AlertFired    bool    `json:"alert_fired"`
}

type BurnRateAlert struct {
	SLOID       string  `json:"slo_id"`
	Service     string  `json:"service"`
	BurnRate    float64 `json:"burn_rate"`
	Window      string  `json:"window"`
	Severity    string  `json:"severity"` // page, ticket
	FiredAt     int64   `json:"fired_at"`
	Message     string  `json:"message"`
}

// ── SLI Data Point ────────────────────────────────────────────────────────────

type SLIPoint struct {
	Service   string  `json:"service"`
	Timestamp int64   `json:"timestamp_ms"`
	Good      int64   `json:"good_events"`
	Total     int64   `json:"total_events"`
	LatencyMs float64 `json:"latency_ms,omitempty"`
}

// ── SLO Store ─────────────────────────────────────────────────────────────────

type SLOStore struct {
	mu     sync.RWMutex
	slos   map[string]*SLO
	points map[string][]*SLIPoint // sloID → points
	alerts []*BurnRateAlert
}

func NewSLOStore() *SLOStore {
	return &SLOStore{
		slos:   make(map[string]*SLO),
		points: make(map[string][]*SLIPoint),
	}
}

func (s *SLOStore) AddSLO(slo *SLO) {
	s.mu.Lock()
	defer s.mu.Unlock()
	slo.CreatedAt = time.Now().UnixMilli()
	slo.Enabled = true
	s.slos[slo.ID] = slo
}

func (s *SLOStore) GetSLO(id string) (*SLO, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	slo, ok := s.slos[id]
	return slo, ok
}

func (s *SLOStore) ListSLOs() []*SLO {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*SLO, 0, len(s.slos))
	for _, slo := range s.slos {
		out = append(out, slo)
	}
	return out
}

func (s *SLOStore) RecordPoint(sloID string, point *SLIPoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.points[sloID] = append(s.points[sloID], point)
	// Keep last 10000 points per SLO
	if len(s.points[sloID]) > 10000 {
		s.points[sloID] = s.points[sloID][len(s.points[sloID])-10000:]
	}
}

func (s *SLOStore) GetPoints(sloID string, sinceMs int64) []*SLIPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*SLIPoint
	for _, p := range s.points[sloID] {
		if p.Timestamp >= sinceMs {
			out = append(out, p)
		}
	}
	return out
}

func (s *SLOStore) AddAlert(alert *BurnRateAlert) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts = append(s.alerts, alert)
}

func (s *SLOStore) GetAlerts(limit int) []*BurnRateAlert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.alerts) <= limit {
		cp := make([]*BurnRateAlert, len(s.alerts))
		copy(cp, s.alerts)
		return cp
	}
	cp := make([]*BurnRateAlert, limit)
	copy(cp, s.alerts[len(s.alerts)-limit:])
	return cp
}

// ── SLO Engine ────────────────────────────────────────────────────────────────

type SLOEngine struct {
	store *SLOStore
}

func NewSLOEngine(store *SLOStore) *SLOEngine {
	return &SLOEngine{store: store}
}

// windowDuration returns duration in milliseconds for a window string
func windowDuration(w string) int64 {
	switch w {
	case "1h":
		return int64(time.Hour / time.Millisecond)
	case "6h":
		return int64(6 * time.Hour / time.Millisecond)
	case "24h", "1d":
		return int64(24 * time.Hour / time.Millisecond)
	case "7d":
		return int64(7 * 24 * time.Hour / time.Millisecond)
	case "30d":
		return int64(30 * 24 * time.Hour / time.Millisecond)
	default:
		return int64(24 * time.Hour / time.Millisecond)
	}
}

// calculateCompliance computes SLO compliance % for a window
func (e *SLOEngine) calculateCompliance(slo *SLO, windowMs int64) float64 {
	since := time.Now().UnixMilli() - windowMs
	points := e.store.GetPoints(slo.ID, since)
	if len(points) == 0 {
		return 100.0 // no data = assume good
	}

	switch slo.Type {
	case SLOErrorRate:
		var totalGood, total int64
		for _, p := range points {
			totalGood += p.Good
			total += p.Total
		}
		if total == 0 {
			return 100.0
		}
		return float64(totalGood) / float64(total) * 100.0

	case SLOLatency:
		var goodCount, total int64
		for _, p := range points {
			total += p.Total
			if p.LatencyMs <= slo.ThresholdMs {
				goodCount += p.Total
			}
		}
		if total == 0 {
			return 100.0
		}
		return float64(goodCount) / float64(total) * 100.0

	case SLOThroughput:
		// For throughput: compliance = fraction of windows meeting min throughput
		compliant := 0
		for _, p := range points {
			if float64(p.Total) >= slo.Target {
				compliant++
			}
		}
		return float64(compliant) / float64(len(points)) * 100.0
	}
	return 100.0
}

// calculateBurnRate computes how fast we're consuming error budget
// burn_rate = (1 - compliance/100) / (1 - target/100)
// burn_rate > 1 means budget is depleting faster than target allows
func calculateBurnRate(compliance, target float64) float64 {
	errorRate := 1 - compliance/100.0
	allowedErrorRate := 1 - target/100.0
	if allowedErrorRate <= 0 {
		return 0
	}
	return errorRate / allowedErrorRate
}

func (e *SLOEngine) EvaluateSLO(slo *SLO) *SLOStatus {
	if !slo.Enabled {
		return &SLOStatus{SLO: slo, Status: "disabled"}
	}

	windowMs := windowDuration(slo.Window)
	compliance := e.calculateCompliance(slo, windowMs)
	errorBudgetRemaining := (compliance - slo.Target) / (100 - slo.Target) * 100
	if errorBudgetRemaining > 100 {
		errorBudgetRemaining = 100
	}

	burn1h := calculateBurnRate(e.calculateCompliance(slo, windowDuration("1h")), slo.Target)
	burn6h := calculateBurnRate(e.calculateCompliance(slo, windowDuration("6h")), slo.Target)
	burn24h := calculateBurnRate(e.calculateCompliance(slo, windowDuration("24h")), slo.Target)

	status := "ok"
	alertFired := false

	// Multi-window burn rate alerting (Google SRE model)
	// Page: 2% budget in 1h (burn rate 14.4) AND 5% budget in 6h (burn rate 6)
	if burn1h > 14.4 && burn6h > 6 {
		status = "critical"
		alertFired = true
		e.store.AddAlert(&BurnRateAlert{
			SLOID:    slo.ID,
			Service:  slo.Service,
			BurnRate: burn1h,
			Window:   "1h|6h",
			Severity: "page",
			FiredAt:  time.Now().UnixMilli(),
			Message:  fmt.Sprintf("SLO '%s' burning budget at %.1fx rate — page immediately", slo.Name, burn1h),
		})
	} else if burn6h > 6 && burn24h > 3 {
		// Ticket: 5% budget in 6h (burn rate 6) AND 10% in 24h (burn rate 3)
		status = "warning"
		alertFired = true
		e.store.AddAlert(&BurnRateAlert{
			SLOID:    slo.ID,
			Service:  slo.Service,
			BurnRate: burn6h,
			Window:   "6h|24h",
			Severity: "ticket",
			FiredAt:  time.Now().UnixMilli(),
			Message:  fmt.Sprintf("SLO '%s' elevated burn rate %.1fx — create ticket", slo.Name, burn6h),
		})
	} else if compliance < slo.Target {
		status = "breached"
	}

	return &SLOStatus{
		SLO:          slo,
		CurrentValue: compliance,
		ErrorBudget:  math.Max(0, errorBudgetRemaining),
		BurnRate1h:   burn1h,
		BurnRate6h:   burn6h,
		BurnRate24h:  burn24h,
		Status:       status,
		AlertFired:   alertFired,
	}
}

// ── HTTP Handler ──────────────────────────────────────────────────────────────

type handler struct {
	engine *SLOEngine
	store  *SLOStore
}

func (h *handler) createSLO(w http.ResponseWriter, r *http.Request) {
	var slo SLO
	if err := json.NewDecoder(r.Body).Decode(&slo); err != nil || slo.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid SLO definition"})
		return
	}
	h.store.AddSLO(&slo)
	slog.Info("SLO created", "id", slo.ID, "service", slo.Service)
	writeJSON(w, http.StatusCreated, slo)
}

func (h *handler) listSLOs(w http.ResponseWriter, r *http.Request) {
	slos := h.store.ListSLOs()
	if slos == nil {
		slos = []*SLO{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"slos": slos, "count": len(slos)})
}

func (h *handler) getSLOStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/slos/")
	id = strings.TrimSuffix(id, "/status")
	slo, ok := h.store.GetSLO(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "SLO not found"})
		return
	}
	writeJSON(w, http.StatusOK, h.engine.EvaluateSLO(slo))
}

func (h *handler) getAllStatuses(w http.ResponseWriter, r *http.Request) {
	slos := h.store.ListSLOs()
	statuses := make([]*SLOStatus, 0, len(slos))
	for _, slo := range slos {
		statuses = append(statuses, h.engine.EvaluateSLO(slo))
	}
	writeJSON(w, http.StatusOK, map[string]any{"statuses": statuses})
}

func (h *handler) recordSLIPoint(w http.ResponseWriter, r *http.Request) {
	sloID := r.URL.Query().Get("slo_id")
	if sloID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slo_id required"})
		return
	}
	var point SLIPoint
	if err := json.NewDecoder(r.Body).Decode(&point); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid point"})
		return
	}
	if point.Timestamp == 0 {
		point.Timestamp = time.Now().UnixMilli()
	}
	h.store.RecordPoint(sloID, &point)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "recorded"})
}

func (h *handler) getAlerts(w http.ResponseWriter, r *http.Request) {
	alerts := h.store.GetAlerts(50)
	if alerts == nil {
		alerts = []*BurnRateAlert{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts, "count": len(alerts)})
}

func (h *handler) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (h *handler) readiness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *handler) metricsEndpoint(w http.ResponseWriter, _ *http.Request) {
	slos := h.store.ListSLOs()
	for _, slo := range slos {
		status := h.engine.EvaluateSLO(slo)
		fmt.Fprintf(w, "slo_compliance{slo=%q,service=%q} %f\n", slo.ID, slo.Service, status.CurrentValue)
		fmt.Fprintf(w, "slo_error_budget_remaining{slo=%q,service=%q} %f\n", slo.ID, slo.Service, status.ErrorBudget)
		fmt.Fprintf(w, "slo_burn_rate_1h{slo=%q,service=%q} %f\n", slo.ID, slo.Service, status.BurnRate1h)
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
	store := NewSLOStore()
	engine := NewSLOEngine(store)
	h := &handler{engine: engine, store: store}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/slos", methodHandler(map[string]http.HandlerFunc{
		"POST": h.createSLO,
		"GET":  h.listSLOs,
	}))
	mux.HandleFunc("/v1/slos/", methodHandler(map[string]http.HandlerFunc{
		"GET": h.getSLOStatus,
	}))
	mux.HandleFunc("/v1/slos/status", methodHandler(map[string]http.HandlerFunc{
		"GET": h.getAllStatuses,
	}))
	mux.HandleFunc("/v1/sli", methodHandler(map[string]http.HandlerFunc{
		"POST": h.recordSLIPoint,
	}))
	mux.HandleFunc("/v1/alerts", methodHandler(map[string]http.HandlerFunc{
		"GET": h.getAlerts,
	}))
	mux.HandleFunc("/healthz/live", h.liveness)
	mux.HandleFunc("/healthz/ready", h.readiness)
	mux.HandleFunc("/metrics", h.metricsEndpoint)

	port := getEnv("HTTP_PORT", "8091")
	srv := &http.Server{
		Addr:         net.JoinHostPort("", port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		slog.Info("SLO Engine started", "port", port)
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
// slo type
// slo struct
// slo status
// burn rate alert
// sli point
// slo store
// add slo
// get slo
// list slos
// record point
// get points
// add alert
// get alerts
// window duration
// compliance error rate
// compliance latency
// compliance throughput
// burn rate calc
// evaluate slo
// page alert
// ticket alert
// error budget
// create slo handler
// list slos handler
// status handler
// all statuses
// sli handler
// alerts handler
// metrics endpoint
// health handlers
// routes
