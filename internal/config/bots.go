package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// DefaultBotProfile is the installation-wide definition of a bot.
// Empty referer and IPv6 stay off: both match ordinary visitors.
// Search-engine IP lists, UA signatures and the proxy check are on.
func DefaultBotProfile() Bots {
	return Bots{
		CheckUA:  true,
		EmptyUA:  true,
		Proxy:    true,
		IPBaidu:  true,
		IPBing:   true,
		IPGoogle: true,
		IPMail:   true,
		IPYahoo:  true,
		IPYandex: true,
		IPOthers: true,
	}
}

// profileKeys is the set of JSON keys a bot profile file may hold: the
// detection switches of Bots, without the stream-only redirect, out and header.
var profileKeys = sync.OnceValue(func() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(Bots{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "redirect" && name != "out" && name != "header" {
			keys[name] = true
		}
	}
	return keys
})

// profileObject decodes b as a JSON object. A JSON null is a null value of a
// key, not an object, and gives an error like every other non-object.
func profileObject(b []byte) (map[string]json.RawMessage, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		return nil, fmt.Errorf("bot profile: %w", err)
	}
	if keys == nil {
		return nil, errors.New("bot profile: not a JSON object")
	}
	return keys, nil
}

// isNull reports whether a raw JSON value is null.
func isNull(v json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(v), []byte("null")) }

// ParseBotProfileExplicit decodes the installation-wide bot profile stored by
// the admin panel. A document without ch_proxy, or with "ch_proxy": null, has
// the proxy check on; an explicit value is taken as written, and proxySet
// reports which of the two it was.
// It returns an error when b is not a JSON object; null is not an object.
func ParseBotProfileExplicit(b []byte) (p Bots, proxySet bool, err error) {
	keys, err := profileObject(b)
	if err != nil {
		return Bots{}, false, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return Bots{}, false, fmt.Errorf("bot profile: %w", err)
	}
	v, ok := keys["ch_proxy"]
	proxySet = ok && !isNull(v)
	if !proxySet {
		p.Proxy = true
	}
	return p, proxySet, nil
}

// ParseBotProfile is ParseBotProfileExplicit without the proxySet flag.
func ParseBotProfile(b []byte) (Bots, error) {
	p, _, err := ParseBotProfileExplicit(b)
	return p, err
}

// botProfileFile is Bots with ch_proxy written only when Proxy is set. The
// outer field shadows the embedded one; a pointer to false is written, nil is not.
type botProfileFile struct {
	Proxy *bool `json:"ch_proxy,omitempty"`
	Bots
}

// marshalBotProfile encodes p as indented JSON, with ch_proxy present only
// when proxySet is true. The stream encoding of Bots drops false values.
func marshalBotProfile(p Bots, proxySet bool) ([]byte, error) {
	f := botProfileFile{Bots: p}
	if proxySet {
		f.Proxy = &p.Proxy
	}
	return json.MarshalIndent(f, "", "  ")
}

// MarshalBotProfile encodes p as indented JSON with an explicit ch_proxy, true
// or false, so a later ParseBotProfile does not read a missing key as "on".
// It returns the encoding error, which cannot happen for this type in practice.
func MarshalBotProfile(p Bots) ([]byte, error) { return marshalBotProfile(p, true) }

// MergeBotProfile applies body to the stored profile file and returns the new
// file content. Keys of body replace the stored ones; keys it does not mention
// stay, so a client that does not show a switch (save_ip) cannot lose it.
// A null value removes the key. Keys that are not detection switches,
// including the stream-only redirect, out and header, are dropped from body.
// ch_proxy is written only when body or stored has it as a value, so a profile
// that never chose keeps the default that waits for the Private Relay list;
// {"ch_proxy": null} returns to it. A switch set to false is not written.
// stored may be empty or unreadable, which counts as an empty profile.
// It returns an error when body is not a JSON object or a value is not a boolean.
func MergeBotProfile(stored, body []byte) ([]byte, error) {
	patch, err := profileObject(body)
	if err != nil {
		return nil, err
	}
	merged, err := profileObject(stored)
	if err != nil {
		merged = map[string]json.RawMessage{}
	}
	known := profileKeys()
	for k, v := range merged {
		if !known[k] || isNull(v) {
			delete(merged, k)
		}
	}
	for k, v := range patch {
		switch {
		case !known[k]:
		case isNull(v):
			delete(merged, k)
		default:
			merged[k] = v
		}
	}
	doc, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	var p Bots
	if err := json.Unmarshal(doc, &p); err != nil {
		return nil, fmt.Errorf("bot profile: %w", err)
	}
	_, proxySet := merged["ch_proxy"]
	return marshalBotProfile(p, proxySet)
}