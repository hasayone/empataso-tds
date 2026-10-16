package ktimport

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// FetchLandingArchive читает архив локального лендинга.
//
// Параметры: ctx — отмена; id — числовой id лендинга Keitaro.
// Результат: сырые байты тела при статусе 200. JSON не разбирается. Предел тела 64 МБ.
// Ошибки: путь вне allowlist, клиент не задан, сеть, статус не 200, тело больше предела. Ключ в текст ошибки не входит.
func (c *Client) FetchLandingArchive(ctx context.Context, id int) ([]byte, error) {
	path := fmt.Sprintf("/admin_api/v1/landing_pages/%d/download", id)
	if err := Allow(http.MethodGet, path); err != nil {
		return nil, err
	}
	if c.HTTP == nil {
		return nil, fmt.Errorf("keitaro: HTTP-клиент не задан")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("keitaro: запрос не собран: %s", path)
	}
	req.Header.Set("Api-Key", c.Key)
	req.Header.Set("Accept", "*/*")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keitaro: GET %s не выполнен", path)
	}
	defer res.Body.Close()
	limit := int64(getBodyLimit) + 1
	buf, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("keitaro: чтение %s", path)
	}
	if int64(len(buf)) >= limit {
		return nil, fmt.Errorf("keitaro: ответ %s больше предела", path)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keitaro: GET %s вернул %d", path, res.StatusCode)
	}
	return buf, nil
}
