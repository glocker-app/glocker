package stats

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Rule is one categorization rule: optional program/title regexes -> tag.
// Mirrors the client-side model in the embedded frontend.
type Rule struct {
	Program string `json:"program"`
	Title   string `json:"title"`
	Tag     string `json:"tag"`
}

// rulesConfig is the persisted usage config: rules plus a per-tag colour map.
type rulesConfig struct {
	Rules  []Rule            `json:"rules"`
	Colors map[string]string `json:"colors"`
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// sanitizeRules drops rules with no tag and trims fields; never returns nil so
// the JSON is always an array.
func sanitizeRules(in []Rule) []Rule {
	out := []Rule{}
	for _, r := range in {
		tag := strings.TrimSpace(r.Tag)
		if tag == "" {
			continue
		}
		out = append(out, Rule{
			Program: strings.TrimSpace(r.Program),
			Title:   strings.TrimSpace(r.Title),
			Tag:     tag,
		})
	}
	return out
}

// sanitizeColors keeps only tag -> #rrggbb entries; never returns nil.
func sanitizeColors(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if strings.TrimSpace(k) != "" && hexColor.MatchString(v) {
			out[k] = strings.ToLower(v)
		}
	}
	return out
}

// parseConfigBytes decodes either a { rules, colors } object or a legacy bare
// rules array, sanitizing the result. A corrupt payload yields empty config.
func parseConfigBytes(data []byte) ([]Rule, map[string]string) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return []Rule{}, map[string]string{}
	}
	if trimmed[0] == '[' {
		var arr []Rule
		_ = json.Unmarshal(trimmed, &arr)
		return sanitizeRules(arr), map[string]string{}
	}
	var obj rulesConfig
	_ = json.Unmarshal(trimmed, &obj)
	return sanitizeRules(obj.Rules), sanitizeColors(obj.Colors)
}

