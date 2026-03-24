package main

import (
	"fmt"
	"testing"
	"time"
)

func TestTailSampler_AlwaysKeepsErrors(t *testing.T) {
	ts := NewTailSampler(0.0)
	spans := []*Span{{TraceID: "t1", SpanID: "s1", Error: true, Service: "svc"}}
	if !ts.ShouldKeep(spans) {
		t.Error("error spans should always be kept")
	}
}

func TestTailSampler_AlwaysKeepsSlowRequests(t *testing.T) {
	ts := NewTailSampler(0.0)
	spans := []*Span{{TraceID: "t1", SpanID: "s1", DurationMs: 600, Service: "svc"}}
	if !ts.ShouldKeep(spans) {
		t.Error("slow requests >500ms should always be kept")
	}
}

func TestTailSampler_AlwaysKeepsServerErrors(t *testing.T) {
	ts := NewTailSampler(0.0)
	spans := []*Span{{TraceID: "t1", SpanID: "s1", StatusCode: 503, Service: "svc"}}
	if !ts.ShouldKeep(spans) {
		t.Error("5xx status codes should always be kept")
	}
}

func TestTailSampler_DropsHealthyAtZeroRate(t *testing.T) {
	ts := NewTailSampler(0.0)
	spans := []*Span{{TraceID: "t1", SpanID: "s1", DurationMs: 10, StatusCode: 200, Service: "svc"}}
	kept := 0
	for i := 0; i < 100; i++ {
		if ts.ShouldKeep(spans) {
			kept++
		}
	}
	if kept > 5 {
		t.Errorf("at 0%% rate, expected <=5 kept out of 100, got %d", kept)
	}
}

func TestTailSampler_KeepsAllAtFullRate(t *testing.T) {
	ts := NewTailSampler(1.0)
	spans := []*Span{{TraceID: "t1", SpanID: "s1", DurationMs: 10, StatusCode: 200, Service: "svc"}}
	kept := 0
	for i := 0; i < 100; i++ {
		if ts.ShouldKeep(spans) {
			kept++
		}
	}
	if kept < 90 {
		t.Errorf("at 100%% rate, expected >=90 kept, got %d", kept)
	}
}

func TestTailSampler_AddAndFlush(t *testing.T) {
	ts := NewTailSampler(1.0)
	span := &Span{TraceID: "trace-1", SpanID: "span-1", Service: "svc", DurationMs: 10}
	ts.AddSpan(span)
	spans, ok := ts.FlushTrace("trace-1")
	if !ok {
		t.Fatal("expected trace to be kept")
	}
	if len(spans) != 1 {
		t.Errorf("expected 1 span, got %d", len(spans))
	}
}

func TestTailSampler_FlushNonexistent(t *testing.T) {
	ts := NewTailSampler(1.0)
	_, ok := ts.FlushTrace("nonexistent")
	if ok {
		t.Error("expected false for nonexistent trace")
	}
}

func TestTailSampler_Stats(t *testing.T) {
	ts := NewTailSampler(1.0)
	ts.AddSpan(&Span{TraceID: "t1", SpanID: "s1", Service: "svc"})
	stats := ts.Stats()
	if stats["pending_traces"] != 1 {
		t.Errorf("expected 1 pending trace, got %d", stats["pending_traces"])
	}
}

func TestTailSampler_ErrorBeatsZeroRate(t *testing.T) {
	ts := NewTailSampler(0.0)
	spans := []*Span{
		{TraceID: "t1", SpanID: "s1", DurationMs: 5, StatusCode: 200, Service: "svc"},
		{TraceID: "t1", SpanID: "s2", Error: true, Service: "svc"},
	}
	if !ts.ShouldKeep(spans) {
		t.Error("trace with error span should be kept even at 0%% rate")
	}
}

func TestStorage_StoreAndRetrieveSpans(t *testing.T) {
	s := NewStorage(100)
	span := &Span{TraceID: "t1", SpanID: "s1", Service: "api"}
	s.StoreSpans([]*Span{span})
	result := s.GetSpansByTrace("t1")
	if len(result) != 1 {
		t.Errorf("expected 1 span, got %d", len(result))
	}
}

func TestStorage_GetSpansByTraceFilters(t *testing.T) {
	s := NewStorage(100)
	s.StoreSpans([]*Span{
		{TraceID: "t1", SpanID: "s1", Service: "api"},
		{TraceID: "t2", SpanID: "s2", Service: "api"},
		{TraceID: "t1", SpanID: "s3", Service: "db"},
	})
	result := s.GetSpansByTrace("t1")
	if len(result) != 2 {
		t.Errorf("expected 2 spans for t1, got %d", len(result))
	}
}

func TestStorage_GetRecentSpans(t *testing.T) {
	s := NewStorage(100)
	for i := 0; i < 10; i++ {
		s.StoreSpans([]*Span{{
			TraceID: fmt.Sprintf("t%d", i),
			SpanID:  fmt.Sprintf("s%d", i),
			Service: "svc",
		}})
	}
	recent := s.GetRecentSpans(5)
	if len(recent) != 5 {
		t.Errorf("expected 5 recent spans, got %d", len(recent))
	}
}

func TestStorage_GetRecentSpansLessThanLimit(t *testing.T) {
	s := NewStorage(100)
	s.StoreSpans([]*Span{{TraceID: "t1", SpanID: "s1", Service: "svc"}})
	recent := s.GetRecentSpans(10)
	if len(recent) != 1 {
		t.Errorf("expected 1 span, got %d", len(recent))
	}
}

func TestStorage_MaxSizeEviction(t *testing.T) {
	s := NewStorage(5)
	for i := 0; i < 10; i++ {
		s.StoreSpans([]*Span{{
			TraceID: fmt.Sprintf("t%d", i),
			SpanID:  fmt.Sprintf("s%d", i),
			Service: "svc",
		}})
	}
	if len(s.spans) > 5 {
		t.Errorf("expected max 5 spans, got %d", len(s.spans))
	}
}

func TestStorage_StoreAndRetrieveMetrics(t *testing.T) {
	s := NewStorage(100)
	m := &Metric{Name: "http_requests_total", Value: 42, Service: "api", Timestamp: time.Now().UnixMilli()}
	s.StoreMetric(m)
	result := s.GetMetricsByName("http_requests_total", 10)
	if len(result) != 1 {
		t.Errorf("expected 1 metric, got %d", len(result))
	}
	if result[0].Value != 42 {
		t.Errorf("expected value 42, got %f", result[0].Value)
	}
}

func TestStorage_GetMetricsByNameFilters(t *testing.T) {
	s := NewStorage(100)
	s.StoreMetric(&Metric{Name: "latency", Value: 10, Service: "api"})
	s.StoreMetric(&Metric{Name: "errors", Value: 1, Service: "api"})
	s.StoreMetric(&Metric{Name: "latency", Value: 20, Service: "api"})
	result := s.GetMetricsByName("latency", 10)
	if len(result) != 2 {
		t.Errorf("expected 2 latency metrics, got %d", len(result))
	}
}

func TestStorage_StoreAndRetrieveLogs(t *testing.T) {
	s := NewStorage(100)
	l := &LogEntry{TraceID: "t1", Service: "api", Level: "error", Message: "db timeout"}
	s.StoreLog(l)
	result := s.GetLogsByTrace("t1")
	if len(result) != 1 {
		t.Errorf("expected 1 log, got %d", len(result))
	}
	if result[0].Message != "db timeout" {
		t.Errorf("unexpected message: %s", result[0].Message)
	}
}

func TestStorage_GetLogsFiltersNonmatching(t *testing.T) {
	s := NewStorage(100)
	s.StoreLog(&LogEntry{TraceID: "t1", Service: "api", Level: "error", Message: "err1"})
	s.StoreLog(&LogEntry{TraceID: "t2", Service: "api", Level: "info", Message: "ok"})
	result := s.GetLogsByTrace("t1")
	if len(result) != 1 {
		t.Errorf("expected 1 log for t1, got %d", len(result))
	}
}

func TestStorage_Stats(t *testing.T) {
	s := NewStorage(100)
	s.StoreSpans([]*Span{{TraceID: "t1", SpanID: "s1", Service: "svc"}})
	s.StoreMetric(&Metric{Name: "m", Value: 1, Service: "svc"})
	s.StoreLog(&LogEntry{Service: "svc", Level: "info", Message: "ok"})
	stats := s.Stats()
	if stats["spans"] != 1 {
		t.Errorf("expected 1 span")
	}
	if stats["metrics"] != 1 {
		t.Errorf("expected 1 metric")
	}
	if stats["logs"] != 1 {
		t.Errorf("expected 1 log")
	}
}

func TestDeploymentTracker_Record(t *testing.T) {
	dt := NewDeploymentTracker()
	dt.Record(&Deployment{Service: "api", Version: "v1.2.0", CommitSHA: "abc123"})
	recent := dt.GetRecent(10)
	if len(recent) != 1 {
		t.Errorf("expected 1 deployment, got %d", len(recent))
	}
	if recent[0].Version != "v1.2.0" {
		t.Errorf("expected v1.2.0, got %s", recent[0].Version)
	}
}

func TestDeploymentTracker_GetRecentLimit(t *testing.T) {
	dt := NewDeploymentTracker()
	for i := 0; i < 10; i++ {
		dt.Record(&Deployment{
			Service:   "api",
			Version:   fmt.Sprintf("v1.%d.0", i),
			CommitSHA: newID(),
		})
	}
	recent := dt.GetRecent(3)
	if len(recent) != 3 {
		t.Errorf("expected 3, got %d", len(recent))
	}
}

func TestDeploymentTracker_EmptyGetRecent(t *testing.T) {
	dt := NewDeploymentTracker()
	recent := dt.GetRecent(5)
	if len(recent) != 0 {
		t.Errorf("expected 0, got %d", len(recent))
	}
}

func TestNewID_Unique(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := newID()
		if ids[id] {
			t.Errorf("duplicate ID: %s", id)
		}
		ids[id] = true
	}
}

func TestNewID_Length(t *testing.T) {
	id := newID()
	if len(id) != 16 {
		t.Errorf("expected 16 char ID, got %d", len(id))
	}
}
// always keep error
// always keep slow
// always keep 5xx
// zero rate drops
// full rate keeps
// add and flush
// flush nonexistent
// sampler stats
// error beats zero rate
// store spans
// store filters
// recent spans
// recent less than limit
// max eviction
// metrics store
// metrics filter
// logs by trace
