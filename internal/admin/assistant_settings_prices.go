package admin

import (
	"regexp"
	"strings"
)

// aiDefaultPrices is the starting price table in US dollars per million tokens, by model
// id. A model is matched by exact id first, then by the longest listed id that the model id
// extends only by a dated or preview revision of the same model (see aiRevisionRe), after
// the vendor prefix of OpenRouter ids ("anthropic/", "openai/", "google/") is removed.
// Any other extension names another model, which has no price here. The values are starting
// points: the administrator corrects them in the settings, and a model with no price here
// and none set has no USD accounting.
var aiDefaultPrices = map[string]aiPrice{
	"claude-opus-4":         {InputPerM: 15, OutputPerM: 75},
	"claude-opus-4-1":       {InputPerM: 15, OutputPerM: 75},
	"claude-sonnet-4":       {InputPerM: 3, OutputPerM: 15},
	"claude-sonnet-4-5":     {InputPerM: 3, OutputPerM: 15},
	"claude-haiku-4-5":      {InputPerM: 1, OutputPerM: 5},
	"claude-3-7-sonnet":     {InputPerM: 3, OutputPerM: 15},
	"claude-3-5-sonnet":     {InputPerM: 3, OutputPerM: 15},
	"claude-3-5-haiku":      {InputPerM: 0.8, OutputPerM: 4},
	"claude-haiku-4":        {InputPerM: 1, OutputPerM: 5},
	"gpt-4o":                {InputPerM: 2.5, OutputPerM: 10},
	"gpt-4o-mini":           {InputPerM: 0.15, OutputPerM: 0.6},
	"gpt-4.1":               {InputPerM: 2, OutputPerM: 8},
	"gpt-4.1-mini":          {InputPerM: 0.4, OutputPerM: 1.6},
	"gpt-4.1-nano":          {InputPerM: 0.1, OutputPerM: 0.4},
	"o3":                    {InputPerM: 2, OutputPerM: 8},
	"o3-mini":               {InputPerM: 1.1, OutputPerM: 4.4},
	"o4-mini":               {InputPerM: 1.1, OutputPerM: 4.4},
	"gemini-2.5-pro":        {InputPerM: 1.25, OutputPerM: 10},
	"gemini-2.5-flash":      {InputPerM: 0.3, OutputPerM: 2.5},
	"gemini-2.5-flash-lite": {InputPerM: 0.1, OutputPerM: 0.4},
	"gemini-2.0-flash":      {InputPerM: 0.1, OutputPerM: 0.4},
}

// aiRevisionRe matches what a model id may add to a priced id and still be the same model:
// a date (-YYYYMMDD, -YYYY-MM-DD), a month-day or a three-digit revision, -latest, or
// -preview with an optional date. "-pro", "-mini", "-audio-preview" and the like are other
// models and do not match.
var aiRevisionRe = regexp.MustCompile(`^-(?:\d{8}|\d{4}-\d{2}-\d{2}|\d{4}|\d{3}|latest|preview(?:-\d{8}|-\d{4}-\d{2}-\d{2}|-\d{2}-\d{2})?)$`)

// aiNormalizeModel returns the id used to look a model up in the price table: lower case,
// without the "models/" and vendor prefixes, and with dots of Claude versions as dashes.
func aiNormalizeModel(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	id = strings.TrimPrefix(id, "models/")
	for _, vendor := range []string{"anthropic/", "openai/", "google/"} {
		if rest, ok := strings.CutPrefix(id, vendor); ok {
			id = rest
			break
		}
	}
	if strings.HasPrefix(id, "claude-") {
		id = strings.ReplaceAll(id, ".", "-")
	}
	return id
}

// aiPriceOverride returns the administrator price of model from over, by the id as given or
// by its normalized form, and whether there is one.
func aiPriceOverride(model string, over map[string]aiPrice) (aiPrice, bool) {
	if p, ok := over[model]; ok {
		return p, true
	}
	p, ok := over[aiNormalizeModel(model)]
	return p, ok
}

// aiPriceTable returns the price of model in the built-in table and whether there is one.
func aiPriceTable(model string) (aiPrice, bool) {
	id := aiNormalizeModel(model)
	if p, ok := aiDefaultPrices[id]; ok {
		return p, true
	}
	best := ""
	for k := range aiDefaultPrices {
		rest, ok := strings.CutPrefix(id, k)
		if ok && len(k) > len(best) && aiRevisionRe.MatchString(rest) {
			best = k
		}
	}
	if best == "" {
		return aiPrice{}, false
	}
	return aiDefaultPrices[best], true
}

// aiCost returns the price of in input and out output tokens in US dollars.
func aiCost(p aiPrice, in, out int) float64 {
	return float64(in)*p.InputPerM/1e6 + float64(out)*p.OutputPerM/1e6
}
