package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
)

const (
	// checkerStatusMacro is the place of the conversion status in the postback template.
	checkerStatusMacro = "{checker_status}"
	// checkerTokenMacro is the place of the tracking token in the postback template.
	checkerTokenMacro = "{checker_token}"
	// checkerManagedPrefix starts the id of every s2s template that the integration owns.
	checkerManagedPrefix = "checker-"
	// msgCheckerManaged is the answer to a write of a template that the integration owns.
	msgCheckerManaged = "шаблон управляется интеграцией чекера"
	// checkerMaxPostbackURL is the longest postback template.
	checkerMaxPostbackURL = 2000
	// checkerExampleBase is the address shown in the default template while no address is saved.
	checkerExampleBase = "https://empataso.checkmy.store"
	// checkerPostbackPath is the path and query of the default template; the host comes from base_url.
	checkerPostbackPath = "/api/conversions/track?token=" + checkerTokenMacro +
		"&bundle_id={sub_id_1}&subid={subid}&status=" + checkerStatusMacro +
		"&offer={offer_name}&affiliate_network={affiliate_network_name}&country={country}&original_status={original_status}"
	// checkerMaskedToken replaces the tracking token in every address that is shown.
	checkerMaskedToken = "••••"
)

// checkerPostbackKinds are the managed templates: the fixed id, the conversion status the
// engine matches and the name of the template.
var checkerPostbackKinds = []struct{ id, status, name string }{
	{"checker-lead", "lead", "Чекер — лид"},
	{"checker-sale", "sale", "Чекер — продажа"},
	{"checker-rejected", "rejected", "Чекер — отклонено"},
}

// isCheckerManagedID reports whether an s2s template id belongs to the integration.
// id is the template id. Such a template is written only by syncCheckerPostbacks.
func isCheckerManagedID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), checkerManagedPrefix)
}

// splitCheckerManaged separates the catalog into the templates of other owners and the
// managed ones, each in its stored order.
// list is the stored catalog. Neither result shares memory with list.
func splitCheckerManaged(list []config.OutboundPostback) (own, managed []config.OutboundPostback) {
	for _, p := range list {
		if isCheckerManagedID(p.ID) {
			managed = append(managed, p)
		} else {
			own = append(own, p)
		}
	}
	return own, managed
}

// checkerPostbackDefault returns the template used while postback_url is empty.
// base is the saved checker address; an empty one gives the example address.
func checkerPostbackDefault(base string) string {
	if base == "" {
		base = checkerExampleBase
	}
	return base + checkerPostbackPath
}

// checkerPostbackProblem checks the postback template and method of the settings.
// raw is the template; an empty one is allowed and means the default. method is empty, GET or POST.
// The template must be an https address of a host that ends in .checkmy.store and hold the
// {checker_status} placeholder. It returns the client message, or an empty string when both are usable.
func checkerPostbackProblem(raw, method string) string {
	if method != "" && method != http.MethodGet && method != http.MethodPost {
		return "postback_method must be GET or POST"
	}
	if raw == "" {
		return ""
	}
	const bad = "postback_url must be an https://<name>.checkmy.store address of at most 2000 characters"
	if len(raw) > checkerMaxPostbackURL {
		return bad
	}
	if !strings.Contains(raw, checkerStatusMacro) {
		return "postback_url must contain " + checkerStatusMacro
	}
	probe := strings.NewReplacer(checkerStatusMacro, "x", checkerTokenMacro, "x").Replace(raw)
	u, err := url.Parse(probe)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return bad
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, checkerHostSuffix) || len(host) == len(checkerHostSuffix) {
		return bad
	}
	return ""
}

// postbackTemplate returns the template in force: the saved one, or the default built from base_url.
// It returns an empty string when neither is available.
func (c checkerSettings) postbackTemplate() string {
	if c.PostbackURL != "" {
		return c.PostbackURL
	}
	if c.BaseURL == "" {
		return ""
	}
	return checkerPostbackDefault(c.BaseURL)
}

// postbackMethod returns the method of the postback: POST, or GET when none is saved.
func (c checkerSettings) postbackMethod() string {
	if c.PostbackMethod == http.MethodPost {
		return http.MethodPost
	}
	return http.MethodGet
}

// postbackFilled reports whether the postback may be offered: the integration is on, the template
// in force is valid and, when it asks for the token, the token is saved.
func (c checkerSettings) postbackFilled() bool {
	t := c.postbackTemplate()
	if !c.Enabled || t == "" || checkerPostbackProblem(t, "") != "" {
		return false
	}
	return !strings.Contains(t, checkerTokenMacro) || c.PostbackToken != ""
}

// checkerPostbackView is the postback part of GET /api/checker/status.
// URLMasked is the template with the token hidden and {checker_status} as it is.
type checkerPostbackView struct {
	Available bool   `json:"available"`
	Method    string `json:"method"`
	URLMasked string `json:"url_masked"`
}

// postbackView builds the postback part of the status answer; it is empty unless the postback is filled.
func (c checkerSettings) postbackView() checkerPostbackView {
	if !c.postbackFilled() {
		return checkerPostbackView{}
	}
	return checkerPostbackView{
		Available: true, Method: c.postbackMethod(),
		URLMasked: strings.ReplaceAll(c.postbackTemplate(), checkerTokenMacro, checkerMaskedToken),
	}
}

// managedPostbacks builds the three s2s templates of the integration from the settings.
// Each carries the token and its own status in the address; the section is the main one.
// The caller checks postbackFilled first.
func (c checkerSettings) managedPostbacks() []config.OutboundPostback {
	tpl := strings.ReplaceAll(c.postbackTemplate(), checkerTokenMacro, url.QueryEscape(c.PostbackToken))
	out := make([]config.OutboundPostback, 0, len(checkerPostbackKinds))
	for _, k := range checkerPostbackKinds {
		out = append(out, config.OutboundPostback{
			ID: k.id, Name: k.name, URL: strings.ReplaceAll(tpl, checkerStatusMacro, k.status),
			Method: c.postbackMethod(), Statuses: []string{k.status},
		})
	}
	return out
}

// syncCheckerPostbacks makes the managed templates of s2s.json match the settings.
// A filled postback writes the three templates, anything else removes them; the other templates
// stay as they are and the file is not written when nothing changes. The settings are read under
// groupsMu, so two syncs never write an older state over a newer one. checkerMu is not held and
// no request leaves the server. It returns the read or write error of the catalog; a server
// without a catalog does nothing.
func (s *Server) syncCheckerPostbacks(ctx context.Context) error {
	if s.cfg.S2S == nil {
		return nil
	}
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	set := s.checkerSettings()
	stored, err := s.cfg.S2S.List(ctx)
	if err != nil {
		return err
	}
	next := make([]config.OutboundPostback, 0, len(stored)+len(checkerPostbackKinds))
	for _, p := range stored {
		if !isCheckerManagedID(p.ID) {
			next = append(next, p)
		}
	}
	if set.postbackFilled() {
		next = append(next, set.managedPostbacks()...)
	}
	if len(stored) == 0 && len(next) == 0 {
		return nil
	}
	before, _ := json.Marshal(stored)
	after, _ := json.Marshal(next)
	if string(before) == string(after) {
		return nil
	}
	return s.cfg.S2S.Save(ctx, next)
}
