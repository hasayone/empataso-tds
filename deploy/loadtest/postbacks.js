// k6 conversion load against the engine's /postback.
//
// Each request converts one click id from cids.json exactly once, with status
// lead, payout 1.5 and tid "lt-<RUN>-<n>". Use only click ids of campaigns
// without outbound postback templates (quiet_groups.json from targets.mjs):
// a campaign with templates would forward the test conversion outside.
//
// Environment:
//   BASE     engine address, default http://engine:8080
//   RUN      run label (letters and digits), required
//   RATE     postbacks per second
//   DURATION length of the run, e.g. 60s
//   DATA     path of cids.json, default /data/cids.json
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

const cids = new SharedArray("cids", () => JSON.parse(readFile(ENV.DATA || "/data/cids.json")));

/** Responses by status: 200 stored, 404 click not found, 5xx store unavailable, neterr no answer. */
const stored = new Counter("pb_200");
const notFound = new Counter("pb_404");
const other4 = new Counter("pb_4xx_other");
const unavailable = new Counter("pb_5xx");
const netErr = new Counter("pb_neterr");
const latency = new Trend("pb_ms", true);

export const options = {
  scenarios: {
    postbacks: {
      executor: "constant-arrival-rate",
      rate: Number(ENV.RATE || 34),
      timeUnit: "1s",
      duration: ENV.DURATION || "60s",
      preAllocatedVUs: 100,
      maxVUs: 1000,
    },
  },
  discardResponseBodies: true,
  summaryTrendStats: ["count", "avg", "med", "p(95)", "p(99)", "max"],
};

/** default sends one postback for the next unused click id. */
export default function () {
  const n = exec.scenario.iterationInTest;
  if (n >= cids.length) return;
  const url = `${BASE}/postback?subid=${encodeURIComponent(cids[n])}&status=lead&payout=1.5&tid=lt-${RUN}-${n}`;
  const res = http.get(url, { tags: { name: "postback" }, timeout: "30s" });
  latency.add(res.timings.duration);
  if (res.status === 200) stored.add(1);
  else if (res.status === 404) notFound.add(1);
  else if (res.status >= 500) unavailable.add(1);
  else if (res.status === 0) netErr.add(1);
  else other4.add(1);
}

/** handleSummary writes the k6 summary next to the data file. */
export function handleSummary(data) {
  return { [`/data/${RUN}.summary.json`]: JSON.stringify(data) };
}
