package logbuf

import (
	"time"
	"unicode/utf8"

	"github.com/hasayone/empataso-tds/internal/config"
)

// Event is a single log row (fields match the ClickHouse events schema).
type Event struct {
	Ts        time.Time
	GroupID   string
	GroupName string
	// OriginID and OriginName are the first campaign of the visit. Empty means
	// the store falls back to GroupID and GroupName on insert.
	OriginID   string
	OriginName string
	Stream     string
	Out        string
	Keyword    string
	Redirect   string
	Device     string
	Operator   string
	Country    string
	City       string
	Region     string
	Lang       string
	Uniq       uint8
	Bot        string
	IP         string
	Referer    string
	UserAgent  string
	Domain     string
	Page       string
	SE         string
	OS         string
	OSVersion  string
	Browser    string
	BrowserV   string
	Brand      string
	Counter    uint32
	CID        string
	Postback   string
	ASN        uint32
	Org        string
	Timezone   string
	// Via names the stream that handed the visitor over when the event's
	// group is not the one they entered through: "<group id>/<stream>".
	Via       string
	OfferID   string
	LandingID string
	SourceID  string
	NetworkID string
	// Subs holds sub_id_1 through sub_id_30. A slot is written only when that
	// column is present; the store omits the rest.
	Subs [config.MaxSubID]string
	// CostCents and CostCur are a price that arrived on the click itself.
	// Zero means the click has no incoming price and a manual window may include it.
	CostCents    int64
	CostCur      string
	Site         string
	AdCampaignID string
	CreativeID   string
	ExternalID   string
	XRW          string
	// DeviceModel is the phone or tablet model from the User-Agent or the
	// Sec-CH-UA-Model header. Empty when the visitor did not reveal one.
	DeviceModel string
	// Step is 0 for the first step (a landing or a direct offer) and 1 for the
	// /offer step. Unique counts of a campaign include only step 0.
	Step uint8
	// RootCID is the click id of the first click in the chain. Empty means
	// this click is the root. Old spool segments omit the field and read empty.
	RootCID string `json:"root_cid,omitempty"`
}

// MaxOut is the byte limit of Event.Out: a full page body is not a destination.
const MaxOut = 2048

// CapOut trims the destination of a click to MaxOut bytes on a rune boundary.
// s is the URL, landing id, campaign id or body the visitor was given.
// It returns s unchanged when it fits. It does not return an error.
func CapOut(s string) string {
	if len(s) <= MaxOut {
		return s
	}
	cut := MaxOut
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
