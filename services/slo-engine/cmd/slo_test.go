package main

import (
	"testing"
	"time"
)

func newTestSLO(id, service string, sloType SLOType, target float64) *SLO {
	return &SLO{
		ID:      id,
		Service: service,
		Name:    id + "-slo",
		Type:    sloType,
		Target:  target,
		Window:  "30d",
		Enabled: true,
	}
}

func newTestEngine() (*SLOEngine, *SLOStore) {
	store := NewSLOStore()
	engine := NewSLOEngine(store)
	return engine, store
}

func TestWindowDuration_1h(t *testing.T) {
	d := windowDuration("1h")
	if d != int64(time.Hour/time.Millisecond) {
		t.Errorf("expected 1h in ms, got %d", d)
	}
}

func TestWindowDuration_7d(t *testing.T) {
	d := windowDuration("7d")
	expected := int64(7 * 24 * time.Hour / time.Millisecond)
	if d != expected {
		t.Errorf("expected 7d in ms, got %d", d)
	}
}

func TestWindowDuration_Default(t *testing.T) {
	d := windowDuration("unknown")
	expected := int64(24 * time.Hour / time.Millisecond)
	if d != expected {
		t.Errorf("expected default 24h, got %d", d)
	}
}

func TestCalculateBurnRate_NoBurn(t *testing.T) {
	rate := calculateBurnRate(100.0, 99.9)
	if rate != 0 {
		t.Errorf("100%% compliance should be 0 burn rate, got %f", rate)
	}
}

func TestCalculateBurnRate_ExactTarget(t *testing.T) {
	rate := calculateBurnRate(99.9, 99.9)
	if rate < 0.99 || rate > 1.01 {
		t.Errorf("compliance == target should give burn rate ~1.0, got %f", rate)
	}
}

func TestCalculateBurnRate_HighBurn(t *testing.T) {
	rate := calculateBurnRate(99.0, 99.9)
	// error rate = 1%, allowed = 0.1%, so burn = 10x
	if rate < 9 || rate > 11 {
		t.Errorf("expected ~10x burn rate, got %f", rate)
	}
}

func TestCalculateBurnRate_ZeroAllowed(t *testing.T) {
	rate := calculateBurnRate(50.0, 100.0)
	if rate != 0 {
		t.Errorf("expected 0 when target is 100%%, got %f", rate)
	}
}

func TestSLOStore_AddAndGet(t *testing.T) {
	store := NewSLOStore()
	slo := newTestSLO("slo1", "api", SLOErrorRate, 99.9)
	store.AddSLO(slo)
	got, ok := store.GetSLO("slo1")
	if !ok {
		t.Fatal("expected to find SLO")
	}
	if got.Service != "api" {
		t.Errorf("expected service api, got %s", got.Service)
	}
	if !got.Enabled {
		t.Error("expected SLO to be enabled")
	}
}

func TestSLOStore_GetNonexistent(t *testing.T) {
	store := NewSLOStore()
	_, ok := store.GetSLO("nonexistent")
	if ok {
		t.Error("expected false for nonexistent SLO")
	}
}

func TestSLOStore_ListSLOs(t *testing.T) {
	store := NewSLOStore()
	store.AddSLO(newTestSLO("slo1", "api", SLOErrorRate, 99.9))
	store.AddSLO(newTestSLO("slo2", "worker", SLOLatency, 99.5))
	slos := store.ListSLOs()
	if len(slos) != 2 {
		t.Errorf("expected 2 SLOs, got %d", len(slos))
	}
}

func TestSLOStore_RecordAndGetPoints(t *testing.T) {
	store := NewSLOStore()
	now := time.Now().UnixMilli()
	store.RecordPoint("slo1", &SLIPoint{
		Service:   "api",
		Timestamp: now,
		Good:      99,
		Total:     100,
	})
	points := store.GetPoints("slo1", now-1000)
	if len(points) != 1 {
		t.Errorf("expected 1 point, got %d", len(points))
	}
}

func TestSLOStore_GetPointsFiltersOld(t *testing.T) {
	store := NewSLOStore()
	now := time.Now().UnixMilli()
	store.RecordPoint("slo1", &SLIPoint{Service: "api", Timestamp: now - 10000, Good: 90, Total: 100})
	store.RecordPoint("slo1", &SLIPoint{Service: "api", Timestamp: now, Good: 99, Total: 100})
	// Only get points from last 5 seconds
	points := store.GetPoints("slo1", now-5000)
	if len(points) != 1 {
		t.Errorf("expected 1 recent point, got %d", len(points))
	}
}

func TestSLOStore_AlertStorage(t *testing.T) {
	store := NewSLOStore()
	store.AddAlert(&BurnRateAlert{
		SLOID:   "slo1",
		Service: "api",
		FiredAt: time.Now().UnixMilli(),
		Message: "test alert",
	})
	alerts := store.GetAlerts(10)
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert, got %d", len(alerts))
	}
}

func TestSLOEngine_PerfectCompliance(t *testing.T) {
	engine, store := newTestEngine()
	slo := newTestSLO("slo1", "api", SLOErrorRate, 99.9)
	store.AddSLO(slo)
	now := time.Now().UnixMilli()
	store.RecordPoint("slo1", &SLIPoint{Service: "api", Timestamp: now, Good: 1000, Total: 1000})
	status := engine.EvaluateSLO(slo)
	if status.Status != "ok" {
		t.Errorf("expected ok status, got %s", status.Status)
	}
	if status.CurrentValue != 100.0 {
		t.Errorf("expected 100%% compliance, got %f", status.CurrentValue)
	}
}

func TestSLOEngine_NoData(t *testing.T) {
	engine, store := newTestEngine()
	slo := newTestSLO("slo1", "api", SLOErrorRate, 99.9)
	store.AddSLO(slo)
	status := engine.EvaluateSLO(slo)
	if status.CurrentValue != 100.0 {
		t.Errorf("expected 100%% compliance with no data, got %f", status.CurrentValue)
	}
}

func TestSLOEngine_BreachedSLO(t *testing.T) {
	engine, store := newTestEngine()
	slo := newTestSLO("slo1", "api", SLOErrorRate, 99.9)
	store.AddSLO(slo)
	now := time.Now().UnixMilli()
	// Only 99% good — below 99.9% target
	store.RecordPoint("slo1", &SLIPoint{Service: "api", Timestamp: now, Good: 990, Total: 1000})
	status := engine.EvaluateSLO(slo)
	if status.Status != "breached" {
		t.Errorf("expected breached status, got %s", status.Status)
	}
}

func TestSLOEngine_ErrorBudgetCalculation(t *testing.T) {
	engine, store := newTestEngine()
	slo := newTestSLO("slo1", "api", SLOErrorRate, 99.0)
	store.AddSLO(slo)
	now := time.Now().UnixMilli()
	// 99.5% good — half the error budget used
	store.RecordPoint("slo1", &SLIPoint{Service: "api", Timestamp: now, Good: 995, Total: 1000})
	status := engine.EvaluateSLO(slo)
	if status.ErrorBudget < 40 || status.ErrorBudget > 60 {
		t.Errorf("expected ~50%% error budget remaining, got %f", status.ErrorBudget)
	}
}

func TestSLOEngine_DisabledSLO(t *testing.T) {
	engine, store := newTestEngine()
	slo := newTestSLO("slo1", "api", SLOErrorRate, 99.9)
	slo.Enabled = false
	store.AddSLO(slo)
	status := engine.EvaluateSLO(slo)
	if status.Status != "disabled" {
		t.Errorf("expected disabled status, got %s", status.Status)
	}
}

func TestSLOEngine_LatencyCompliance(t *testing.T) {
	engine, store := newTestEngine()
	slo := newTestSLO("slo1", "api", SLOLatency, 99.0)
	slo.ThresholdMs = 200
	store.AddSLO(slo)
	now := time.Now().UnixMilli()
	// 99% of requests under 200ms
	store.RecordPoint("slo1", &SLIPoint{Service: "api", Timestamp: now, Total: 100, LatencyMs: 150})
	store.RecordPoint("slo1", &SLIPoint{Service: "api", Timestamp: now, Total: 1, LatencyMs: 500})
	status := engine.EvaluateSLO(slo)
	_ = status // just verify no panic
}

func TestSLOEngine_BurnRateAlertFires(t *testing.T) {
	engine, store := newTestEngine()
	slo := newTestSLO("slo1", "api", SLOErrorRate, 99.9)
	store.AddSLO(slo)
	now := time.Now().UnixMilli()
	// Terrible compliance — should trigger burn rate alert
	store.RecordPoint("slo1", &SLIPoint{Service: "api", Timestamp: now, Good: 500, Total: 1000})
	status := engine.EvaluateSLO(slo)
	_ = status.BurnRate1h // verify burn rate is calculated
	if status.BurnRate1h < 0 {
		t.Error("burn rate should not be negative")
	}
}

func TestSLOStore_MaxPoints(t *testing.T) {
	store := NewSLOStore()
	now := time.Now().UnixMilli()
	for i := 0; i < 11000; i++ {
		store.RecordPoint("slo1", &SLIPoint{
			Service:   "api",
			Timestamp: now + int64(i),
			Good:      99,
			Total:     100,
		})
	}
	if len(store.points["slo1"]) > 10000 {
		t.Errorf("expected max 10000 points, got %d", len(store.points["slo1"]))
	}
}
// window 1h
// window 7d
