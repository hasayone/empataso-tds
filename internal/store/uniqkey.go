package store

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
)

// UniqIdentity is the visitor part of a Redis uniqueness key.
//
// ip is the address. ua is the User-Agent header. byUA false returns ip.String(),
// so a key already stored for the IP method stays the same key. byUA true appends
// sixteen hex characters of SHA-256(ua) and does not put the agent text into the key.
func UniqIdentity(ip netip.Addr, ua string, byUA bool) string {
	if !byUA {
		return ip.String()
	}
	sum := sha256.Sum256([]byte(ua))
	return ip.String() + ":" + hex.EncodeToString(sum[:8])
}
