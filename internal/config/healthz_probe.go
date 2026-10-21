package config

// ValidHealthzProbe reports whether probe matches ^[0-9a-f]{1,64}$.
// probe is the raw query value. It returns false when probe is empty, longer than 64, or not lowercase hex.
func ValidHealthzProbe(probe string) bool {
	if len(probe) == 0 || len(probe) > 64 {
		return false
	}
	for i := 0; i < len(probe); i++ {
		c := probe[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
