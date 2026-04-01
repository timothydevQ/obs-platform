package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// ── Anthropic Types ───────────────────────────────────────────────────────────

type AnthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AnthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []AnthropicMessage `json:"messages"`
}

type AnthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type AnthropicResponse struct {
	Content []AnthropicContent `json:"content"`
}

// ── Anomaly Types ─────────────────────────────────────────────────────────────

type AnomalyType string

const (
	AnomalyLatencySpike   AnomalyType = "latency_spike"
	AnomalyErrorSurge     AnomalyType = "error_surge"
	AnomalyThroughputDrop AnomalyType = "throughput_drop"
	AnomalyDBSlow         AnomalyType = "db_slow_queries"
	AnomalyExternalSlow   AnomalyType = "external_api_slow"
)

type Anomaly struct {
	Type        AnomalyType       `json:"type"`
	Service     string            `json:"service"`
	Operation   string            `json:"operation,omitempty"`
	Value       float64           `json:"value"`
	Baseline    float64           `json:"baseline"`
	ZScore      float64           `json:"z_score"`
	Severity    string            `json:"severity"` // low, medium, high, critical
	DetectedAt  int64             `json:"detected_at"`
	TraceIDs    []string          `json:"trace_ids,omitempty"`
	Context     map[string]string `json:"context,omitempty"`
}

type RootCauseHint struct {
	Anomaly     *Anomaly `json:"anomaly"`
	Summary     string   `json:"summary"`
	LikelyCause string   `json:"likely_cause"`
	Evidence    []string `json:"evidence"`
	Suggestions []string `json:"suggestions"`
	Confidence  float64  `json:"confidence"`
	GeneratedBy string   `json:"generated_by"` // "heuristic" or "llm"
}

// ── Heuristic Analyzer ────────────────────────────────────────────────────────

type MetricWindow struct {
	values []float64
	mu     sync.Mutex
}

func (mw *MetricWindow) Add(v float64) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	mw.values = append(mw.values, v)
	if len(mw.values) > 60 {
		mw.values = mw.values[1:]
	}
}

func (mw *MetricWindow) Stats() (mean, stddev float64, count int) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	n := len(mw.values)
	if n == 0 {
		return 0, 0, 0
	}
	var sum float64
	for _, v := range mw.values {
		sum += v
	}
	mean = sum / float64(n)
	var variance float64
	for _, v := range mw.values {
		d := v - mean
		variance += d * d
	}
	stddev = math.Sqrt(variance / float64(n))
	return mean, stddev, n
}

func (mw *MetricWindow) ZScore(current float64) float64 {
	mean, stddev, count := mw.Stats()
	if count < 10 || stddev == 0 {
		return 0
	}
	return math.Abs(current-mean) / stddev
}

type HeuristicAnalyzer struct {
	mu      sync.Mutex
	windows map[string]*MetricWindow // key: "service:operation:metric"
}

func NewHeuristicAnalyzer() *HeuristicAnalyzer {
	return &HeuristicAnalyzer{
		windows: make(map[string]*MetricWindow),
	}
}

func (h *HeuristicAnalyzer) getWindow(key string) *MetricWindow {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.windows[key]; !ok {
		h.windows[key] = &MetricWindow{}
	}
	return h.windows[key]
}

func (h *HeuristicAnalyzer) Record(service, operation, metric string, value float64) {
	key := fmt.Sprintf("%s:%s:%s", service, operation, metric)
	h.getWindow(key).Add(value)
}

func (h *HeuristicAnalyzer) DetectAnomalies(service, operation, metric string, current float64) *Anomaly {
	key := fmt.Sprintf("%s:%s:%s", service, operation, metric)
	w := h.getWindow(key)
	mean, _, count := w.Stats()
	if count < 10 {
		return nil
	}
	zscore := w.ZScore(current)
	if zscore < 3.0 {
		return nil
	}

	severity := "low"
	if zscore > 10 {
		severity = "critical"
	} else if zscore > 6 {
		severity = "high"
	} else if zscore > 3 {
		severity = "medium"
	}

	var anomalyType AnomalyType
	switch metric {
	case "latency":
		anomalyType = AnomalyLatencySpike
	case "error_rate":
		anomalyType = AnomalyErrorSurge
	case "throughput":
		anomalyType = AnomalyThroughputDrop
	case "db_latency":
		anomalyType = AnomalyDBSlow
	default:
		anomalyType = AnomalyLatencySpike
	}

	return &Anomaly{
		Type:       anomalyType,
		Service:    service,
		Operation:  operation,
		Value:      current,
		Baseline:   mean,
		ZScore:     zscore,
		Severity:   severity,
		DetectedAt: time.Now().UnixMilli(),
	}
}

// ── LLM Client ────────────────────────────────────────────────────────────────

type LLMClient struct {
	apiKey string
	client *http.Client
}

func NewLLMClient(apiKey string) *LLMClient {
	return &LLMClient{
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (l *LLMClient) Analyze(anomaly *Anomaly) (string, error) {
	if l.apiKey == "" {
		return "", nil
	}

	prompt := fmt.Sprintf(
		"You are an SRE analyzing a production anomaly. Be concise (2-3 sentences max).\n\n"+
			"Service: %s\nOperation: %s\nAnomaly: %s\nCurrent value: %.2f\nBaseline: %.2f\nZ-score: %.1f\nSeverity: %s\n\n"+
			"Provide: 1) Most likely root cause, 2) One immediate action to take.",
		anomaly.Service, anomaly.Operation, anomaly.Type,
		anomaly.Value, anomaly.Baseline, anomaly.ZScore, anomaly.Severity,
	)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 200,
		Messages:  []AnthropicMessage{{Role: "user", Content: prompt}},
	}

	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", l.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := l.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var ar AnthropicResponse
	if err := json.Unmarshal(respBody, &ar); err != nil || len(ar.Content) == 0 {
		return "", fmt.Errorf("invalid response from LLM")
	}
	return ar.Content[0].Text, nil
}

// ── Root Cause Engine ─────────────────────────────────────────────────────────

type RootCauseEngine struct {
	heuristic *HeuristicAnalyzer
	llm       *LLMClient
}

func NewRootCauseEngine(heuristic *HeuristicAnalyzer, llm *LLMClient) *RootCauseEngine {
	return &RootCauseEngine{heuristic: heuristic, llm: llm}
}

func (e *RootCauseEngine) generateHints(anomaly *Anomaly) *RootCauseHint {
	hint := &RootCauseHint{
		Anomaly:     anomaly,
		Confidence:  0.6,
		GeneratedBy: "heuristic",
	}

	switch anomaly.Type {
	case AnomalyLatencySpike:
		hint.LikelyCause = fmt.Sprintf("Latency spike in %s:%s — %.0fms vs %.0fms baseline (%.1fx increase)",
			anomaly.Service, anomaly.Operation, anomaly.Value, anomaly.Baseline, anomaly.Value/anomaly.Baseline)
		hint.Evidence = []string{
			fmt.Sprintf("Z-score of %.1f indicates significant deviation", anomaly.ZScore),
			fmt.Sprintf("Current: %.0fms, Baseline: %.0fms", anomaly.Value, anomaly.Baseline),
		}
		hint.Suggestions = []string{
			"Check downstream service call durations in trace waterfall",
			"Inspect database query plans for slow queries",
			"Verify external API response times",
			"Check for GC pauses or memory pressure",
		}
		hint.Summary = fmt.Sprintf("P99 latency for %s spiked to %.0fms (%.1f sigma above baseline)",
			anomaly.Service, anomaly.Value, anomaly.ZScore)

	case AnomalyErrorSurge:
		hint.LikelyCause = fmt.Sprintf("Error rate surge in %s — %.1f%% vs %.1f%% baseline",
			anomaly.Service, anomaly.Value*100, anomaly.Baseline*100)
		hint.Evidence = []string{
			fmt.Sprintf("Error rate %.1fx above normal", anomaly.Value/anomaly.Baseline),
			"Check recent deployments for regression",
		}
		hint.Suggestions = []string{
			"Examine error logs for common exception patterns",
			"Check if dependent services have active incidents",
			"Review recent deployment — rollback if needed",
			"Verify database connection pool health",
		}
		hint.Summary = fmt.Sprintf("Error rate in %s surged to %.1f%% (z=%.1f)",
			anomaly.Service, anomaly.Value*100, anomaly.ZScore)

	case AnomalyDBSlow:
		hint.LikelyCause = "Database query slowdown — possible lock contention, missing index, or connection pool exhaustion"
		hint.Evidence = []string{
			fmt.Sprintf("DB latency: %.0fms vs %.0fms normal", anomaly.Value, anomaly.Baseline),
			"Correlates with application latency spike",
		}
		hint.Suggestions = []string{
			"Run EXPLAIN on slowest queries",
			"Check for table locks with SHOW PROCESSLIST",
			"Verify connection pool size and wait times",
			"Check disk I/O metrics for storage bottleneck",
		}
		hint.Summary = fmt.Sprintf("Database queries in %s slowed to %.0fms average", anomaly.Service, anomaly.Value)

	case AnomalyThroughputDrop:
		hint.LikelyCause = "Throughput drop — possible upstream load balancer issue or consumer lag"
		hint.Evidence = []string{
			fmt.Sprintf("Throughput: %.0f rps vs %.0f rps baseline", anomaly.Value, anomaly.Baseline),
		}
		hint.Suggestions = []string{
			"Check load balancer health and connection counts",
			"Verify upstream client configurations",
			"Check for rate limiting hitting thresholds",
		}
		hint.Summary = fmt.Sprintf("Throughput for %s dropped to %.0f RPS (%.1f sigma below baseline)",
			anomaly.Service, anomaly.Value, anomaly.ZScore)
	}

	return hint
}

func (e *RootCauseEngine) Analyze(anomaly *Anomaly) *RootCauseHint {
	hint := e.generateHints(anomaly)

	// Try LLM enhancement
	if llmText, err := e.llm.Analyze(anomaly); err == nil && llmText != "" {
		hint.Summary = llmText
		hint.GeneratedBy = "llm"
		hint.Confidence = 0.85
	}

	return hint
}

// ── Incident Summarizer ───────────────────────────────────────────────────────

type IncidentSummary struct {
	AnomalyCount    int      `json:"anomaly_count"`
	AffectedServices []string `json:"affected_services"`
	Severity        string   `json:"severity"`
	Summary         string   `json:"summary"`
	TopHints        []*RootCauseHint `json:"top_hints"`
	GeneratedAt     int64    `json:"generated_at"`
}

func summarizeIncident(hints []*RootCauseHint) *IncidentSummary {
	if len(hints) == 0 {
		return &IncidentSummary{GeneratedAt: time.Now().UnixMilli()}
	}

	serviceSet := make(map[string]bool)
	maxSeverity := "low"
	severityOrder := map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3}

	for _, h := range hints {
		serviceSet[h.Anomaly.Service] = true
		if severityOrder[h.Anomaly.Severity] > severityOrder[maxSeverity] {
			maxSeverity = h.Anomaly.Severity
		}
	}

	services := make([]string, 0, len(serviceSet))
	for svc := range serviceSet {
		services = append(services, svc)
	}
	sort.Strings(services)

	// Top 3 hints by z-score
	sort.Slice(hints, func(i, j int) bool {
		return hints[i].Anomaly.ZScore > hints[j].Anomaly.ZScore
	})
	topN := 3
	if len(hints) < topN {
		topN = len(hints)
	}

	summary := fmt.Sprintf("%d anomalies detected across %d service(s): %s",
		len(hints), len(services), strings.Join(services, ", "))

	return &IncidentSummary{
		AnomalyCount:     len(hints),
		AffectedServices: services,
		Severity:         maxSeverity,
		Summary:          summary,
		TopHints:         hints[:topN],
		GeneratedAt:      time.Now().UnixMilli(),
	}
}

// ── HTTP Handler ──────────────────────────────────────────────────────────────

type handler struct {
	engine    *RootCauseEngine
	heuristic *HeuristicAnalyzer
}

func (h *handler) analyzeAnomaly(w http.ResponseWriter, r *http.Request) {
	var anomaly Anomaly
	if err := json.NewDecoder(r.Body).Decode(&anomaly); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid anomaly"})
		return
	}
	if anomaly.Service == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "service required"})
		return
	}
	if anomaly.DetectedAt == 0 {
		anomaly.DetectedAt = time.Now().UnixMilli()
	}
	hint := h.engine.Analyze(&anomaly)
	writeJSON(w, http.StatusOK, hint)
}

func (h *handler) recordMetric(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service   string  `json:"service"`
		Operation string  `json:"operation"`
		Metric    string  `json:"metric"`
		Value     float64 `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	h.heuristic.Record(req.Service, req.Operation, req.Metric, req.Value)

	// Auto-detect anomalies
	anomaly := h.heuristic.DetectAnomalies(req.Service, req.Operation, req.Metric, req.Value)
	if anomaly != nil {
		hint := h.engine.Analyze(anomaly)
		slog.Warn("Anomaly detected", "service", anomaly.Service, "type", anomaly.Type, "zscore", anomaly.ZScore)
		writeJSON(w, http.StatusOK, map[string]any{"anomaly_detected": true, "hint": hint})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"anomaly_detected": false})
}

func (h *handler) summarize(w http.ResponseWriter, r *http.Request) {
	var hints []*RootCauseHint
	if err := json.NewDecoder(r.Body).Decode(&hints); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid hints"})
		return
	}
	writeJSON(w, http.StatusOK, summarizeIncident(hints))
}

func (h *handler) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (h *handler) readiness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *handler) metricsEndpoint(w http.ResponseWriter, _ *http.Request) {
	h.heuristic.mu.Lock()
	windowCount := len(h.heuristic.windows)
	h.heuristic.mu.Unlock()
	fmt.Fprintf(w, "ai_analyzer_metric_windows %d\n", windowCount)
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
	heuristic := NewHeuristicAnalyzer()
	llm := NewLLMClient(getEnv("ANTHROPIC_API_KEY", ""))
	engine := NewRootCauseEngine(heuristic, llm)
	h := &handler{engine: engine, heuristic: heuristic}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/analyze", methodHandler(map[string]http.HandlerFunc{"POST": h.analyzeAnomaly}))
	mux.HandleFunc("/v1/record", methodHandler(map[string]http.HandlerFunc{"POST": h.recordMetric}))
	mux.HandleFunc("/v1/summarize", methodHandler(map[string]http.HandlerFunc{"POST": h.summarize}))
	mux.HandleFunc("/healthz/live", h.liveness)
	mux.HandleFunc("/healthz/ready", h.readiness)
	mux.HandleFunc("/metrics", h.metricsEndpoint)

	port := getEnv("HTTP_PORT", "8092")
	srv := &http.Server{
		Addr:         net.JoinHostPort("", port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		slog.Info("AI Analyzer started", "port", port)
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

var _ = math.Pi // ensure math is used
// anthropic types
// anthropic response
// anomaly types
// anomaly struct
// root cause hint
// metric window
// window stats
// window zscore
// zero stddev guard
// window max size
// heuristic analyzer
// get window
// record metric
