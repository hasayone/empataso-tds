package config

import (
	"errors"
	"strings"
)

// MainBoardID is the id of the default panel section.
const MainBoardID = "main"

// ErrInvalidBoardIcon is returned when icon is not an allowed panel icon key.
var ErrInvalidBoardIcon = errors.New("config: invalid board icon")

// allowedBoardIcons lists icon keys accepted by the admin API (same set as the panel SPA).
var allowedBoardIcons = map[string]struct{}{
	"dashboard": {}, "flows": {}, "logs": {}, "menu": {}, "conversions": {}, "keywords": {},
	"lists": {}, "tools": {}, "automation": {}, "networks": {}, "sources": {}, "offers": {},
	"landings": {}, "domains": {}, "report": {}, "settings": {}, "logout": {}, "sun": {}, "moon": {},
	"plus": {}, "upload": {}, "x": {}, "check": {}, "back": {}, "arrow": {}, "down": {}, "download": {},
	"link": {}, "copy": {}, "edit": {}, "grip": {}, "more": {}, "play": {}, "trash": {}, "dup": {},
	"move": {}, "target": {}, "bot": {}, "send": {}, "refresh": {}, "table": {}, "cards": {}, "alert": {},
	"calendar": {}, "board": {}, "click": {}, "user": {}, "mail": {}, "trendup": {}, "trenddown": {},
	"banknote": {}, "percent": {}, "chart": {}, "expand": {}, "collapse": {},
	// Extra keys are Lucide icon names (ISC). Copyright Lucide Icons and Contributors.
	"activity": {},
	"alarm-clock": {},
	"anchor": {},
	"aperture": {},
	"archive": {},
	"award": {},
	"axe": {},
	"backpack": {},
	"badge": {},
	"banana": {},
	"barcode": {},
	"bath": {},
	"battery": {},
	"beaker": {},
	"bed": {},
	"beer": {},
	"bell": {},
	"bike": {},
	"bird": {},
	"bluetooth": {},
	"bomb": {},
	"bone": {},
	"book": {},
	"bookmark": {},
	"box": {},
	"brain": {},
	"briefcase": {},
	"brush": {},
	"bug": {},
	"building": {},
	"bus": {},
	"cake": {},
	"calculator": {},
	"camera": {},
	"car": {},
	"cat": {},
	"cherry": {},
	"cigarette": {},
	"clipboard": {},
	"clock": {},
	"cloud": {},
	"clover": {},
	"code": {},
	"coffee": {},
	"coins": {},
	"compass": {},
	"computer": {},
	"cookie": {},
	"cpu": {},
	"credit-card": {},
	"croissant": {},
	"crown": {},
	"database": {},
	"diamond": {},
	"dice-5": {},
	"dog": {},
	"door-open": {},
	"droplet": {},
	"drum": {},
	"dumbbell": {},
	"ear": {},
	"egg": {},
	"euro": {},
	"eye": {},
	"factory": {},
	"feather": {},
	"ferris-wheel": {},
	"file": {},
	"film": {},
	"filter": {},
	"fingerprint": {},
	"fish": {},
	"flag": {},
	"flame": {},
	"flashlight": {},
	"flower": {},
	"folder": {},
	"gamepad-2": {},
	"gauge": {},
	"gem": {},
	"gift": {},
	"glasses": {},
	"globe": {},
	"graduation-cap": {},
	"grape": {},
	"guitar": {},
	"hammer": {},
	"hand": {},
	"hard-drive": {},
	"hash": {},
	"headphones": {},
	"heart": {},
	"home": {},
	"hourglass": {},
	"ice-cream-cone": {},
	"image": {},
	"inbox": {},
	"infinity": {},
	"joystick": {},
	"key": {},
	"keyboard": {},
	"lamp": {},
	"landmark": {},
	"languages": {},
	"laptop": {},
	"layers": {},
	"leaf": {},
	"library": {},
	"lightbulb": {},
	"loader": {},
	"lock": {},
	"luggage": {},
	"magnet": {},
	"map": {},
	"map-pin": {},
	"martini": {},
	"medal": {},
	"megaphone": {},
	"message-circle": {},
	"mic": {},
	"microscope": {},
	"milk": {},
	"monitor": {},
	"mountain": {},
	"mouse": {},
	"music": {},
	"navigation": {},
	"newspaper": {},
	"package": {},
	"paintbrush": {},
	"palette": {},
	"paperclip": {},
	"party-popper": {},
	"paw-print": {},
	"pen": {},
	"pencil": {},
	"phone": {},
	"pie-chart": {},
	"piggy-bank": {},
	"pin": {},
	"pizza": {},
	"plane": {},
	"plug": {},
	"podcast": {},
	"printer": {},
	"puzzle": {},
	"qr-code": {},
	"rabbit": {},
	"radio": {},
	"receipt": {},
	"recycle": {},
	"rocket": {},
	"ruler": {},
	"sailboat": {},
	"satellite": {},
	"save": {},
	"scale": {},
	"scan": {},
	"school": {},
	"scissors": {},
	"scroll": {},
	"search": {},
	"server": {},
	"shield": {},
	"ship": {},
	"shirt": {},
	"shopping-bag": {},
	"shopping-cart": {},
	"shrimp": {},
	"signal": {},
	"siren": {},
	"skull": {},
	"smartphone": {},
	"smile": {},
	"snowflake": {},
	"sofa": {},
	"sparkles": {},
	"speaker": {},
	"star": {},
	"sticker": {},
	"store": {},
	"sword": {},
	"syringe": {},
	"telescope": {},
	"tent": {},
	"terminal": {},
	"thermometer": {},
	"ticket": {},
	"timer": {},
	"tornado": {},
	"tractor": {},
	"train-front": {},
	"tree-pine": {},
	"trophy": {},
	"truck": {},
	"tv": {},
	"umbrella": {},
	"unlock": {},
	"usb": {},
	"utensils": {},
	"vault": {},
	"video": {},
	"volume-2": {},
	"wallet": {},
	"warehouse": {},
	"watch": {},
	"waves": {},
	"webcam": {},
	"wifi": {},
	"wind": {},
	"wine": {},
	"wrench": {},
	"zap": {},
	// Android and iOS outlines are Tabler icons (MIT). Copyright Paweł Kuna.
	"android": {},
	"ios": {},
	"seo": {},
}

// Board member roles stored on a section.
const (
	BoardRoleRead    = "read"
	BoardRoleEdit    = "edit"
	BoardRoleCreated = "created"
)

// ErrInvalidBoardMember is returned when a board members list is invalid.
var ErrInvalidBoardMember = errors.New("config: invalid board member")

// BoardMember is one user's role on a panel section.
type BoardMember struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// allowedBoardIconColors lists icon tint ids accepted on a section.
var allowedBoardIconColors = map[string]struct{}{
	"clicks": {}, "uniq": {}, "conv": {}, "rev": {}, "profit": {},
	"cost": {}, "bots": {}, "roi": {}, "cr": {}, "warn": {},
}

// Board is one panel section stored in boards.json.
type Board struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Icon      string        `json:"icon,omitempty"`
	IconColor string        `json:"icon_color,omitempty"`
	Members   []BoardMember `json:"members,omitempty"`
	// IncludeAll is only meaningful on the main section.
	// Nil and true mean the main view lists rows of other sections the session can open.
	// A false pointer disables that view. True is not stored.
	IncludeAll *bool `json:"include_all,omitempty"`
}

// IncludeAllEnabled reports whether flag shows other sections inside the main view.
// flag is the stored pointer. Nil and true mean enabled. A false pointer means disabled.
// It does not return an error.
func IncludeAllEnabled(flag *bool) bool {
	return flag == nil || *flag
}

// ValidateBoardMembers checks members for allowed roles, non-empty user ids and no duplicates.
func ValidateBoardMembers(members []BoardMember) error {
	if members == nil {
		return nil
	}
	seen := make(map[string]bool, len(members))
	for _, m := range members {
		if strings.TrimSpace(m.UserID) == "" {
			return ErrInvalidBoardMember
		}
		switch m.Role {
		case BoardRoleRead, BoardRoleEdit, BoardRoleCreated:
		default:
			return ErrInvalidBoardMember
		}
		if seen[m.UserID] {
			return ErrInvalidBoardMember
		}
		seen[m.UserID] = true
	}
	return nil
}

// ValidateBoardIcon reports whether icon is empty or an allowed icon key.
func ValidateBoardIcon(icon string) error {
	if icon == "" {
		return nil
	}
	if _, ok := allowedBoardIcons[icon]; !ok {
		return ErrInvalidBoardIcon
	}
	return nil
}

// ValidateBoardIconColor reports whether color is empty or one of the ten icon tints.
// A bad id returns ErrInvalidBoardIcon.
func ValidateBoardIconColor(color string) error {
	if color == "" {
		return nil
	}
	if _, ok := allowedBoardIconColors[color]; !ok {
		return ErrInvalidBoardIcon
	}
	return nil
}

// NormalizeBoardID maps empty and legacy "main" spellings to MainBoardID.
func NormalizeBoardID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || strings.EqualFold(id, MainBoardID) {
		return MainBoardID
	}
	return id
}

// SameBoard reports whether a and b refer to the same section after normalization.
func SameBoard(a, b string) bool {
	return NormalizeBoardID(a) == NormalizeBoardID(b)
}
