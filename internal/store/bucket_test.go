package store

import (
	"strings"
	"testing"
)

func TestBucketWhereSkipsBotsAndPricedClicks(t *testing.T) {
	plain := bucketWhere(false, true)
	for _, part := range []string{"(bot = '' OR bot = '-')", "cost_cents = 0"} {
		if !strings.Contains(plain, part) {
			t.Fatalf("default window missing %s in %s", part, plain)
		}
	}
	withBots := bucketWhere(true, true)
	if strings.Contains(withBots, "bot = ") || !strings.Contains(withBots, "cost_cents = 0") {
		t.Fatalf("include bots: %s", withBots)
	}
}
