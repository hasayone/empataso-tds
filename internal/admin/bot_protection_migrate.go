package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/emtds/bridge/ktimport"
)

// migrateBotProtection404 переписывает сохранённые потоки Bot Protection и «Замыкающий» с show_html_404
// в 404_not_found и удаляет осиротевшие файлы страниц.
//
// Параметры: ctx — контекст для GroupsStore.
// Результат: nil после успешной миграции или когда менять нечего.
// Ошибки: сбой чтения или записи groups.json; удаление страницы только логируется.
func (s *Server) migrateBotProtection404(ctx context.Context) error {
	if s.cfg.Groups == nil {
		return nil
	}
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return fmt.Errorf("admin: bot protection migrate: %w", err)
	}
	changed, cleared := fixBotProtectionStreams(groups)
	if !changed {
		return nil
	}
	refs := collectPageRefs(groups)
	if err := s.cfg.Groups.Save(ctx, groups); err != nil {
		return fmt.Errorf("admin: bot protection migrate save: %w", err)
	}
	if s.cfg.Pages == nil {
		return nil
	}
	for _, name := range cleared {
		if _, still := refs[name]; still {
			continue
		}
		if err := s.cfg.Pages.Delete(name); err != nil && s.cfg.Log != nil {
			s.cfg.Log.Warn("bot protection migrate page delete", "name", name, "err", err)
		}
	}
	return nil
}

// fixBotProtectionStreams правит потоки Bot Protection и «Замыкающий» с HTML-404 в groups на месте.
//
// Параметры: groups — все кампании из хранилища.
// Результат: true, если хотя бы один поток изменён, и имена @page:, снятые с таких потоков.
// Ошибки: нет.
func fixBotProtectionStreams(groups []config.Group) (changed bool, clearedPages []string) {
	for gi := range groups {
		for si := range groups[gi].Streams {
			st := &groups[gi].Streams[si]
			if !ktimport.BotProtectionStreamName(st.Name) || st.Out.Redirect != "show_html_404" {
				continue
			}
			if name, ok := streamPageName(st.Out.Out); ok {
				clearedPages = append(clearedPages, name)
			}
			st.Out.Redirect = "404_not_found"
			st.Out.Out = ""
			changed = true
		}
	}
	return changed, clearedPages
}

// collectPageRefs собирает имена файлов @page: из кампаний и потоков.
//
// Параметры: groups — конфигурация кампаний.
// Результат: множество имён page-*.html.
// Ошибки: нет.
func collectPageRefs(groups []config.Group) map[string]struct{} {
	out := map[string]struct{}{}
	for _, g := range groups {
		if name, ok := streamPageName(g.Out); ok {
			out[name] = struct{}{}
		}
		for _, st := range g.Streams {
			if name, ok := streamPageName(st.Out.Out); ok {
				out[name] = struct{}{}
			}
			if name, ok := streamPageName(st.Bots.Out); ok {
				out[name] = struct{}{}
			}
		}
	}
	return out
}

// streamPageName разбирает ссылку @page: в out потока или кампании.
//
// Параметры: raw — значение out.
// Результат: имя файла и true для @page:page-….html.
// Ошибки: нет.
func streamPageName(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	const prefix = "@page:"
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}
	name := strings.TrimSpace(s[len(prefix):])
	if !pageNameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", false
	}
	return name, true
}
