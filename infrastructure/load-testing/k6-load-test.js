import http from "k6/http";
import { check, sleep } from "k6";
import { Rate, Trend } from "k6/metrics";

const errorRate = new Rate("errors");
const collectorLatency = new Trend("collector_latency");

export const options = {
  stages: [
    { duration: "1m", target: 100 },
    { duration: "3m", target: 100 },
    { duration: "1m", target: 0 },
  ],
  thresholds: {
    http_req_duration: ["p(99)<500"],
    errors: ["rate<0.01"],
  },
};

const BASE = "http://localhost:4318";

export default function () {
  const traces = [{
    trace_id: `${Math.random().toString(16).substr(2, 16)}`,
    span_id: `${Math.random().toString(16).substr(2, 8)}`,
    service: "load-test",
    operation: "test-operation",
    start_time_ms: Date.now(),
    duration_ms: Math.random() * 200,
    status_code: 200,
    error: Math.random() < 0.01,
  }];

  const res = http.post(`${BASE}/v1/traces`,
    JSON.stringify(traces),
    { headers: { "Content-Type": "application/json" } }
  );

  collectorLatency.add(res.timings.duration);
  errorRate.add(res.status !== 202);
  check(res, { "status is 202": (r) => r.status === 202 });
  sleep(0.1);
}
// thresholds
// ramp up
// trace gen
// error inject
// custom metrics
// sustained
// spike
// think time
// summary
// batch test
