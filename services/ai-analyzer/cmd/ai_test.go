package main

import (
	"testing"
)

func newTestHeuristic() *HeuristicAnalyzer {
	return NewHeuristicAnalyzer()
}

func TestMetricWindow_AddAndStats(t *testing.T) {
	mw := &MetricWindow{}
	for i := 0; i < 10; i++ {
		mw.Add(100.0)
	}
	mean, stddev, count := mw.Stats()
	if count != 10 {
		t.Errorf("expected count 10, got %d", count)
	}
	if mean != 100.0 {
		t.Errorf("expected mean 100, got %f", mean)
	}
	if stddev != 0 {
		t.Errorf("expected stddev 0 for constant values, got %f", stddev)
	}
}

func TestMetricWindow_MaxSize(t *testing.T) {
	mw := &MetricWindow{}
	for i := 0; i < 100; i++ {
		mw.Add(float64(i))
	}
	if len(mw.values) > 60 {
		t.Errorf("expected max 60 values, got %d", len(mw.values))
	}
}

func TestMetricWindow_Empty(t *testing.T) {
	mw := &MetricWindow{}
	mean, stddev, count := mw.Stats()
	if count != 0 || mean != 0 || stddev != 0 {
		t.Error("expected all zeros for empty window")
	}
}

func TestMetricWindow_ZScore_NotEnoughData(t *testing.T) {
	mw := &MetricWindow{}
	for i := 0; i < 5; i++ {
		mw.Add(100.0)
	}
	z := mw.ZScore(1000.0)
	if z != 0 {
		t.Errorf("expected 0 z-score with <10 samples, got %f", z)
	}
}

func TestMetricWindow_ZScore_ConstantBaseline(t *testing.T) {
	mw := &MetricWindow{}
	for i := 0; i < 20; i++ {
		mw.Add(100.0)
	}
	z := mw.ZScore(1000.0)
	if z != 0 {
		t.Errorf("expected 0 z-score when stddev is 0, got %f", z)
	}
}

func TestMetricWindow_ZScore_WithVariance(t *testing.T) {
	mw := &MetricWindow{}
	base := []float64{98, 102, 99, 101, 100, 103, 97, 101, 99, 100, 98, 102, 101, 99, 100}
	for _, v := range base {
		mw.Add(v)
	}
	z := mw.ZScore(200.0) // big spike
	if z < 3 {
		t.Errorf("expected high z-score for spike, got %f", z)
	}
}

func TestHeuristicAnalyzer_Record(t *testing.T) {
	h := newTestHeuristic()
	h.Record("api", "GET /users", "latency", 100.0)
	h.mu.Lock()
	_, ok := h.windows["api:GET /users:latency"]
	h.mu.Unlock()
	if !ok {
		t.Error("expected window to be created")
	}
}

func TestHeuristicAnalyzer_NoAnomalyWithFewSamples(t *testing.T) {
	h := newTestHeuristic()
	for i := 0; i < 5; i++ {
		h.Record("api", "GET /users", "latency", 100.0)
	}
	anomaly := h.DetectAnomalies("api", "GET /users", "latency", 1000.0)
	if anomaly != nil {
		t.Error("should not detect anomaly with fewer than 10 samples")
	}
}

func TestHeuristicAnalyzer_DetectsLatencySpike(t *testing.T) {
	h := newTestHeuristic()
	base := []float64{98, 102, 99, 101, 100, 103, 97, 101, 99, 100, 98, 102, 101, 99, 100}
	for _, v := range base {
		h.Record("api", "GET /users", "latency", v)
	}
	anomaly := h.DetectAnomalies("api", "GET /users", "latency", 5000.0)
	if anomaly == nil {
		t.Fatal("expected anomaly for 50x spike")
	}
	if anomaly.Type != AnomalyLatencySpike {
		t.Errorf("expected AnomalyLatencySpike, got %s", anomaly.Type)
	}
	if anomaly.Service != "api" {
		t.Errorf("expected service api, got %s", anomaly.Service)
	}
}

func TestHeuristicAnalyzer_DetectsErrorSurge(t *testing.T) {
	h := newTestHeuristic()
	base := []float64{0.01, 0.01, 0.02, 0.01, 0.01, 0.02, 0.01, 0.01, 0.01, 0.02,
		0.01, 0.01, 0.02, 0.01, 0.01}
	for _, v := range base {
		h.Record("api", "POST /orders", "error_rate", v)
	}
	anomaly := h.DetectAnomalies("api", "POST /orders", "error_rate", 0.5)
	if anomaly == nil {
		t.Fatal("expected anomaly for error surge")
	}
	if anomaly.Type != AnomalyErrorSurge {
		t.Errorf("expected AnomalyErrorSurge, got %s", anomaly.Type)
	}
}

func TestHeuristicAnalyzer_SeverityLevels(t *testing.T) {
	h := newTestHeuristic()
	base := []float64{98, 102, 99, 101, 100, 103, 97, 101, 99, 100, 98, 102, 101, 99, 100, 98, 102, 99, 101, 100}
	for _, v := range base {
		h.Record("api", "op", "latency", v)
	}
	anomaly := h.DetectAnomalies("api", "op", "latency", 100000.0) // extreme spike
	if anomaly != nil && anomaly.Severity != "critical" {
		t.Errorf("expected critical severity for extreme spike, got %s", anomaly.Severity)
	}
}

func TestHeuristicAnalyzer_DBSlowMetric(t *testing.T) {
	h := newTestHeuristic()
	base := []float64{10, 11, 10, 12, 10, 11, 10, 12, 10, 11, 10, 11, 12, 10, 11}
	for _, v := range base {
		h.Record("api", "query", "db_latency", v)
	}
	anomaly := h.DetectAnomalies("api", "query", "db_latency", 500.0)
	if anomaly == nil {
		t.Fatal("expected anomaly for db spike")
	}
	if anomaly.Type != AnomalyDBSlow {
		t.Errorf("expected AnomalyDBSlow, got %s", anomaly.Type)
	}
}

func TestRootCauseEngine_GeneratesHints(t *testing.T) {
	h := newTestHeuristic()
	llm := NewLLMClient("") // no API key
	engine := NewRootCauseEngine(h, llm)
	anomaly := &Anomaly{
		Type:     AnomalyLatencySpike,
		Service:  "api",
		Operation: "GET /users",
		Value:    1500.0,
		Baseline: 100.0,
		ZScore:   8.5,
		Severity: "high",
	}
	hint := engine.Analyze(anomaly)
	if hint == nil {
		t.Fatal("expected non-nil hint")
	}
	if hint.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(hint.Suggestions) == 0 {
		t.Error("expected at least one suggestion")
	}
	if hint.GeneratedBy != "heuristic" {
		t.Errorf("expected heuristic (no API key), got %s", hint.GeneratedBy)
	}
}

func TestRootCauseEngine_ErrorSurgeHints(t *testing.T) {
	h := newTestHeuristic()
	engine := NewRootCauseEngine(h, NewLLMClient(""))
	anomaly := &Anomaly{
		Type:     AnomalyErrorSurge,
		Service:  "payment",
		Value:    0.25,
		Baseline: 0.01,
		ZScore:   12.0,
		Severity: "critical",
	}
	hint := engine.Analyze(anomaly)
	if hint.LikelyCause == "" {
		t.Error("expected non-empty likely cause")
	}
}

func TestRootCauseEngine_DBSlowHints(t *testing.T) {
	h := newTestHeuristic()
	engine := NewRootCauseEngine(h, NewLLMClient(""))
	anomaly := &Anomaly{
		Type:     AnomalyDBSlow,
		Service:  "user-service",
		Value:    800.0,
		Baseline: 15.0,
		ZScore:   5.2,
		Severity: "high",
	}
	hint := engine.Analyze(anomaly)
	if len(hint.Suggestions) == 0 {
		t.Error("expected DB-specific suggestions")
	}
}

func TestRootCauseEngine_ThroughputDropHints(t *testing.T) {
	h := newTestHeuristic()
	engine := NewRootCauseEngine(h, NewLLMClient(""))
	anomaly := &Anomaly{
		Type:     AnomalyThroughputDrop,
		Service:  "api-gateway",
		Value:    50.0,
		Baseline: 1000.0,
		ZScore:   6.0,
		Severity: "high",
	}
	hint := engine.Analyze(anomaly)
	if hint.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

func TestSummarizeIncident_Empty(t *testing.T) {
	result := summarizeIncident([]*RootCauseHint{})
	if result.AnomalyCount != 0 {
		t.Errorf("expected 0 anomalies, got %d", result.AnomalyCount)
	}
}

func TestSummarizeIncident_MultipleServices(t *testing.T) {
	hints := []*RootCauseHint{
		{Anomaly: &Anomaly{Service: "api", ZScore: 5.0, Severity: "medium", Type: AnomalyLatencySpike}},
		{Anomaly: &Anomaly{Service: "db", ZScore: 8.0, Severity: "high", Type: AnomalyDBSlow}},
		{Anomaly: &Anomaly{Service: "cache", ZScore: 3.5, Severity: "low", Type: AnomalyThroughputDrop}},
	}
	result := summarizeIncident(hints)
	if result.AnomalyCount != 3 {
		t.Errorf("expected 3 anomalies, got %d", result.AnomalyCount)
	}
	if len(result.AffectedServices) != 3 {
		t.Errorf("expected 3 services, got %d", len(result.AffectedServices))
	}
	if result.Severity != "high" {
		t.Errorf("expected max severity high, got %s", result.Severity)
	}
}

func TestSummarizeIncident_TopHintsLimit(t *testing.T) {
	hints := make([]*RootCauseHint, 10)
	for i := range hints {
		hints[i] = &RootCauseHint{
			Anomaly: &Anomaly{Service: "svc", ZScore: float64(i), Severity: "low", Type: AnomalyLatencySpike},
		}
	}
	result := summarizeIncident(hints)
	if len(result.TopHints) > 3 {
		t.Errorf("expected max 3 top hints, got %d", len(result.TopHints))
	}
}

func TestSummarizeIncident_SortsByZScore(t *testing.T) {
	hints := []*RootCauseHint{
		{Anomaly: &Anomaly{Service: "a", ZScore: 3.0, Severity: "low", Type: AnomalyLatencySpike}},
		{Anomaly: &Anomaly{Service: "b", ZScore: 9.0, Severity: "critical", Type: AnomalyErrorSurge}},
	}
	result := summarizeIncident(hints)
	if len(result.TopHints) > 0 && result.TopHints[0].Anomaly.ZScore < 9.0 {
		t.Error("expected top hint to be highest z-score")
	}
}

func TestAnthropicRequest_Serialization(t *testing.T) {
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 200,
		Messages:  []AnthropicMessage{{Role: "user", Content: "test"}},
	}
	if req.Model == "" {
		t.Error("expected non-empty model")
	}
	if len(req.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(req.Messages))
	}
}

func TestLLMClient_NoKeyReturnsEmpty(t *testing.T) {
	llm := NewLLMClient("")
	result, err := llm.Analyze(&Anomaly{
		Type: AnomalyLatencySpike, Service: "api", ZScore: 5.0,
	})
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if result != "" {
		t.Error("expected empty result with no API key")
	}
}

func TestGetEnv_Present(t *testing.T) {
	t.Setenv("TEST_KEY_AI", "testvalue")
	got := getEnv("TEST_KEY_AI", "fallback")
	if got != "testvalue" {
		t.Errorf("expected testvalue, got %s", got)
	}
}

func TestGetEnv_Missing(t *testing.T) {
	got := getEnv("THIS_KEY_DOES_NOT_EXIST_AI_XYZ", "fallback")
	if got != "fallback" {
		t.Errorf("expected fallback, got %s", got)
	}
}
// window add stats
// window max size
// window empty
// zscore few samples
// zscore zero stddev
// zscore variance
// heuristic record
// no anomaly few
// latency spike
// error surge
// severity levels
// db slow
// hint non nil
// hint error surge
// hint db
// hint throughput
// hint heuristic
// summarize empty
// summarize multi
// summarize top hints
// summarize sort
// anthropic serial
// llm no key
// getenv present
// getenv missing
