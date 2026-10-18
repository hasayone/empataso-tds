package ktimport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSubIDFilterDropsOverLongValues(t *testing.T) {
	long := strings.Repeat("x", maxSubIDValueLen+1)
	exact := strings.Repeat("y", maxSubIDValueLen)
	payload, _ := json.Marshal([]string{"a", long, exact})
	rules, ok, _, losses := mapFilters([]KFilter{subFilter("sub_id_2", "accept", string(payload))}, nil, false)
	if !ok {
		t.Fatal("the campaign must stay importable")
	}
	if got := strings.Join(rules.SubIDs["sub_id_2"].Values, "|"); got != "a|"+exact {
		t.Errorf("kept values = %q, want the short ones only", got)
	}
	if len(losses) != 1 || losses[0].Kind != "skipped" || !strings.Contains(losses[0].Detail, "sub_id_2") {
		t.Errorf("losses = %+v, want one skipped loss for sub_id_2", losses)
	}
}

func TestSubIDFilterAllValuesTooLongIsSkipped(t *testing.T) {
	payload, _ := json.Marshal([]string{strings.Repeat("x", 300)})
	rules, ok, _, losses := mapFilters([]KFilter{
		subFilter("sub_id_1", "accept", string(payload)),
		subFilter("sub_id_2", "accept", `["a"]`),
	}, nil, false)
	if !ok {
		t.Fatal("the other filter keeps the stream on")
	}
	if _, has := rules.SubIDs["sub_id_1"]; has || len(rules.SubIDs["sub_id_2"].Values) != 1 {
		t.Errorf("sub_ids = %+v, want only sub_id_2", rules.SubIDs)
	}
	if len(losses) != 1 || losses[0].Kind != "skipped" {
		t.Errorf("losses = %+v, want one skipped loss", losses)
	}
}
