package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func convExtraColList(click, ext, model, origin bool) []string {
	_, cols := convExtra(click, ext, model, origin, false, false)
	if cols == "" {
		return nil
	}
	return strings.Split(cols, ", ")
}

func stubOutputNames(stubs string) []string {
	if stubs == "" {
		return nil
	}
	var names []string
	for _, part := range strings.Split(stubs, ", ") {
		if i := strings.LastIndex(part, " AS "); i >= 0 {
			names = append(names, strings.TrimSpace(part[i+4:]))
		}
	}
	return names
}

func postbackAllCols(extra []string) []string {
	base := []string{"ts", "group_name", "stream_name", "domain", "page", "device", "country", "city", "profit", "cid", "status"}
	return append(base, extra...)
}

func writePostbackScanMark(dest any, col string, mask int) {
	mark := fmt.Sprintf("%s_%04b", col, mask)
	switch v := dest.(type) {
	case *string:
		*v = mark
	case *float64:
		*v = float64(mask) + 0.25
	case *uint32:
		*v = uint32(mask + 1000)
	case *time.Time:
		*v = time.Unix(int64(2000+mask), 0).UTC()
	}
}

func readPostbackCol(p *PostbackRow, subs []string, col string) (any, bool) {
	switch col {
	case "ts":
		return p.TS, true
	case "group_name":
		return p.Group, true
	case "stream_name":
		return p.Stream, true
	case "domain":
		return p.Domain, true
	case "page":
		return p.Page, true
	case "device":
		return p.Device, true
	case "country":
		return p.Country, true
	case "city":
		return p.City, true
	case "profit":
		return p.Profit, true
	case "depsum":
		return p.DepSum, true
	case "cid":
		return p.CID, true
	case "status":
		return p.Status, true
	case "offer_id":
		return p.Offer, true
	case "landing_id":
		return p.Landing, true
	case "source_id":
		return p.Source, true
	case "network_id":
		return p.Network, true
	case "group_id":
		return p.CampaignID, true
	case "click_ts":
		return p.ClickTS, true
	case "original_status":
		return p.OriginalStatus, true
	case "prev_status":
		return p.PrevStatus, true
	case "tid":
		return p.TID, true
	case "ip", "region", "lang", "os", "os_version", "browser", "browser_ver", "brand", "useragent", "referer", "se", "keyword", "org", "site", "ad_campaign_id", "creative_id", "external_id", "xrw", "device_model", "origin_group_name", "origin_group_id":
		return postbackStringField(p, col), true
	case "asn":
		return p.ASN, true
	default:
		if strings.HasPrefix(col, "sub_id_") {
			var n int
			if _, err := fmt.Sscanf(col, "sub_id_%d", &n); err == nil && n >= 1 && n <= len(subs) {
				return subs[n-1], true
			}
		}
		return nil, false
	}
}

func postbackStringField(p *PostbackRow, col string) string {
	switch col {
	case "ip":
		return p.IP
	case "region":
		return p.Region
	case "lang":
		return p.Lang
	case "os":
		return p.OS
	case "os_version":
		return p.OSVersion
	case "browser":
		return p.Browser
	case "browser_ver":
		return p.BrowserVer
	case "brand":
		return p.Brand
	case "useragent":
		return p.UserAgent
	case "referer":
		return p.Referer
	case "se":
		return p.SE
	case "keyword":
		return p.Keyword
	case "org":
		return p.Org
	case "site":
		return p.Site
	case "ad_campaign_id":
		return p.AdCampaignID
	case "creative_id":
		return p.CreativeID
	case "external_id":
		return p.ExternalID
	case "xrw":
		return p.XRW
	case "device_model":
		return p.DeviceModel
	case "origin_group_name":
		return p.OriginCampaign
	case "origin_group_id":
		return p.OriginCampaignID
	default:
		return ""
	}
}

func scanMarkEqual(col string, mask int, got any) bool {
	switch v := got.(type) {
	case string:
		return v == fmt.Sprintf("%s_%04b", col, mask)
	case float64:
		return v == float64(mask)+0.25
	case uint32:
		return v == uint32(mask+1000)
	case time.Time:
		return v.Equal(time.Unix(int64(2000+mask), 0).UTC())
	default:
		return false
	}
}

// Postback SELECT/scan and conversionUnion must share one column order.
func TestPostbackColumnsMatchDest(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		click := mask&1 != 0
		ext := mask&2 != 0
		model := mask&4 != 0
		origin := mask&8 != 0
		c := &CH{clickCols: click, extConv: ext, modelConv: model}
		sel, extra := c.postbackColumns(origin)
		wantExtra := convExtraColList(click, ext, model, origin)
		if len(extra) != len(wantExtra) {
			t.Fatalf("mask %04b: extra cols %v, convExtra %v", mask, extra, wantExtra)
		}
		for i := range extra {
			if extra[i] != wantExtra[i] {
				t.Fatalf("mask %04b: extra[%d]=%q want %q", mask, i, extra[i], wantExtra[i])
			}
		}
		var p PostbackRow
		subs := make([]string, 12)
		dest := c.postbackDest(&p, subs, extra)
		allCols := postbackAllCols(extra)
		if got, want := len(dest), len(allCols); got != want {
			t.Fatalf("mask %04b: %d scan targets for %d columns", mask, got, want)
		}
		if got, want := len(dest), strings.Count(sel, ",")+1; got != want {
			t.Fatalf("mask %04b: %d scan targets for %d columns in %q", mask, got, want, sel)
		}
		for i, col := range allCols {
			writePostbackScanMark(dest[i], col, mask)
		}
		for i, col := range allCols {
			got, ok := readPostbackCol(&p, subs, col)
			if !ok {
				t.Fatalf("mask %04b: no reader for %q at %d", mask, col, i)
			}
			if !scanMarkEqual(col, mask, got) {
				t.Fatalf("mask %04b: col %q at %d got %#v", mask, col, i, got)
			}
		}
		stubs, _ := convExtra(click, ext, model, origin, false, false)
		stubNames := stubOutputNames(stubs)
		if len(stubNames) != len(wantExtra) {
			t.Fatalf("mask %04b: stub count %d != col count %d", mask, len(stubNames), len(wantExtra))
		}
		for i := range stubNames {
			if stubNames[i] != wantExtra[i] {
				t.Fatalf("mask %04b: union stub[%d]=%q want %q", mask, i, stubNames[i], wantExtra[i])
			}
		}
	}
}

func logJournalCols(c *CH, extCols []string, originEvents bool) []string {
	cols := []string{
		"ts", "group_name", "stream_name", "country", "city", "device", "os", "browser",
		"brand", "bot", "uniq", "ip", "keyword", "out", "region", "lang", "operator",
		"redirect", "referer", "useragent", "domain", "cid",
	}
	if c.netCols {
		cols = append(cols, "asn", "org", "timezone", "via")
	} else {
		cols = append(cols, "asn", "org", "timezone")
	}
	cols = append(cols, "page", "os_version", "browser_ver", "se", "group_id")
	if c.clickCols {
		cols = append(cols, subSelectCols(c)...)
		cols = append(cols, "offer_id", "landing_id", "source_id", "network_id")
	}
	cols = append(cols, extCols...)
	if c.modelEvents {
		cols = append(cols, "device_model")
	}
	if originEvents {
		cols = append(cols, "origin_group_name", "origin_group_id")
	} else {
		cols = append(cols, "origin_group_name", "origin_group_id")
	}
	return cols
}

func readLogCol(r *LogRow, subs []string, col string) (any, bool) {
	switch col {
	case "ts":
		return r.TS, true
	case "group_name":
		return r.Group, true
	case "uniq":
		return r.Uniq, true
	case "asn":
		return r.ASN, true
	case "device_model":
		return r.DeviceModel, true
	case "origin_group_name":
		return r.OriginCampaign, true
	case "origin_group_id":
		return r.OriginCampaignID, true
	case "site":
		return r.Site, true
	default:
		if strings.HasPrefix(col, "sub_id_") {
			var n int
			if _, err := fmt.Sscanf(col, "sub_id_%d", &n); err == nil && n >= 1 && n <= len(subs) {
				return subs[n-1], true
			}
		}
		if col == "offer_id" {
			return r.OfferID, true
		}
		return nil, false
	}
}

func writeLogScanMark(dest any, col string, mask int) {
	mark := fmt.Sprintf("log_%s_%d", col, mask)
	switch v := dest.(type) {
	case *string:
		*v = mark
	case *uint32:
		*v = uint32(mask + 2000)
	case *uint8:
		*v = uint8(mask + 1)
	case *time.Time:
		*v = time.Unix(int64(3000+mask), 0).UTC()
	}
}

func TestLogDestScanOrder(t *testing.T) {
	for _, origin := range []bool{false, true} {
		for mask := 0; mask < 8; mask++ {
			c := &CH{
				netCols: mask&1 != 0, clickCols: mask&2 != 0,
				extEvents: mask&4 != 0, modelEvents: true,
			}
			extCols := columnsOf("events", waveExt)
			if !c.extEvents {
				extCols = nil
			}
			var r LogRow
			dest, subs := logDest(c, &r, extCols, origin)
			cols := logJournalCols(c, extCols, origin)
			if len(dest) != len(cols) {
				t.Fatalf("origin=%v mask=%d: %d dest vs %d cols", origin, mask, len(dest), len(cols))
			}
			for i, col := range cols {
				writeLogScanMark(dest[i], col, mask)
			}
			check := []string{"device_model", "uniq", "group_name"}
			if c.extEvents {
				check = append(check, "site")
			}
			if c.clickCols {
				check = append(check, "offer_id")
			}
			if origin {
				check = append(check, "origin_group_name", "origin_group_id")
			}
			for _, col := range check {
				got, ok := readLogCol(&r, subs, col)
				if !ok {
					continue
				}
				want := fmt.Sprintf("log_%s_%d", col, mask)
				switch v := got.(type) {
				case string:
					if v != want {
						t.Fatalf("origin=%v mask=%d col=%q got %q want %q", origin, mask, col, v, want)
					}
				case uint8:
					if v != uint8(mask+1) {
						t.Fatalf("origin=%v mask=%d col=%q got %v", origin, mask, col, v)
					}
				}
			}
		}
	}
}
