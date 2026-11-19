package admin

import (
	"fmt"
	"strings"

	"github.com/hasayone/empataso-tds/internal/store"
)

// tablePageParams documents limit, offset, sort and dir of a table paged on the server (pageParams).
// keys are the sort keys of the table and def the column of its default order.
func tablePageParams(keys []string, def string) []queryParam {
	list := strings.Join(keys, ", ")
	most, page := store.PageMaxRows, store.PageDefaultRows
	return []queryParam{
		intQ("limit", fmt.Sprintf("Page size, 1 to %d. Default: %d. Passing limit, offset or sort switches the answer to one page with totals.", most, page)).
			loc(fmt.Sprintf("Размер страницы, от 1 до %d. По умолчанию %d. limit, offset или sort переводят ответ в одну страницу с итогами.", most, page),
				fmt.Sprintf("Розмір сторінки, від 1 до %d. За замовчуванням %d. limit, offset або sort переводять відповідь в одну сторінку з підсумками.", most, page)),
		intQ("offset", "First row of the page, from 0. Default: 0. An offset past the end gives an empty page with the real total.").
			loc("Первая строка страницы, от 0. По умолчанию 0. Смещение за концом даёт пустую страницу с настоящим total.",
				"Перший рядок сторінки, від 0. За замовчуванням 0. Зсув за кінцем дає порожню сторінку зі справжнім total."),
		strQ("sort", "Column to sort the whole set by: "+list+". Default: "+def+". Ties are broken by the row identity, so pages do not overlap. A hidden column is 400 \"bad sort\".").
			loc("Колонка, по которой сортируется весь набор: "+list+". По умолчанию "+def+". Равные значения упорядочены по самой строке, поэтому страницы не пересекаются. Скрытая колонка — 400 «bad sort».",
				"Колонка, за якою сортується весь набір: "+list+". За замовчуванням "+def+". Рівні значення впорядковано за самим рядком, тому сторінки не перетинаються. Прихована колонка — 400 «bad sort»."),
		strQ("dir", "Sort direction: asc or desc. Default: desc.").
			loc("Направление сортировки: asc или desc. По умолчанию desc.", "Напрямок сортування: asc або desc. За замовчуванням desc."),
	}
}
