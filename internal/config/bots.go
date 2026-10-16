package config

// DefaultBotProfile is the installation-wide definition of a bot.
// Empty referer and IPv6 stay off: both match ordinary visitors.
// Search-engine IP lists and UA signatures are on.
func DefaultBotProfile() Bots {
	return Bots{
		CheckUA:  true,
		EmptyUA:  true,
		IPBaidu:  true,
		IPBing:   true,
		IPGoogle: true,
		IPMail:   true,
		IPYahoo:  true,
		IPYandex: true,
		IPOthers: true,
	}
}
