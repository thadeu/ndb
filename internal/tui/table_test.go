package tui

import (
	"bytes"
	"strings"
	"testing"
)

func render(t Table, st Style) []string {
	var b bytes.Buffer

	t.Render(&b, st)

	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
}

var plain = Style{Unicode: true}

func TestRenderDrawsUnicodeBox(t *testing.T) {
	tb := Table{
		Columns: []Column{{Title: "NAME"}, {Title: "ROWS", Align: Right}},
		Rows:    [][]string{{"orders", "1,200"}, {"events", "3"}},
	}

	got := strings.Join(render(tb, plain), "\n")

	want := strings.Join([]string{
		"┌────────┬───────┐",
		"│ NAME   │  ROWS │",
		"├────────┼───────┤",
		"│ orders │ 1,200 │",
		"│ events │     3 │",
		"└────────┴───────┘",
	}, "\n")

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderASCIIFallback(t *testing.T) {
	tb := Table{Columns: []Column{{Title: "A"}, {Title: "B", Align: Right}}, Rows: [][]string{{"x", "1"}}}

	got := strings.Join(render(tb, Style{}), "\n")

	want := "+---+---+\n| A | B |\n+---+---+\n| x | 1 |\n+---+---+"

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}

	for _, r := range got {
		if r > 127 {
			t.Fatalf("non-ASCII rune %q in ASCII table", r)
		}
	}
}

func TestRenderFooterHasItsOwnSeparator(t *testing.T) {
	tb := Table{Columns: []Column{{Title: "N"}, {Title: "V", Align: Right}}, Rows: [][]string{{"a", "1"}}, Footer: []string{"TOTAL", "1"}}

	lines := render(tb, plain)

	if len(lines) != 7 || !strings.HasPrefix(lines[4], "├") || !strings.Contains(lines[5], "TOTAL") {
		t.Fatalf("lines:\n%s", strings.Join(lines, "\n"))
	}
}

func TestRenderShrinksWidestTextColumnAndKeepsNumbers(t *testing.T) {
	tb := Table{
		Columns: []Column{{Title: "ID", Keep: true}, {Title: "NOTE"}, {Title: "ROWS", Align: Right}},
		Rows:    [][]string{{"c-0001", strings.Repeat("long text ", 10), "1,234,567"}},
	}

	lines := render(tb, Style{Unicode: true, Width: 50})

	for _, l := range lines {
		if w := DisplayWidth(l); w > 50 {
			t.Fatalf("line wider than 50 (%d): %q", w, l)
		}
	}

	row := lines[3]

	if !strings.Contains(row, "c-0001") || !strings.Contains(row, "1,234,567") || !strings.Contains(row, "…") {
		t.Fatalf("row = %q", row)
	}

	if DisplayWidth(row) != 50 {
		t.Fatalf("row width = %d, want 50", DisplayWidth(row))
	}
}

func TestRenderNoWidthMeansFullWidth(t *testing.T) {
	long := strings.Repeat("x", 300)
	tb := Table{Columns: []Column{{Title: "A"}}, Rows: [][]string{{long}}}

	lines := render(tb, plain)

	if !strings.Contains(lines[3], long) {
		t.Fatal("cell was cut with no width limit")
	}
}

func TestRenderShrinksNumbersOnlyAsALastResort(t *testing.T) {
	tb := Table{
		Columns: []Column{{Title: "A"}, {Title: "B", Align: Right}},
		Rows:    [][]string{{"abcdefghij", "1234567890"}},
	}

	lines := render(tb, Style{Unicode: true, Width: 24})

	for _, l := range lines {
		if DisplayWidth(l) > 24 {
			t.Fatalf("line wider than 24: %q", l)
		}
	}

	if !strings.Contains(lines[3], "1234567890") {
		t.Fatalf("number was cut before the text: %q", lines[3])
	}
}

func TestRenderWideRunesCountTwoCells(t *testing.T) {
	tb := Table{Columns: []Column{{Title: "NAME"}}, Rows: [][]string{{"日本語"}, {"abcdef"}}}

	lines := render(tb, plain)

	for _, l := range lines {
		if DisplayWidth(l) != DisplayWidth(lines[0]) {
			t.Fatalf("ragged table:\n%s", strings.Join(lines, "\n"))
		}
	}
}

func TestTruncateRespectsWideRunes(t *testing.T) {
	got := Truncate("日本語です", 6, "…")

	if DisplayWidth(got) > 6 || !strings.HasSuffix(got, "…") {
		t.Fatalf("got %q (%d cells)", got, DisplayWidth(got))
	}

	if Truncate("short", 10, "…") != "short" {
		t.Fatal("short string changed")
	}
}

func TestDisplayWidth(t *testing.T) {
	cases := map[string]int{"abc": 3, "日本": 4, "é": 1, "é": 1, "😀": 2, "": 0, "a\tb": 3}

	for in, want := range cases {
		if got := DisplayWidth(in); got != want {
			t.Errorf("DisplayWidth(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestControlCharactersBecomeSpaces(t *testing.T) {
	tb := Table{Columns: []Column{{Title: "A"}}, Rows: [][]string{{"a\nb\tc"}}}

	lines := render(tb, plain)

	if len(lines) != 5 || !strings.Contains(lines[3], "a b c") {
		t.Fatalf("lines:\n%s", strings.Join(lines, "\n"))
	}
}

func TestColorOnlyWhenAsked(t *testing.T) {
	tb := Table{Columns: []Column{{Title: "A"}}, Rows: [][]string{{"x"}}}

	if strings.Contains(strings.Join(render(tb, plain), ""), "\x1b[") {
		t.Fatal("escape codes without Color")
	}

	colored := strings.Join(render(tb, Style{Unicode: true, Color: true}), "\n")

	if !strings.Contains(colored, "\x1b[1m") || !strings.Contains(colored, "\x1b[2m") {
		t.Fatalf("no bold header or dim border: %q", colored)
	}
}

func TestColoredLinesHaveTheSameWidthAsPlain(t *testing.T) {
	tb := Table{Columns: []Column{{Title: "A"}, {Title: "B", Align: Right}}, Rows: [][]string{{"x", "1"}}}

	a := render(tb, plain)
	b := render(tb, Style{Unicode: true, Color: true})

	for i := range a {
		if DisplayWidth(StripANSI(b[i])) != DisplayWidth(a[i]) {
			t.Fatalf("line %d: %q vs %q", i, b[i], a[i])
		}
	}
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestStyleFromEnvironment(t *testing.T) {
	utf := map[string]string{"LANG": "en_US.UTF-8"}

	cases := []struct {
		name    string
		env     map[string]string
		tty     bool
		ttyW    int
		unicode bool
		color   bool
		width   int
	}{
		{"tty utf8", utf, true, 120, true, true, 120},
		{"pipe", utf, false, 0, true, false, 0},
		{"no color", map[string]string{"LANG": "en_US.UTF-8", "NO_COLOR": "1"}, true, 90, true, false, 90},
		{"dumb", map[string]string{"LANG": "en_US.UTF-8", "TERM": "dumb"}, true, 90, false, false, 90},
		{"c locale", map[string]string{"LANG": "C"}, true, 90, false, true, 90},
		{"no locale", map[string]string{}, true, 90, false, true, 90},
		{"LC_ALL wins", map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, true, 90, false, true, 90},
		{"utf8 lowercase", map[string]string{"LC_CTYPE": "pt_BR.utf8"}, true, 90, true, true, 90},
		{"columns fallback", map[string]string{"LANG": "x.UTF-8", "COLUMNS": "77"}, true, 0, true, true, 77},
		{"default 100", utf, true, 0, true, true, 100},
		{"columns ignored on pipe", map[string]string{"LANG": "x.UTF-8", "COLUMNS": "77"}, false, 0, true, false, 0},
	}

	for _, c := range cases {
		got := NewStyle(env(c.env), c.tty, c.ttyW)

		if got.Unicode != c.unicode || got.Color != c.color || got.Width != c.width {
			t.Errorf("%s: got %+v, want unicode=%v color=%v width=%d", c.name, got, c.unicode, c.color, c.width)
		}
	}
}

func TestHumanBytesAndThousands(t *testing.T) {
	bytes := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB"}

	for in, want := range bytes {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}

	nums := map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -4321: "-4,321"}

	for in, want := range nums {
		if got := Thousands(in); got != want {
			t.Errorf("Thousands(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksNumeric(t *testing.T) {
	for _, s := range []string{"12", "-3.5", "1,234", "1e3"} {
		if !LooksNumeric(s) {
			t.Errorf("%q should be numeric", s)
		}
	}

	for _, s := range []string{"", "abc", "12abc", "-"} {
		if LooksNumeric(s) {
			t.Errorf("%q should not be numeric", s)
		}
	}
}

func TestRenderKeepsTitlesWhileOtherCellsCanShrink(t *testing.T) {
	tb := Table{
		Columns: []Column{{Title: "MODE"}, {Title: "INDEXES"}},
		Rows:    [][]string{{"engine", "author (ready), status (ready), created (ready)"}},
	}

	lines := render(tb, Style{Unicode: true, Width: 30})

	if !strings.Contains(lines[1], "MODE") || !strings.Contains(lines[1], "INDEXES") || !strings.Contains(lines[3], "engine") {
		t.Fatalf("titles or short cells were cut:\n%s", strings.Join(lines, "\n"))
	}
}

func statsLikeTable() Table {
	return Table{
		Columns: []Column{
			{Title: "collection", Keep: true},
			{Title: "mode"},
			{Title: "rows", Align: Right},
			{Title: "size", Align: Right},
			{Title: "segments", Align: Right},
			{Title: "wal", Align: Right},
			{Title: "quarantined", Align: Right},
			{Title: "indexes"},
		},
		Rows: [][]string{
			{"calls", "direct", "3", "128.3 KiB", "0", "0", "1", "status (ready)"},
			{"posts", "engine", "4", "132.7 KiB", "1", "1", "0", "author (ready), status (ready)"},
		},
	}
}

func TestRenderShortTextIsNeverCutBeforeWideText(t *testing.T) {
	lines := render(statsLikeTable(), Style{Unicode: true, Width: 80})

	for _, l := range lines {
		if w := DisplayWidth(l); w > 80 {
			t.Fatalf("line wider than 80 (%d): %q", w, l)
		}
	}

	all := strings.Join(lines, "\n")

	for _, want := range []string{"direct", "engine", "128.3 KiB", "quarantined"} {
		if !strings.Contains(all, want) {
			t.Fatalf("%q was cut:\n%s", want, all)
		}
	}

	if !strings.Contains(all, "…") || strings.Contains(all, "status (ready)") {
		t.Fatalf("indexes was not the cut column:\n%s", all)
	}
}

func TestShrinkProportionalTakesMoreFromTheWiderColumn(t *testing.T) {
	widths := []int{40, 20, 6}

	shrinkProportional(widths, []bool{true, true, false}, []int{10, 10, 10}, 15)

	if widths[2] != 6 || widths[0]+widths[1] != 45 || widths[0] <= widths[1]+10 {
		t.Fatalf("widths = %v", widths)
	}

	if widths[0] < 10 || widths[1] < 10 {
		t.Fatalf("below the floor: %v", widths)
	}
}

func TestShrinkProportionalStopsAtTheFloor(t *testing.T) {
	widths := []int{14, 12}

	shrinkProportional(widths, []bool{true, true}, []int{10, 10}, 100)

	if widths[0] != 10 || widths[1] != 10 {
		t.Fatalf("widths = %v", widths)
	}
}
