package admin

import (
	"slices"
	"testing"

	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
)

// Hiding the uniq field hides every key that carries it, the first-step
// uniques of the catalog facts included.
func TestHiddenUniqCoversUniqFirst(t *testing.T) {
	keys := fieldJSONKeys("uniq")
	for _, want := range []string{"uniq", "unique", "uniq_first"} {
		if !slices.Contains(keys, want) {
			t.Errorf("fieldJSONKeys(uniq) = %v, missing %q", keys, want)
		}
	}
}

// The step column is internal: a role cannot hide it, the role save refuses it,
// and the field list of the interface does not carry it. An ordinary field is the
// control: the same calls accept it.
func TestStepIsNotARoleField(t *testing.T) {
	if hiddenIDOK("step") || !hiddenIDOK("ip") {
		t.Errorf("hiddenIDOK: step=%v ip=%v, want false and true", hiddenIDOK("step"), hiddenIDOK("ip"))
	}
	if _, err := normalizeAccess(security.Access{Hidden: []string{"step"}}); !errorsIsAccess(err, errAccessHidden) {
		t.Errorf("a role hiding step: %v, want errAccessHidden", err)
	}
	if _, err := normalizeAccess(security.Access{Hidden: []string{"ip"}}); err != nil {
		t.Errorf("a role hiding ip: %v", err)
	}
	listed := fieldsResponseOf(store.FieldsInfo{Fields: store.StaticFields()})
	seenIP := false
	for _, f := range listed.Fields {
		if f.Name == "step" {
			t.Error("step is listed in the field list of the interface")
		}
		seenIP = seenIP || f.Name == "ip"
	}
	if !seenIP {
		t.Error("the field list lost its ordinary fields")
	}
}
