package admin

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Built-in values of the assistant settings. A limit left out of ai.json takes the
// default; a limit stored as zero means no limit.
const (
	aiDefaultMaxOutput             = 2048
	aiDefaultMaxSteps              = 6
	aiDefaultTurnInput             = 60000
	aiDefaultDailyTokens     int64 = 2_000_000
	aiDefaultUserDailyTokens int64 = 300_000
	aiDefaultDailyUSD              = 5.0
	aiDefaultUserDailyUSD          = 2.0
)

// Bounds of the values a PUT accepts. aiMaxOutputCap, aiMaxStepsCap and aiTurnInputCap are the
// hard ceilings of the per-turn limits, which have no unlimited form: 32000 output tokens is
// the largest answer the supported models give, 20 steps bounds the tool loop, and 200000
// input tokens is the context window of the smallest supported model. The daily limits take
// 0 for no limit and are bound by aiTokensCap and aiUSDCap.
const (
	aiSettingsBody  = 1 << 18
	aiMaxOutputCap  = 32000
	aiMaxStepsCap   = 20
	aiTurnInputCap  = 200_000
	aiTokensCap     = 1_000_000_000_000
	aiUSDCap        = 1_000_000
	aiPriceCap      = 100_000
	aiMaxModels     = 1000
	aiModelIDMax    = 128
	aiKeyMax        = 512
	aiKeyMaskPrefix = "••••"
)

// aiLimits holds the effective limits of the settings: defaults applied, zero meaning no limit
// for the four daily values.
type aiLimits struct {
	MaxOutput       int
	MaxSteps        int
	TurnInput       int
	DailyTokens     int64
	UserDailyTokens int64
	DailyUSD        float64
	UserDailyUSD    float64
}

// aiOr returns *p, or def when p is nil.
func aiOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// aiClampTurn returns the effective per-turn limit for v: def when v is not positive,
// ceiling when v is above it, otherwise v.
func aiClampTurn(v, def, ceiling int) int {
	switch {
	case v <= 0:
		return def
	case v > ceiling:
		return ceiling
	}
	return v
}

// aiClampDaily returns the stored daily limit p made safe: nil (the default) for a negative
// or NaN value, ceiling for a value above it, otherwise p itself. Zero stays zero, no limit.
func aiClampDaily[T int64 | float64](p *T, ceiling T) *T {
	if p == nil {
		return nil
	}
	switch {
	case math.IsNaN(float64(*p)) || *p < 0:
		return nil
	case *p > ceiling:
		v := ceiling
		return &v
	}
	return p
}

// limits returns the effective limits of st: the per-turn values inside 1..ceiling and the
// daily values clamped, so a hand-edited ai.json cannot get past a ceiling.
func (st aiSettings) limits() aiLimits {
	return aiLimits{
		MaxOutput:       aiClampTurn(st.MaxOutputTokens, aiDefaultMaxOutput, aiMaxOutputCap),
		MaxSteps:        aiClampTurn(st.MaxSteps, aiDefaultMaxSteps, aiMaxStepsCap),
		TurnInput:       aiClampTurn(st.TurnInputTokens, aiDefaultTurnInput, aiTurnInputCap),
		DailyTokens:     aiOr(aiClampDaily(st.DailyTokens, aiTokensCap), aiDefaultDailyTokens),
		UserDailyTokens: aiOr(aiClampDaily(st.UserDailyTokens, aiTokensCap), aiDefaultUserDailyTokens),
		DailyUSD:        aiOr(aiClampDaily(st.DailyUSD, aiUSDCap), aiDefaultDailyUSD),
		UserDailyUSD:    aiOr(aiClampDaily(st.UserDailyUSD, aiUSDCap), aiDefaultUserDailyUSD),
	}
}

// aiNormalize fills what a stored file may lack: a known provider, non-nil maps and
// lists, and the effective per-turn limits (default for a missing or non-positive value,
// the ceiling for a larger one). Daily limits that are negative or above their ceiling are
// reset to the default or cut to the ceiling; zero stays zero. Keys of unknown providers and
// empty keys are dropped, and the permissions are made safe by aiNormalizePerms.
func aiNormalize(st aiSettings) aiSettings {
	if !aiKnownProvider(st.Provider) {
		st.Provider = aiProviderAnthropic
	}
	keys := make(map[string]string, len(st.Keys))
	for p, k := range st.Keys {
		if k != "" && aiKnownProvider(p) {
			keys[p] = k
		}
	}
	st.Keys = keys
	if st.AllowedModels == nil {
		st.AllowedModels = []string{}
	}
	lim := st.limits()
	st.MaxOutputTokens, st.MaxSteps, st.TurnInputTokens = lim.MaxOutput, lim.MaxSteps, lim.TurnInput
	st.DailyTokens = aiClampDaily(st.DailyTokens, aiTokensCap)
	st.UserDailyTokens = aiClampDaily(st.UserDailyTokens, aiTokensCap)
	st.DailyUSD = aiClampDaily(st.DailyUSD, aiUSDCap)
	st.UserDailyUSD = aiClampDaily(st.UserDailyUSD, aiUSDCap)
	st.Perms = aiNormalizePerms(st.Perms)
	return st
}

// aiKnownProvider reports whether id is one of the supported providers.
func aiKnownProvider(id string) bool {
	for _, p := range aiProviderIDs {
		if p == id {
			return true
		}
	}
	return false
}

// aiLoad reads ai.json. A missing file, or an empty Config.AIFile, gives the defaults.
// A file that cannot be read or parsed gives the defaults and an error that never holds
// the content, so a caller may repair the file with a PUT.
func (s *Server) aiLoad() (aiSettings, error) {
	st := aiSettings{Provider: aiProviderAnthropic}
	if s.cfg.AIFile == "" {
		return aiNormalize(st), nil
	}
	raw, err := os.ReadFile(s.cfg.AIFile)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return aiNormalize(st), nil
	case err != nil:
		return aiNormalize(aiSettings{}), errors.New("ai.json cannot be read")
	}
	if json.Unmarshal(raw, &st) != nil {
		return aiNormalize(aiSettings{}), errors.New("ai.json is not valid JSON")
	}
	return aiNormalize(st), nil
}

// aiSave writes st to ai.json with mode 0600, atomically. The caller holds s.ai.mu.
func (s *Server) aiSave(st aiSettings) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.cfg.AIFile, append(raw, '\n'), 0o600)
}

// aiProviderFor builds the provider client for the provider and the key stored in st.
// It returns an ai_bad_key error when the key is not set.
func (s *Server) aiProviderFor(st aiSettings) (aiProvider, error) {
	key := st.Keys[st.Provider]
	if key == "" {
		return nil, newAIError(http.StatusBadRequest, aiErrBadKey, "no API key is set for the provider")
	}
	return s.aiNewProvider(st.Provider, key)
}

// aiNewProvider builds the client of provider with key. The base address and the HTTP
// client come from the Config when a test injects them. An unknown provider is an error.
func (s *Server) aiNewProvider(provider, key string) (aiProvider, error) {
	base, hc := s.aiBaseURL(provider), s.aiHTTPClient(provider)
	switch provider {
	case aiProviderAnthropic:
		return newAnthropicProvider(base, key, hc), nil
	case aiProviderOpenAI, aiProviderOpenRouter, aiProviderGemini:
		return newOpenAIProvider(provider, base, key, hc), nil
	}
	return nil, errors.New("unknown provider")
}

// aiKeyMask returns the mask shown for a stored key: four bullets and the last four
// characters, or the bullets alone for a short key. An empty key has no mask.
func aiKeyMask(key string) string {
	switch {
	case key == "":
		return ""
	case len(key) < 12:
		return aiKeyMaskPrefix
	}
	return aiKeyMaskPrefix + key[len(key)-4:]
}

// aiKeyIsMask reports whether v means "keep the stored key": empty, made of mask
// characters only, or starting with the mask the settings view shows.
func aiKeyIsMask(v string) bool {
	return isKeitaroKeyMask(v) || strings.HasPrefix(strings.TrimSpace(v), aiKeyMaskPrefix)
}

// aiPlainASCII reports whether v is 1..limit printable ASCII characters without spaces.
func aiPlainASCII(v string, limit int) bool {
	if v == "" || len(v) > limit {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] <= 0x20 || v[i] >= 0x7f {
			return false
		}
	}
	return true
}

// aiKeyPrefixes maps a key prefix that only one provider issues to that provider. Longer prefixes
// come first, so "sk-ant-" and "sk-or-" win over the bare "sk-" of OpenAI.
var aiKeyPrefixes = []struct{ prefix, provider string }{
	{"sk-ant-", aiProviderAnthropic},
	{"sk-or-", aiProviderOpenRouter},
	{"AIza", aiProviderGemini},
	{"sk-", aiProviderOpenAI},
}

// aiKeyProvider returns the provider whose key format key has, empty when the format is not known.
func aiKeyProvider(key string) string {
	for _, p := range aiKeyPrefixes {
		if strings.HasPrefix(key, p.prefix) {
			return p.provider
		}
	}
	return ""
}

// aiCleanKey trims a typed key of provider prov and checks it. field names the request field in
// the error. It returns the trimmed key, or ai_bad_request with reason invalid for a key that is
// not 1..aiKeyMax printable ASCII characters, or reason wrong_provider (params provider: the
// provider the key belongs to) when the key format belongs to another provider.
func aiCleanKey(field, prov, key string) (string, error) {
	key = strings.TrimSpace(key)
	if !aiPlainASCII(key, aiKeyMax) {
		return "", aiBadRequest(field, aiReasonInvalid, "key is not valid")
	}
	if owner := aiKeyProvider(key); owner != "" && owner != prov {
		e := aiBadRequest(field, aiReasonWrongProvider, "the key belongs to provider "+owner)
		e.Params["provider"] = owner
		return "", e
	}
	return key, nil
}

// aiSettingsPut is the body of PUT /api/ai/settings. A nil field is left as stored.
// Keys maps a provider to a new key; an empty or masked value keeps the stored key and
// ClearKeys removes keys. Prices replaces the whole map of administrator prices and Perms the
// assistant permissions.
type aiSettingsPut struct {
	Enabled         *bool               `json:"enabled"`
	Provider        *string             `json:"provider"`
	Keys            map[string]string   `json:"keys"`
	ClearKeys       []string            `json:"clear_keys"`
	Model           *string             `json:"model"`
	AllowedModels   *[]string           `json:"allowed_models"`
	MaxOutputTokens *int                `json:"max_output_tokens"`
	MaxSteps        *int                `json:"max_steps"`
	TurnInputTokens *int                `json:"turn_input_tokens"`
	DailyTokens     *int64              `json:"daily_tokens"`
	UserDailyTokens *int64              `json:"user_daily_tokens"`
	DailyUSD        *float64            `json:"daily_usd"`
	UserDailyUSD    *float64            `json:"user_daily_usd"`
	Prices          *map[string]aiPrice `json:"prices"`
	Perms           *aiPerms            `json:"perms"`
}

// aiBadLimit builds the 400 ai_bad_limit error for the limit field name that accepts min to max.
// The params are field, min and max, so a client can word the refusal itself; the English text
// is the fallback and says that 0 means no limit when min is 0.
func aiBadLimit(name string, min, max float64) *aiError {
	lo, hi := strconv.FormatFloat(min, 'f', -1, 64), strconv.FormatFloat(max, 'f', -1, 64)
	text := name + " must be from " + lo + " to " + hi
	if min == 0 {
		text += ", 0 means no limit"
	}
	return newAIErrorP(http.StatusBadRequest, aiErrBadLimit, text,
		map[string]any{"field": name, "min": min, "max": max})
}

// aiMergeCount sets *dst to *src when src is set, after checking that it is a finite number
// from 0 (no limit) to limit. name is the field name for the ai_bad_limit error.
func aiMergeCount[T int64 | float64](dst **T, src *T, limit T, name string) error {
	if src == nil {
		return nil
	}
	f := float64(*src)
	if math.IsNaN(f) || math.IsInf(f, 0) || *src < 0 || *src > limit {
		return aiBadLimit(name, 0, float64(limit))
	}
	v := *src
	*dst = &v
	return nil
}

// aiMergeSettings applies a PUT body on top of the stored settings.
// cur is the stored (normalized) settings; the result is what to store. It returns an *aiError:
// ai_bad_request for a bad field (params field and reason, perms included), ai_bad_model with the reason
// not_allowed when the default model is not in allowed_models, ai_bad_limit for a limit out of range.
// The result may be enabled only with a non-empty allowed_models (ai_bad_request, field
// allowed_models, reason required) and with a key of its provider (field keys, reason required),
// also in the test mode of Config.AITestBaseURL, which takes a placeholder key; a disabled
// assistant may keep an empty list and no key.
func aiMergeSettings(cur aiSettings, p aiSettingsPut) (aiSettings, error) {
	next := cur
	next.Keys = make(map[string]string, len(cur.Keys)+len(p.Keys))
	for k, v := range cur.Keys {
		next.Keys[k] = v
	}
	if p.Enabled != nil {
		next.Enabled = *p.Enabled
	}
	if p.Provider != nil {
		if !aiKnownProvider(*p.Provider) {
			return cur, aiBadProvider("provider")
		}
		next.Provider = *p.Provider
	}
	for prov, key := range p.Keys {
		if !aiKnownProvider(prov) {
			return cur, aiBadRequest("keys", aiReasonUnknownProvider, "unknown provider in keys")
		}
		if aiKeyIsMask(key) {
			continue
		}
		key, err := aiCleanKey("keys", prov, key)
		if err != nil {
			return cur, err
		}
		next.Keys[prov] = key
	}
	for _, prov := range p.ClearKeys {
		if !aiKnownProvider(prov) {
			return cur, aiBadRequest("clear_keys", aiReasonUnknownProvider, "unknown provider in clear_keys")
		}
		delete(next.Keys, prov)
	}
	if p.AllowedModels != nil {
		list, err := aiCleanModels(*p.AllowedModels)
		if err != nil {
			return cur, err
		}
		next.AllowedModels = list
	}
	if p.Model != nil {
		next.Model = strings.TrimSpace(*p.Model)
	}
	if next.Model != "" && !aiContains(next.AllowedModels, next.Model) {
		return cur, aiBadModelErr("the model is not in allowed_models", next.Model, aiModelNotAllowed)
	}
	if err := aiMergeLimits(&next, p); err != nil {
		return cur, err
	}
	if p.Prices != nil {
		prices, err := aiCleanPrices(*p.Prices)
		if err != nil {
			return cur, err
		}
		next.Prices = prices
	}
	if p.Perms != nil {
		perms, err := aiCleanPerms(*p.Perms)
		if err != nil {
			return cur, err
		}
		next.Perms = perms
	}
	if next.Enabled && len(next.AllowedModels) == 0 {
		return cur, aiBadRequest("allowed_models", aiReasonRequired, "allowed_models is required while the assistant is enabled")
	}
	if next.Enabled && next.Keys[next.Provider] == "" {
		return cur, aiBadRequest("keys", aiReasonRequired, "a key of the provider is required while the assistant is enabled")
	}
	return next, nil
}

// aiMergeLimits applies the limit fields of p to next. A per-turn limit must be from 1 to its
// ceiling (it has no unlimited form); a daily limit may be 0, which is stored as zero and
// means no limit. A value outside its range is an ai_bad_limit error and nothing is applied.
func aiMergeLimits(next *aiSettings, p aiSettingsPut) error {
	for _, c := range []struct {
		src   *int
		dst   *int
		limit int
		name  string
	}{
		{p.MaxOutputTokens, &next.MaxOutputTokens, aiMaxOutputCap, "max_output_tokens"},
		{p.MaxSteps, &next.MaxSteps, aiMaxStepsCap, "max_steps"},
		{p.TurnInputTokens, &next.TurnInputTokens, aiTurnInputCap, "turn_input_tokens"},
	} {
		if c.src == nil {
			continue
		}
		if *c.src < 1 || *c.src > c.limit {
			return aiBadLimit(c.name, 1, float64(c.limit))
		}
		*c.dst = *c.src
	}
	*next = aiNormalize(*next)
	if err := aiMergeCount(&next.DailyTokens, p.DailyTokens, int64(aiTokensCap), "daily_tokens"); err != nil {
		return err
	}
	if err := aiMergeCount(&next.UserDailyTokens, p.UserDailyTokens, int64(aiTokensCap), "user_daily_tokens"); err != nil {
		return err
	}
	if err := aiMergeCount(&next.DailyUSD, p.DailyUSD, float64(aiUSDCap), "daily_usd"); err != nil {
		return err
	}
	return aiMergeCount(&next.UserDailyUSD, p.UserDailyUSD, float64(aiUSDCap), "user_daily_usd")
}

// aiCleanModels trims and de-duplicates a model list, keeping its order.
// An id must be 1..aiModelIDMax printable characters without spaces; the list holds at
// most aiMaxModels ids.
func aiCleanModels(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, m := range in {
		m = strings.TrimSpace(m)
		if !aiPlainASCII(m, aiModelIDMax) {
			return nil, aiBadRequest("allowed_models", aiReasonInvalid, "allowed_models holds a bad model id")
		}
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	if len(out) > aiMaxModels {
		return nil, aiBadRequest("allowed_models", aiReasonTooMany, "too many allowed_models")
	}
	return out, nil
}

// aiCleanPrices checks the administrator price map: ids as for models, both prices
// finite and between 0 and aiPriceCap. An empty map becomes nil.
func aiCleanPrices(in map[string]aiPrice) (map[string]aiPrice, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > aiMaxModels {
		return nil, aiBadRequest("prices", aiReasonTooMany, "too many prices")
	}
	okVal := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= aiPriceCap }
	out := make(map[string]aiPrice, len(in))
	for m, p := range in {
		m = strings.TrimSpace(m)
		if !aiPlainASCII(m, aiModelIDMax) || !okVal(p.InputPerM) || !okVal(p.OutputPerM) {
			return nil, aiBadRequest("prices", aiReasonBadPrice, "prices hold a bad model id or price")
		}
		out[m] = p
	}
	return out, nil
}

// aiContains reports whether list holds v.
func aiContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
