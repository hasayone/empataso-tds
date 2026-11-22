package admin

import (
	"net/url"
	"reflect"
	"testing"
)

// TestParseCondsStatusList checks the query the conversions page sends for "Status in list: Lead, Sale":
// two f=status:in: parameters become one condition with both values, so the server matches either, and the
// campaign condition beside it stays a separate one.
func TestParseCondsStatusList(t *testing.T) {
	q := url.Values{}
	q.Add("f", "campaign_id:eq:c1")
	q.Add("f", "status:in:lead")
	q.Add("f", "status:in:sale")
	conds, err := parseConds(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(conds) != 2 || conds[0].Field != "campaign_id" || conds[0].Op != "eq" {
		t.Fatalf("campaign condition first and alone: %+v", conds)
	}
	if got := conds[1]; got.Field != "status" || got.Op != "in" || !reflect.DeepEqual(got.Values, []string{"lead", "sale"}) {
		t.Fatalf("the two statuses must be one list: %+v", got)
	}
}
