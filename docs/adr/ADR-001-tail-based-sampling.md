# ADR-001: Tail-Based Sampling Over Head-Based

## Status
Accepted

## Context
We need to decide how to sample telemetry data at scale without losing critical signals.

## Decision
Implement tail-based sampling in the collector service.

## Rationale
- Head-based sampling makes decisions before trace completion — misses slow requests and errors
- Tail-based sampling buffers the full trace, then decides: always keep errors, slow requests, and rare operations
- Cost-aware: drops healthy high-volume traffic, preserving signal quality over quantity

## Consequences
- Memory overhead for trace buffering (60s window)
- Requires trace-complete detection heuristic
- 100% capture of errors and slow requests regardless of volume
<!-- context -->
