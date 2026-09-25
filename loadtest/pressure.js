// Pressure: hammer the stack with its default limits on, and check that every
// answer is a success or a deliberate refusal (429 rate limited, 503 shed),
// never a server error or a timeout.
//
//   docker compose up -d --wait
//   docker run --rm -i --network host grafana/k6:2.3.0 run - < loadtest/pressure.js
import http from "k6/http";
import { check } from "k6";
import { Counter } from "k6/metrics";

const BASE = __ENV.BASE_URL || "http://localhost:8080";
const refused = new Counter("refused");
const failures = new Counter("server_errors");

export const options = {
  scenarios: {
    logins: { executor: "constant-arrival-rate", rate: 100, timeUnit: "1s", duration: "20s", preAllocatedVUs: 50, exec: "login" },
    reads: { executor: "constant-arrival-rate", rate: 500, timeUnit: "1s", duration: "20s", preAllocatedVUs: 100, exec: "read" },
  },
  thresholds: {
    server_errors: ["count==0"],
    http_req_duration: ["p(99)<2000"],
    refused: ["count>0"], // the limits must actually engage
  },
};

function classify(r) {
  if (r.status === 429 || r.status === 503) refused.add(1);
  if (r.status >= 500 && r.status !== 503) failures.add(1);
  if (r.status === 0) failures.add(1); // timeout or connection error
  check(r, {
    "deliberate answer": (r) => [200, 201, 401, 429, 503].includes(r.status),
    "429 carries Retry-After": (r) => r.status !== 429 || r.headers["Retry-After"] !== undefined,
  });
}

// Credential stuffing from one address: the auth-route limit must engage.
export function login() {
  classify(http.post(`${BASE}/api/v1/auth/login`,
    JSON.stringify({ email: `nobody-${__ITER}@example.com`, password: "not the password at all" }),
    { headers: { "Content-Type": "application/json" }, responseCallback: http.expectedStatuses(401, 429) }));
}

// Unauthenticated flood: rejected cheaply with 401, then 429 per address.
export function read() {
  classify(http.get(`${BASE}/api/v1/sessions`, { responseCallback: http.expectedStatuses(401, 429, 503) }));
}
