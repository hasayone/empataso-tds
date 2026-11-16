package admin

import "github.com/emtds/bridge/ktimport"

// importPages writes HTML files before groups are saved.
// res is the mapper output for this run. It returns how many files were written.
// When the pages store is missing, outputs that already name a page file stay
// as they are and the result includes one skipped loss.
// An error means a page file could not be written.
func (s *Server) importPages(res *ktimport.Result) (int, []ktimport.Loss, error) {
	if res == nil {
		return 0, nil, nil
	}
	if s.cfg.Pages == nil {
		return 0, []ktimport.Loss{{
			Kind:   "skipped",
			Ref:    "pages",
			Detail: "pages store unavailable",
		}}, nil
	}
	n := 0
	for name, content := range res.Pages {
		if err := s.cfg.Pages.Write(name, content); err != nil {
			return 0, nil, err
		}
		n++
	}
	return n, nil, nil
}
