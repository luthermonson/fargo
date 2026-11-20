package reconcile

import "strings"

// SanitizeID builds a transaction id of the form
// <mode>/<management-namespace>/<object-name>/<revision>, with each segment
// cleaned so it is safe to use in a URL path and as a ledger config key. The
// revision is often a git SHA or a Flux source revision like
// "main@sha1:abcd"; everything that is not a safe character becomes '-'.
func SanitizeID(mode, namespace, name, revision string) string {
	segs := []string{
		sanitizeSegment(mode),
		sanitizeSegment(namespace),
		sanitizeSegment(name),
		sanitizeSegment(revision),
	}
	return strings.Join(segs, "/")
}

func sanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "_"
	}
	// Keep segments bounded so the full id stays a reasonable ledger key.
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}
