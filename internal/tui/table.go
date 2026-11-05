// Package tui renders command output for people: tables that fit the
// terminal, with byte sizes and numbers a person can read.
//
// Cell width follows the East Asian Width property (golang.org/x/text/width):
// wide and fullwidth runes, which include most emoji, take two cells;
// combining marks take none. A ZWJ emoji sequence (a family, a flag with
// modifiers) is counted rune by rune, so it can be too wide by a few cells.
package tui

import (
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
	"golang.org/x/text/width"
)

// Align is the side a cell sticks to.
type Align int

const (
	Left Align = iota
	Right
)

// Column is one column. A Right column holds numbers; Keep marks a column
// (ids) that is cut only after every other text column is as small as it goes.
type Column struct {
	Title string
	Align Align
	Keep  bool
}

// Table is a header, rows and an optional footer row (totals).
type Table struct {
	Columns []Column
	Rows    [][]string
	Footer  []string
}

// Style is how a table is drawn. Width 0 means no limit.
type Style struct {
	Width   int
	Unicode bool
	Color   bool
}

const (
	defaultWidth = 100
	minCellWidth = 4
	shortCell    = 8
	wideFloor    = 10

	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiCyan  = "\x1b[36m"
)

// StyleFor detects the style of f (normally os.Stdout).
func StyleFor(f *os.File) Style {
	fd := int(f.Fd())
	tty := term.IsTerminal(fd)
	ttyWidth := 0

	if tty {
		if w, _, err := term.GetSize(fd); err == nil {
			ttyWidth = w
		}
	}

	return NewStyle(os.Getenv, tty, ttyWidth)
}

// NewStyle decides the style from the environment. ttyWidth is the width of
// the terminal (0 = unknown). Off a terminal there is no width limit and no
// color. On a terminal the width is the terminal width, then COLUMNS, then 100.
// Color needs a terminal, no NO_COLOR and TERM other than dumb. Box-drawing
// characters need a UTF-8 locale (LC_ALL, then LC_CTYPE, then LANG) and TERM
// other than dumb; the rest gets ASCII.
func NewStyle(getenv func(string) string, tty bool, ttyWidth int) Style {
	dumb := getenv("TERM") == "dumb"

	st := Style{
		Unicode: utf8Locale(getenv) && !dumb,
		Color:   tty && getenv("NO_COLOR") == "" && !dumb,
	}

	if !tty {
		return st
	}

	switch {
	case ttyWidth > 0:
		st.Width = ttyWidth
	default:
		if n, err := strconv.Atoi(getenv("COLUMNS")); err == nil && n > 0 {
			st.Width = n
		} else {
			st.Width = defaultWidth
		}
	}

	return st
}

func utf8Locale(getenv func(string) string) bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			v = strings.ToLower(v)

			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}

	return false
}

type glyphs struct {
	h, v       string
	tl, tm, tr string
	ml, mm, mr string
	bl, bm, br string
	ellipsis   string
}

var unicodeGlyphs = glyphs{"─", "│", "┌", "┬", "┐", "├", "┼", "┤", "└", "┴", "┘", "…"}

var asciiGlyphs = glyphs{"-", "|", "+", "+", "+", "+", "+", "+", "+", "+", "+", "..."}

// Render writes the table. Cells with control characters get spaces. When
// st.Width is set and the table is wider, the widest text columns shrink
// first and their cells end in an ellipsis.
func (t Table) Render(w io.Writer, st Style) {
	g := asciiGlyphs

	if st.Unicode {
		g = unicodeGlyphs
	}

	rows := t.cleanRows()
	widths := t.fit(rows, st.Width, DisplayWidth(g.ellipsis))

	var b strings.Builder

	t.rule(&b, g, st, widths, g.tl, g.tm, g.tr)
	t.line(&b, g, st, widths, titles(t.Columns), true)
	t.rule(&b, g, st, widths, g.ml, g.mm, g.mr)

	for _, r := range rows {
		t.line(&b, g, st, widths, r, false)
	}

	if t.Footer != nil {
		t.rule(&b, g, st, widths, g.ml, g.mm, g.mr)
		t.line(&b, g, st, widths, clean(t.Footer), false)
	}

	t.rule(&b, g, st, widths, g.bl, g.bm, g.br)

	_, _ = io.WriteString(w, b.String())
}

func titles(cols []Column) []string {
	out := make([]string, len(cols))

	for i, c := range cols {
		out[i] = c.Title
	}

	return out
}

func (t Table) cleanRows() [][]string {
	out := make([][]string, len(t.Rows))

	for i, r := range t.Rows {
		out[i] = clean(r)
	}

	return out
}

// clean turns control characters into spaces, so a cell is one line.
func clean(row []string) []string {
	out := make([]string, len(row))

	for i, c := range row {
		out[i] = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}

			return r
		}, c)
	}

	return out
}

// fit returns the content width of each column.
func (t Table) fit(rows [][]string, limit, ellipsisW int) []int {
	widths := make([]int, len(t.Columns))

	for i, c := range t.Columns {
		widths[i] = DisplayWidth(c.Title)
	}

	for _, r := range append(append([][]string{}, rows...), clean(t.Footer)) {
		for i := range widths {
			if i < len(r) {
				widths[i] = max(widths[i], DisplayWidth(r[i]))
			}
		}
	}

	if limit <= 0 {
		return widths
	}

	floors := make([]int, len(widths))

	for i, c := range t.Columns {
		floors[i] = max(DisplayWidth(c.Title), minCellWidth, ellipsisW+1)
	}

	// Wide text columns give up space first, in proportion to how much each
	// can lose, down to wideFloor. Short text, numbers and Keep columns stay.
	wide := t.wideText(widths)

	shrinkProportional(widths, wide, t.wideFloors(), frame(widths)-limit)

	// Still too wide: the wide text goes on down to its smallest cut, still
	// before any short text, number or Keep column loses a cell.
	least := make([]int, len(widths))

	for i := range least {
		least[i] = ellipsisW + 1
	}

	shrinkProportional(widths, wide, least, frame(widths)-limit)

	// Then the old order: text columns, then the Right and Keep columns. A
	// column goes down to the width of its title before any goes below it.
	for _, textOnly := range []bool{true, false} {
		for _, byTitle := range []bool{true, false} {
			for frame(widths) > limit {
				i := widest(widths, t.Columns, textOnly, floors, byTitle, max(minCellWidth, ellipsisW+1))
				if i < 0 {
					break
				}

				widths[i]--
			}
		}
	}

	return widths
}

// wideText marks the text columns whose content is longer than 8 cells and
// longer than their title: the only columns that are cut first.
func (t Table) wideText(widths []int) []bool {
	out := make([]bool, len(widths))

	for i, c := range t.Columns {
		isText := c.Align == Left && !c.Keep
		out[i] = isText && widths[i] > max(shortCell, DisplayWidth(c.Title))
	}

	return out
}

// wideFloors is the least width of a wide text column: wideFloor cells, or
// its title when that is longer.
func (t Table) wideFloors() []int {
	out := make([]int, len(t.Columns))

	for i, c := range t.Columns {
		out[i] = max(wideFloor, DisplayWidth(c.Title))
	}

	return out
}

// shrinkProportional takes excess cells from the marked columns, each in
// proportion to its room above its floor. It never goes below a floor.
func shrinkProportional(widths []int, marked []bool, floors []int, excess int) {
	if excess <= 0 {
		return
	}

	room := make([]int, len(widths))
	total := 0

	for i, w := range widths {
		if marked[i] && w > floors[i] {
			room[i] = w - floors[i]
			total += room[i]
		}
	}

	if total == 0 {
		return
	}

	if excess >= total {
		for i := range widths {
			widths[i] -= room[i]
		}

		return
	}

	left := excess

	for i := range widths {
		cut := excess * room[i] / total
		widths[i] -= cut
		room[i] -= cut
		left -= cut
	}

	for left > 0 {
		best := -1

		for i := range widths {
			if room[i] > 0 && (best < 0 || room[i] > room[best]) {
				best = i
			}
		}

		widths[best]--
		room[best]--
		left--
	}
}

// frame is the width of the whole table: cell padding and borders included.
func frame(widths []int) int {
	n := 1

	for _, w := range widths {
		n += w + 3
	}

	return n
}

// widest picks the widest column above its floor in the tier, or -1.
func widest(widths []int, cols []Column, textOnly bool, floors []int, byTitle bool, minimum int) int {
	best := -1

	for i, w := range widths {
		isText := cols[i].Align == Left && !cols[i].Keep
		floor := minimum

		if byTitle {
			floor = floors[i]
		}

		if isText != textOnly || w <= floor {
			continue
		}

		if best < 0 || w > widths[best] {
			best = i
		}
	}

	return best
}

func (t Table) rule(b *strings.Builder, g glyphs, st Style, widths []int, left, mid, right string) {
	var s strings.Builder

	s.WriteString(left)

	for i, w := range widths {
		s.WriteString(strings.Repeat(g.h, w+2))

		if i < len(widths)-1 {
			s.WriteString(mid)
		}
	}

	s.WriteString(right)

	b.WriteString(dim(s.String(), st))
	b.WriteByte('\n')
}

func (t Table) line(b *strings.Builder, g glyphs, st Style, widths []int, cells []string, header bool) {
	b.WriteString(dim(g.v, st))

	for i, w := range widths {
		cell := ""

		if i < len(cells) {
			cell = cells[i]
		}

		cell = Truncate(cell, w, g.ellipsis)

		pad := strings.Repeat(" ", w-DisplayWidth(cell))

		if t.Columns[i].Align == Right {
			cell = pad + cell
		} else {
			cell += pad
		}

		if header && st.Color {
			cell = ansiBold + ansiCyan + cell + ansiReset
		}

		b.WriteString(" " + cell + " ")
		b.WriteString(dim(g.v, st))
	}

	b.WriteByte('\n')
}

func dim(s string, st Style) string {
	if !st.Color {
		return s
	}

	return ansiDim + s + ansiReset
}

// DisplayWidth is how many terminal cells s takes.
func DisplayWidth(s string) int {
	n := 0

	for _, r := range s {
		n += runeWidth(r)
	}

	return n
}

func runeWidth(r rune) int {
	switch {
	case r == '\t':
		return 1
	case r == 0 || unicode.IsControl(r):
		return 0
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	}

	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}

	return 1
}

// Truncate cuts s to at most w cells. A cut string ends with ellipsis.
func Truncate(s string, w int, ellipsis string) string {
	if DisplayWidth(s) <= w {
		return s
	}

	room := w - DisplayWidth(ellipsis)
	if room < 0 {
		room = 0
	}

	n := 0
	end := 0

	for i, r := range s {
		rw := runeWidth(r)

		if n+rw > room {
			break
		}

		n += rw
		end = i + utf8.RuneLen(r)
	}

	return s[:end] + ellipsis
}

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// StripANSI removes color codes.
func StripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

var byteUnits = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}

// HumanBytes prints n with binary units: 0 B, 1.5 KiB, 3.2 GiB.
func HumanBytes(n int64) string {
	f := float64(n)
	i := 0

	for f >= 1024 && i < len(byteUnits)-1 {
		f /= 1024
		i++
	}

	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}

	return fmt.Sprintf("%.1f %s", f, byteUnits[i])
}

// Thousands groups digits by three: 1234567 becomes 1,234,567.
func Thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	sign := ""

	if n < 0 {
		sign, s = "-", s[1:]
	}

	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}

	return sign + s
}

// LooksNumeric tells whether a cell is a number (with optional separators),
// so a column of them can be right-aligned.
func LooksNumeric(s string) bool {
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return false
	}

	f, err := strconv.ParseFloat(s, 64)

	return err == nil && !math.IsNaN(f)
}
