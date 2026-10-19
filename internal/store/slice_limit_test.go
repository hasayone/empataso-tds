package store

import (
	"fmt"
	"strings"
	"testing"
)

// TestSliceEventsSQLAsksOneExtraGroup checks that the visit query asks for one group beyond
// SliceLimit, which is how SliceMeta tells a cut report from one that fits.
func TestSliceEventsSQLAsksOneExtraGroup(t *testing.T) {
	want := fmt.Sprintf("LIMIT %d", SliceLimit+1)
	for _, click := range []bool{true, false} {
		q := sliceEventsSQL(click, "uniq()", "uniqIf()", "")
		if !strings.Contains(q, want) {
			t.Errorf("clickCols=%v: query has no %q:\n%s", click, want, q)
		}
		if strings.Contains(q, fmt.Sprintf("LIMIT %d\n", SliceLimit)) || strings.HasSuffix(q, fmt.Sprintf("LIMIT %d", SliceLimit)) {
			t.Errorf("clickCols=%v: query still stops at the limit itself", click)
		}
	}
	if SliceLimit != 2000 {
		t.Errorf("SliceLimit = %d, the report page says 2000", SliceLimit)
	}
}
