package ktimport

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestCampaignSettingLosses(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		camps := settingCampaigns(t, `[
			{"id":1,"alias":"a","name":"A","state":"active","cookies_ttl":24,"bind_visitors":"s","cost_value":0,"cost_auto":false,"uniqueness":null,"uniqueness_param":"","uniqueness_method":0},
			{"id":2,"alias":"b","name":"B","state":"active","cookies_ttl":0,"bind_visitors":"","cost_value":"0","cost_auto":false,"uniqueness":false,"uniqueness_param":[],"uniqueness_method":{}},
			{"id":3,"alias":"c","name":"C","state":"active","bind_visitors":"S","uniqueness":"","uniqueness_param":0,"uniqueness_method":false}
		]`)
		groups, losses := mapSettingCampaigns(camps)
		if details := campaignSettingDetails(losses); len(details) != 0 {
			t.Fatalf("defaults %+v", details)
		}
		assertSettingGroupsOpen(t, groups)
	})
	t.Run("cookies", func(t *testing.T) {
		camps := settingCampaigns(t, `[
			{"id":1,"alias":"a","name":"A","state":"active","cookies_ttl":48},
			{"id":2,"alias":"b","name":"B","state":"active","cookies_ttl":12},
			{"id":3,"alias":"c","name":"C","state":"active","cookies_ttl":24,"bind_visitors":"s","cost_value":0,"cost_auto":false}
		]`)
		groups, losses := mapSettingCampaigns(camps)
		if got := campaignSettingDetails(losses); len(got) != 0 {
			t.Fatalf("cookies %+v", got)
		}
		if len(groups) != 3 || groups[0].UniqSeconds != 48*3600 || groups[1].UniqSeconds != 12*3600 || groups[2].UniqSeconds != 0 {
			t.Fatalf("uniq seconds %+v", groups)
		}
		assertSettingGroupsOpen(t, groups)
	})
	t.Run("cookies odd", func(t *testing.T) {
		camps := settingCampaigns(t, `[
			{"id":1,"alias":"a","name":"A","state":"active","cookies_ttl":-1},
			{"id":2,"alias":"b","name":"B","state":"active","cookies_ttl":24}
		]`)
		groups, losses := mapSettingCampaigns(camps)
		if got := campaignSettingDetails(losses); len(got) != 1 || got[0] != "срок уникальности кампании не переносится: 1" {
			t.Fatalf("odd cookies %+v", got)
		}
		if groups[0].UniqSeconds != 0 || groups[1].UniqSeconds != 0 {
			t.Fatalf("odd seconds %+v", groups)
		}
		assertSettingGroupsOpen(t, groups)
	})
	t.Run("bind", func(t *testing.T) {
		camps := settingCampaigns(t, `[
			{"id":1,"alias":"a","name":"A","state":"active","bind_visitors":"SL"},
			{"id":2,"alias":"b","name":"B","state":"active","bind_visitors":"slo"},
			{"id":3,"alias":"c","name":"C","state":"active","bind_visitors":"s"}
		]`)
		groups, losses := mapSettingCampaigns(camps)
		if got := campaignSettingDetails(losses); len(got) != 1 || got[0] != "привязка посетителя не переносится: 2" {
			t.Fatalf("bind %+v", got)
		}
		assertSettingGroupsOpen(t, groups)
	})
	t.Run("cost", func(t *testing.T) {
		camps := settingCampaigns(t, `[
			{"id":1,"alias":"a","name":"A","state":"active","cost_value":1.5,"cost_auto":false},
			{"id":2,"alias":"b","name":"B","state":"active","cost_value":0,"cost_auto":true},
			{"id":3,"alias":"c","name":"C","state":"active","cost_value":0,"cost_auto":false}
		]`)
		groups, losses := mapSettingCampaigns(camps)
		if got := campaignSettingDetails(losses); len(got) != 1 || got[0] != "стоимость кампании не переносится: 2" {
			t.Fatalf("cost %+v", got)
		}
		assertSettingGroupsOpen(t, groups)
	})
	t.Run("uniqueness", func(t *testing.T) {
		camps := settingCampaigns(t, `[
			{"id":1,"alias":"a","name":"A","state":"active","uniqueness":"ip"},
			{"id":2,"alias":"b","name":"B","state":"active","uniqueness_param":["sub"]},
			{"id":3,"alias":"c","name":"C","state":"active","uniqueness":null,"uniqueness_param":"","uniqueness_method":0}
		]`)
		groups, losses := mapSettingCampaigns(camps)
		if got := campaignSettingDetails(losses); len(got) != 1 || got[0] != "уникальность кампании по параметру не переносится: 2" {
			t.Fatalf("uniqueness %+v", got)
		}
		assertSettingGroupsOpen(t, groups)
	})
}

// settingCampaigns читает кампании из JSON, как их отдаёт трекер.
//
// Параметры: t — тест; raw — JSON-массив кампаний.
// Результат: кампании с сырыми ключами уникальности.
// Ошибки: падение теста, если JSON не разбирается.
func settingCampaigns(t *testing.T, raw string) []Campaign {
	t.Helper()
	var camps []Campaign
	if err := json.Unmarshal([]byte(raw), &camps); err != nil {
		t.Fatal(err)
	}
	return camps
}

// mapSettingCampaigns прогоняет кампании через маппер вместе с живым потоком unique.
//
// Параметры: camps — кампании.
// Результат: группы и отчёт.
// Ошибки: нет.
func mapSettingCampaigns(camps []Campaign) ([]Group, []Loss) {
	streams := map[string][]KStream{}
	for _, camp := range camps {
		streams[strconv.Itoa(int(camp.ID))] = []KStream{{
			ID: 9, Type: "regular", Name: "main", State: "active",
			ActionType: "http", ActionPayload: "https://land.example/a",
			Filters:    []KFilter{{Name: "unique", Mode: "accept", Payload: json.RawMessage(`["1"]`)}},
		}}
	}
	ctx := &mapCtx{}
	return ctx.mapCampaigns(Snapshot{Campaigns: camps, Streams: streams}, nil)
}

// campaignSettingDetails оставляет строки отчёта о настройках кампании.
//
// Параметры: losses — отчёт прогона.
// Результат: тексты строк ref campaigns.
// Ошибки: нет.
func campaignSettingDetails(losses []Loss) []string {
	var out []string
	for _, l := range losses {
		if l.Kind == "approximated" && l.Ref == "campaigns" {
			out = append(out, l.Detail)
		}
	}
	return out
}

// assertSettingGroupsOpen проверяет, что настройки кампании не выключили группу и не попали в UniqMethod.
//
// Параметры: t — тест; groups — группы прогона.
// Результат: нет. Поток unique остаётся включённым.
// Ошибки: падение теста, если статус стал false или поле уникальности заполнено.
func assertSettingGroupsOpen(t *testing.T, groups []Group) {
	t.Helper()
	if len(groups) == 0 {
		t.Fatal("no groups")
	}
	for _, g := range groups {
		if !g.Status {
			t.Fatalf("status false %+v", g)
		}
		raw, err := json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		var probe struct {
			UniqMethod string `json:"uniq_method"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatal(err)
		}
		if probe.UniqMethod != "" {
			t.Fatalf("uniq method %q in %s", probe.UniqMethod, raw)
		}
		text := string(raw)
		for _, needle := range []string{"uniq_method", "cookies_ttl", "bind_visitors", "cost_auto", `"uniqueness"`} {
			if strings.Contains(text, needle) {
				t.Fatalf("group copied %s into %s", needle, text)
			}
		}
		if len(g.Streams) != 1 || !g.Streams[0].Status || g.Streams[0].Rules.Unique != 1 {
			t.Fatalf("stream %+v", g.Streams)
		}
	}
}
