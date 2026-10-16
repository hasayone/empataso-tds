package ktimport

import (
	"crypto/sha1"
	"encoding/hex"
	"net/netip"
	"sort"
	"strings"
)

// listBag — имена файлов .dat и их текст. Одинаковый текст делит одно имя.
type listBag map[string]string

// put записывает список IP.
//
// Параметры: name — имя файла; content — текст списка.
// Результат: запись в мешке. Повтор того же имени оставляет тот же текст.
// Ошибки: нет. nil-мешок ничего не пишет.
func (b *listBag) put(name, content string) {
	if b == nil {
		return
	}
	if *b == nil {
		*b = listBag{}
	}
	(*b)[name] = content
}

// IPListName собирает имя файла списка по его тексту.
//
// Параметры: content — текст списка, включая перевод строки в конце.
// Результат: ip-, первые 12 hex sha1 текста и суффикс .dat.
// Ошибки: нет.
func IPListName(content string) string {
	sum := sha1.Sum([]byte(content))
	return "ip-" + hex.EncodeToString(sum[:])[:12] + ".dat"
}

// ipFilter переносит фильтр ip в файл списка и правило ip_list.
//
// Параметры: f — фильтр; lists — мешок файлов этого прогона.
// Результат: правило с флагом 2 для accept и 1 для reject. Негодные строки отбрасываются.
// Ошибки: нет. Если годных строк нет, фильтр не переносится.
func ipFilter(f KFilter, lists *listBag) (Rules, bool, bool, string) {
	if emptyPayload(f.Payload) {
		return Rules{}, true, false, ""
	}
	values, mode, _, ok := readPayload(f.Payload)
	if !ok {
		return Rules{}, false, false, "фильтр ip не разобран"
	}
	if mode == "" {
		mode = f.Mode
	}
	var good, bad []string
	seen := map[string]bool{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if !validIPEntry(v) {
			bad = append(bad, v)
			continue
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		good = append(good, v)
	}
	if len(good) == 0 {
		return Rules{}, false, false, "фильтр ip не разобран"
	}
	sort.Strings(good)
	content := strings.Join(good, "\n") + "\n"
	name := IPListName(content)
	lists.put(name, content)
	flag := 2
	if isReject(mode) {
		flag = 1
	}
	why := ""
	approx := false
	if len(bad) > 0 {
		approx = true
		bad = unique(bad)
		if len(bad) > 3 {
			bad = bad[:3]
		}
		why = "фильтр ip отбросил негодные записи: " + strings.Join(bad, ", ")
	}
	return Rules{IPList: IPListFilter{Flag: flag, File: name}}, true, approx, why
}

// validIPEntry проверяет одну строку списка IP.
//
// Параметры: s — строка без краевых пробелов.
// Результат: true для адреса, префикса или диапазона из двух адресов.
// Ошибки: нет.
func validIPEntry(s string) bool {
	if strings.Contains(s, "/") {
		_, err := netip.ParsePrefix(s)
		return err == nil
	}
	if strings.Contains(s, "-") {
		parts := strings.SplitN(s, "-", 2)
		a, err1 := netip.ParseAddr(strings.TrimSpace(parts[0]))
		b, err2 := netip.ParseAddr(strings.TrimSpace(parts[1]))
		return err1 == nil && err2 == nil && a.IsValid() && b.IsValid()
	}
	addr, err := netip.ParseAddr(s)
	return err == nil && addr.IsValid()
}
