package store

import (
	"strings"
	"testing"
)

// The scan targets must line up with the SELECT list for every wave combination.
func TestPostbackColumnsMatchDest(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		c := &CH{clickCols: mask&1 != 0, extConv: mask&2 != 0, modelConv: mask&4 != 0}
		sel, ext := c.postbackColumns()
		var p PostbackRow
		dest := c.postbackDest(&p, make([]string, 12), ext)
		if got, want := len(dest), strings.Count(sel, ",")+1; got != want {
			t.Errorf("mask %03b: %d scan targets for %d columns in %q", mask, got, want, sel)
		}
		if has := strings.Contains(sel, "device_model"); has != c.modelConv {
			t.Errorf("mask %03b: device_model selected = %v, want %v", mask, has, c.modelConv)
		}
		if c.modelConv && dest[len(dest)-1] != &p.DeviceModel {
			t.Errorf("mask %03b: device_model is not the last target", mask)
		}
	}
}
