// k6 click load against the engine.
//
// Every request is one click from targets.json (see targets.mjs): a real
// campaign path, a real visitor address in X-Forwarded-For, its user agent,
// a language and a keyword. sub_id_12 carries "lt-<RUN>-<n>" so the run's
// rows can be counted and removed in ClickHouse. Redirects are not followed.
//
// Environment:
//   BASE     engine address, default http://engine:8080
//   RUN      run label (letters and digits), required
//   RATE     clicks per second for a flat run
//   DURATION length of a flat run, e.g. 60s
//   STAGES   step run instead of RATE: "100:60s,250:60s,..." (rate:duration)
//   DATA     path of targets.json, default /data/targets.json
//
// The summary is written to /data/<RUN>.summary.json.
import http from "k6/http";
import exec from "k6/execution";
import { Counter, Trend } from "k6/metrics";
import { SharedArray } from "k6/data";

/** ENV is the k6 environment map; readFile is k6 open, allowed only in the init stage. */
// eslint-disable-next-line no-undef
const ENV = __ENV;
// eslint-disable-next-line no-undef
const readFile = open;

const BASE = ENV.BASE || "http://engine:8080";
const RUN = ENV.RUN;
if (!RUN || !/^[A-Za-z0-9]+$/.test(RUN)) {
  throw new Error("RUN must be letters and digits");
}

const targets = new SharedArray("targets", () => JSON.parse(readFile(ENV.DATA || "/data/targets.json")));

/** Responses by status class; neterr is a request that got no HTTP answer. */
const byClass = {
  2: new Counter("clicks_2xx"),
  3: new Counter("clicks_3xx"),
  4: new Counter("clicks_4xx"),
  5: new Counter("clicks_5xx"),
  0: new Counter("clicks_neterr"),
};
/** Latency of answered clicks (status below 500). */
const latency = new Trend("click_ms", true);

/** scenario returns the k6 executor for RATE and DURATION, or for STAGES; it throws when both are unset. */
function scenario() {
  if (ENV.STAGES) {
    const stages = [];
    for (const part of ENV.STAGES.split(",")) {
      const [rate, dur] = part.split(":");
      stages.push({ target: Number(rate), duration: "1s" });
      stages.push({ target: Number(rate), duration: dur });
    }
    return {
      executor: "ramping-arrival-rate",
      startRate: Number(ENV.STAGES.split(":")[0]),
      timeUnit: "1s",
      preAllocatedVUs: 200,
      maxVUs: 2000,
      stages,
    };
  }
  if (!ENV.RATE) {
    throw new Error("set RATE and DURATION, or STAGES");
  }
  return {
    executor: "constant-arrival-rate",
    rate: Number(ENV.RATE),
    timeUnit: "1s",
    duration: ENV.DURATION || "60s",
    preAllocatedVUs: 200,
    maxVUs: 2000,
  };
}

export const options = {
  scenarios: { clicks: scenario() },
  maxRedirects: 0,
  discardResponseBodies: true,
  summaryTrendStats: ["count", "avg", "med", "p(95)", "p(99)", "max"],
  insecureSkipTLSVerify: true,
};

/** default sends one click and records its status class. */
export default function () {
  const n = exec.scenario.iterationInTest;
  const t = targets[n % targets.length];
  const q = `sub_id_12=lt-${RUN}-${n}` + (t.kw ? `&keyword=${encodeURIComponent(t.kw)}` : "") + "&utm_source=lt";
  const headers = { "X-Forwarded-For": t.ip, "User-Agent": t.ua, Referer: "https://news.example.org/lt" };
  if (t.lang) headers["Accept-Language"] = t.lang;
  const res = http.get(`${BASE}${t.p}?${q}`, { headers, tags: { name: "click" }, timeout: "30s" });
  byClass[Math.floor(res.status / 100)]?.add(1);
  if (res.status > 0 && res.status < 500) {
    latency.add(res.timings.duration);
  }
}

/** handleSummary writes the k6 summary next to the data file. */
export function handleSummary(data) {
  return { [`/data/${RUN}.summary.json`]: JSON.stringify(data) };
}
