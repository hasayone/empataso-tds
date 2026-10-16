package ktimport

import (
	"context"
	"errors"
	"fmt"
)

// conversionLogColumns — базовые колонки журнала конверсий, без sub_id.
var conversionLogColumns = []string{
	"sub_id", "status", "revenue", "cost", "campaign_id", "campaign",
	"stream_id", "stream", "offer_id", "landing_id", "affiliate_network_id", "ts_id",
	"country_code", "city", "region", "device_type", "os", "browser", "operator",
	"ip", "user_agent", "postback_datetime", "click_datetime",
}

// conversionLogExtra — колонки, которых может не быть в старой редакции.
var conversionLogExtra = []string{
	"previous_status", "original_status", "tid", "language", "os_version", "browser_version",
	"device_model", "referrer", "search_engine", "keyword", "isp",
	"ad_campaign_id", "external_id", "creative_id",
}

// clickLogColumns — базовые колонки журнала кликов, без sub_id.
var clickLogColumns = []string{
	"sub_id", "datetime", "campaign_id", "campaign", "stream_id", "stream",
	"offer_id", "landing_id", "affiliate_network_id", "ts_id", "country_code",
	"region", "city", "language", "os", "os_version", "browser", "browser_version",
	"device_type", "device_model", "operator", "is_bot", "is_unique_campaign",
	"ip", "referrer", "search_engine", "keyword", "user_agent", "destination",
	"cost", "revenue",
}

// clickLogExtra — колонки клика, которых может не быть в старой редакции.
var clickLogExtra = []string{"ad_campaign_id", "external_id", "creative_id"}

// FetchConversions читает журнал конверсий постранично и отдаёт строки в fn.
//
// Параметры: ctx — отмена; from и to — границы range в UTC; fn — обработчик одной строки.
// Результат: сколько строк передано в fn. Если расширенный набор получил статус не 200
// и ни одна строка ещё не отдана, чтение повторяется базовым набором.
// Ошибки: путь вне AllowLog, сеть, статус не 200 уже на базовом наборе, строка не JSON, ошибка fn, журнал не двигается.
func (c *Client) FetchConversions(ctx context.Context, from, to string, fn func(ConversionRow) error) (int, error) {
	return fetchLog(c, ctx, "/admin_api/v1/conversions/log", from, to, conversionLogColumns, conversionLogExtra, "conversions", "postback_datetime", fn)
}

// FetchClicks читает журнал кликов постранично и отдаёт строки в fn.
//
// Параметры: ctx — отмена; from и to — границы range в UTC; fn — обработчик одной строки.
// Результат: сколько строк передано в fn. Откат на базовые колонки такой же, как у конверсий.
// Ошибки: путь вне AllowLog, сеть, статус не 200 уже на базовом наборе, строка не JSON, ошибка fn, журнал не двигается.
func (c *Client) FetchClicks(ctx context.Context, from, to string, fn func(ClickRow) error) (int, error) {
	return fetchLog(c, ctx, "/admin_api/v1/clicks/log", from, to, clickLogColumns, clickLogExtra, "clicks", "datetime", fn)
}

// ExtendedColumnsRejected сообщает, что последнее чтение журнала откатилось на базовые колонки.
//
// Параметры: нет.
// Результат: true после отката. До чтения и после принятого расширенного набора — false.
// Ошибки: нет. nil-клиент даёт false.
func (c *Client) ExtendedColumnsRejected() bool {
	if c == nil {
		return false
	}
	return c.extRejected
}

// fetchLog читает журнал расширенным набором и один раз откатывается на базовый.
//
// Параметры: path — журнал; from и to — границы; base — колонки без sub id; extra — добавка;
// stage — имя прогресса; sortBy — сортировка; fn — обработчик строки.
// Результат: число строк. Флаг отката сбрасывается в начале и ставится только перед повтором.
// Ошибки: те же, что у fetchRows. Повтор бывает, когда статус не 200 и ни одна строка не отдана.
func fetchLog[T any](c *Client, ctx context.Context, path, from, to string, base, extra []string, stage, sortBy string, fn func(T) error) (int, error) {
	if c != nil {
		c.extRejected = false
	}
	n, err := fetchRows(c, ctx, path, from, to, logColumns(joinCols(base, extra)), stage, logPageLimit, sortBy, fn)
	if n != 0 || !statusRejected(err) {
		return n, err
	}
	if c != nil {
		c.extRejected = true
	}
	return fetchRows(c, ctx, path, from, to, logColumns(append([]string{}, base...)), stage, logPageLimit, sortBy, fn)
}

// joinCols склеивает два списка колонок в новый срез.
//
// Параметры: base и extra — части. Исходные срезы не меняются.
// Результат: base, затем extra.
// Ошибки: нет.
func joinCols(base, extra []string) []string {
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	return append(out, extra...)
}

// httpStatusError — ответ журнала со статусом не 200.
type httpStatusError struct {
	path string
	code int
}

// Error возвращает текст статуса без ключа API.
//
// Параметры: нет.
// Результат: путь и код. nil даёт пустую строку.
// Ошибки: нет.
func (e *httpStatusError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("keitaro: POST %s вернул %d", e.path, e.code)
}

// statusRejected сообщает, что ошибка — это статус журнала не 200.
//
// Параметры: err — ошибка postLog. nil даёт false.
// Результат: true только для httpStatusError.
// Ошибки: нет.
func statusRejected(err error) bool {
	var se *httpStatusError
	return errors.As(err, &se)
}
