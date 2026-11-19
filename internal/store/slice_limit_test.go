package store

import (
	"fmt"
	"strings"
	"testing"
)

// TestSliceEventsSQLAsksOneExtraGroup checks that the visit query asks for one group beyond
// the limit it is given, which is how SliceMeta and SliceAll tell a cut report from one that fits.
func TestSliceEventsSQLAsksOneExtraGroup(t *testing.T) {
	for _, limit := range []int{SliceLimit, PageMaxRows} {
		want := fmt.Sprintf("LIMIT %d", limit+1)
		for _, click := range []bool{true, false} {
			q := sliceEventsSQL(click, "uniq()", "uniqIf()", "", limit)
			if !strings.Contains(q, want) {
				t.Errorf("clickCols=%v: query has no %q:\n%s", click, want, q)
			}
			if strings.Contains(q, fmt.Sprintf("LIMIT %d\n", limit)) || strings.HasSuffix(q, fmt.Sprintf("LIMIT %d", limit)) {
				t.Errorf("clickCols=%v: query still stops at the limit itself", click)
			}
		}
	}
	if SliceLimit != 2000 {
		t.Errorf("SliceLimit = %d, the old report answer says 2000", SliceLimit)
	}
}
