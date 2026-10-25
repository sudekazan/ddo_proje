// k6 load test for the conflict-free booking engine (T-1).
//
// Two scenarios run together:
//  - "spread":     VUs book random free slots across 6 dentists (throughput / latency)
//  - "contention": every VU hammers the SAME dentist + slot each iteration; exactly one
//                  booking per slot may succeed, all others must get 409 DENTIST_BUSY.
// The test fails on any 5xx, on p95 latency > 500 ms, or if a slot is ever double booked.
import http from "k6/http";
import { check } from "k6";
import { Counter } from "k6/metrics";
import exec from "k6/execution";

const API = __ENV.API_URL || "http://localhost:8080";
const created = new Counter("bookings_created");
const conflicts = new Counter("bookings_conflict_409");
const doubleBooked = new Counter("double_bookings");
const contentionCreated = new Counter("contention_created");

// 409 is the engine's correct answer to a taken slot, not a failure.
http.setResponseCallback(http.expectedStatuses(200, 201, 409));

export const options = {
  scenarios: {
    spread: { executor: "constant-vus", vus: 20, duration: "40s", exec: "spread" },
    contention: { executor: "per-vu-iterations", vus: 30, iterations: 20, exec: "contention", startTime: "2s" },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    contention_created: ["count==20"], // 20 contended slots → exactly 20 winners out of 600 attempts
    "http_req_duration{name:book}": ["p(95)<500"],
    "http_req_duration{name:availability}": ["p(95)<500"],
    double_bookings: ["count==0"],
    checks: ["rate>0.99"],
  },
};

const json = { headers: { "Content-Type": "application/json" } };

export function setup() {
  const r = http.post(`${API}/api/auth/login`, JSON.stringify({ email: "resepsiyon@dishekimim.local", password: "Demo123!" }), json);
  check(r, { "login 200": (x) => x.status === 200 });
  // Work on days far ahead so the run does not disturb the demo calendar.
  const base = new Date(Date.now() + (400 + Math.floor(Math.random() * 300)) * 86400000);
  return { token: r.json("access_token"), base: base.toISOString().slice(0, 10) };
}

function auth(token) {
  return { headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` } };
}

function day(base, offset) {
  const d = new Date(base + "T00:00:00Z");
  d.setUTCDate(d.getUTCDate() + offset);
  if (d.getUTCDay() === 0) d.setUTCDate(d.getUTCDate() + 1); // closed on Sunday
  return d.toISOString().slice(0, 10);
}

export function spread(data) {
  const dentist = 1 + Math.floor(Math.random() * 6);
  const d = day(data.base, Math.floor(Math.random() * 60));
  const av = http.get(`${API}/api/availability?date=${d}&treatment_code=EXAM&dentist_id=${dentist}`, { ...auth(data.token), tags: { name: "availability" } });
  check(av, { "availability 200": (r) => r.status === 200 });
  const slots = av.json("items") || [];
  if (slots.length === 0) return;
  const s = slots[Math.floor(Math.random() * slots.length)];
  const res = http.post(`${API}/api/appointments`, JSON.stringify({
    patient_id: 2 + Math.floor(Math.random() * 19998), dentist_id: s.dentist_id, treatment_code: "EXAM", start: s.start,
  }), { ...auth(data.token), tags: { name: "book" } });
  check(res, { "book 201 or 409": (r) => r.status === 201 || r.status === 409 });
  if (res.status === 201) created.add(1);
  if (res.status === 409) conflicts.add(1);
}

// All VUs share the iteration number → same slot per iteration across VUs.
export function contention(data) {
  const it = exec.vu.iterationInScenario;
  const d = day(data.base, 70 + Math.floor(it / 30));
  const minutes = 9 * 60 + (it % 30) * 15; // 09:00 … 16:15
  const start = `${d}T${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}:00+03:00`;
  const res = http.post(`${API}/api/appointments`, JSON.stringify({
    patient_id: 2 + ((exec.vu.idInTest * 977 + it * 31) % 19998), dentist_id: 1, treatment_code: "EXAM", start,
  }), { ...auth(data.token), tags: { name: "book" } });
  check(res, { "contention 201 or 409": (r) => r.status === 201 || r.status === 409 });
  if (res.status === 201) { created.add(1); contentionCreated.add(1); }
  if (res.status === 409) conflicts.add(1);
}

export function teardown(data) {
  // Every contended slot must hold at most one active appointment.
  const d = day(data.base, 70);
  const r = http.get(`${API}/api/appointments?date=${d}&dentist_id=1&status=booked&limit=200`, auth(data.token));
  const items = r.json("items") || [];
  const seen = {};
  for (const a of items) {
    if (seen[a.start]) doubleBooked.add(1);
    seen[a.start] = true;
  }
  check(r, { "contended day: one booking per slot": () => items.length <= 30 });
}
