package admin

import (
	"crypto/rand"
	"errors"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
)

const (
	// campaignIDAlphabet is the character set of a new campaign slug, the same
	// set as CAMPAIGN_ID_ALPHABET in the panel: letters and digits without 0, O, 1, l and I.
	campaignIDAlphabet = "23456789abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ"
	// campaignIDLen is the length of a slug drawn for a new campaign or a copy.
	campaignIDLen = 9
)

// placeholderCampaignID reports whether id is a slug the server must replace.
// id is the campaign slug from the client. An empty id, flow_0000, flow_0001
// and the old panel form flow plus digits (flow0, flow1) are placeholders.
// Any other slug, including a number or an imported alias, is the caller's own.
// It does not return an error.
func placeholderCampaignID(id string) bool {
	if id == "" {
		return true
	}
	rest, ok := strings.CutPrefix(id, "flow")
	if !ok || rest == "" {
		return false
	}
	rest = strings.TrimPrefix(rest, "_")
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// campaignIDSet returns every campaign id and alias in groups.
// groups is the list to read. An empty id or alias is left out.
// The set is new. It does not return an error.
func campaignIDSet(groups []config.Group) map[string]struct{} {
	taken := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		if g.ID != "" {
			taken[g.ID] = struct{}{}
		}
		for _, a := range g.Aliases {
			if a != "" {
				taken[a] = struct{}{}
			}
		}
	}
	return taken
}

// freshCampaignID returns a campaign slug of campaignIDLen characters from campaignIDAlphabet
// that is not a key of taken. taken is the set of ids and aliases already used; a nil set
// means nothing is used, and the set is not modified.
// It returns an error when crypto/rand cannot supply bytes or every draw is already taken.
func freshCampaignID(taken map[string]struct{}) (string, error) {
	// limit is the largest multiple of the alphabet size below 256, so a byte at or
	// above it is skipped and every character is equally likely.
	const limit = 256 - 256%len(campaignIDAlphabet)
	buf := make([]byte, 32)
	for attempt := 0; attempt < 16; attempt++ {
		body := make([]byte, 0, campaignIDLen)
		for len(body) < campaignIDLen {
			if _, err := rand.Read(buf); err != nil {
				return "", err
			}
			for _, b := range buf {
				if int(b) < limit && len(body) < campaignIDLen {
					body = append(body, campaignIDAlphabet[int(b)%len(campaignIDAlphabet)])
				}
			}
		}
		id := string(body)
		if _, used := taken[id]; !used {
			return id, nil
		}
	}
	return "", errors.New("campaign id is taken")
}

// fillNewCampaignIDs gives a random slug to every new campaign whose id is empty or a sequential stub.
// stored is the list already saved. groups is the candidate list and is updated in place.
// A campaign whose id is already in stored keeps that id, including a number, an imported
// alias and a legacy flow_0000. A new campaign with any other id keeps the id the client sent.
// replaced maps a non-empty stub id to the slug that took its place; an empty stub is only
// visible on the row. It returns an error when a slug cannot be drawn, and groups is then
// only partly updated.
func fillNewCampaignIDs(stored, groups []config.Group) (map[string]string, error) {
	storedIDs := make(map[string]struct{}, len(stored))
	for _, g := range stored {
		if g.ID != "" {
			storedIDs[g.ID] = struct{}{}
		}
	}
	taken := campaignIDSet(stored)
	for i := range groups {
		id := groups[i].ID
		if _, exists := storedIDs[id]; exists || placeholderCampaignID(id) {
			continue
		}
		taken[id] = struct{}{}
		for _, a := range groups[i].Aliases {
			if a != "" {
				taken[a] = struct{}{}
			}
		}
	}
	replaced := map[string]string{}
	for i := range groups {
		id := groups[i].ID
		if _, exists := storedIDs[id]; exists || !placeholderCampaignID(id) {
			continue
		}
		next, err := freshCampaignID(taken)
		if err != nil {
			return replaced, err
		}
		groups[i].ID = next
		taken[next] = struct{}{}
		for _, a := range groups[i].Aliases {
			if a != "" {
				taken[a] = struct{}{}
			}
		}
		if id != "" {
			replaced[id] = next
		}
	}
	return replaced, nil
}
