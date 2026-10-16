package main

import (
	"encoding/base64"
	"encoding/json"
)

// apiRequest — data from an API client as base64(JSON).
type apiRequest struct {
	KeyAPI    string   `json:"key_api"`
	ID        string   `json:"id"`
	IP        string   `json:"ip"`
	Referer   string   `json:"referer"`
	UserAgent string   `json:"useragent"`
	SE        string   `json:"se"`
	Lang      string   `json:"lang"`
	Uniq      string   `json:"uniq"`
	Key       string   `json:"key"`
	Domain    string   `json:"domain"`
	Page      string   `json:"page"`
	CFCountry string   `json:"cf_country"`
	Pars      []string `json:"pars"`
}

// parseAPIRequest decodes param as base64(JSON) and checks the API key.
//
// param is the ?api= query value, standard base64 or URL-safe base64. verify
// reports whether the decoded key_api is accepted; a nil verify rejects the
// request. A decode error, a JSON error, or a rejected key returns nil and
// false. On success the decoded request is returned and the second result
// is true.
func parseAPIRequest(param string, verify func(string) bool) (*apiRequest, bool) {
	raw, err := base64.StdEncoding.DecodeString(param)
	if err != nil {
		if raw, err = base64.URLEncoding.DecodeString(param); err != nil {
			return nil, false
		}
	}
	var req apiRequest
	if json.Unmarshal(raw, &req) != nil {
		return nil, false
	}
	if verify == nil || !verify(req.KeyAPI) {
		return nil, false
	}
	return &req, true
}
