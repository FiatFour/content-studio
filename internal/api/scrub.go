package api

import "regexp"

// absPath matches an absolute path with at least two segments, so "and/or" and "1/2" are left alone.
var absPath = regexp.MustCompile(`(^|[\s'"=(:])(/[\w.\-]+(?:/[\w.\-]+)+)`)

// scrubPaths replaces absolute file paths in stored error text with their base name. Stage errors
// are stored verbatim (a rewrite needs the exact reason), so this runs only on the way out.
func scrubPaths(s *string) *string {
	if s == nil {
		return nil
	}
	out := absPath.ReplaceAllStringFunc(*s, func(m string) string {
		sub := absPath.FindStringSubmatch(m)
		p := sub[2]
		for i := len(p) - 1; i >= 0; i-- {
			if p[i] == '/' {
				return sub[1] + "…/" + p[i+1:]
			}
		}
		return m
	})
	return &out
}
