# Runbook: High Error Rate Alert

## Severity Assessment
- P0 (>10% error rate): Page on-call immediately
- P1 (1-10% error rate): Respond within 15 minutes
- P2 (<1% error rate): Investigate within 1 hour

## Immediate Steps

### 1. Check service map
curl http://slo-engine:8091/v1/slos/status

### 2. Find error traces
curl "http://query-api:8090/v1/traces?errors_only=true&limit=10"

### 3. Get root cause hints
curl -X POST http://ai-analyzer:8092/v1/analyze \
  -d '{"type":"error_surge","service":"<service>","value":0.1,"baseline":0.01}'

### 4. Check recent deployments
curl http://collector:4318/v1/deployments

## Escalation
If error rate exceeds 5% for more than 5 minutes and root cause is unknown, escalate to service owner.
<!-- assess -->
<!-- steps -->
<!-- escalation -->
