# obs-platform

A production-grade distributed observability platform built in Go. Collects traces, metrics, and logs across services, correlates them in real time, powers SLO tracking with burn-rate alerting, and generates AI-assisted root cause hints during incidents.

[![CI](https://github.com/yourorg/obs-platform/actions/workflows/ci-cd.yml/badge.svg)](https://github.com/yourorg/obs-platform/actions)
[![Go](https://img.shields.io/badge/Go-1.22-blue)](https://golang.org)

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Instrumented Services                    │
│          (emit traces, metrics, logs via HTTP)               │
└────────────────────────┬────────────────────────────────────┘
                         │
              ┌──────────▼──────────┐
              │     Collector        │  Port 4318
              │  Tail-Based Sampler  │  • Always keep errors + slow requests
              │  Deployment Tracker  │  • Sample healthy traffic at 10%
              └──────────┬──────────┘
                         │
         ┌───────────────┼───────────────┐
         │               │               │
┌────────▼───────┐ ┌────▼────────┐ ┌────▼──────────┐
│   Query API    │ │ SLO Engine  │ │ AI Analyzer   │
│  Port 8090     │ │ Port 8091   │ │ Port 8092     │
│ Unified query  │ │ Burn-rate   │ │ Anomaly       │
│ Trace+log corr │ │ alerting    │ │ detection +   │
│ Service map    │ │ Error budget│ │ root cause    │
└────────────────┘ └─────────────┘ └───────────────┘
                                          │
                              ┌───────────▼───────────┐
                              │    Service Mapper       │
                              │       Port 8093         │
                              │  Real-time dependency   │
                              │  graph + propagation    │
                              └───────────────────────┘
```

## Services

| Service | Port | Responsibility |
|---|---|---|
| `collector` | 4318 | Receive traces/metrics/logs, tail-based sampling, deployment tracking |
| `query-api` | 8090 | Unified query: correlate traces+logs, latency percentiles, service map |
| `slo-engine` | 8091 | SLO definitions, burn-rate alerts (Google SRE multi-window model) |
| `ai-analyzer` | 8092 | Z-score anomaly detection, heuristic root cause hints, LLM enhancement |
| `service-mapper` | 8093 | Real-time service dependency graph, error propagation tracing |

## Key Features

### Tail-Based Sampling
The collector applies tail-based sampling — the advanced alternative to head-based sampling. It buffers the entire trace before deciding:
- **Always keep:** errors, 5xx responses, requests >500ms
- **Sample down:** healthy traffic at a configurable rate (default 10%)
- **Result:** 100% capture of interesting signals, dramatic cost reduction on healthy volume

### SLO Burn Rate Alerting (Google SRE Model)
The SLO engine implements multi-window burn rate alerting:
- **Page alert:** burn rate >14.4x in 1h AND >6x in 6h (2% budget in 1 hour)
- **Ticket alert:** burn rate >6x in 6h AND >3x in 24h (5% budget in 6 hours)

### Deployment Correlation
Track every deploy with version, commit SHA, and timestamp. The query API answers: *"Did latency spike after this deploy?"* by comparing P99 in the 5-minute window before and after each deployment.

### AI-Assisted Root Cause Analysis
The AI analyzer runs on every anomaly:
1. Z-score detection (3-sigma threshold) across per-service metric windows
2. Heuristic hints by anomaly type (latency spike → DB, errors → recent deploy)
3. Optional LLM enhancement via Anthropic API when `ANTHROPIC_API_KEY` is set

### Real-Time Service Dependency Graph
The service mapper builds a live graph from call records:
- Nodes: per-service health scores (0–100), error rates, P99 latency
- Edges: RPS, error rate, P50/P95/P99, slow/error flags
- Error propagation tracing: trace how failures cascade downstream

## Quick Start

```bash
# Start everything
docker compose up -d

# Send a trace
curl -X POST http://localhost:4318/v1/traces \
  -H "Content-Type: application/json" \
  -d '[{
    "trace_id": "abc123",
    "span_id": "span1",
    "service": "api",
    "operation": "GET /users",
    "start_time_ms": 1700000000000,
    "duration_ms": 450,
    "status_code": 200,
    "error": false
  }]'

# Query the trace
curl http://localhost:8090/v1/traces/abc123

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

# Get SLO status with burn rates
curl http://localhost:8091/v1/slos/api-latency/status

# Get real-time service map
curl http://localhost:8093/v1/graph

# Record a deployment
curl -X POST http://localhost:4318/v1/deployments \
  -H "Content-Type: application/json" \
  -d '{"service":"api","version":"v2.1.0","commit_sha":"abc1234","environment":"production"}'
```

## Observability Stack

| Tool | URL | Purpose |
|---|---|---|
| Grafana | http://localhost:3000 | Dashboards (admin/admin) |
| Prometheus | http://localhost:9090 | Metrics |
| Jaeger | http://localhost:16686 | Trace viewer |
| Loki | http://localhost:3100 | Log aggregation |
| Tempo | http://localhost:3200 | Trace storage |

## Running Tests

```bash
# All services
for svc in collector query-api slo-engine ai-analyzer service-mapper; do
  cd services/$svc && go test -v -race ./... && cd ../..
done

# Single service
cd services/collector && go test -v -race ./...
```

## Load Testing

```bash
k6 run infrastructure/load-testing/k6-load-test.js
```

## Design Decisions

| Decision | ADR |
|---|---|
| Tail-based vs head-based sampling | [ADR-001](docs/adr/ADR-001-tail-based-sampling.md) |
| Unified query API design | [ADR-002](docs/adr/ADR-002-unified-query-api.md) |

## Runbooks

- [Debug High Error Rate](docs/runbooks/debug-high-error-rate.md)
- [SLO Burn Rate Alert](docs/runbooks/slo-burn-rate-alert.md)

## SLO Definitions

| Service | SLI | Target |
|---|---|---|
| collector | Ingestion P99 < 50ms | 99.9% |
| query-api | Query P99 < 200ms | 99.5% |
| slo-engine | Evaluation latency < 100ms | 99.9% |

## Tech Stack

Go · Docker · Kubernetes · GitHub Actions · Prometheus · Grafana · Jaeger · Loki · Tempo · ArgoCD
<!-- init -->
<!-- overview -->
<!-- arch -->
<!-- services -->
<!-- quickstart -->
<!-- stack -->
<!-- slo -->
<!-- design -->
<!-- prereqs -->
<!-- tests -->
<!-- load test -->
<!-- adrs -->
<!-- badges -->
<!-- final -->
<!-- ai section -->
<!-- deployment corr -->
<!-- sampling -->
<!-- slo burns -->
<!-- service map -->
<!-- key features -->
<!-- internals -->
<!-- roadmap -->
<!-- contributing -->
<!-- license -->
<!-- docker quick -->
<!-- k8s -->
<!-- env vars -->
