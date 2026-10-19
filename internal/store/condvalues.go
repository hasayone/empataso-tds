package store

import "strings"

// SplitCondList splits an in/nin filter value on commas.
// Only \\ and \, are escapes; any other lone backslash is kept.
func SplitCondList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			if i+1 < len(s) {
				switch s[i+1] {
				case '\\':
					b.WriteByte('\\')
					i++
					continue
				case ',':
					b.WriteByte(',')
					i++
					continue
				}
			}
			b.WriteByte('\\')
			continue
		}
		if s[i] == ',' {
			out = append(out, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(s[i])
	}
	out = append(out, b.String())
	return out
}

// ExpandCondValues normalises raw query values for one operator.
// Call with the operator from the URL before eq/ne are merged into in/nin.
// in and nin split each raw string on commas (only \\ and \, are escapes).
// eq and ne keep each raw string unchanged (commas and backslashes stay as sent).
func ExpandCondValues(op string, raw []string) []string {
	var out []string
	for _, v := range raw {
		switch op {
		case "in", "nin":
			out = append(out, SplitCondList(v)...)
		default:
			out = append(out, v)
		}
	}
	return out
}
