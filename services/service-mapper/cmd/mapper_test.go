package main

import (
	"testing"
	"time"
)

func newTestStore() *GraphStore { return NewGraphStore(10000) }
func newTestBuilder() *GraphBuilder { return NewGraphBuilder(newTestStore()) }

func sampleRecord(src, dst string, durationMs float64, hasError bool) *CallRecord {
	return &CallRecord{
		Source:     src,
		Target:     dst,
		DurationMs: durationMs,
		Error:      hasError,
		Timestamp:  time.Now().UnixMilli(),
	}
}

func TestPercentile_Empty(t *testing.T) {
	result := percentile([]float64{}, 99)
	if result != 0 {
		t.Errorf("expected 0 for empty, got %f", result)
	}
}

func TestPercentile_Single(t *testing.T) {
	result := percentile([]float64{42.5}, 50)
	if result != 42.5 {
		t.Errorf("expected 42.5, got %f", result)
	}
}

func TestPercentile_P99Of100(t *testing.T) {
	data := make([]float64, 100)
	for i := range data {
		data[i] = float64(i + 1)
	}
	result := percentile(data, 99)
	if result < 98 || result > 100 {
		t.Errorf("p99 of 1-100 should be ~99, got %f", result)
	}
}

func TestHealthScore_Perfect(t *testing.T) {
	score := healthScore(0.0, 0.0)
	if score != 100 {
		t.Errorf("expected 100 health score, got %f", score)
	}
}

func TestHealthScore_AllErrors(t *testing.T) {
	score := healthScore(1.0, 1000.0)
	if score != 0 {
		t.Errorf("expected 0 health score, got %f", score)
	}
}

func TestHealthScore_HighLatency(t *testing.T) {
	score := healthScore(0.0, 10000.0)
	if score != 50 {
		t.Errorf("expected 50 health score for high latency, got %f", score)
	}
}

func TestGraphStore_AddAndCount(t *testing.T) {
	store := newTestStore()
	store.Add([]*CallRecord{
		sampleRecord("api", "db", 10, false),
		sampleRecord("api", "cache", 1, false),
	})
	if store.Count() != 2 {
		t.Errorf("expected 2 records, got %d", store.Count())
	}
}

func TestGraphStore_GetSinceFilters(t *testing.T) {
	store := newTestStore()
	old := time.Now().UnixMilli() - 60000
	recent := time.Now().UnixMilli()
	store.Add([]*CallRecord{
		{Source: "a", Target: "b", DurationMs: 10, Timestamp: old},
		{Source: "a", Target: "c", DurationMs: 10, Timestamp: recent},
	})
	results := store.GetSince(recent - 1000)
	if len(results) != 1 {
		t.Errorf("expected 1 recent record, got %d", len(results))
	}
}

func TestGraphStore_MaxSize(t *testing.T) {
	store := NewGraphStore(5)
	for i := 0; i < 10; i++ {
		store.Add([]*CallRecord{sampleRecord("a", "b", 10, false)})
	}
	if store.Count() > 5 {
		t.Errorf("expected max 5 records, got %d", store.Count())
	}
}

func TestGraphBuilder_EmptyGraph(t *testing.T) {
	b := newTestBuilder()
	graph := b.Build(300000)
	if graph == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(graph.Nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(graph.Nodes))
	}
}

func TestGraphBuilder_SingleEdge(t *testing.T) {
	store := newTestStore()
	b := NewGraphBuilder(store)
	store.Add([]*CallRecord{sampleRecord("api", "db", 100, false)})
	graph := b.Build(300000)
	if len(graph.Edges) != 1 {
		t.Errorf("expected 1 edge, got %d", len(graph.Edges))
	}
	if graph.Edges[0].Source != "api" || graph.Edges[0].Target != "db" {
		t.Errorf("expected api→db, got %s→%s", graph.Edges[0].Source, graph.Edges[0].Target)
	}
}

func TestGraphBuilder_NodeHasIncomingCalls(t *testing.T) {
	store := newTestStore()
	b := NewGraphBuilder(store)
	store.Add([]*CallRecord{
		sampleRecord("api", "db", 50, false),
		sampleRecord("api", "db", 100, false),
		sampleRecord("worker", "db", 30, false),
	})
	graph := b.Build(300000)
	var dbNode *ServiceStats
	for _, n := range graph.Nodes {
		if n.Name == "db" {
			dbNode = n
		}
	}
	if dbNode == nil {
		t.Fatal("expected db node")
	}
	if dbNode.CallsIn != 3 {
		t.Errorf("expected 3 calls to db, got %d", dbNode.CallsIn)
	}
}

func TestGraphBuilder_ErrorRate(t *testing.T) {
	store := newTestStore()
	b := NewGraphBuilder(store)
	store.Add([]*CallRecord{
		sampleRecord("api", "db", 50, true),
		sampleRecord("api", "db", 50, false),
		sampleRecord("api", "db", 50, false),
		sampleRecord("api", "db", 50, false),
	})
	graph := b.Build(300000)
	if len(graph.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(graph.Edges))
	}
	edge := graph.Edges[0]
	if edge.ErrorRate < 0.24 || edge.ErrorRate > 0.26 {
		t.Errorf("expected ~0.25 error rate, got %f", edge.ErrorRate)
	}
}

func TestGraphBuilder_SlowEdgeDetected(t *testing.T) {
	store := newTestStore()
	b := NewGraphBuilder(store)
	for i := 0; i < 10; i++ {
		store.Add([]*CallRecord{sampleRecord("api", "slow-service", 600, false)})
	}
	graph := b.Build(300000)
	if len(graph.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(graph.Edges))
	}
	if !graph.Edges[0].IsSlow {
		t.Error("expected IsSlow=true for p99>500ms edge")
	}
}

func TestGraphBuilder_HighErrorEdgeDetected(t *testing.T) {
	store := newTestStore()
	b := NewGraphBuilder(store)
	store.Add([]*CallRecord{
		sampleRecord("api", "flaky", 50, true),
		sampleRecord("api", "flaky", 50, true),
	})
	graph := b.Build(300000)
	if len(graph.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(graph.Edges))
	}
	if !graph.Edges[0].HasErrors {
		t.Error("expected HasErrors=true for >1%% error rate")
	}
}

func TestGraphBuilder_RPSCalculation(t *testing.T) {
	store := newTestStore()
	b := NewGraphBuilder(store)
	// Add 60 records in a 60s window → 1 RPS
	for i := 0; i < 60; i++ {
		store.Add([]*CallRecord{sampleRecord("api", "db", 10, false)})
	}
	graph := b.Build(60000) // 60s window
	if len(graph.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(graph.Edges))
	}
	rps := graph.Edges[0].RPS
	if rps < 0.5 || rps > 5.0 {
		t.Errorf("expected ~1 RPS, got %f", rps)
	}
}

func TestTraceErrorPropagation_SingleService(t *testing.T) {
	store := newTestStore()
	b := NewGraphBuilder(store)
	store.Add([]*CallRecord{
		{Source: "gateway", Target: "api", DurationMs: 100, Error: false, Timestamp: time.Now().UnixMilli()},
		{Source: "api", Target: "db", DurationMs: 500, Error: true, Timestamp: time.Now().UnixMilli()},
	})
	path := b.TraceErrorPropagation("gateway", 300000)
	if path == nil {
		t.Fatal("expected non-nil path")
	}
	if path.Service != "gateway" {
		t.Errorf("expected root service gateway, got %s", path.Service)
	}
}

func TestTraceErrorPropagation_NotFound(t *testing.T) {
	b := newTestBuilder()
	path := b.TraceErrorPropagation("nonexistent", 300000)
	if path == nil {
		t.Fatal("expected non-nil path even for service with no data")
	}
}

func TestGraphBuilder_MultipleEdgesFromSameSource(t *testing.T) {
	store := newTestStore()
	b := NewGraphBuilder(store)
	store.Add([]*CallRecord{
		sampleRecord("api", "db", 50, false),
		sampleRecord("api", "cache", 5, false),
		sampleRecord("api", "queue", 2, false),
	})
	graph := b.Build(300000)
	if len(graph.Edges) != 3 {
		t.Errorf("expected 3 edges, got %d", len(graph.Edges))
	}
}

func TestGraphBuilder_P99Calculation(t *testing.T) {
	store := newTestStore()
	b := NewGraphBuilder(store)
	for i := 1; i <= 100; i++ {
		store.Add([]*CallRecord{{
			Source:     "api",
			Target:     "db",
			DurationMs: float64(i),
			Timestamp:  time.Now().UnixMilli(),
		}})
	}
	graph := b.Build(300000)
	if len(graph.Edges) != 1 {
		t.Fatalf("expected 1 edge")
	}
	if graph.Edges[0].P99Ms < 98 || graph.Edges[0].P99Ms > 100 {
		t.Errorf("expected P99 ~99, got %f", graph.Edges[0].P99Ms)
	}
}

func TestGetEnv_Present(t *testing.T) {
	t.Setenv("TEST_MAPPER_KEY", "hello")
	if getEnv("TEST_MAPPER_KEY", "fallback") != "hello" {
		t.Error("expected env value")
	}
}

func TestGetEnv_Missing(t *testing.T) {
	if getEnv("MAPPER_MISSING_KEY_XYZ", "default") != "default" {
		t.Error("expected fallback")
	}
}
// percentile empty
// percentile single
// percentile p99
// health perfect
// health all errors
// health high latency
// store add count
// store get since
// store max size
// builder empty
// builder single edge
// builder node calls
// builder error rate
// builder slow edge
// builder error edge
// builder rps
// propagation single
// propagation not found
// multi edges
// p99 calculation
// getenv
// concurrent add
// node health
// edge sort
// node sort
