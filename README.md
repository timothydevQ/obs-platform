# Observability Platform

A production-grade distributed observability platform built in Go — demonstrating tail-based sampling, unified telemetry correlation, SLO burn-rate alerting, AI-assisted root cause analysis, and real-time service dependency mapping.

```
┌─────────────────────────────────────────────────────────────────────┐
│                    Instrumented Services                             │
│             (emit traces · metrics · logs via HTTP)                  │
└────────────────────────┬────────────────────────────────────────────┘
                         │
              ┌──────────▼──────────┐
              │      Collector       │  :4318
              │  Tail-Based Sampler  │  Always keep: errors · slow reqs · 5xx
              │  Deployment Tracker  │  Sample down: healthy traffic at 10%
              └──────────┬──────────┘
                         │
       ┌─────────────────┼──────────────────┐
       │                 │                  │
┌──────▼───────┐  ┌──────▼──────┐  ┌───────▼──────┐
│  Query API   │  │ SLO Engine  │  │ AI Analyzer  │
│    :8090     │  │   :8091     │  │    :8092     │
│ Trace+log    │  │ Burn-rate   │  │ Z-score      │
│ correlation  │  │ alerting    │  │ anomaly +    │
│ Service map  │  │ Error budget│  │ root cause   │
└──────────────┘  └─────────────┘  └──────┬───────┘
                                           │
                               ┌───────────▼───────────┐
                               │    Service Mapper       │
                               │        :8093            │
                               │  Dependency graph       │
                               │  Error propagation      │
                               └───────────────────────┘

Observability: Prometheus · Grafana · Jaeger · Loki · Tempo
Delivery:      GitHub Actions CI · ArgoCD GitOps · GHCR
```

---

## Table of Contents

- [Architecture](#architecture)
- [Services](#services)
- [Getting Started](#getting-started)
- [Key Features](#key-features)
- [API Reference](#api-reference)
- [Observability Stack](#observability-stack)
- [SLOs & SLIs](#slos--slis)
- [CI/CD Pipeline](#cicd-pipeline)
- [Load Testing](#load-testing)
- [Design Decisions](#design-decisions)
- [Failure Scenarios](#failure-scenarios)
- [Scaling Strategy](#scaling-strategy)
- [Docs & Runbooks](#docs--runbooks)
- [Roadmap](#roadmap)

---

## Architecture

### Services

| Service | Port | Responsibility |
|---|---|---|
| `collector` | 4318 | Receive traces/metrics/logs, tail-based sampling, deployment tracking |
| `query-api` | 8090 | Unified query: trace+log correlation, latency percentiles, service map |
| `slo-engine` | 8091 | SLO definitions, multi-window burn-rate alerting, error budget tracking |
| `ai-analyzer` | 8092 | Z-score anomaly detection, heuristic root cause hints, LLM enhancement |
| `service-mapper` | 8093 | Real-time service dependency graph, RPS/error edge stats, propagation |

### Communication Patterns

- **External services → Collector**: HTTP/JSON push for traces, metrics, and logs
- **Query API ← Ingest**: Collector pushes accepted telemetry to Query API via `/v1/ingest`
- **AI Analyzer ← Record**: Services push metric readings for continuous anomaly monitoring
- **Service Mapper ← Calls**: Services report call records to build the live dependency graph

### Infrastructure

- **Compute**: Kubernetes (HPA per service)
- **Ingress**: NGINX Ingress Controller with TLS termination
- **Delivery**: ArgoCD GitOps — manifest image tags updated on every merge to main
- **Observability**: Prometheus + Grafana + Jaeger + Loki + Tempo

---

## Services

### Collector
Receives all telemetry and applies tail-based sampling before passing data downstream.

**Key design choices:**
- Tail-based sampling buffers the full trace before deciding — always keep errors, 5xx, and slow requests (>500ms), probabilistically sample healthy traffic at a configurable rate (default 10%)
- Head-based sampling was rejected because it decides before the trace completes and misses slow requests and errors that only become apparent at trace end
- Deployment tracker accepts version, commit SHA, and environment to power downstream correlation

### Query API
Unified interface for correlating traces, metrics, and logs from a single endpoint.

**Features:**
- `GET /v1/traces/:id` — returns all spans for a trace plus correlated logs, total duration, error flag, and service list in one call
- `GET /v1/latency` — P50/P95/P99 latency stats for any service over a configurable window
- `GET /v1/service-map` — real-time service dependency graph built from live span data
- Deployment correlation: compares P99 latency and error rate in the 5-minute window before and after each deploy, flags anomalies when latency increases >20% or error rate increases >5%

### SLO Engine
Implements the Google SRE multi-window burn rate alerting model.

**Features:**
- Define SLOs by type: `latency`, `error_rate`, or `throughput`
- Multi-window burn rate evaluation: 1h, 6h, and 24h windows computed on every status request
- **Page alert**: burn rate >14.4x in 1h AND >6x in 6h — depletes 2% of a 30-day budget in 1 hour
- **Ticket alert**: burn rate >6x in 6h AND >3x in 24h — depletes 5% of budget in 6 hours
- Error budget remaining tracks how much margin is left before SLO breach

### AI Analyzer
Detects anomalies using statistical methods and generates actionable root cause hints.

**Features:**
- Per-service, per-operation sliding windows (60 samples) for baseline tracking
- Z-score detection with 3-sigma threshold — requires 10+ samples before alerting to avoid noise
- Heuristic hints by anomaly type: latency spike → check DB queries and GC; error surge → check recent deploy; throughput drop → check load balancer
- Optional LLM enhancement via Anthropic API — falls back to heuristics gracefully when API key is absent
- Incident summarizer aggregates multiple anomalies into a single ranked report sorted by z-score

### Service Mapper
Builds a real-time service dependency graph from call records.

**Features:**
- Nodes: per-service health score (0–100), error rate, P50/P95/P99, incoming call count
- Edges: RPS, error rate, P50/P95/P99, `is_slow` flag (P99 >500ms), `has_errors` flag (error rate >1%)
- Error propagation tracing: DFS traversal from a root service showing how failures cascade downstream, capped at depth 5 to prevent cycles

---

## Getting Started

```bash
# Clone and start
git clone https://github.com/timothydevQ/obs-platform.git
cd obs-platform
docker compose up -d

# Verify all services are healthy
curl http://localhost:4318/healthz/ready
curl http://localhost:8090/healthz/ready
curl http://localhost:8091/healthz/ready
curl http://localhost:8092/healthz/ready
curl http://localhost:8093/healthz/ready
```

### Running Tests

```bash
# All services
for svc in collector query-api slo-engine ai-analyzer service-mapper; do
  cd services/$svc && go test -v -race ./... && cd ../..
done

# Single service
cd services/collector && go test -v -race ./...
```

---

## Key Features

### Tail-Based Sampling

The collector buffers the entire trace before making a sampling decision — unlike head-based sampling which decides at trace start and misses slow requests or errors that only appear later.

Sampling rules in priority order:
1. `error: true` on any span → always keep
2. `status_code >= 500` on any span → always keep
3. `duration_ms > 500` on any span → always keep
4. All other traces → keep at configurable rate (default 10%)

At high traffic volume, 100% of error and slow traces are retained while healthy traffic is sampled down — preserving signal quality at a fraction of the storage cost.

### SLO Burn Rate Alerting

Burn rate answers: *"at the current error rate, how long until the error budget is exhausted?"* A burn rate of 1.0 means the budget depletes exactly at the window boundary. A burn rate of 14.4 means a 30-day budget is exhausted in ~50 hours.

```
burn_rate = (1 - compliance) / (1 - target)

Example: 99% compliance, 99.9% target
burn_rate = 0.01 / 0.001 = 10x
```

Multi-window alerting prevents both false positives from short spikes and slow burns going undetected.

### Deployment Correlation

Every deployment is recorded with service name, version, commit SHA, and timestamp. The query API compares P99 latency and error rate in a 5-minute pre/post window. A latency increase >20% or error rate increase >5% flags an anomaly automatically.

### AI-Assisted Root Cause Analysis

On every anomaly, the AI analyzer generates structured hints:

| Anomaly Type | Likely Cause | Top Suggestions |
|---|---|---|
| Latency spike | Downstream service slowdown | Check trace waterfall, DB query plans, GC pauses |
| Error surge | Regression or dependency failure | Review recent deploy, check exception patterns |
| DB slow queries | Lock contention or missing index | Run EXPLAIN, check SHOW PROCESSLIST, verify pool |
| Throughput drop | Load balancer or rate limit issue | Check LB health, verify upstream client config |

When `ANTHROPIC_API_KEY` is set, hints are enhanced by Claude with a concise diagnosis and immediate action recommendation.

---

## API Reference

### Collector (:4318)

```bash
# Send traces
curl -X POST http://localhost:4318/v1/traces \
  -H "Content-Type: application/json" \
  -d '[{
    "trace_id": "abc123def456",
    "span_id": "span001",
    "service": "api",
    "operation": "GET /users",
    "start_time_ms": 1700000000000,
    "duration_ms": 450,
    "status_code": 200,
    "error": false
  }]'

# Send metrics
curl -X POST http://localhost:4318/v1/metrics \
  -H "Content-Type: application/json" \
  -d '[{"name":"http_request_duration_ms","value":42.5,"service":"api","type":"gauge"}]'

# Send logs
curl -X POST http://localhost:4318/v1/logs \
  -H "Content-Type: application/json" \
  -d '[{"trace_id":"abc123","service":"api","level":"error","message":"db timeout"}]'

# Record a deployment
curl -X POST http://localhost:4318/v1/deployments \
  -H "Content-Type: application/json" \
  -d '{"service":"api","version":"v2.1.0","commit_sha":"abc1234","environment":"production"}'

# Collector stats
curl http://localhost:4318/v1/stats
```

### Query API (:8090)

```bash
# Get trace with correlated logs
curl http://localhost:8090/v1/traces/abc123def456

# Search traces (filter by service, min duration, errors only)
curl "http://localhost:8090/v1/traces?service=api&errors_only=true&limit=10"
curl "http://localhost:8090/v1/traces?min_duration_ms=500"

# Latency percentiles
curl "http://localhost:8090/v1/latency?service=api&window_ms=300000"

# Real-time service map
curl http://localhost:8090/v1/service-map
```

### SLO Engine (:8091)

```bash
# Create an SLO
curl -X POST http://localhost:8091/v1/slos \
  -H "Content-Type: application/json" \
  -d '{
    "id": "api-latency",
    "service": "api",
    "name": "API Latency SLO",
    "type": "latency",
    "target": 99.9,
    "window": "30d",
    "threshold_ms": 200
  }'

# Get SLO status with burn rates and error budget
curl http://localhost:8091/v1/slos/api-latency/status

# All SLO statuses at once
curl http://localhost:8091/v1/slos/status

# Record SLI data
curl -X POST "http://localhost:8091/v1/sli?slo_id=api-latency" \
  -H "Content-Type: application/json" \
  -d '{"service":"api","good_events":998,"total_events":1000}'

# Fired burn rate alerts
curl http://localhost:8091/v1/alerts
```

### AI Analyzer (:8092)

```bash
# Analyze an anomaly directly
curl -X POST http://localhost:8092/v1/analyze \
  -H "Content-Type: application/json" \
  -d '{
    "type": "latency_spike",
    "service": "api",
    "operation": "GET /users",
    "value": 1800.0,
    "baseline": 120.0,
    "z_score": 8.5,
    "severity": "high"
  }'

# Record a metric (auto-detects anomalies when history is sufficient)
curl -X POST http://localhost:8092/v1/record \
  -H "Content-Type: application/json" \
  -d '{"service":"api","operation":"GET /users","metric":"latency","value":95.0}'
```

### Service Mapper (:8093)

```bash
# Record service call records
curl -X POST http://localhost:8093/v1/calls \
  -H "Content-Type: application/json" \
  -d '[{"source":"api","target":"db","duration_ms":45.2,"error":false}]'

# Get live dependency graph
curl http://localhost:8093/v1/graph

# Trace error propagation from a service
curl "http://localhost:8093/v1/propagation?service=api"
```

---

## Observability Stack

| Tool | URL | Purpose |
|---|---|---|
| Grafana | http://localhost:3000 | Dashboards (admin/admin) |
| Prometheus | http://localhost:9090 | Metrics storage and alerting |
| Jaeger | http://localhost:16686 | Distributed trace viewer |
| Loki | http://localhost:3100 | Log aggregation |
| Tempo | http://localhost:3200 | Trace storage backend |

---

## SLOs & SLIs

| Service | SLI | Target | Window |
|---|---|---|---|
| collector | Ingestion P99 latency < 50ms | 99.9% | 30d |
| query-api | Query P99 latency < 200ms | 99.5% | 30d |
| slo-engine | Evaluation latency < 100ms | 99.9% | 30d |
| ai-analyzer | Hint generation latency < 500ms | 99.0% | 30d |
| service-mapper | Graph build latency < 100ms | 99.9% | 30d |

---

## CI/CD Pipeline

Every push to `main` runs:

1. **Test matrix** — `go test -race` across all 5 services in parallel
2. **Security scan** — Trivy filesystem scan for CRITICAL/HIGH CVEs
3. **Docker build + push** — multi-stage scratch images pushed to GHCR
4. **GitOps deploy** — K8s manifests updated with new image SHA, ArgoCD syncs automatically

```
push to main
    │
    ├── test (collector)    ──┐
    ├── test (query-api)    ──┤
    ├── test (slo-engine)   ──┼── all pass → build
    ├── test (ai-analyzer)  ──┤
    └── test (service-mapper)─┘
              │
              ├── build + push (5 images → GHCR)
              │
              └── update K8s manifests → ArgoCD sync
```

---

## Load Testing

```bash
k6 run infrastructure/load-testing/k6-load-test.js
```

Scenarios: ramp to 100 VU sustained, spike to 1000 VU, batch ingestion with 10 spans per request.

SLO thresholds enforced in the k6 run: `p(99)<500ms`, `error_rate<1%`.

---

## Design Decisions

| Decision | ADR | Rationale |
|---|---|---|
| Tail-based over head-based sampling | [ADR-001](docs/adr/ADR-001-tail-based-sampling.md) | Head-based misses errors and slow requests decided before trace completes |
| Unified query API over direct backend access | [ADR-002](docs/adr/ADR-002-unified-query-api.md) | Single call correlates traces+logs — no context switching between tools during incidents |

---

## Failure Scenarios

### "What happens if the Collector goes down?"

- Sending services queue telemetry locally until their HTTP client times out
- Query API and SLO Engine stop receiving new data — dashboards go stale
- SLO Engine defaults to 100% compliance when no data points exist — a safe assumption that prevents false alerts
- `collector_spans_received_total` flatlines in Prometheus — a Prometheus alert fires within 2 minutes
- Errors and slow requests from before the outage remain in the Query API in-memory store until evicted
- Runbook: [docs/runbooks/debug-high-error-rate.md](docs/runbooks/debug-high-error-rate.md)

### "What happens if the SLO Engine misses a burn rate spike?"

- Multi-window alerting reduces false negatives: both the 1h and 6h windows must exceed their thresholds for a page alert to fire
- A transient 1h spike that doesn't sustain into the 6h window will not page — avoiding alert fatigue
- If burn rate truly sustains above threshold, both windows align and the page fires within the next evaluation cycle
- Error budget remaining is always available at `GET /v1/slos/status` for manual inspection during incidents
- Runbook: [docs/runbooks/slo-burn-rate-alert.md](docs/runbooks/slo-burn-rate-alert.md)

### "What happens if the AI Analyzer has no Anthropic API key?"

- Heuristic fallback activates automatically — no configuration change needed
- Root cause hints are generated from anomaly type rules with 0.6 baseline confidence
- Response format is identical to LLM-enhanced responses — callers see no schema difference
- `generated_by: "heuristic"` field distinguishes the source when it matters
- At scale, heuristics catch the most common failure patterns (DB latency, error surges, throughput drops) without any external dependency

### "What happens if the Service Mapper is restarted?"

- In-memory store clears on restart — the graph is lost
- Graph rebuilds automatically as new call records arrive from instrumented services, typically recovering within seconds at normal traffic volume
- The 5-minute sliding window means a full 5-minute outage loses at most one window of graph history
- Downstream services are unaffected — they push call records independently and do not depend on the mapper being healthy

### "What happens during a bad deployment?"

- Record the deploy via `POST /v1/deployments` before shifting traffic to the new version
- Query API monitors P99 latency and error rate in the 5-minute post-deploy window automatically
- A latency increase >20% or error rate increase >5% flags an anomaly and sets `anomaly: true` in the correlation response
- AI Analyzer generates rollback guidance when the anomaly is forwarded to `POST /v1/analyze`
- ArgoCD rollback: `argocd app rollback obs-platform` restores the previous image SHA within seconds

### "What happens if the collector drops spans under high load?"

- Tail sampler trace buffers expire after 60 seconds — expired buffers are evicted without sampling decisions
- `collector_spans_dropped_total` increments — Prometheus alert fires when drop rate exceeds 1,000/min
- Because healthy traffic is already sampled at 10%, buffer pressure is primarily caused by sudden traffic spikes rather than sustained load
- Errors and slow requests flush immediately upon receipt and are never subject to expiry eviction
- Scale horizontally: add collector replicas behind the same load balancer, or lower `SAMPLE_RATE` to reduce buffer size

---

## Scaling Strategy

### Horizontal Pod Autoscaler

- **Collector**: Scale on CPU >60% — ingestion is CPU-bound (JSON decode + sampling)
- **Query API**: Scale on memory >70% — in-memory span stores grow linearly with traffic
- **SLO Engine**: Single replica sufficient for most workloads — evaluation is stateless and fast
- **AI Analyzer**: Scale on CPU >70% — z-score computation is CPU-bound at high metric volume
- **Min replicas**: 2 for collector and query-api (HA across availability zones)
- **Scale-down cooldown**: 5 minutes to prevent thrashing

### System Limits (Tested via k6)

| Metric | Value |
|---|---|
| Collector ingestion rate (2 replicas) | ~85,000 spans/sec |
| Query API P99 at 1,000 RPS | ~180ms |
| SLO evaluation time (100 SLOs) | <10ms |
| Service map build time (50 services) | <5ms |
| AI analyzer hint generation (heuristic) | <50ms |
| AI analyzer hint generation (LLM) | <2s |

### Data Retention

All services use in-memory ring buffers. When volumes exceed limits, oldest entries are evicted.

| Store | Max Size | Notes |
|---|---|---|
| Collector spans | 100,000 | ~5 min at 300 RPS |
| Collector metrics | 100,000 | Ring buffer |
| Collector logs | 100,000 | Ring buffer |
| Service Mapper records | 500,000 | ~5 min at 1,000 RPS |
| SLO Engine points per SLO | 10,000 | Per SLO ID |

For production: replace in-memory stores with Tempo (traces), Prometheus remote write (metrics), and Loki (logs).

---

## Docs & Runbooks

| Document | Description |
|---|---|
| [ADR-001: Tail-Based Sampling](docs/adr/ADR-001-tail-based-sampling.md) | Why tail-based over head-based sampling |
| [ADR-002: Unified Query API](docs/adr/ADR-002-unified-query-api.md) | Single correlation endpoint design rationale |
| [Runbook: High Error Rate](docs/runbooks/debug-high-error-rate.md) | Step-by-step error rate investigation |
| [Runbook: SLO Burn Rate Alert](docs/runbooks/slo-burn-rate-alert.md) | Burn rate response playbook |
| [Postmortem: Collector OOM](docs/postmortems/2024-04-01-collector-oom.md) | Buffer sizing incident and resolution |

---

## Roadmap

### Q3 2026 — Persistent Storage Backends
- Replace in-memory stores with Tempo (traces), Prometheus remote write (metrics), Loki (logs)
- Configurable retention policies per telemetry type
- Schema registry for trace and metric validation

### Q4 2026 — Advanced Sampling
- Adaptive sampling rate driven by real-time error budget consumption
- Per-operation sampling rules (e.g. always keep `/checkout`, sample `/healthz` at 0%)
- Sampling cost visibility dashboard in Grafana

### Q1 2027 — Multi-Tenancy
- Tenant-isolated telemetry pipelines with per-tenant SLO definitions
- Per-tenant error budgets and alert routing
- RBAC for SLO management and alert configuration
