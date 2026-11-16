// Builds the k6 click and postback inputs from local state.
//
// Usage: node targets.mjs <groups.json> <sample.tsv> <out dir>
//
// groups.json is the engine's campaign file. sample.tsv holds real local
// clicks: group_id, ip, user agent and country, tab-separated. The script
// writes targets.json (one click per entry, about 8% of them bots) and
// quiet_groups.json (active campaigns without outbound postback templates).
// Only campaigns that are active and exist in groups.json are used.
// Campaigns that can serve a local landing or hand the visitor to another
// campaign are left out: a landing is PHP code that may call outside services
// on every view, and a test click must not reach them.
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

/** Bot user agents mixed into the click list: crawlers, scripts and an empty one. */
const BOT_UAS = [
  "curl/8.4.0",
  "python-requests/2.31.0",
  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/120.0.0.0 Safari/537.36",
  "Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)",
  "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
  "",
];

/** Share of clicks that carry a bot user agent. */
const BOT_SHARE = 0.08;

/** Accept-Language values picked per click. */
const LANGS = ["en-US,en;q=0.9", "pt-BR,pt;q=0.9", "fr-FR,fr;q=0.8", "es-MX,es;q=0.9", "ru-RU,ru;q=0.9", ""];

/** Keywords sent in the keyword parameter. */
const KEYWORDS = ["casino bonus", "aviator", "sports bet", "slots", "free spins", ""];

/**
 * main reads the inputs named on the command line and writes the two outputs.
 * It exits with code 2 on missing arguments; JSON and file errors are thrown.
 */
function main() {
  const [groupsPath, samplePath, outDir] = process.argv.slice(2);
  if (!groupsPath || !samplePath || !outDir) {
    console.error("usage: node targets.mjs <groups.json> <sample.tsv> <out dir>");
    process.exit(2);
  }
  const groups = JSON.parse(readFileSync(groupsPath, "utf8"));
  const active = new Set();
  const quiet = [];
  let unsafe = 0;
  for (const g of groups) {
    if (!g.status) continue;
    if (servesOutside(g)) {
      unsafe++;
      continue;
    }
    active.add(g.id);
    const outbound = (g.postbacks?.length ?? 0) + (g.postback_ids?.length ?? 0);
    if (outbound === 0) quiet.push(g.id);
  }
  const targets = [];
  let seed = 1;
  const rnd = () => {
    seed = (seed * 48271) % 2147483647;
    return seed / 2147483647;
  };
  const pick = (list) => list[Math.floor(rnd() * list.length)];
  for (const line of readFileSync(samplePath, "utf8").split(/\r?\n/)) {
    const [gid, ip, ua, country] = line.replace(/^\uFEFF/, "").split("\t");
    if (!gid || !ip || !active.has(gid)) continue;
    const bot = rnd() < BOT_SHARE;
    targets.push({
      p: "/" + encodeURIComponent(gid),
      ip,
      ua: bot ? pick(BOT_UAS) : ua,
      lang: pick(LANGS),
      kw: pick(KEYWORDS),
      c: country || "",
    });
  }
  writeFileSync(join(outDir, "targets.json"), JSON.stringify(targets));
  writeFileSync(join(outDir, "quiet_groups.json"), JSON.stringify(quiet));
  console.log(JSON.stringify({ targets: targets.length, campaigns: new Set(targets.map((t) => t.p)).size, quiet: quiet.length, skipped_landing_or_link: unsafe }));
}

/**
 * servesOutside reports whether campaign g can run a landing or pass the
 * visitor on: a landing or group redirect on the campaign or on any stream, or
 * a stream with landings in its rotation. It does not fail.
 */
function servesOutside(g) {
  const risky = (r) => r === "landing" || r === "group";
  if (risky(g.redirect)) return true;
  return (g.streams ?? []).some((s) => risky(s.out?.redirect) || (s.out?.landings?.length ?? 0) > 0 || risky(s.bots?.redirect));
}

main();
