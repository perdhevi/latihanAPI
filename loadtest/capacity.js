// Capacity: realistic authenticated traffic with rate limits switched off, to
// measure latency and find where the stack saturates.
//
//   RATE_LIMIT_IP=off RATE_LIMIT_USER=off RATE_LIMIT_AUTH=off docker compose up -d --wait
//   docker run --rm -i --network host grafana/k6:2.3.0 run - < loadtest/capacity.js
//
// Run it against a disposable stack: it creates accounts and data.
import http from "k6/http";
import { check } from "k6";

const BASE = __ENV.BASE_URL || "http://localhost:8080";
const USERS = Number(__ENV.USERS || 20);
const json = { "Content-Type": "application/json" };

export const options = {
  scenarios: {
    mixed: { executor: "constant-vus", vus: Number(__ENV.VUS || 50), duration: __ENV.DURATION || "30s" },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    "http_req_duration{kind:read}": ["p(95)<200"],
    "http_req_duration{kind:write}": ["p(95)<300"],
  },
};

// One account per simulated user, each with a profile and a plan.
export function setup() {
  const users = [];
  const run = Date.now();
  for (let i = 0; i < USERS; i++) {
    const reg = http.post(`${BASE}/api/v1/auth/register`,
      JSON.stringify({ email: `load-${run}-${i}@example.com`, password: "correct horse battery staple" }), { headers: json });
    const token = reg.json("access_token");
    const auth = { ...json, Authorization: `Bearer ${token}` };
    http.post(`${BASE}/api/v1/users`, JSON.stringify({ display_name: `Load ${i}` }), { headers: auth });
    const plan = http.post(`${BASE}/api/v1/plans`, JSON.stringify({
      name: "Leg day",
      exercises: [{ name: "Squat", kind: "strength", sets: [{ repetitions: 8, weight_kg: 50 }] }],
    }), { headers: auth });
    users.push({ auth, plan: plan.headers.Location });
  }
  return users;
}

export default function (users) {
  const u = users[(__VU - 1) % users.length];
  const read = { headers: u.auth, tags: { kind: "read" } };
  const write = (extra) => ({ headers: { ...u.auth, ...extra }, tags: { kind: "write" } });

  check(http.get(`${BASE}/api/v1/sessions?limit=20`, read), { "list 200": (r) => r.status === 200 });
  const plan = http.get(`${BASE}${u.plan}`, read);
  check(plan, { "plan 200": (r) => r.status === 200 });

  const key = `${__VU}-${__ITER}`;
  const session = http.post(`${BASE}/api/v1/sessions`, JSON.stringify({
    name: "Walk", performed_at: new Date().toISOString(),
    exercises: [{ name: "Walk", kind: "cardio", minutes: 20 }],
  }), write({ "Idempotency-Key": key }));
  check(session, { "session 201": (r) => r.status === 201 });

  // Conditional update: 412 is a correct answer when another VU of the same user won.
  const put = http.put(`${BASE}${u.plan}`, JSON.stringify({
    name: `Leg day ${__ITER}`,
    exercises: [{ name: "Squat", kind: "strength", sets: [{ repetitions: 8, weight_kg: 50 }] }],
  }), { ...write({ "If-Match": plan.headers.Etag }), responseCallback: http.expectedStatuses(200, 412) });
  check(put, { "put 200 or 412": (r) => r.status === 200 || r.status === 412 });
}
