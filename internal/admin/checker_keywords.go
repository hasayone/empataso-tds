package admin

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"

	"github.com/hasayone/empataso-tds/internal/config"
)

const (
	// checkerSampleMax is the most campaigns the preview lists as examples.
	checkerSampleMax = 50
	// checkerFillBody is the largest body of the fill request.
	checkerFillBody = 1 << 10
)

// checkerFillItem is a campaign the fill will give a keyword read from its name.
type checkerFillItem struct {
	group   config.Group
	keyword string
}

// checkerKeywordPlan sorts the campaigns by what the fill does with them.
// fill holds the campaigns that get a keyword, keep counts the campaigns left as they are
// (they have a keyword or their record is being sent or linked) and noKey counts the campaigns
// whose name gives no keyword.
type checkerKeywordPlan struct {
	fill  []checkerFillItem
	keep  int
	noKey int
}

// checkerPlanKeywords sorts every campaign for the fill.
// groups is the campaign list. The caller holds checkerMu, so the records do not change meanwhile.
// A campaign whose record has a keyword is kept; so is one whose empty-keyword record is being sent
// or linked, because those records are never touched. Any other campaign gets the keyword of
// its name, or counts as without a key when the name gives none.
func (s *Server) checkerPlanKeywords(groups []config.Group) checkerKeywordPlan {
	var plan checkerKeywordPlan
	for _, g := range groups {
		rec, has := s.checker.apps[g.ID]
		if has && (rec.Keyword != "" || rec.State == checkerStSending || rec.State == checkerStLinked) {
			plan.keep++
			continue
		}
		kw := checkerKeywordFromName(g.Name)
		if kw == "" {
			plan.noKey++
			continue
		}
		plan.fill = append(plan.fill, checkerFillItem{group: g, keyword: kw})
	}
	return plan
}

// checkerKeyCount is one keyword of the preview with the number of campaigns that would get it.
type checkerKeyCount struct {
	Keyword   string `json:"keyword"`
	Campaigns int    `json:"campaigns"`
}

// checkerSample is one campaign of the preview with the keyword it would get.
type checkerSample struct {
	GroupID string `json:"group_id"`
	Num     int    `json:"num"`
	Name    string `json:"name"`
	Keyword string `json:"keyword"`
}

// checkerPreviewView is the body of GET /api/checker/keywords/preview.
type checkerPreviewView struct {
	Campaigns int               `json:"campaigns"`
	Fill      int               `json:"fill"`
	Keep      int               `json:"keep"`
	NoKey     int               `json:"no_key"`
	Keys      []checkerKeyCount `json:"keys"`
	Sample    []checkerSample   `json:"sample"`
}

// handleCheckerKeywordsPreview shows what the keyword fill would do, without changing anything.
// The answer holds the number of campaigns, of those that would get a keyword (fill), of those
// kept and of those without a key, every different keyword with its campaign count (the largest
// first) and up to 50 example campaigns. A missing groups store is 503; a list error is 500.
// No call leaves the server.
func (s *Server) handleCheckerKeywordsPreview(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	groups, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	s.checker.mu.Lock()
	plan := s.checkerPlanKeywords(groups)
	s.checker.mu.Unlock()
	counts := map[string]int{}
	view := checkerPreviewView{
		Campaigns: len(groups), Fill: len(plan.fill), Keep: plan.keep, NoKey: plan.noKey,
		Keys: []checkerKeyCount{}, Sample: []checkerSample{},
	}
	for _, it := range plan.fill {
		counts[it.keyword]++
		if len(view.Sample) < checkerSampleMax {
			view.Sample = append(view.Sample, checkerSample{GroupID: it.group.ID, Num: it.group.Num, Name: it.group.Name, Keyword: it.keyword})
		}
	}
	for kw, n := range counts {
		view.Keys = append(view.Keys, checkerKeyCount{Keyword: kw, Campaigns: n})
	}
	sort.Slice(view.Keys, func(i, j int) bool {
		if view.Keys[i].Campaigns != view.Keys[j].Campaigns {
			return view.Keys[i].Campaigns > view.Keys[j].Campaigns
		}
		return view.Keys[i].Keyword < view.Keys[j].Keyword
	})
	writeJSON(w, http.StatusOK, view)
}

// checkerFillPrev is a record as it was before the fill, kept to put it back when the write fails.
type checkerFillPrev struct {
	id  string
	rec checkerApp
	had bool
}

// handleCheckerKeywordsFill gives every campaign without a keyword the one read from its name.
// The body must be {"confirm": true}, otherwise the answer is 400. The campaigns are read
// again and sorted under checkerMu. Only an empty keyword is filled: a campaign without a record gets
// a draft, a record in draft, queued or error gets the keyword; a filled keyword and a record being
// sent or linked are left alone. All records are written in one atomic write. The answer is
// {"filled", "kept", "no_key"}. The integration may be off: nothing leaves the server. A missing
// groups store is 503, a list error 500, a write failure 500 "save error" with nothing changed.
func (s *Server) handleCheckerKeywordsFill(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, checkerFillBody)).Decode(&body); err != nil || !body.Confirm {
		writeErr(w, http.StatusBadRequest, "confirmation required")
		return
	}
	if !s.checkerWritable(w) {
		return
	}
	groups, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	uid := s.viewerFrom(r.Context()).uid
	now := checkerStamp(s.checkerNow())
	s.checker.mu.Lock()
	plan := s.checkerPlanKeywords(groups)
	prev := make([]checkerFillPrev, 0, len(plan.fill))
	for _, it := range plan.fill {
		rec, had := s.checker.apps[it.group.ID]
		prev = append(prev, checkerFillPrev{id: it.group.ID, rec: rec, had: had})
		if !had {
			rec = checkerApp{State: checkerStDraft, CreatedBy: uid, CreatedAt: now}
		}
		rec.Keyword, rec.UpdatedAt = it.keyword, now
		if rec.Name == "" {
			rec.Name = checkerAppName(it.group.Num, it.keyword)
		}
		s.checker.apps[it.group.ID] = rec
	}
	var serr error
	if len(prev) > 0 {
		serr = s.checkerSaveLocked()
		if serr != nil {
			for _, p := range prev {
				if p.had {
					s.checker.apps[p.id] = p.rec
				} else {
					delete(s.checker.apps, p.id)
				}
			}
		}
	}
	s.checker.mu.Unlock()
	if serr != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	log.Printf("checker: keywords filled=%d kept=%d no_key=%d", len(plan.fill), plan.keep, plan.noKey)
	writeJSON(w, http.StatusOK, map[string]int{"filled": len(plan.fill), "kept": plan.keep, "no_key": plan.noKey})
}
