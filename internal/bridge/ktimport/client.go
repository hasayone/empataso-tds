package ktimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Client ходит в Keitaro только по allowlist: GET конфигурации и два POST журнала.
// Progress, если не nil, получает этап, сколько уже сделано и сколько всего.
// Из пула потоков вызовы могут идти параллельно.
type Client struct {
	Base     string
	Key      string
	HTTP     *http.Client
	Progress func(stage string, done, total int)
	// extRejected — последнее чтение журнала откатилось на базовые колонки.
	extRejected bool
}

const (
	logPageLimit        = 10000
	logBodyLimit        = 32 << 20
	getBodyLimit        = 64 << 20
	streamFetchParallel = 8
)

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
	streams, err := c.fetchCampaignStreams(ctx, snap.Campaigns)
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
		return 0, nil, fmt.Errorf("keitaro: %s %s не выполнен", method, path)
	}
	defer res.Body.Close()
	limit := int64(getBodyLimit) + 1
	if method == http.MethodPost {
		limit = int64(logBodyLimit) + 1
	}
	buf, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return res.StatusCode, nil, fmt.Errorf("keitaro: чтение %s", path)
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

// logColumns добавляет sub_id_1..sub_id_12 к списку колонок журнала.
//
// Параметры: base — колонки без sub id.
// Результат: новый срез, base не меняется.
// Ошибки: нет.
func logColumns(base []string) []string {
	out := append([]string{}, base...)
	for i := 1; i <= 12; i++ {
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
		raw, err := c.postLog(ctx, path, payload)
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
