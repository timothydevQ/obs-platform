package main

import (
	"fmt"
	"testing"
	"time"
)

func newTestStore() *Store { return NewStore(10000) }
func newTestSvc() *QueryService { return &QueryService{store: newTestStore()} }

func TestPercentile_Empty(t *testing.T) {
	result := percentile([]float64{}, 99)
	if result != 0 {
		t.Errorf("expected 0 for empty slice, got %f", result)
	}
}

func TestPercentile_Single(t *testing.T) {
	result := percentile([]float64{42.0}, 99)
	if result != 42.0 {
		t.Errorf("expected 42.0, got %f", result)
	}
}

func TestPercentile_P50(t *testing.T) {
	data := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	result := percentile(data, 50)
	if result < 4 || result > 6 {
		t.Errorf("p50 of 1-10 should be around 5, got %f", result)
	}
}

func TestPercentile_P99(t *testing.T) {
	data := make([]float64, 100)
	for i := range data {
		data[i] = float64(i + 1)
	}
	result := percentile(data, 99)
	if result < 98 || result > 100 {
		t.Errorf("p99 of 1-100 should be ~99, got %f", result)
	}
}

func TestGetTrace_Found(t *testing.T) {
	svc := newTestSvc()
	svc.store.AddSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api", DurationMs: 100},
		{TraceID: "t1", SpanID: "s2", ParentID: "s1", Service: "db", DurationMs: 20},
	})
	tv := svc.GetTrace("t1")
	if tv == nil {
		t.Fatal("expected trace view, got nil")
	}
	if len(tv.Spans) != 2 {
		t.Errorf("expected 2 spans, got %d", len(tv.Spans))
	}
	if tv.TraceID != "t1" {
		t.Errorf("wrong trace ID: %s", tv.TraceID)
	}
}

func TestGetTrace_NotFound(t *testing.T) {
	svc := newTestSvc()
	tv := svc.GetTrace("nonexistent")
	if tv != nil {
		t.Error("expected nil for nonexistent trace")
	}
}

func TestGetTrace_IncludesCorrelatedLogs(t *testing.T) {
	svc := newTestSvc()
	svc.store.AddSpans([]*Span{{TraceID: "t1", SpanID: "s1", Service: "api", DurationMs: 50}})
	svc.store.AddLogs([]*LogEntry{
		{TraceID: "t1", Service: "api", Level: "error", Message: "timeout"},
		{TraceID: "t2", Service: "api", Level: "info", Message: "ok"},
	})
	tv := svc.GetTrace("t1")
	if len(tv.Logs) != 1 {
		t.Errorf("expected 1 correlated log, got %d", len(tv.Logs))
	}
}

func TestGetTrace_DetectsError(t *testing.T) {
	svc := newTestSvc()
	svc.store.AddSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api", Error: true, DurationMs: 100},
	})
	tv := svc.GetTrace("t1")
	if !tv.HasError {
		t.Error("expected HasError to be true")
	}
}

func TestGetTrace_ServicesDeduped(t *testing.T) {
	svc := newTestSvc()
	svc.store.AddSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api", DurationMs: 100},
		{TraceID: "t1", SpanID: "s2", Service: "api", DurationMs: 10},
		{TraceID: "t1", SpanID: "s3", Service: "db", DurationMs: 20},
	})
	tv := svc.GetTrace("t1")
	if len(tv.Services) != 2 {
		t.Errorf("expected 2 unique services, got %d", len(tv.Services))
	}
}

func TestSearchTraces_AllResults(t *testing.T) {
	svc := newTestSvc()
	svc.store.AddSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api", DurationMs: 100, StartTime: time.Now().UnixMilli()},
		{TraceID: "t2", SpanID: "s2", Service: "api", DurationMs: 200, StartTime: time.Now().UnixMilli()},
	})
	results := svc.SearchTraces("", 0, false, 100)
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestSearchTraces_FiltersByService(t *testing.T) {
	svc := newTestSvc()
	svc.store.AddSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api", DurationMs: 100, StartTime: time.Now().UnixMilli()},
		{TraceID: "t2", SpanID: "s2", Service: "worker", DurationMs: 200, StartTime: time.Now().UnixMilli()},
	})
	results := svc.SearchTraces("api", 0, false, 100)
	if len(results) != 1 {
		t.Errorf("expected 1 result for api, got %d", len(results))
	}
}

func TestSearchTraces_FiltersByMinDuration(t *testing.T) {
	svc := newTestSvc()
	svc.store.AddSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api", DurationMs: 50, StartTime: time.Now().UnixMilli()},
		{TraceID: "t2", SpanID: "s2", Service: "api", DurationMs: 600, StartTime: time.Now().UnixMilli()},
	})
	results := svc.SearchTraces("", 500, false, 100)
	if len(results) != 1 {
		t.Errorf("expected 1 slow trace, got %d", len(results))
	}
}

func TestSearchTraces_FiltersByErrorsOnly(t *testing.T) {
	svc := newTestSvc()
	svc.store.AddSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api", Error: true, DurationMs: 100, StartTime: time.Now().UnixMilli()},
		{TraceID: "t2", SpanID: "s2", Service: "api", Error: false, DurationMs: 100, StartTime: time.Now().UnixMilli()},
	})
	results := svc.SearchTraces("", 0, true, 100)
	if len(results) != 1 {
		t.Errorf("expected 1 error trace, got %d", len(results))
	}
}

func TestSearchTraces_RespectsLimit(t *testing.T) {
	svc := newTestSvc()
	for i := 0; i < 20; i++ {
		svc.store.AddSpans([]*Span{{
			TraceID:   fmt.Sprintf("t%d", i),
			SpanID:    fmt.Sprintf("s%d", i),
			Service:   "api",
			DurationMs: 10,
			StartTime: time.Now().UnixMilli(),
		}})
	}
	results := svc.SearchTraces("", 0, false, 5)
	if len(results) != 5 {
		t.Errorf("expected 5 results (limit), got %d", len(results))
	}
}

func TestGetLatencyStats_Empty(t *testing.T) {
	svc := newTestSvc()
	stats := svc.GetLatencyStats("api", 60_000)
	if stats.Count != 0 {
		t.Errorf("expected 0 count for empty store, got %d", stats.Count)
	}
}

func TestGetLatencyStats_RootSpansOnly(t *testing.T) {
	svc := newTestSvc()
	now := time.Now().UnixMilli()
	svc.store.AddSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api", DurationMs: 100, StartTime: now},         // root
		{TraceID: "t1", SpanID: "s2", ParentID: "s1", Service: "api", DurationMs: 50, StartTime: now}, // child
	})
	stats := svc.GetLatencyStats("api", 60_000)
	if stats.Count != 1 {
		t.Errorf("expected 1 root span, got %d", stats.Count)
	}
}

func TestGetLatencyStats_P99Calculation(t *testing.T) {
	svc := newTestSvc()
	now := time.Now().UnixMilli()
	for i := 1; i <= 100; i++ {
		svc.store.AddSpans([]*Span{{
			TraceID:   fmt.Sprintf("t%d", i),
			SpanID:    fmt.Sprintf("s%d", i),
			Service:   "api",
			DurationMs: float64(i),
			StartTime: now,
		}})
	}
	stats := svc.GetLatencyStats("api", 60_000)
	if stats.P99Ms < 98 || stats.P99Ms > 100 {
		t.Errorf("expected P99 ~99, got %f", stats.P99Ms)
	}
}

func TestBuildServiceMap_Empty(t *testing.T) {
	svc := newTestSvc()
	sm := svc.BuildServiceMap()
	if sm == nil {
		t.Fatal("expected non-nil service map")
	}
	if len(sm.Nodes) != 0 {
		t.Errorf("expected 0 nodes for empty store, got %d", len(sm.Nodes))
	}
}

func TestBuildServiceMap_SingleService(t *testing.T) {
	svc := newTestSvc()
	svc.store.AddSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api", DurationMs: 100},
	})
	sm := svc.BuildServiceMap()
	if len(sm.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(sm.Nodes))
	}
	if sm.Nodes[0].Name != "api" {
		t.Errorf("expected node 'api', got %s", sm.Nodes[0].Name)
	}
}

func TestBuildServiceMap_CreatesEdges(t *testing.T) {
	svc := newTestSvc()
	svc.store.AddSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api", DurationMs: 100},
		{TraceID: "t1", SpanID: "s2", ParentID: "s1", Service: "db", DurationMs: 20},
	})
	sm := svc.BuildServiceMap()
	if len(sm.Edges) != 1 {
		t.Errorf("expected 1 edge api→db, got %d", len(sm.Edges))
	}
	if sm.Edges[0].Source != "api" || sm.Edges[0].Target != "db" {
		t.Errorf("expected edge api→db, got %s→%s", sm.Edges[0].Source, sm.Edges[0].Target)
	}
}

func TestBuildServiceMap_HealthScore(t *testing.T) {
	svc := newTestSvc()
	svc.store.AddSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api", DurationMs: 10},
	})
	sm := svc.BuildServiceMap()
	if sm.Nodes[0].HealthScore <= 0 || sm.Nodes[0].HealthScore > 100 {
		t.Errorf("health score out of range: %f", sm.Nodes[0].HealthScore)
	}
}

func TestCorrelateDeployment_NoAnomaly(t *testing.T) {
	svc := newTestSvc()
	deployTime := time.Now().Add(-2 * time.Minute)
	window := int64(5 * time.Minute / time.Millisecond)

	// Add stable traffic before and after deploy
	for i := 0; i < 10; i++ {
		offset := int64(i) * 10000
		svc.store.AddSpans([]*Span{{
			TraceID:   fmt.Sprintf("pre%d", i),
			SpanID:    fmt.Sprintf("s%d", i),
			Service:   "api",
			DurationMs: 100,
			StartTime: deployTime.UnixMilli() - window + offset,
		}})
		svc.store.AddSpans([]*Span{{
			TraceID:   fmt.Sprintf("post%d", i),
			SpanID:    fmt.Sprintf("sp%d", i),
			Service:   "api",
			DurationMs: 105, // slight increase but not anomalous
			StartTime: deployTime.UnixMilli() + offset,
		}})
	}

	d := &Deployment{Service: "api", Version: "v2.0.0", CommitSHA: "abc", DeployedAt: deployTime}
	result := svc.CorrelateDeployment(d)
	if result.Anomaly {
		t.Error("expected no anomaly for stable traffic")
	}
}

func TestStore_Stats(t *testing.T) {
	s := newTestStore()
	s.AddSpans([]*Span{{TraceID: "t1", SpanID: "s1", Service: "api"}})
	s.AddMetrics([]*MetricPoint{{Name: "m", Value: 1, Service: "api"}})
	s.AddLogs([]*LogEntry{{Service: "api", Level: "info", Message: "ok"}})
	stats := s.Stats()
	if stats["spans"] != 1 { t.Error("expected 1 span") }
	if stats["metrics"] != 1 { t.Error("expected 1 metric") }
	if stats["logs"] != 1 { t.Error("expected 1 log") }
}
// percentile empty
// percentile single
// percentile p50
// percentile p99
// get trace found
// get trace not found
// trace logs
// trace error flag
// trace services dedup
// search all
// search service
// search duration
// search errors
// search limit
// latency empty
// latency root only
// latency p99
// service map empty
// service map single
// service map edges
// health score
// deployment no anomaly
// store stats
// store max span
// store max metric
// latency all services
// search sorted
// ingest spans
// trace duration
// trace no logs
// search empty
// latency by service
// latency all
// service map edges multi
// store add metrics
// store add logs
// store add deployment
// v2 service filter
// v2 multiple services
// v2 trace ids
