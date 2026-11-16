//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLogicKeyLifetime runs keyLife and keyLastUse under node: no expiry and an old
// record are "never expires", a past expiry is expired, seven days or less is
// expiring with whole days rounded up, later is "until", and a missing or broken
// last use reads as not used yet. It skips without node.
func TestLogicKeyLifetime(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	m := pureRe.FindStringSubmatch(spaSource())
	if m == nil {
		t.Fatal("the pure helpers block is not in the SPA")
	}
	f := filepath.Join(t.TempDir(), "keylife.js")
	if err := os.WriteFile(f, []byte(m[1]+keyLifeChecks), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("key lifetime checks failed:\n%s", out)
	}
}

const keyLifeChecks = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
const now = Date.parse('2026-11-16T12:00:00Z');
const day = 86400000;
const iso = ms => new Date(ms).toISOString();
eq(keyLife({}, now), {tone:'', key:'key.life.never', days:0, at:''}, 'old record never expires');
eq(keyLife(null, now).key, 'key.life.never', 'no row');
eq(keyLife({expires_at:'0001-01-01T00:00:00Z'}, now).key, 'key.life.never', 'zero time');
eq(keyLife({expires_at:iso(now - 1)}, now).tone, 'bad', 'past expiry');
eq(keyLife({expires_at:iso(now)}, now).key, 'key.life.expired', 'expiry at now');
eq(keyLife({expires_at:iso(now + 3 * day - 1000)}, now), {tone:'warn', key:'key.life.soon', days:3, at:iso(now + 3 * day - 1000)}, 'rounds up');
eq(keyLife({expires_at:iso(now + 7 * day)}, now).tone, 'warn', 'seven days is soon');
eq(keyLife({expires_at:iso(now + 30 * day)}, now).key, 'key.life.until', 'a month left');
eq(keyLastUse({}), {at:'', ip:''}, 'not used');
eq(keyLastUse({last_used_at:'nope', last_used_ip:'1.2.3.x'}), {at:'', ip:''}, 'broken time');
eq(keyLastUse({last_used_at:'2026-11-16T11:00:00Z', last_used_ip:'203.0.113.x'}), {at:'2026-11-16T11:00:00Z', ip:'203.0.113.x'}, 'used');
eq(KEY_TTL_DAYS, [0, 7, 30, 90, 365], 'lifetimes');
if (failed) process.exit(1);
`
