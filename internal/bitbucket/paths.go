package bitbucket

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// pathSegment validates and escapes a single URL path segment (workspace,
// repo slug, branch name, commit hash, UUID, ...). It rejects empty values and
// the dot-segments "." and "..", which url.PathEscape leaves intact and which
// would let a value re-aim the request at a different endpoint.
func pathSegment(name, v string) (string, error) {
	if v == "" || v == "." || v == ".." {
		return "", fmt.Errorf("invalid %s %q: must not be empty, '.' or '..'", name, v)
	}
	if hasDotPart(v) {
		return "", fmt.Errorf("invalid %s %q: must not contain '.' or '..' path components", name, v)
	}
	return url.PathEscape(v), nil
}

// pathUUID validates and escapes an identifier that never legitimately
// contains a path separator (pipeline and step UUIDs). On top of the
// pathSegment checks it rejects '/' and '\\' outright, so the value cannot be
// smuggled through as an encoded %2F sequence.
func pathUUID(name, v string) (string, error) {
	if strings.ContainsAny(v, `/\`) {
		return "", fmt.Errorf("invalid %s %q: must not contain '/' or '\\'", name, v)
	}
	return pathSegment(name, v)
}

// hasDotPart reports whether any '/'-separated part of v is "." or "..".
func hasDotPart(v string) bool {
	for _, s := range strings.Split(v, "/") {
		if s == "." || s == ".." {
			return true
		}
	}
	return false
}

// pathMulti validates and escapes a value that may span several path
// segments, such as a file path ("a/b.go") or a diff spec ("main..feature",
// "feature/x..main"). Any '/'-separated segment equal to "." or ".." and any
// control character is rejected. Each segment is then escaped individually
// (so '?', '#' and '%' become %3F, %23 and %25) and rejoined with '/'. A
// trailing slash is preserved.
func pathMulti(name, v string) (string, error) {
	if v == "" {
		return "", fmt.Errorf("invalid %s: must not be empty", name)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("invalid %s: must not contain control characters", name)
		}
	}
	segs := strings.Split(v, "/")
	for i, s := range segs {
		if s == "." || s == ".." {
			return "", fmt.Errorf("invalid %s %q: must not contain '.' or '..' path segments", name, v)
		}
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/"), nil
}

// rejectDotSegments is a last line of defence applied to every request path:
// after percent-decoding, no path segment (or '/'-separated part of one)
// may be "." or "..".
func rejectDotSegments(path string) error {
	p := path
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	for _, s := range strings.Split(p, "/") {
		d, err := url.PathUnescape(s)
		if err != nil {
			return fmt.Errorf("invalid request path: %w", err)
		}
		// A decoded segment may itself contain '/' (from %2F); check each part.
		if hasDotPart(d) {
			return fmt.Errorf("invalid request path %q: dot segments are not allowed", path)
		}
	}
	return nil
}
