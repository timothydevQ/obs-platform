# Postmortem: Collector OOM — April 1, 2024

## Summary
The collector service ran out of memory and restarted 3 times in 2 hours, causing a 12-minute gap in trace data.

## Timeline
- 14:00 UTC: Trace volume spiked 8x due to load test
- 14:23 UTC: Collector memory at 480MB (limit: 512MB)
- 14:31 UTC: First OOM kill — container restarted
- 14:45 UTC: Second OOM kill during recovery
- 14:52 UTC: Third OOM kill — PagerDuty alert fired
- 15:04 UTC: Memory limit increased to 2GB, stable

## Root Cause
Tail sampling buffer retained traces for 60 seconds. During traffic spike, 8x volume filled the buffer before expiry goroutine could flush. No memory ceiling on buffer size.

## Action Items
- Add max buffer size limit to TailSampler (done)
- Add memory pressure metric to collector (done)
- Add HPA based on memory utilization (in progress)
- Load test tail sampler under 10x normal volume (scheduled)
<!-- timeline -->
<!-- root cause -->
<!-- actions -->
<!-- prevention -->
