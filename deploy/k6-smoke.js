// Load smoke test (Section 18, Phase 7). Exercises the golden agent path under
// concurrency: register -> create list -> create tasks -> list.
//
//   BASE_URL=https://todo.cobanov.run k6 run deploy/k6-smoke.js
//
// Defaults to a local server. Uses cookie auth (k6 keeps a cookie jar per VU).
import http from "k6/http";
import { check, sleep } from "k6";

const BASE = __ENV.BASE_URL || "http://localhost:8080";

export const options = {
  scenarios: {
    agents: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "20s", target: 50 },
        { duration: "40s", target: 200 },
        { duration: "20s", target: 0 },
      ],
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.02"],
    http_req_duration: ["p(95)<800"],
  },
};

const JSON_HEADERS = { "Content-Type": "application/json", "X-CSRF-Token": "1" };

export default function () {
  const email = `k6-${__VU}-${__ITER}-${Date.now()}@example.com`;
  const creds = JSON.stringify({ email, password: "loadtest-password" });

  const reg = http.post(`${BASE}/v1/auth/register`, creds, { headers: JSON_HEADERS });
  check(reg, { "registered": (r) => r.status === 201 });
  if (reg.status !== 201) return;

  const list = http.post(`${BASE}/v1/lists`, JSON.stringify({ name: "Load" }), { headers: JSON_HEADERS });
  check(list, { "list created": (r) => r.status === 201 });
  const listId = list.json("id");

  for (let i = 0; i < 5; i++) {
    const t = http.post(
      `${BASE}/v1/tasks`,
      JSON.stringify({ list_id: listId, title: `task ${i}`, priority: i % 4 }),
      { headers: JSON_HEADERS },
    );
    check(t, { "task created": (r) => r.status === 201 });
  }

  const listed = http.get(`${BASE}/v1/tasks?list_id=${listId}`);
  check(listed, { "listed": (r) => r.status === 200 });

  sleep(1);
}
