# ADR-002: Unified Query API Over Direct Backend Access

## Status
Accepted

## Context
Engineers need to correlate traces, metrics, and logs across services during incidents.

## Decision
Build a unified query API that correlates signals from a single endpoint.

## Rationale
- Eliminates context switching between Jaeger, Grafana, and Kibana during incidents
- Enables "show logs for this trace ID" in one API call
- Powers automated root cause analysis with correlated data
- Deployment correlation answers "did this deploy cause the spike?"

## Consequences
- Additional service to maintain
- Query API becomes single point of failure for debugging workflows
- Must sync data from collector on a push model
<!-- context -->
<!-- tradeoffs -->
<!-- correlation -->
<!-- deployment -->
<!-- ai layer -->
<!-- auth -->
<!-- perf -->
