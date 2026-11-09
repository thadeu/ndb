package bench

import "testing"

func TestParseFootprint(t *testing.T) {
	cases := map[string]int64{
		"ndb [1]: 64-bit    Footprint: 35 MB\n    phys_footprint: 35 MB\n": 35 * 1024,
		"    phys_footprint: 1904 KB\n":                                    1904,
		"    phys_footprint: 1.5 GB\n":                                     1536 * 1024,
	}

	for out, want := range cases {
		if got, ok := parseFootprint(out); !ok || got != want {
			t.Fatalf("parseFootprint(%q) = %d, %v; want %d", out, got, ok, want)
		}
	}

	if _, ok := parseFootprint("no such process"); ok {
		t.Fatal("parseFootprint read a value from an error")
	}
}
