package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

const defaultCompanyName = "CustomTDS"

var uiMu sync.Mutex

var (
	uiColKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	uiLogoRe   = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp|svg\+xml);base64,[A-Za-z0-9+/=]+$`)
)

type uiCol struct {
	ID string `json:"id"`
	On bool   `json:"on"`
}

type uiDash struct {
	Panels []string `json:"panels"`
	Dim    string   `json:"dim"`
}

type uiCompany struct {
	Name string `json:"name"`
	Logo string `json:"logo"`
}

type uiPrefs struct {
	Cols    map[string][]uiCol `json:"cols"`
	Dash    uiDash             `json:"dash"`
	Company uiCompany          `json:"company"`
}

func defaultUIPrefs() uiPrefs {
	return uiPrefs{
		Cols: make(map[string][]uiCol),
		Dash: uiDash{Panels: nil, Dim: ""},
		Company: uiCompany{
			Name: defaultCompanyName,
			Logo: "",
		},
	}
}

// readUI loads UI preferences from path.
// A missing or empty file yields defaults. Invalid JSON returns an error.
func readUI(path string) (uiPrefs, error) {
	if path == "" {
		return defaultUIPrefs(), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultUIPrefs(), nil
		}
		return uiPrefs{}, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return defaultUIPrefs(), nil
	}
	var p uiPrefs
	if err := json.Unmarshal(b, &p); err != nil {
		return uiPrefs{}, err
	}
	if p.Cols == nil {
		p.Cols = make(map[string][]uiCol)
	}
	p.Company.Name = strings.TrimSpace(p.Company.Name)
	if p.Company.Name == "" {
		p.Company.Name = defaultCompanyName
	}
	return p, nil
}

// normalizeUI validates and normalizes p in place.
// It returns a client-facing error message on violation.
func normalizeUI(p *uiPrefs) error {
	if p.Cols == nil {
		p.Cols = make(map[string][]uiCol)
	}
	if len(p.Cols) > 32 {
		return errors.New("too many column groups")
	}
	for key, cols := range p.Cols {
		if !uiColKeyRe.MatchString(key) {
			return errors.New("invalid cols key")
		}
		if len(cols) > 200 {
			return errors.New("too many columns")
		}
		seen := map[string]bool{}
		for i := range cols {
			id := strings.TrimSpace(cols[i].ID)
			if id == "" {
				return errors.New("column id required")
			}
			if utf8.RuneCountInString(id) > 64 {
				return errors.New("column id too long")
			}
			if seen[id] {
				return errors.New("duplicate column id")
			}
			seen[id] = true
			cols[i].ID = id
		}
		p.Cols[key] = cols
	}
	if len(p.Dash.Panels) > 64 {
		return errors.New("too many dashboard panels")
	}
	for i, panel := range p.Dash.Panels {
		if utf8.RuneCountInString(panel) > 64 {
			return errors.New("dashboard panel too long")
		}
		p.Dash.Panels[i] = panel
	}
	if utf8.RuneCountInString(p.Dash.Dim) > 64 {
		return errors.New("dashboard dimension too long")
	}
	p.Company.Name = strings.TrimSpace(p.Company.Name)
	if p.Company.Name == "" {
		p.Company.Name = defaultCompanyName
	}
	if utf8.RuneCountInString(p.Company.Name) > 64 {
		return errors.New("company name too long")
	}
	logo := p.Company.Logo
	if logo != "" {
		if len(logo) > 280_000 {
			return errors.New("company logo too large")
		}
		if !uiLogoRe.MatchString(logo) {
			return errors.New("invalid company logo")
		}
	}
	return nil
}

func writeUI(path string, p uiPrefs) error {
	if p.Cols == nil {
		p.Cols = make(map[string][]uiCol)
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o644)
}

func (s *Server) handleUIGet(w http.ResponseWriter, r *http.Request) {
	if s.cfg.UIPath == "" {
		writeJSON(w, http.StatusOK, defaultUIPrefs())
		return
	}
	p, err := readUI(s.cfg.UIPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleUIPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.UIPath == "" {
		writeErr(w, http.StatusServiceUnavailable, "ui file not configured")
		return
	}
	var p uiPrefs
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := normalizeUI(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	uiMu.Lock()
	defer uiMu.Unlock()
	if err := writeUI(s.cfg.UIPath, p); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleBrandGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var company uiCompany
	if s.cfg.UIPath == "" {
		company = defaultUIPrefs().Company
		writeJSON(w, http.StatusOK, company)
		return
	}
	p, err := readUI(s.cfg.UIPath)
	if err != nil {
		company = defaultUIPrefs().Company
	} else {
		company = p.Company
	}
	writeJSON(w, http.StatusOK, company)
}
