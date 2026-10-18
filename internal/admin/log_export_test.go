package admin

import (
	"testing"

	"github.com/egerkuzma/kuztds/internal/store"
)

func TestLogExportCarriesDeviceModel(t *testing.T) {
	head := logExportHeader()
	rec := logExportRecord(store.LogRow{DeviceModel: "SM-S918B", Brand: "Samsung"})
	if len(head) != len(rec) {
		t.Fatalf("header has %d columns, record %d", len(head), len(rec))
	}
	at := -1
	for i, name := range head {
		if name == "device_model" {
			at = i
		}
	}
	if at < 0 || rec[at] != "SM-S918B" {
		t.Fatalf("device_model column %d in %v, value %q", at, head, rec[at])
	}
}
