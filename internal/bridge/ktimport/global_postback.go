package ktimport

import (
	"crypto/sha1"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
)

// Signature собирает подпись шаблона для сравнения кампаний и каталога.
//
// Параметры: получатель — шаблон кампании или каталога.
// Результат: метод, URL и статусы через перевод строки. Метод в верхнем регистре, пустой становится GET. Статусы обрезаны, в нижнем регистре, без пустых, по алфавиту, через запятую.
// Ошибки: нет.
func (p Postback) Signature() string {
	method, rawURL, statuses := normalizeParts(p)
	return method + "\n" + rawURL + "\n" + strings.Join(statuses, ",")
}

// normalizeParts приводит метод, URL и статусы к виду подписи.
//
// Параметры: p — шаблон.
// Результат: метод, URL и статусы. Пустой список статусов — nil.
// Ошибки: нет.
func normalizeParts(p Postback) (string, string, []string) {
	method := strings.ToUpper(strings.TrimSpace(p.Method))
	if method == "" {
		method = "GET"
	}
	var statuses []string
	for _, s := range p.Statuses {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		statuses = append(statuses, s)
	}
	sort.Strings(statuses)
	return method, strings.TrimSpace(p.URL), statuses
}

// PostbackID считает идентификатор общего шаблона.
//
// Параметры: sig — строка Signature.
// Результат: pb- и первые 10 hex-символов sha1 подписи.
// Ошибки: нет.
func PostbackID(sig string) string {
	sum := sha1.Sum([]byte(sig))
	return "pb-" + hex.EncodeToString(sum[:])[:10]
}

// ExtractGlobalPostback выносит самый общий шаблон из кампаний в каталог s2s.
//
// Параметры: groups — результат маппера; existing — текущий каталог, можно nil.
// Результат: общий шаблон или nil, если постбеков не было, и копия групп. У кампаний с выбранной подписью инлайн-копии сняты, id добавлен в PostbackIDs без повтора.
// Ошибки: нет. Входной срез не меняется.
func ExtractGlobalPostback(groups []Group, existing []Postback) (*Postback, []Group) {
	counts := countSignatures(groups)
	if len(counts) == 0 {
		return nil, copyGroups(groups)
	}
	sig := pickSignature(counts)
	pb := globalPostback(sig, groups, existing)
	return &pb, bindGlobal(groups, sig, pb.ID)
}

// countSignatures считает, в скольких кампаниях встретилась подпись.
//
// Параметры: groups — группы маппера.
// Результат: подпись → число кампаний. Повтор внутри одной кампании даёт +1 один раз.
// Ошибки: нет.
func countSignatures(groups []Group) map[string]int {
	counts := make(map[string]int)
	for _, g := range groups {
		seen := make(map[string]struct{})
		for _, p := range g.Postbacks {
			sig := p.Signature()
			if _, ok := seen[sig]; ok {
				continue
			}
			seen[sig] = struct{}{}
			counts[sig]++
		}
	}
	return counts
}

// pickSignature выбирает подпись общего шаблона.
//
// Параметры: counts — подпись → число кампаний.
// Результат: самая частая подпись. При равном счёте — меньшая строка, не порядок входа.
// Ошибки: нет. Пустая карта даёт пустую строку.
func pickSignature(counts map[string]int) string {
	best := ""
	bestN := 0
	first := true
	for sig, n := range counts {
		if first || n > bestN || (n == bestN && sig < best) {
			best = sig
			bestN = n
			first = false
		}
	}
	return best
}

// globalPostback собирает запись каталога для выбранной подписи.
//
// Параметры: sig — выбранная подпись; groups — группы с образцом; existing — каталог.
// Результат: копия записи existing с той же подписью либо новый шаблон с id PostbackID.
// Ошибки: нет.
func globalPostback(sig string, groups []Group, existing []Postback) Postback {
	if prev, ok := findExisting(existing, sig); ok {
		return clonePostback(prev)
	}
	sample := normalizeSample(groups, sig)
	sample.ID = PostbackID(sig)
	sample.Name = keitaroPostbackName(sample.URL)
	return sample
}

// findExisting ищет в каталоге шаблон с той же подписью.
//
// Параметры: existing — каталог; sig — подпись.
// Результат: первая подходящая запись и true. Иначе нулевое значение и false.
// Ошибки: нет.
func findExisting(existing []Postback, sig string) (Postback, bool) {
	for _, p := range existing {
		if p.Signature() == sig {
			return p, true
		}
	}
	return Postback{}, false
}

// normalizeSample берёт первый шаблон кампании с этой подписью и нормализует его.
//
// Параметры: groups — группы; sig — подпись.
// Результат: метод, URL и статусы как в подписи. ID и Name пустые.
// Ошибки: нет. Если образца нет, метод GET и остальные поля пустые.
func normalizeSample(groups []Group, sig string) Postback {
	for _, g := range groups {
		for _, p := range g.Postbacks {
			if p.Signature() != sig {
				continue
			}
			method, rawURL, statuses := normalizeParts(p)
			return Postback{Method: method, URL: rawURL, Statuses: statuses}
		}
	}
	return Postback{Method: "GET"}
}

// keitaroPostbackName собирает имя общего шаблона по хосту URL.
//
// Параметры: rawURL — URL шаблона.
// Результат: Keitaro и хост через пробел. Без хоста — Keitaro.
// Ошибки: нет.
func keitaroPostbackName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return "Keitaro"
	}
	return "Keitaro " + u.Hostname()
}

// bindGlobal копирует группы и привязывает общий id к кампаниям с подписью.
//
// Параметры: groups — вход; sig — подпись; id — id каталога.
// Результат: новая копия. Инлайн-копии подписи удалены, пустой Postbacks — nil. id добавлен в PostbackIDs один раз.
// Ошибки: нет.
func bindGlobal(groups []Group, sig, id string) []Group {
	if groups == nil {
		return nil
	}
	out := make([]Group, len(groups))
	for i, g := range groups {
		out[i] = g
		if !hasSignature(g.Postbacks, sig) {
			continue
		}
		out[i].Postbacks = dropSignature(g.Postbacks, sig)
		out[i].PostbackIDs = addPostbackID(g.PostbackIDs, id)
	}
	return out
}

// copyGroups копирует срез групп, не трогая вложенные срезы.
//
// Параметры: groups — вход, можно nil.
// Результат: новый срез той же длины или nil.
// Ошибки: нет.
func copyGroups(groups []Group) []Group {
	if groups == nil {
		return nil
	}
	out := make([]Group, len(groups))
	copy(out, groups)
	return out
}

// hasSignature сообщает, есть ли в кампании шаблон с подписью.
//
// Параметры: posts — шаблоны кампании; sig — подпись.
// Результат: true, если хотя бы один шаблон совпал.
// Ошибки: нет.
func hasSignature(posts []Postback, sig string) bool {
	for _, p := range posts {
		if p.Signature() == sig {
			return true
		}
	}
	return false
}

// dropSignature убирает шаблоны с подписью.
//
// Параметры: posts — шаблоны; sig — подпись.
// Результат: остальные шаблоны в прежнем порядке. Пустой результат — nil.
// Ошибки: нет.
func dropSignature(posts []Postback, sig string) []Postback {
	var kept []Postback
	for _, p := range posts {
		if p.Signature() == sig {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// addPostbackID добавляет id в конец списка без повтора.
//
// Параметры: ids — текущие id; id — добавляемый.
// Результат: новый срез. Если id уже есть, состав прежний.
// Ошибки: нет.
func addPostbackID(ids []string, id string) []string {
	for _, cur := range ids {
		if cur == id {
			return append([]string(nil), ids...)
		}
	}
	out := make([]string, len(ids)+1)
	copy(out, ids)
	out[len(ids)] = id
	return out
}

// clonePostback копирует шаблон и его статусы.
//
// Параметры: p — запись каталога.
// Результат: копия. nil-статусы остаются nil.
// Ошибки: нет.
func clonePostback(p Postback) Postback {
	if p.Statuses != nil {
		p.Statuses = append([]string(nil), p.Statuses...)
	}
	return p
}
