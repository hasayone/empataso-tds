package ktimport

import "fmt"

// MergeByID подменяет записи прошлого переноса и дописывает новые.
//
// Параметры: existing — то, что уже лежит в emtds; incoming — результат маппера; idOf — достаёт id; imported — true для id из манифеста. nil значит, что таких id нет.
// Результат: список, где запись из incoming заменила старую только при imported(id). Чужой id не тронут. Отсутствующая в incoming запись сохранена. Потери этого слияния отбрасываются.
// Ошибки: нет.
func MergeByID[T any](existing, incoming []T, idOf func(T) string, imported func(id string) bool) []T {
	out, _ := MergeByIDReport(existing, incoming, idOf, imported)
	return out
}

// MergeByIDReport делает то же слияние и возвращает строки отчёта.
//
// Параметры: existing — хранилище; incoming — результат маппера; idOf — достаёт id; imported — true для id из манифеста.
// Результат: список и потери. Конфликт: id есть в обоих списках, но imported(id) ложь, локальная запись остаётся. Для групп пустой номер берётся из старой записи, столкновение номера с локальной группой сдвигает номер импорта.
// Ошибки: нет.
func MergeByIDReport[T any](existing, incoming []T, idOf func(T) string, imported func(id string) bool) ([]T, []Loss) {
	if imported == nil {
		imported = func(string) bool { return false }
	}
	inc := map[string]T{}
	var order []string
	for _, item := range incoming {
		id := idOf(item)
		if _, ok := inc[id]; !ok {
			order = append(order, id)
		}
		inc[id] = item
	}
	applied := map[string]bool{}
	seen := map[string]bool{}
	var out []T
	var losses []Loss
	for _, item := range existing {
		id := idOf(item)
		next, ok := inc[id]
		if !ok {
			out = append(out, item)
			continue
		}
		seen[id] = true
		if imported(id) {
			applied[id] = true
			out = append(out, keepItemNum(item, next))
			continue
		}
		out = append(out, item)
		losses = append(losses, Loss{
			Kind:   "conflict",
			Ref:    id,
			Detail: "id занят локальной записью",
		})
	}
	for _, id := range order {
		if seen[id] {
			continue
		}
		applied[id] = true
		out = append(out, inc[id])
	}
	if out == nil {
		out = []T{}
	}
	losses = append(losses, finishNums(out, applied, imported)...)
	return out, losses
}

// finishNums выравнивает номера, если список состоит из групп.
//
// Параметры: items — результат слияния; applied — id, на место которых встала запись импорта; imported — проверка манифеста.
// Результат: строки о сдвиге номера. Другие типы не меняются.
// Ошибки: нет.
func finishNums[T any](items []T, applied map[string]bool, imported func(string) bool) []Loss {
	groups, ok := any(items).([]Group)
	if !ok {
		return nil
	}
	losses := bumpNumCollisions(groups, applied, imported)
	AssignNums(groups)
	return losses
}

// bumpNumCollisions сдвигает номер импорта, если его уже заняла локальная группа.
//
// Параметры: groups — список после подстановки записей, изменяется на месте; applied — id записей импорта; imported — id прошлого переноса.
// Результат: строки approximated. Номер локальной группы и номер группы вне applied не меняются.
// Ошибки: нет.
func bumpNumCollisions(groups []Group, applied map[string]bool, imported func(string) bool) []Loss {
	if imported == nil {
		imported = func(string) bool { return false }
	}
	if applied == nil {
		applied = map[string]bool{}
	}
	protected := map[int]bool{}
	maxN := 0
	for _, g := range groups {
		if g.Num > maxN {
			maxN = g.Num
		}
		if applied[g.ID] || imported(g.ID) || g.Num <= 0 {
			continue
		}
		protected[g.Num] = true
	}
	var losses []Loss
	for i := range groups {
		g := &groups[i]
		if !applied[g.ID] || g.Num <= 0 || !protected[g.Num] {
			continue
		}
		old := g.Num
		maxN++
		g.Num = maxN
		losses = append(losses, Loss{
			Kind:   "approximated",
			Ref:    g.ID,
			Detail: fmt.Sprintf("номер %d занят, выдан %d", old, g.Num),
		})
	}
	return losses
}

// keepItemNum сохраняет номер группы при замене по id.
//
// Параметры: old — запись из хранилища; next — запись импорта.
// Результат: next. Для группы с пустым номером номер берётся из old. Для другого типа next не меняется.
// Ошибки: нет.
func keepItemNum[T any](old, next T) T {
	oldG, ok1 := any(old).(Group)
	nextG, ok2 := any(next).(Group)
	if !ok1 || !ok2 {
		return next
	}
	return any(keepNum(oldG, nextG)).(T)
}

// PurgeImported убирает группы, чей id есть в манифесте.
//
// Параметры: groups — полный список; imported — true для id прошлого переноса. nil ничего не удаляет.
// Результат: список без этих групп. Чужие группы остаются. Номера не пересчитываются.
// Ошибки: нет.
func PurgeImported(groups []Group, imported func(id string) bool) []Group {
	return PurgeByID(groups, func(g Group) string { return g.ID }, imported)
}

// PurgeByID убирает записи, чей id есть в манифесте.
//
// Параметры: list — записи; idOf — достаёт id; imported — true для id прошлого переноса.
// Результат: список без этих id. nil-проверка оставляет список как есть.
// Ошибки: нет.
func PurgeByID[T any](list []T, idOf func(T) string, imported func(id string) bool) []T {
	if imported == nil {
		imported = func(string) bool { return false }
	}
	var out []T
	for _, item := range list {
		if imported(idOf(item)) {
			continue
		}
		out = append(out, item)
	}
	if out == nil {
		out = []T{}
	}
	return out
}
