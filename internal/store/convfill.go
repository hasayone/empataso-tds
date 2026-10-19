package store

import "time"

// ClickFill is click metadata used to backfill empty conversion fields.
type ClickFill struct {
	TS         time.Time
	GroupID    string
	GroupName  string
	IP         string
	UserAgent  string
	OS         string
	OSVersion  string
	Browser    string
	BrowserVer string
	Brand      string
	Referer    string
	Lang       string
	City       string
	Region     string
	LandingID  string
	OfferID    string
	NetworkID  string
	SourceID   string
}

// cidsNeedingClickFill returns distinct click ids from rows that still miss
// click-backed fields. The order follows the first appearance in rows.
func cidsNeedingClickFill(rows []PostbackRow) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, p := range rows {
		if !postbackNeedsClickFill(p) {
			continue
		}
		cid := p.CID
		if cid == "" {
			continue
		}
		if _, ok := seen[cid]; ok {
			continue
		}
		seen[cid] = struct{}{}
		out = append(out, cid)
	}
	return out
}

// postbackNeedsClickFill reports whether row is missing data that a click may supply.
// p is the journal row. It returns true when the campaign id, click time,
// IP, user agent, OS, browser or landing is empty. An empty campaign name alone does not
// start a lookup: the name is taken from the answer of a lookup that the other fields start.
func postbackNeedsClickFill(p PostbackRow) bool {
	if clickTSEmpty(p.ClickTS) {
		return true
	}
	if p.CampaignID == "" {
		return true
	}
	if p.IP == "" {
		return true
	}
	if p.UserAgent == "" {
		return true
	}
	if p.OS == "" {
		return true
	}
	if p.Browser == "" {
		return true
	}
	if p.Landing == "" {
		return true
	}
	return false
}

// fillFromClicks copies snap into each row only where the row field is empty.
// clicks maps cid to the newest click row for that id. Missing keys are skipped.
func fillFromClicks(rows []PostbackRow, clicks map[string]ClickFill) {
	for i := range rows {
		snap, ok := clicks[rows[i].CID]
		if !ok {
			continue
		}
		fillPostbackFromClick(&rows[i], snap)
	}
}

// fillPostbackFromClick writes snap into p without overwriting non-empty values.
// p is the journal row. snap is the click found by cid. The campaign id fills
// CampaignID and the campaign name fills Group. It does not return an error.
func fillPostbackFromClick(p *PostbackRow, snap ClickFill) {
	if p.CampaignID == "" && snap.GroupID != "" {
		p.CampaignID = snap.GroupID
	}
	if p.Group == "" && snap.GroupName != "" {
		p.Group = snap.GroupName
	}
	if clickTSEmpty(p.ClickTS) && !snap.TS.IsZero() {
		p.ClickTS = snap.TS
	}
	if p.IP == "" && snap.IP != "" {
		p.IP = snap.IP
	}
	if p.UserAgent == "" && snap.UserAgent != "" {
		p.UserAgent = snap.UserAgent
	}
	if p.OS == "" && snap.OS != "" {
		p.OS = snap.OS
	}
	if p.OSVersion == "" && snap.OSVersion != "" {
		p.OSVersion = snap.OSVersion
	}
	if p.Browser == "" && snap.Browser != "" {
		p.Browser = snap.Browser
	}
	if p.BrowserVer == "" && snap.BrowserVer != "" {
		p.BrowserVer = snap.BrowserVer
	}
	if p.Brand == "" && snap.Brand != "" {
		p.Brand = snap.Brand
	}
	if p.Referer == "" && snap.Referer != "" {
		p.Referer = snap.Referer
	}
	if p.Lang == "" && snap.Lang != "" {
		p.Lang = snap.Lang
	}
	if p.City == "" && snap.City != "" {
		p.City = snap.City
	}
	if p.Region == "" && snap.Region != "" {
		p.Region = snap.Region
	}
	if p.Landing == "" && snap.LandingID != "" {
		p.Landing = snap.LandingID
	}
	if p.Offer == "" && snap.OfferID != "" {
		p.Offer = snap.OfferID
	}
	if p.Network == "" && snap.NetworkID != "" {
		p.Network = snap.NetworkID
	}
	if p.Source == "" && snap.SourceID != "" {
		p.Source = snap.SourceID
	}
}

// FillConversionInsert copies snap into row only where row fields are empty.
// snap nil is a no-op.
func FillConversionInsert(row *ConversionInsert, snap *ClickFill) {
	if snap == nil {
		return
	}
	if row.GroupID == "" && snap.GroupID != "" {
		row.GroupID = snap.GroupID
	}
	if row.GroupName == "" && snap.GroupName != "" {
		row.GroupName = snap.GroupName
	}
	if clickTSEmpty(row.ClickTS) && !snap.TS.IsZero() {
		row.ClickTS = snap.TS
	}
	if row.IP == "" && snap.IP != "" {
		row.IP = snap.IP
	}
	if row.UserAgent == "" && snap.UserAgent != "" {
		row.UserAgent = snap.UserAgent
	}
	if row.OS == "" && snap.OS != "" {
		row.OS = snap.OS
	}
	if row.OSVersion == "" && snap.OSVersion != "" {
		row.OSVersion = snap.OSVersion
	}
	if row.Browser == "" && snap.Browser != "" {
		row.Browser = snap.Browser
	}
	if row.BrowserV == "" && snap.BrowserVer != "" {
		row.BrowserV = snap.BrowserVer
	}
	if row.Brand == "" && snap.Brand != "" {
		row.Brand = snap.Brand
	}
	if row.Referer == "" && snap.Referer != "" {
		row.Referer = snap.Referer
	}
	if row.Lang == "" && snap.Lang != "" {
		row.Lang = snap.Lang
	}
	if row.City == "" && snap.City != "" {
		row.City = snap.City
	}
	if row.Region == "" && snap.Region != "" {
		row.Region = snap.Region
	}
	if row.LandingID == "" && snap.LandingID != "" {
		row.LandingID = snap.LandingID
	}
	if row.OfferID == "" && snap.OfferID != "" {
		row.OfferID = snap.OfferID
	}
	if row.NetworkID == "" && snap.NetworkID != "" {
		row.NetworkID = snap.NetworkID
	}
	if row.SourceID == "" && snap.SourceID != "" {
		row.SourceID = snap.SourceID
	}
}

// clickTSEmpty is true for a missing click time or the Unix epoch placeholder.
func clickTSEmpty(ts time.Time) bool {
	return ts.IsZero() || ts.Unix() <= 0
}
