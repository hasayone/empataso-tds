package ktimport

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const botProtectionPrefix = "Bot Protection"
const closingStreamPrefix = "Замыкающий"

// BotProtectionStreamName сообщает, что имя потока — Bot Protection или «Замыкающий» в Keitaro или emtds.
//
// Параметры: name — имя потока.
// Результат: true, когда после обрезки пробелов имя без учёта регистра равно одному из этих имён
// или начинается с него, а следующий символ не буква и не цифра.
// Ошибки: нет.
func BotProtectionStreamName(name string) bool {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{botProtectionPrefix, closingStreamPrefix} {
		if streamNameMatchesPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// streamNameMatchesPrefix проверяет имя потока на точное совпадение с prefix или префикс с разделителем.
//
// Параметры: name — уже обрезанное имя; prefix — эталонное имя потока.
// Результат: true при EqualFold с prefix или при префиксе prefix, если следующий символ не буква и не цифра.
// Ошибки: нет.
func streamNameMatchesPrefix(name, prefix string) bool {
	if strings.EqualFold(name, prefix) {
		return true
	}
	plen := len(prefix)
	if len(name) <= plen {
		return false
	}
	if !strings.EqualFold(name[:plen], prefix) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name[plen:])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}
