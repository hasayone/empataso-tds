package ktimport

import (
	"crypto/sha1"
	"encoding/hex"
)

// pageInline — максимум байт текста, который остаётся прямо в out.
const pageInline = 512

// pageBag — имена файлов страниц и их HTML. Одинаковый текст делит одно имя.
type pageBag map[string]string

// put записывает страницу.
//
// Параметры: name — имя файла; content — HTML или текст после замены макросов.
// Результат: запись в мешке. Повтор того же имени оставляет тот же текст.
// Ошибки: нет. nil-мешок ничего не пишет.
func (b *pageBag) put(name, content string) {
	if b == nil {
		return
	}
	if *b == nil {
		*b = pageBag{}
	}
	(*b)[name] = content
}

// PageName собирает имя файла страницы по её тексту.
//
// Параметры: content — текст после замены макросов.
// Результат: page-, первые 12 hex sha1 текста и суффикс .html.
// Ошибки: нет.
func PageName(content string) string {
	sum := sha1.Sum([]byte(content))
	return "page-" + hex.EncodeToString(sum[:])[:12] + ".html"
}

// pageRef заменяет длинный текст страницы ссылкой на общий файл.
//
// Параметры: redirect — тип выхода kuztds; content — текст после замены макросов; pages — мешок этого прогона.
// Результат: исходный текст или @page: и имя файла. Короче 513 байт и чужой тип остаются как есть.
// Ошибки: нет.
func pageRef(redirect, content string, pages *pageBag) string {
	if !textPage(redirect) || len(content) <= pageInline {
		return content
	}
	name := PageName(content)
	pages.put(name, content)
	return "@page:" + name
}

// textPage сообщает, что тип выхода хранит HTML или текст, а не адрес.
//
// Параметры: redirect — тип kuztds.
// Результат: true для show_page_html, show_html_404, show_text и javascript.
// Ошибки: нет.
func textPage(redirect string) bool {
	switch redirect {
	case "show_page_html", "show_html_404", "show_text", "javascript":
		return true
	default:
		return false
	}
}
