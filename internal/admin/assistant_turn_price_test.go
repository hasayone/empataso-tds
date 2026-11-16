package admin

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"
)

// turnUSD runs one turn of model in a new chat and returns the usd of its usage event, with ok
// false when the event has none.
func (e *turnEnv) turnUSD(t *testing.T, model string) (usd float64, ok bool) {
	t.Helper()
	resp := e.send(t, e.newChat(t), `{"text":"hi","model":"`+model+`"}`)
	defer resp.Body.Close()
	events, _ := readSSE(t, resp.Body)
	if last := lastEvent(t, events); last.name != "done" {
		t.Fatalf("the turn did not end: %v", eventNames(events))
	}
	usage := eventsNamed(events, "usage")
	if len(usage) != 1 {
		t.Fatalf("events %v", eventNames(events))
	}
	var v struct {
		USD *float64 `json:"usd"`
	}
	if err := json.Unmarshal([]byte(usage[0].data), &v); err != nil {
		t.Fatal(err)
	}
	if v.USD == nil {
		return 0, false
	}
	return *v.USD, true
}

func TestAITurnUSDUsesTheSamePriceAsAccounting(t *testing.T) {
	stub := aiScriptStub(t, aiAnthText("ok", 10000, 2000))
	e := newTurnEnv(t, stub, "")
	in, out := 2.0, 10.0
	e.s.aiCatalogPut(&aiCatalog{Provider: aiProviderAnthropic, FetchedAt: e.s.aiModelsNow().UTC(),
		Models: []aiModelInfo{{ID: "cat-priced", InputPerM: &in, OutputPerM: &out}, {ID: "cat-free"}}})
	body := `{"enabled":true,"provider":"anthropic","keys":{"anthropic":"` + aiCanary + `"},"allowed_models":["cat-priced","cat-free"],"model":"cat-priced"}`
	if code, raw := e.do(t, http.MethodPut, "/api/ai/settings", body); code != http.StatusOK {
		t.Fatalf("%d %s", code, raw)
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

	usd, ok := e.turnUSD(t, "cat-priced")
	if _, total, _ := e.s.aiUsageSnapshot(); !ok || !near(usd, 0.04) || !near(total.USD, usd) {
		t.Fatalf("a catalog-only price counts the turn: shown %v %v, charged %v", usd, ok, total.USD)
	}

	if code, raw := e.do(t, http.MethodPut, "/api/ai/settings", `{"prices":{"cat-priced":{"input_per_m":1,"output_per_m":1}}}`); code != http.StatusOK {
		t.Fatalf("%d %s", code, raw)
	}
	usd, ok = e.turnUSD(t, "cat-priced")
	if _, total, _ := e.s.aiUsageSnapshot(); !ok || !near(usd, 0.012) || !near(total.USD, 0.052) {
		t.Fatalf("the administrator price wins over the catalog: shown %v %v, charged %v", usd, ok, total.USD)
	}

	if _, ok = e.turnUSD(t, "cat-free"); ok {
		t.Fatal("a model with no price shows no usd")
	}
	if _, total, _ := e.s.aiUsageSnapshot(); !near(total.USD, 0.052) {
		t.Fatalf("a model with no price charges no usd: %v", total.USD)
	}
}
