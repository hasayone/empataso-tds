package ktimport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Merge подмешивает группы переноса к уже существующим.
//
// Параметры: existing — группы локальной kuztds; incoming — результат маппера; imported — true для id из манифеста. nil значит, что таких id нет.
// Результат: полный список для PUT и строки отчёта. Запись заменяется только когда imported(id). Иначе локальная остаётся и в отчёт пишется конфликт. Пустые номера дописываются. Номер, занятый локальной группой, сдвигается.
// Ошибки: нет.
func Merge(existing, incoming []Group, imported func(id string) bool) ([]Group, []Loss) {
	return MergeByIDReport(existing, incoming, func(g Group) string { return g.ID }, imported)
}

// AssignNums выдаёт номер каждой группе, у которой он ещё не задан.
//
// Параметры: groups — итоговый список, изменяется на месте.
// Результат: группы с Num <= 0 получают max+1, max+2 и далее в порядке среза. max — наибольший уже заданный номер. Уже заданный номер эта функция не меняет: столкновение с номером локальной группы снимает слияние до вызова.
// Ошибки: нет. nil ничего не делает.
func AssignNums(groups []Group) {
	max := 0
	for _, g := range groups {
		if g.Num > max {
			max = g.Num
		}
	}
	for i := range groups {
		if groups[i].Num > 0 {
			continue
		}
		max++
		groups[i].Num = max
	}
}

// keepNum копирует номер хранилища, если импорт пришёл без номера.
//
// Параметры: old — группа из хранилища; next — группа из импорта.
// Результат: next. Если у next номер не задан, подставляется номер old.
// Ошибки: нет.
func keepNum(old, next Group) Group {
	if next.Num <= 0 {
		next.Num = old.Num
	}
	return next
}

// Validate проверяет список перед записью.
//
// Параметры: groups — группы после слияния; files — имена .dat, которые реально есть.
// Результат: nil, если id уникальны, цепочки не глубже 3, файлы на месте и тело меньше 8 МБ.
// Ошибки: нарушение любой из этих проверок.
func Validate(groups []Group, files map[string]bool) error {
	seen := map[string]bool{}
	byID := map[string]Group{}
	for _, g := range groups {
		if strings.TrimSpace(g.ID) == "" {
			return fmt.Errorf("пустой id группы")
		}
		if seen[g.ID] {
			return fmt.Errorf("повтор id группы %s", g.ID)
		}
		seen[g.ID] = true
		byID[g.ID] = g
	}
	for _, g := range groups {
		if g.Redirect == "group" {
			if err := hops(byID, g.ID, 0, map[string]bool{}); err != nil {
				return err
			}
		}
		for _, s := range g.Streams {
			if s.Out.Redirect == "group" {
				if err := hops(byID, strings.TrimSpace(s.Out.Out), 1, map[string]bool{g.ID: true}); err != nil {
					return err
				}
			}
		}
	}
	raw, err := json.Marshal(groups)
	if err != nil {
		return fmt.Errorf("группы не сериализуются: %w", err)
	}
	if len(raw) > MaxBody {
		return fmt.Errorf("тело групп %d байт больше %d", len(raw), MaxBody)
	}
	if files == nil {
		files = map[string]bool{}
	}
	for name, ok := range files {
		if !ok {
			return fmt.Errorf("список %s отсутствует", name)
		}
	}
	return nil
}

// hops считает длину цепочки group → group.
//
// Параметры: byID — группы; id — текущая; depth — уже пройденные шаги; seen — защита от цикла.
// Результат: nil, если глубина не больше MaxHops.
// Ошибки: глубина больше 3 или цикл.
func hops(byID map[string]Group, id string, depth int, seen map[string]bool) error {
	if id == "" {
		return nil
	}
	if seen[id] {
		return fmt.Errorf("цикл group → group на %s", id)
	}
	if depth > MaxHops {
		return fmt.Errorf("цепочка group → group длиннее %d", MaxHops)
	}
	g, ok := byID[id]
	if !ok {
		return nil
	}
	seen[id] = true
	next := ""
	if g.Redirect == "group" {
		next = strings.TrimSpace(g.Out)
	}
	if next == "" {
		return nil
	}
	return hops(byID, next, depth+1, seen)
}

// SameGroups сравнивает два списка групп как JSON.
//
// Параметры: a и b — списки групп.
// Результат: true, если канонический JSON совпал.
// Ошибки: нет. Ошибка сериализации даёт false.
func SameGroups(a, b []Group) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}
