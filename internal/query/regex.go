package query

import (
	"regexp"
	"regexp/syntax"
	"strings"
)

// maxPatternLen is the longest $regex pattern, in bytes.
const maxPatternLen = 1024

// regexOpts are the $options letters. RE2 has no x flag: it is applied by
// stripping the white space and the comments from the pattern.
const regexOpts = "imsx"

// compileRegex builds the regular expression of a $regex and $options pair.
// The syntax is Go RE2: no backreferences and no look-around.
func compileRegex(pattern, options string) (*regexp.Regexp, string, error) {
	if len(pattern) > maxPatternLen {
		return nil, "", invalid("$regex pattern is longer than %d bytes", maxPatternLen)
	}

	var flags strings.Builder

	extended := false

	for _, c := range options {
		if !strings.ContainsRune(regexOpts, c) {
			return nil, "", invalid("unknown $options flag %q: supported flags are i, m, s, x", c)
		}

		if c == 'x' {
			extended = true

			continue
		}

		if !strings.ContainsRune(flags.String(), c) {
			flags.WriteRune(c)
		}
	}

	if extended {
		pattern = stripExtended(pattern)
	}

	full := pattern

	if flags.Len() > 0 {
		full = "(?" + flags.String() + ")" + pattern
	}

	re, err := regexp.Compile(full)
	if err != nil {
		return nil, "", invalid("$regex: %v", err)
	}

	prefix := ""

	if !strings.ContainsAny(flags.String(), "im") {
		prefix = anchoredPrefix(full)
	}

	return re, prefix, nil
}

// stripExtended drops the unescaped white space and the "#" comments that
// are outside a character class, as the x option says.
func stripExtended(p string) string {
	var (
		sb      strings.Builder
		inClass bool
	)

	for i := 0; i < len(p); i++ {
		c := p[i]

		switch {
		case c == '\\' && i+1 < len(p):
			sb.WriteByte(c)
			sb.WriteByte(p[i+1])

			i++
		case inClass:
			i = copyClassByte(&sb, p, i, &inClass)
		case c == '[':
			inClass = true

			sb.WriteByte(c)

			i = copyClassStart(&sb, p, i)
		case c == '#':
			for i < len(p) && p[i] != '\n' {
				i++
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
		default:
			sb.WriteByte(c)
		}
	}

	return sb.String()
}

// copyClassStart copies the "^" and the first "]" that open a class: that
// "]" is a literal, not the end.
func copyClassStart(sb *strings.Builder, p string, i int) int {
	if i+1 < len(p) && p[i+1] == '^' {
		sb.WriteByte('^')

		i++
	}

	if i+1 < len(p) && p[i+1] == ']' {
		sb.WriteByte(']')

		i++
	}

	return i
}

// copyClassByte copies one byte inside a class, with a "[:name:]" whole.
func copyClassByte(sb *strings.Builder, p string, i int, inClass *bool) int {
	c := p[i]

	if c == '[' && i+1 < len(p) && p[i+1] == ':' {
		if end := strings.Index(p[i:], ":]"); end > 0 {
			sb.WriteString(p[i : i+end+2])

			return i + end + 1
		}
	}

	if c == ']' {
		*inClass = false
	}

	sb.WriteByte(c)

	return i
}

// anchoredPrefix is the literal that every match must start with, for a
// pattern that begins with "^" at the start of the text. It is empty when
// there is none: the pattern is not anchored, starts with a group or an
// alternation, or folds case.
func anchoredPrefix(pattern string) string {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return ""
	}

	re = re.Simplify()

	if re.Op != syntax.OpConcat || len(re.Sub) < 2 || re.Sub[0].Op != syntax.OpBeginText {
		return ""
	}

	lit := re.Sub[1]

	if lit.Op != syntax.OpLiteral || lit.Flags&syntax.FoldCase != 0 {
		return ""
	}

	return string(lit.Rune)
}

// prefixSuccessor is the smallest string above every string that starts
// with p. It is empty when there is none (p is all 0xFF bytes).
func prefixSuccessor(p string) string {
	b := []byte(p)

	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xFF {
			b[i]++

			return string(b[:i+1])
		}
	}

	return ""
}

// matchRegex reports whether any string candidate matches. Non-strings never
// do. A time field keeps its RFC 3339 strings in Raw.
func matchRegex(re *regexp.Regexp, cands []Value) bool {
	for _, c := range cands {
		switch c.Kind {
		case String:
			if re.MatchString(c.Str) {
				return true
			}
		case Time:
			if s, ok := c.Raw.(string); ok && re.MatchString(s) {
				return true
			}
		}
	}

	return false
}
