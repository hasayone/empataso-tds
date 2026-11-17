package ktimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client ходит в Keitaro только по allowlist: GET конфигурации и два POST журнала.
// Progress, если не nil, получает этап, сколько уже сделано и сколько всего.
// Из пула потоков вызовы могут идти параллельно.
type Client struct {
	Base     string
	Key      string
	HTTP     *http.Client
	Progress func(stage string, done, total int)
	// Version — версия Keitaro (Keitaro10 или Keitaro11), по которой журналы просят колонки идентификаторов.
	// Пусто — версия не определена: колонки идентификаторов не просятся.
	Version string
	// extRejected — последнее чтение журнала откатилось на базовые колонки.
	extRejected bool
	// idRejected — последнее чтение журнала откатилось с колонок идентификаторов версии.
	idRejected bool
}

const (
	logPageLimit        = 10000
	logBodyLimit        = 32 << 20
	getBodyLimit        = 64 << 20
	streamFetchParallel = 8
)

// logRetryDelays — паузы перед повторами страницы журнала после временного сбоя; длина задаёт число повторов.
var logRetryDelays = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second}

// transportError — запрос не дошёл до ответа или тело ответа оборвалось.
type transportError struct {
	msg   string
	cause error
}

// Error возвращает текст сбоя с причиной транспорта. Ключ API в заголовке и в текст не входит.
//
// Параметры: нет.
// Результат: текст и причина.
// Ошибки: нет.
func (e *transportError) Error() string {
	return e.msg + ": " + e.cause.Error()
}

// Unwrap отдаёт причину сбоя для errors.Is и errors.As.
func (e *transportError) Unwrap() error {
	return e.cause
}

// Get выполняет GET и возвращает тело.
//
// Параметры: ctx — отмена; path — путь, начинающийся с /admin_api/v1.
// Результат: тело ответа при статусе 200.
// Ошибки: путь вне allowlist, сеть, статус не 200. Ключ в текст ошибки не входит.
func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	code, body, err := c.request(ctx, http.MethodGet, path, nil, Allow)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("keitaro: GET %s вернул %d", path, code)
	}
	return body, nil
}

// FetchCampaigns читает список кампаний одним GET.
//
// Параметры: ctx — отмена.
// Результат: кампании трекера без потоков и справочников.
// Ошибки: сеть, статус не 200, тело не JSON-массив.
func (c *Client) FetchCampaigns(ctx context.Context) ([]Campaign, error) {
	raw, err := c.Get(ctx, "/admin_api/v1/campaigns")
	if err != nil {
		return nil, err
	}
	var camps []Campaign
	if err := json.Unmarshal(raw, &camps); err != nil {
		return nil, fmt.Errorf("keitaro: campaigns не массив")
	}
	return camps, nil
}

// CountCampaigns считает кампании одним GET.
//
// Параметры: ctx — отмена.
// Результат: длина массива кампаний.
// Ошибки: сеть, статус не 200, тело не JSON-массив.
func (c *Client) CountCampaigns(ctx context.Context) (int, error) {
	camps, err := c.FetchCampaigns(ctx)
	if err != nil {
		return 0, err
	}
	return len(camps), nil
}

// FetchSnapshot читает кампании, потоки, офферы, лендинги и справочники.
//
// Параметры: ctx — отмена. Progress, если задан, получает этапы snapshot и streams.
// Результат: снимок для маппера. Группы — склейка type=campaigns, offers, landings и domains.
// Ошибки: сеть, статус не 200 у кампаний, потоков, офферов или лендингов, тело не JSON.
// Не-200 у сетей, источников, доменов и групп снимок не прерывает. Первая ошибка потоков отменяет остальные.
func (c *Client) FetchSnapshot(ctx context.Context) (Snapshot, error) {
	return c.fetchSnapshot(ctx, nil)
}

// FetchCampaignSnapshot читает снимок для досинка одной кампании.
//
// Параметры: ctx — отмена; id — id кампании Keitaro.
// Результат: снимок по правилам ForCampaign: все кампании для alias, потоки только этой кампании,
// офферы, лендинги, сети, источник и домен, на которые она ссылается. Пользователи не читаются.
// Ошибки: id не больше нуля, кампании нет в трекере (ErrCampaignNotFound), ошибки FetchSnapshot.
func (c *Client) FetchCampaignSnapshot(ctx context.Context, id int) (Snapshot, error) {
	if id <= 0 {
		return Snapshot{}, fmt.Errorf("keitaro: id кампании должен быть больше нуля")
	}
	snap, err := c.fetchSnapshot(ctx, func(camps []Campaign) ([]Campaign, error) {
		camp, ok := findCampaign(camps, id)
		if !ok {
			return nil, fmt.Errorf("%w: %d", ErrCampaignNotFound, id)
		}
		return []Campaign{camp}, nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return snap.ForCampaign(id), nil
}

// FetchCampaignsSnapshot читает один снимок для досинка нескольких кампаний.
//
// Параметры: ctx — отмена; ids — id кампаний Keitaro.
// Результат: снимок, как у FetchSnapshot, но потоки только у тех ids, что есть в трекере, и без пользователей.
// Снимок одной кампании из него даёт ForCampaign. id, которого нет в трекере, остаётся без потоков.
// Ошибки: пустой ids или id не больше нуля, ошибки FetchSnapshot.
func (c *Client) FetchCampaignsSnapshot(ctx context.Context, ids []int) (Snapshot, error) {
	want := map[int]bool{}
	for _, id := range ids {
		if id <= 0 {
			return Snapshot{}, fmt.Errorf("keitaro: id кампании должен быть больше нуля")
		}
		want[id] = true
	}
	if len(want) == 0 {
		return Snapshot{}, fmt.Errorf("keitaro: не заданы id кампаний")
	}
	return c.fetchSnapshot(ctx, func(camps []Campaign) ([]Campaign, error) {
		var out []Campaign
		for _, camp := range camps {
			if want[int(camp.ID)] {
				out = append(out, camp)
			}
		}
		return out, nil
	})
}

// fetchSnapshot читает снимок для полного переноса или для выбранных кампаний.
//
// Параметры: ctx — отмена; pick — выбирает из списка трекера кампании, чьи потоки читать; nil читает все.
// Результат: снимок. При заданном pick потоки читаются только у выбранных кампаний, пользователи не читаются.
// Ошибки: как у FetchSnapshot и ошибка pick.
func (c *Client) fetchSnapshot(ctx context.Context, pick func([]Campaign) ([]Campaign, error)) (Snapshot, error) {
	c.report("snapshot", 0, 0)
	var snap Snapshot
	for _, path := range []string{
		"/admin_api/v1/stream_schemas",
		"/admin_api/v1/stream_types",
		"/admin_api/v1/stream_filters",
		"/admin_api/v1/streams_actions",
	} {
		if _, err := c.Get(ctx, path); err != nil {
			return Snapshot{}, err
		}
	}
	var err error
	snap.Campaigns, err = c.FetchCampaigns(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	targets := snap.Campaigns
	if pick != nil {
		if targets, err = pick(snap.Campaigns); err != nil {
			return Snapshot{}, err
		}
	}
	streams, err := c.fetchCampaignStreams(ctx, targets)
	if err != nil {
		return Snapshot{}, err
	}
	snap.Streams = streams
	if err := c.getArray(ctx, "/admin_api/v1/offers", &snap.Offers); err != nil {
		return Snapshot{}, err
	}
	if err := c.getArray(ctx, "/admin_api/v1/landing_pages", &snap.Landings); err != nil {
		return Snapshot{}, err
	}
	if err := c.getOptionalArray(ctx, "/admin_api/v1/affiliate_networks", &snap.AffiliateNetworks); err != nil {
		return Snapshot{}, err
	}
	if err := c.getOptionalArray(ctx, "/admin_api/v1/traffic_sources", &snap.TrafficSources); err != nil {
		return Snapshot{}, err
	}
	if err := c.getOptionalArray(ctx, "/admin_api/v1/domains", &snap.Domains); err != nil {
		return Snapshot{}, err
	}
	for _, kind := range []string{"campaigns", "offers", "landings", "domains"} {
		var part []KGroup
		if err := c.getOptionalArray(ctx, "/admin_api/v1/groups?type="+kind, &part); err != nil {
			return Snapshot{}, err
		}
		for i := range part {
			if strings.TrimSpace(part[i].Type) == "" {
				part[i].Type = kind
			}
		}
		snap.Groups = append(snap.Groups, part...)
	}
	if pick != nil {
		return snap, nil
	}
	ok, err := c.getOptionalArrayOK(ctx, "/admin_api/v1/users", &snap.Users)
	if err != nil {
		return Snapshot{}, err
	}
	snap.UsersDenied = !ok
	return snap, nil
}

// fetchCampaignStreams читает потоки кампаний пулом из streamFetchParallel.
//
// Параметры: ctx — отмена; camps — кампании снимка.
// Результат: карта id кампании → потоки. Порядок ключей не задан.
// Ошибки: сеть, статус не 200, тело не массив. Первая ошибка отменяет остальные запросы.
func (c *Client) fetchCampaignStreams(ctx context.Context, camps []Campaign) (map[string][]KStream, error) {
	out := make(map[string][]KStream, len(camps))
	if len(camps) == 0 {
		return out, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, streamFetchParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	done := 0
	total := len(camps)
	for _, camp := range camps {
		if ctx.Err() != nil {
			break
		}
		id := int(camp.ID)
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			path := fmt.Sprintf("/admin_api/v1/campaigns/%d/streams", id)
			body, err := c.Get(ctx, path)
			var streams []KStream
			if err == nil && json.Unmarshal(body, &streams) != nil {
				err = fmt.Errorf("keitaro: streams кампании %d не массив", id)
			}
			mu.Lock()
			if err != nil {
				if first == nil {
					first = err
					cancel()
				}
				mu.Unlock()
				return
			}
			if first != nil {
				mu.Unlock()
				return
			}
			out[fmt.Sprint(id)] = streams
			done++
			n := done
			mu.Unlock()
			c.report("streams", n, total)
		}()
	}
	wg.Wait()
	if first != nil {
		return nil, first
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("keitaro: чтение потоков прервано")
	}
	return out, nil
}

// report отдаёт ход чтения, если обработчик задан.
//
// Параметры: stage — этап; done и total — прогресс.
// Результат: нет. nil Progress ничего не делает.
// Ошибки: нет.
func (c *Client) report(stage string, done, total int) {
	if c != nil && c.Progress != nil {
		c.Progress(stage, done, total)
	}
}

// postLog отправляет POST в журнал Keitaro.
//
// Параметры: ctx — отмена; path — путь журнала; body — JSON-тело.
// Результат: тело ответа при статусе 200.
// Ошибки: путь вне AllowLog до обращения к сети, сеть, статус не 200. Ключ в текст не входит.
func (c *Client) postLog(ctx context.Context, path string, body any) ([]byte, error) {
	code, buf, err := c.request(ctx, http.MethodPost, path, body, AllowLog)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, &httpStatusError{path: path, code: code}
	}
	return buf, nil
}

// postLogPage читает одну страницу журнала и повторяет её после временного сбоя.
// Повтор безопасен: строки страницы отдаются обработчику только после полного разбора ответа.
//
// Параметры: ctx — отмена, прерывает и паузу между повторами; path — путь журнала; body — JSON-тело.
// Результат: тело ответа при статусе 200.
// Ошибки: как у postLog. Временный сбой — после последнего повтора из logRetryDelays, остальные сразу.
func (c *Client) postLogPage(ctx context.Context, path string, body any) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		raw, err := c.postLog(ctx, path, body)
		if err == nil || attempt >= len(logRetryDelays) || ctx.Err() != nil || !logRetryable(err) {
			return raw, err
		}
		pause := time.NewTimer(logRetryDelays[attempt])
		select {
		case <-ctx.Done():
			pause.Stop()
			return nil, err
		case <-pause.C:
		}
	}
}

// logRetryable сообщает, что сбой страницы журнала временный и страницу стоит запросить снова.
//
// Параметры: err — ошибка postLog, не nil.
// Результат: true для сети и оборванного тела, 429, 502, 503, 504 и 520–524 Cloudflare.
// Остальные статусы, в том числе отказ в колонках, повтора не получают.
// Ошибки: нет.
func logRetryable(err error) bool {
	var te *transportError
	if errors.As(err, &te) {
		return true
	}
	var se *httpStatusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return se.code >= 520 && se.code <= 524
}

// request выполняет один запрос после проверки allow.
//
// Параметры: ctx — отмена; method и path — запрос; body — JSON или nil; allow — проверка пути.
// Результат: код статуса и тело. Тело обрезано пределом.
// Ошибки: отказ allow до транспорта, клиент не задан, сеть, ответ больше предела.
func (c *Client) request(ctx context.Context, method, path string, body any, allow func(string, string) error) (int, []byte, error) {
	if err := allow(method, path); err != nil {
		return 0, nil, err
	}
	if c.HTTP == nil {
		return 0, nil, fmt.Errorf("keitaro: HTTP-клиент не задан")
	}
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("keitaro: тело запроса не собрано")
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
	if err != nil {
		return 0, nil, fmt.Errorf("keitaro: запрос не собран: %s", path)
	}
	req.Header.Set("Api-Key", c.Key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		if inner := errors.Unwrap(err); inner != nil {
			err = inner
		}
		return 0, nil, &transportError{msg: fmt.Sprintf("keitaro: %s %s не выполнен", method, path), cause: err}
	}
	defer res.Body.Close()
	limit := int64(getBodyLimit) + 1
	if method == http.MethodPost {
		limit = int64(logBodyLimit) + 1
	}
	buf, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return res.StatusCode, nil, &transportError{msg: fmt.Sprintf("keitaro: чтение %s", path), cause: err}
	}
	if int64(len(buf)) >= limit {
		return res.StatusCode, nil, fmt.Errorf("keitaro: ответ %s больше предела", path)
	}
	return res.StatusCode, buf, nil
}

// getArray читает JSON-массив по allowlist-пути.
//
// Параметры: ctx — отмена; path — путь; dest — указатель на срез.
// Результат: dest заполнен.
// Ошибки: сеть, статус не 200 или тело не массив.
func (c *Client) getArray(ctx context.Context, path string, dest any) error {
	body, err := c.Get(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("keitaro: %s не массив", strings.TrimPrefix(path, "/admin_api/v1/"))
	}
	return nil
}

// getOptionalArray читает справочник, которого может не быть в редакции Keitaro.
//
// Параметры: ctx — отмена; path — путь; dest — указатель на срез.
// Результат: при 200 dest заполнен. При любом другом статусе dest не меняется.
// Ошибки: путь вне allowlist, сеть или 200 с телом не массивом.
func (c *Client) getOptionalArray(ctx context.Context, path string, dest any) error {
	ok, err := c.getOptionalArrayOK(ctx, path, dest)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return nil
}

// getOptionalArrayOK читает справочник и сообщает, был ли ответ 200.
//
// Параметры: ctx — отмена; path — путь; dest — указатель на срез.
// Результат: ok true при 200 и успешном разборе массива; dest заполнен только тогда.
// Ошибки: путь вне allowlist, сеть или 200 с телом не массивом.
func (c *Client) getOptionalArrayOK(ctx context.Context, path string, dest any) (ok bool, err error) {
	code, body, err := c.request(ctx, http.MethodGet, path, nil, Allow)
	if err != nil {
		return false, err
	}
	if code != http.StatusOK {
		return false, nil
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return false, fmt.Errorf("keitaro: %s не массив", strings.TrimPrefix(path, "/admin_api/v1/"))
	}
	return true, nil
}

// logColumns добавляет sub_id_1..sub_id_30 к списку колонок журнала.
//
// Параметры: base — колонки без sub id.
// Результат: новый срез, base не меняется.
// Ошибки: нет.
func logColumns(base []string) []string {
	out := append([]string{}, base...)
	for i := 1; i <= 30; i++ {
		out = append(out, fmt.Sprintf("sub_id_%d", i))
	}
	return out
}

// fetchRows листает журнал до пустой страницы или пока смещение не дошло до total.
//
// Параметры: path — журнал; from и to — границы; columns — колонки; stage — имя прогресса; limit — размер страницы; sortBy — колонка сортировки по возрастанию; fn — обработчик строки.
// Результат: число строк, отданных в fn. Смещение растёт на число строк страницы. total 0 или его отсутствие останавливает чтение на короткой странице.
// Ошибки: сеть, статус не 200, страница не JSON, строка не разобрана, ошибка fn, две одинаковые страницы подряд.
func fetchRows[T any](c *Client, ctx context.Context, path, from, to string, columns []string, stage string, limit int, sortBy string, fn func(T) error) (int, error) {
	offset := 0
	seen := 0
	var prevID string
	var prevN int
	havePrev := false
	for {
		payload := map[string]any{
			"range":   map[string]string{"from": from, "to": to, "timezone": "UTC"},
			"limit":   limit,
			"offset":  offset,
			"columns": columns,
			"sort":    []map[string]string{{"name": sortBy, "order": "asc"}},
		}
		raw, err := c.postLogPage(ctx, path, payload)
		if err != nil {
			return seen, err
		}
		var box struct {
			Rows  []json.RawMessage `json:"rows"`
			Total FlexInt           `json:"total"`
		}
		if err := json.Unmarshal(raw, &box); err != nil {
			return seen, fmt.Errorf("keitaro: журнал не JSON")
		}
		n := len(box.Rows)
		if n == 0 {
			c.report(stage, seen, int(box.Total))
			return seen, nil
		}
		first := rowSubID(box.Rows[0])
		if havePrev && first == prevID && n == prevN {
			return seen, fmt.Errorf("журнал не двигается")
		}
		prevID = first
		prevN = n
		havePrev = true
		for _, item := range box.Rows {
			var row T
			if err := json.Unmarshal(item, &row); err != nil {
				return seen, fmt.Errorf("keitaro: строка журнала не JSON")
			}
			if err := fn(row); err != nil {
				return seen, err
			}
			seen++
		}
		offset += n
		total := int(box.Total)
		c.report(stage, seen, total)
		if total > 0 {
			if offset >= total {
				return seen, nil
			}
			continue
		}
		if n < limit {
			return seen, nil
		}
	}
}

// rowSubID читает sub_id строки журнала, чтобы заметить повтор страницы.
//
// Параметры: raw — JSON одной строки.
// Результат: sub_id. Пустая строка, если поля нет или JSON битый.
// Ошибки: нет.
func rowSubID(raw json.RawMessage) string {
	var head struct {
		SubID string `json:"sub_id"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return ""
	}
	return head.SubID
}
