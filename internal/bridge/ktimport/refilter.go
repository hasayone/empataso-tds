package ktimport

import (
	"context"
	"reflect"
	"strconv"
	"strings"
)

// StreamFilters — правила одного потока Keitaro после переноса только его фильтров.
// OK false значит, что ни один фильтр не перенёсся и поток надо выключить, Why — причина.
// Skipped — имена фильтров Keitaro, которые перенос пропустил, в порядке потока.
type StreamFilters struct {
	Rules    Rules
	FilterOr bool
	OK       bool
	Why      string
	Skipped  []string
}

// MapStreamFilters переводит фильтры потоков без выходов, офферов и каталогов.
//
// Параметры: streams — потоки Keitaro одной или нескольких кампаний.
// Результат: id потока → перенесённые фильтры; файлы списков IP, которые нужны правилам ip_list
// (имя → текст, nil если файлов нет); строки отчёта со ссылкой stream N.
// Ошибки: нет. Поток с id не больше нуля пропускается.
func MapStreamFilters(streams []KStream) (map[int]StreamFilters, map[string]string, []Loss) {
	out := make(map[int]StreamFilters, len(streams))
	lists := listBag{}
	var losses []Loss
	for _, st := range streams {
		id := int(st.ID)
		if id <= 0 {
			continue
		}
		rules, ok, why, flosses, skipped := mapFilters(st.Filters, &lists, st.FilterOr)
		for _, l := range flosses {
			l.Ref = streamRef(id)
			losses = append(losses, l)
		}
		out[id] = StreamFilters{Rules: rules, FilterOr: st.FilterOr, OK: ok, Why: why, Skipped: skipped}
	}
	if len(lists) == 0 {
		return out, nil, losses
	}
	return out, map[string]string(lists), losses
}

// streamIDPrefix начинает заметку импортированного потока: keitaro_stream_id=N.
const streamIDPrefix = "keitaro_stream_id="

// StreamNote собирает заметку импортированного потока.
//
// Параметры: id — id потока Keitaro; skipped — имена пропущенных фильтров.
// Результат: keitaro_stream_id=N и, если skipped не пуст, пометка с именами через запятую.
// Ошибки: нет.
func StreamNote(id int, skipped []string) string {
	note := streamIDPrefix + strconv.Itoa(id)
	if len(skipped) > 0 {
		note += skippedFiltersNote + strings.Join(skipped, ", ")
	}
	return note
}

// RenoteStream заменяет в заметке потока пометку о пропущенных фильтрах.
//
// Параметры: comment — заметка, которая начинается с keitaro_stream_id=N; skipped — новые имена.
// Результат: заметка без прежней пометки и с новой, если skipped не пуст. Текст между id и
// пометкой остаётся.
// Ошибки: нет.
func RenoteStream(comment string, skipped []string) string {
	base, _, _ := strings.Cut(comment, skippedFiltersNote)
	if len(skipped) > 0 {
		base += skippedFiltersNote + strings.Join(skipped, ", ")
	}
	return base
}

// NoteStreamID читает id потока Keitaro из заметки keitaro_stream_id=N.
//
// Параметры: comment — заметка потока.
// Результат: id и true, если заметка начинается с keitaro_stream_id= и за ним идёт число больше нуля.
// Ошибки: нет.
func NoteStreamID(comment string) (int, bool) {
	rest, ok := strings.CutPrefix(comment, streamIDPrefix)
	if !ok {
		return 0, false
	}
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// RuleKeys возвращает JSON-ключи правил, которые умеет заполнять перенос фильтров Keitaro.
//
// Параметры: нет.
// Результат: имена ключей Rules в порядке полей. Остальные ключи правил движка перенос не трогает.
// Ошибки: нет.
func RuleKeys() []string {
	t := reflect.TypeOf(Rules{})
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}

// FetchStreams читает потоки указанных кампаний, по одному GET на кампанию.
//
// Параметры: ctx — отмена; ids — id кампаний Keitaro. Progress, если задан, получает этап streams.
// Результат: id кампании строкой → её потоки.
// Ошибки: сеть, статус не 200, тело не массив. Первая ошибка отменяет остальные запросы.
func (c *Client) FetchStreams(ctx context.Context, ids []int) (map[string][]KStream, error) {
	camps := make([]Campaign, 0, len(ids))
	for _, id := range ids {
		camps = append(camps, Campaign{ID: FlexInt(id)})
	}
	return c.fetchCampaignStreams(ctx, camps)
}
