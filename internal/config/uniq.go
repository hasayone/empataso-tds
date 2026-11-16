package config

// UniqCookieMaxSeconds is the longest signed uniqueness cookie: 8760 hours.
// Redis keeps its own ceiling and does not use this limit.
const UniqCookieMaxSeconds = 8760 * 3600

// UniqPlan is how one campaign tells a new visitor from a repeat.
type UniqPlan struct {
	// LegacyCookie is the old cookie method: a plain cookie and no Redis.
	LegacyCookie bool
	// Cookie is the signed cookie that lives for the campaign hours.
	Cookie bool
	// ByUA puts a hash of the user-agent into the Redis key.
	ByUA bool
}

// UniqPlanOf reads the uniqueness plan of g.
//
// A nil group, and a group with neither UniqBy nor UniqCookie, keeps the legacy
// switch: UniqMethod "cookie" is LegacyCookie, anything else is Redis by IP.
// Otherwise UniqCookie and UniqBy decide, and UniqMethod is ignored.
// "ip_ua" is the only value that sets ByUA.
func UniqPlanOf(g *Group) UniqPlan {
	if g == nil || (g.UniqBy == "" && !g.UniqCookie) {
		return UniqPlan{LegacyCookie: g != nil && g.UniqMethod == "cookie"}
	}
	return UniqPlan{Cookie: g.UniqCookie, ByUA: g.UniqBy == "ip_ua"}
}

// UniqCookieSeconds is the lifetime of the signed uniqueness cookie.
//
// seconds is the campaign window. Zero or less is one day. The result is never
// above UniqCookieMaxSeconds. Redis does not use this value.
func UniqCookieSeconds(seconds int) int {
	if seconds <= 0 {
		return 86400
	}
	if seconds > UniqCookieMaxSeconds {
		return UniqCookieMaxSeconds
	}
	return seconds
}
