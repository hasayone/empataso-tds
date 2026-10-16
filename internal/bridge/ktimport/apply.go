package ktimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
)

// Admin пишет группы только в локальную kuztds.
type Admin struct {
	Base string
	User string
	Pass string
	HTTP *http.Client
}

// Apply сливает группы, пишет их одним PUT и затем каталоги по одной записи.
//
// Параметры: ctx — отмена; incoming — результат маппера.
// Результат: тот же результат с группами после слияния и строки конфликтов.
// Ошибки: логин, чтение, запись, проверка перед PUT. Пароль в текст ошибки не входит.
// Сети пишутся раньше офферов. Чужой id не затирается и не удаляется.
func (a *Admin) Apply(ctx context.Context, incoming Result) (Result, []Loss, error) {
	if err := a.login(ctx); err != nil {
		return Result{}, nil, err
	}
	existing, err := a.getGroups(ctx)
	if err != nil {
		return Result{}, nil, err
	}
	merged, losses := Merge(existing, incoming.Groups, incoming.Manifest.Imported(KindGroups))
	if err := Validate(merged, map[string]bool{}); err != nil {
		return Result{}, losses, err
	}
	if err := a.putGroups(ctx, merged); err != nil {
		return Result{}, losses, err
	}
	out := incoming
	out.Groups = merged
	if err := a.writeCatalogs(ctx, incoming); err != nil {
		return out, losses, err
	}
	return out, losses, nil
}

// writeCatalogs пишет справочники в админку в порядке зависимостей.
//
// Параметры: ctx — отмена; incoming — каталоги маппера.
// Результат: nil, если каждая запись принята.
// Ошибки: чтение списка или отказ POST/PUT. Пути — CRUD админки kuztds.
func (a *Admin) writeCatalogs(ctx context.Context, incoming Result) error {
	m := incoming.Manifest
	if err := applyCatalog(a, ctx, "/api/affiliate-networks", incoming.Networks, func(v CatNetwork) string { return v.ID }, m.Imported(KindNetworks)); err != nil {
		return err
	}
	if err := applyCatalog(a, ctx, "/api/traffic-sources", incoming.Sources, func(v CatSource) string { return v.ID }, m.Imported(KindSources)); err != nil {
		return err
	}
	if err := applyCatalog(a, ctx, "/api/domains", incoming.Domains, func(v CatDomain) string { return v.ID }, m.Imported(KindDomains)); err != nil {
		return err
	}
	if err := applyCatalog(a, ctx, "/api/landings", incoming.Landings, func(v CatLanding) string { return v.ID }, m.Imported(KindLandings)); err != nil {
		return err
	}
	return applyCatalog(a, ctx, "/api/offers", incoming.Offers, func(v CatOffer) string { return v.ID }, m.Imported(KindOffers))
}

// applyCatalog сливает список и пишет новые записи через POST, id из манифеста через PUT.
//
// Параметры: path — коллекция админки; incoming — записи маппера; idOf — достаёт id; imported — true для id этого прогона.
// Результат: nil после успешной записи всех id из манифеста.
// Ошибки: чтение списка, сеть, статус не 200 и не 201.
func applyCatalog[T any](a *Admin, ctx context.Context, path string, incoming []T, idOf func(T) string, imported func(id string) bool) error {
	existing, err := getCatalog[T](a, ctx, path)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, item := range existing {
		have[idOf(item)] = true
	}
	incomingByID := map[string]T{}
	for _, item := range incoming {
		incomingByID[idOf(item)] = item
	}
	for _, item := range MergeByID(existing, incoming, idOf, imported) {
		id := idOf(item)
		next, ok := incomingByID[id]
		if !ok || imported == nil || !imported(id) {
			continue
		}
		if have[id] {
			if err := a.writeItem(ctx, http.MethodPut, path+"/"+url.PathEscape(id), next); err != nil {
				return err
			}
			continue
		}
		if err := a.writeItem(ctx, http.MethodPost, path, next); err != nil {
			return err
		}
	}
	return nil
}

// getCatalog читает JSON-массив коллекции админки.
//
// Параметры: path — коллекция.
// Результат: записи. null и пустое тело становятся пустым срезом.
// Ошибки: сеть, статус не 200, тело не массив.
func getCatalog[T any](a *Admin, ctx context.Context, path string) ([]T, error) {
	res, err := a.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("kuztds: чтение %s", path)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kuztds: GET %s вернул %d", path, res.StatusCode)
	}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return []T{}, nil
	}
	var list []T
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("kuztds: %s не JSON", path)
	}
	if list == nil {
		list = []T{}
	}
	return list, nil
}

// writeItem отправляет одну запись каталога.
//
// Параметры: ctx — отмена; method — POST или PUT; path — коллекция или объект; item — запись.
// Результат: nil при статусе 200 или 201.
// Ошибки: сеть или другой статус.
func (a *Admin) writeItem(ctx context.Context, method, path string, item any) error {
	body, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("kuztds: тело не собрано")
	}
	res, err := a.do(ctx, method, path, body, a.csrf())
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return fmt.Errorf("kuztds: %s %s вернул %d", method, path, res.StatusCode)
	}
	return nil
}

// login открывает сессию админки.
//
// Параметры: ctx — отмена.
// Результат: cookie сессии сохранена в клиенте.
// Ошибки: пустой пароль, сеть, статус не 200.
func (a *Admin) login(ctx context.Context) error {
	if strings.TrimSpace(a.Pass) == "" {
		return fmt.Errorf("kuztds: пустой пароль админки")
	}
	body, _ := json.Marshal(map[string]string{"login": a.User, "password": a.Pass})
	res, err := a.do(ctx, http.MethodPost, "/api/login", body, "")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("kuztds: логин вернул %d", res.StatusCode)
	}
	return nil
}

// getGroups читает текущие группы.
//
// Параметры: ctx — отмена.
// Результат: список групп. Пустой ответ становится пустым срезом.
// Ошибки: сеть или не JSON.
func (a *Admin) getGroups(ctx context.Context) ([]Group, error) {
	res, err := a.do(ctx, http.MethodGet, "/api/groups", nil, "")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kuztds: GET /api/groups вернул %d", res.StatusCode)
	}
	var groups []Group
	if err := json.NewDecoder(res.Body).Decode(&groups); err != nil {
		return nil, fmt.Errorf("kuztds: группы не JSON")
	}
	if groups == nil {
		groups = []Group{}
	}
	return groups, nil
}

// putGroups заменяет группы одним запросом.
//
// Параметры: ctx — отмена; groups — полный список.
// Результат: nil при статусе 200.
// Ошибки: сеть или статус не 200.
func (a *Admin) putGroups(ctx context.Context, groups []Group) error {
	body, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	res, err := a.do(ctx, http.MethodPut, "/api/groups", body, a.csrf())
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("kuztds: PUT /api/groups вернул %d", res.StatusCode)
	}
	return nil
}

// PutList пишет один .dat до групп.
//
// Параметры: ctx — отмена; name — имя файла; content — текст списка.
// Результат: nil при статусе 200.
// Ошибки: сеть или статус не 200.
func (a *Admin) PutList(ctx context.Context, name, content string) error {
	body, _ := json.Marshal(map[string]string{"content": content})
	res, err := a.do(ctx, http.MethodPut, "/api/lists/"+name, body, a.csrf())
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("kuztds: PUT /api/lists/%s вернул %d", name, res.StatusCode)
	}
	return nil
}

// Simulate прогоняет посетителя через уже собранные группы.
//
// Параметры: ctx — отмена; groups — группы; groupID — какую открыть; country — код страны.
// Результат: выбранный поток и текст выхода.
// Ошибки: сеть, статус не 200, ответ не JSON.
func (a *Admin) Simulate(ctx context.Context, groups []Group, groupID, country string) (selected, out string, err error) {
	if a.csrf() == "" {
		if err = a.login(ctx); err != nil {
			return "", "", err
		}
	}
	payload := map[string]any{
		"groups": groups,
		"group":  groupID,
		"visitor": map[string]any{
			"ip":        "8.8.8.8",
			"useragent": "Mozilla/5.0",
			"country":   country,
		},
	}
	body, _ := json.Marshal(payload)
	res, err := a.do(ctx, http.MethodPost, "/api/simulate", body, a.csrf())
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("kuztds: simulate вернул %d", res.StatusCode)
	}
	var parsed struct {
		Path []struct {
			Selected string `json:"selected"`
			Out      string `json:"out"`
		} `json:"path"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Path) == 0 {
		return "", "", fmt.Errorf("kuztds: simulate без path")
	}
	last := parsed.Path[len(parsed.Path)-1]
	return last.Selected, last.Out, nil
}

// csrf достаёт токен из ответа логина, сохранённого в заголовке клиента.
//
// Параметры: нет.
// Результат: значение X-CSRF-Token или пустая строка, если логин его не сохранил.
// Ошибки: нет.
func (a *Admin) csrf() string {
	return a.HTTP.Transport.(*csrfTransport).token
}

// do выполняет запрос к локальной админке.
//
// Параметры: ctx — отмена; method и path — запрос; body — JSON или nil; csrf — токен для небезопасных методов.
// Результат: ответ. Закрывать тело обязан вызывающий.
// Ошибки: клиент не задан или сеть.
func (a *Admin) do(ctx context.Context, method, path string, body []byte, csrf string) (*http.Response, error) {
	if a.HTTP == nil {
		return nil, fmt.Errorf("kuztds: HTTP-клиент не задан")
	}
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.Base, "/")+path, r)
	if err != nil {
		return nil, fmt.Errorf("kuztds: запрос не собран")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	res, err := a.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kuztds: %s %s не выполнен", method, path)
	}
	if path == "/api/login" && res.StatusCode == http.StatusOK {
		var box struct {
			CSRF string `json:"csrf"`
		}
		raw, _ := io.ReadAll(res.Body)
		_ = json.Unmarshal(raw, &box)
		res.Body = io.NopCloser(bytes.NewReader(raw))
		if tr, ok := a.HTTP.Transport.(*csrfTransport); ok {
			tr.token = box.CSRF
		}
	}
	return res, nil
}

// csrfTransport запоминает CSRF и проксирует запросы.
type csrfTransport struct {
	base  http.RoundTripper
	token string
}

// RoundTrip передаёт запрос базовому транспорту.
//
// Параметры: req — запрос.
// Результат: ответ транспорта.
// Ошибки: ошибка базового транспорта.
func (t *csrfTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req)
}

// NewAdmin собирает клиент с cookie и хранилищем CSRF.
//
// Параметры: base, user, pass — адрес и учётные данные локальной админки.
// Результат: клиент. Пароль только в памяти.
// Ошибки: нет.
func NewAdmin(base, user, pass string) *Admin {
	jar, _ := cookiejar.New(nil)
	tr := &csrfTransport{base: http.DefaultTransport}
	return &Admin{
		Base: base,
		User: user,
		Pass: pass,
		HTTP: &http.Client{Jar: jar, Transport: tr},
	}
}

// AdminFromEnv собирает клиент из переменных окружения.
//
// Параметры: нет.
// Результат: клиент локальной админки.
// Ошибки: пустой пароль.
func AdminFromEnv() (*Admin, error) {
	pass := os.Getenv("KUZTDS_ADMIN_PASSWORD")
	if strings.TrimSpace(pass) == "" {
		return nil, fmt.Errorf("kuztds: пустой KUZTDS_ADMIN_PASSWORD")
	}
	base := os.Getenv("KUZTDS_ADMIN_URL")
	if base == "" {
		base = "http://127.0.0.1:8090"
	}
	user := os.Getenv("KUZTDS_ADMIN_USER")
	if user == "" {
		user = "admin"
	}
	return NewAdmin(base, user, pass), nil
}
