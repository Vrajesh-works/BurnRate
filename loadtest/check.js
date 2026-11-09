// k6 load test for POST /v1/check.
//
// Ramps arrival rate high enough to push pod CPU over the HPA target so the
// Deployment scales out. Many tenants and keys keep the decision mix realistic.
//
//   k6 run -e BASE_URL=http://localhost:8080 loadtest/check.js
//   scripts/load.sh   (runs it inside the kind cluster and records HPA scaling)
import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const PEAK_RPS = parseInt(__ENV.PEAK_RPS || '1500', 10);
const HOLD = __ENV.HOLD || '2m';

export const options = {
  discardResponseBodies: false,
  scenarios: {
    checks: {
      executor: 'ramping-arrival-rate',
      startRate: 50,
      timeUnit: '1s',
      preAllocatedVUs: 100,
      maxVUs: 400,
      stages: [
        { target: PEAK_RPS, duration: '30s' },
        { target: PEAK_RPS, duration: HOLD },
        { target: 0, duration: '15s' },
      ],
    },
  },
  thresholds: {
    // Same targets as the SLOs: errors under 0.1%, p99 under 100 ms.
    http_req_failed: ['rate<0.001'],
    'http_req_duration{name:check}': ['p(99)<100'],
    checks: ['rate>0.999'],
  },
};

const params = { headers: { 'Content-Type': 'application/json' }, tags: { name: 'check' } };

export default function () {
  const tenant = `merchant-${Math.floor(Math.random() * 50)}`;
  const key = `card-auth-${Math.floor(Math.random() * 200)}`;
  const res = http.post(`${BASE_URL}/v1/check`, JSON.stringify({ tenant, key, cost: 1 }), params);
  check(res, {
    'status is 200': (r) => r.status === 200,
    'has decision': (r) => r.body && r.body.indexOf('"allowed"') !== -1,
  });
}
