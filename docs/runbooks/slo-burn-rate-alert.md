# Runbook: SLO Burn Rate Alert

## What is a burn rate alert?
A burn rate alert fires when you are consuming your error budget faster than sustainable.
A burn rate of 1.0 means you will exhaust the budget exactly at the end of the window.
A burn rate of 14.4 means you will exhaust a 30-day budget in 50 hours.

## Response by severity

### Page (burn rate > 14.4 in 1h AND > 6 in 6h)
1. Identify which SLO is firing: GET /v1/slos/status
2. Check service map for the affected service
3. Pull error traces: GET /v1/traces?service=X&errors_only=true
4. Engage AI analyzer for root cause hints
5. Consider rollback if recent deployment correlates

### Ticket (burn rate > 6 in 6h AND > 3 in 24h)
1. Create tracking ticket with SLO ID and current burn rate
2. Schedule investigation within 4 hours
3. Monitor burn rate trend every 30 minutes
<!-- what is -->
