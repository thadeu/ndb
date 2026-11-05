package tailer

import (
	"path"
	"strings"
)

// GlobMatch is path.Match with "**" for zero or more directories.
func GlobMatch(pattern, name string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(name); i++ {
				if matchSegs(pat[1:], name[i:]) {
					return true
				}
			}

			return false
		}

		if len(name) == 0 {
			return false
		}

		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}

		pat, name = pat[1:], name[1:]
	}

	return len(name) == 0
}

// globBase is the directory to walk: the segments before the first one
// with a wildcard. depth is how many levels below it a match can be (-1
// when the pattern has "**").
func globBase(pattern string) (base string, depth int) {
	segs := strings.Split(pattern, "/")
	i := 0

	for i < len(segs)-1 && !strings.ContainsAny(segs[i], "*?[") {
		i++
	}

	for _, s := range segs[i:] {
		if s == "**" {
			return strings.Join(segs[:i], "/"), -1
		}
	}

	base = strings.Join(segs[:i], "/")

	if base == "" && strings.HasPrefix(pattern, "/") {
		base = "/"
	}

	return base, len(segs) - i
}
