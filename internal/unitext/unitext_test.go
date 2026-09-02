package unitext

import "testing"

func TestFoldMatchesDiacriticsBothDirections(t *testing.T) {
	pairs := [][2]string{
		{"Zürich Hauptbahnhof", "zurich hauptbahnhof"},
		{"Đakovo Kraków", "dakovo krakow"}, // the hand-mapped stroked D, plus an acute
		{"Ångström", "angstrom"},           // ring above and umlaut, two combining classes
		{"tréma, cédille et accent grave", "trema, cedille et accent grave"},
		{"customer referral program", "CUSTOMER REFERRAL PROGRAM"}, // ASCII case folding alone
		{"Ísland", "island"},                                       // a mark on an initial capital
		{"exporté en .ics", "exporte en .ics"},                     // punctuation survives folding
		{"réunion Platform Team", "reunion platform team"},
		{"đakovo", "dakovo"}, // lower-case stroked d on its own
		{"ĐAKOVO", "dakovo"}, // and upper-case
	}
	for _, p := range pairs {
		if Fold(p[0]) != Fold(p[1]) {
			t.Errorf("Fold(%q)=%q != Fold(%q)=%q", p[0], Fold(p[0]), p[1], Fold(p[1]))
		}
		if !Contains(p[0], p[1]) {
			t.Errorf("Contains(%q, %q) = false", p[0], p[1])
		}
		if !Contains(p[1], p[0]) {
			t.Errorf("Contains(%q, %q) = false", p[1], p[0])
		}
	}
	if Contains("Zürich", "capacity") {
		t.Error("unrelated text matched")
	}
}

func TestFoldHandlesDecomposedInput(t *testing.T) {
	// The same word written precomposed and decomposed must fold identically:
	// Obsidian on macOS writes decomposed text into files a Linux editor wrote
	// precomposed.
	// The decomposed form is written as an escape rather than as literal bytes,
	// because an editor that normalises the file would silently turn it into
	// the precomposed one and the test would stop proving anything.
	precomposed := "Zürich"
	decomposed := "Zu\u0308rich" // u + combining diaeresis
	if precomposed == decomposed {
		t.Fatal("the two spellings are the same bytes; the test proves nothing")
	}
	if Fold(precomposed) != Fold(decomposed) {
		t.Errorf("%q folds to %q, %q folds to %q", precomposed, Fold(precomposed), decomposed, Fold(decomposed))
	}
}

func TestWidthCountsCellsNotBytes(t *testing.T) {
	decomposed := "e\u0301"
	cases := map[string]int{
		"Zürich":                    6,
		"platform team sync":        18,
		"Ångström":                  8,
		"":                          0,
		"日本":                        4,
		"東京リージョン":                   14,
		"é":                         1,
		decomposed:                  1,
		"Đakovo":                    6,
		"customer referral program": 25,
	}
	for s, want := range cases {
		if got := Width(s); got != want {
			t.Errorf("Width(%q) = %d, want %d (bytes %d)", s, got, want, len(s))
		}
	}
	// The point of the whole function, in both directions: an accented sample
	// is more bytes than cells, and a wide sample is more cells than runes.
	if len("Zürich") == Width("Zürich") {
		t.Error("the accented sample no longer exercises the byte/cell difference")
	}
	if len([]rune("東京リージョン")) == Width("東京リージョン") {
		t.Error("the wide sample no longer exercises the rune/cell difference")
	}
	if decomposed == "é" || len([]rune(decomposed)) <= Width(decomposed) {
		t.Error("the decomposed sample no longer exercises zero-width combining marks")
	}
}

func TestPadAlignsColumnsByCell(t *testing.T) {
	rows := []string{"Zürich", "standup", "Đakovo", "review", "東京"}
	for _, r := range rows {
		if got := Width(PadRight(r, 12)); got != 12 {
			t.Errorf("PadRight(%q, 12) is %d cells wide", r, got)
		}
		if got := Width(PadLeft(r, 12)); got != 12 {
			t.Errorf("PadLeft(%q, 12) is %d cells wide", r, got)
		}
	}
	if got := PadRight("too long for the box", 4); got != "too long for the box" {
		t.Errorf("PadRight shortened an over-wide string: %q", got)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"Zürich Hauptbahnhof", 8, "Zürich …"},
		// A wide rune cannot be split, so the cut lands one cell short rather
		// than one cell over.
		{"東京リージョンの容量", 8, "東京リ…"},
		{"short", 10, "short"},
		{"short", 5, "short"},
		{"abcdef", 3, "ab…"},
		{"", 5, ""},
		{"anything", 0, ""},
	}
	for _, c := range cases {
		got := Truncate(c.in, c.n)
		if got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if Width(got) > c.n {
			t.Errorf("Truncate(%q, %d) = %q is %d cells wide", c.in, c.n, got, Width(got))
		}
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Platform team sync in Zürich":                     "platform-team-sync-in-zurich",
		"customer referral program":                        "customer-referral-program",
		"ask the Zürich datacentre team about CI capacity": "ask-the-zurich-datacentre-team-about-ci-capacity",
		"exporté en .ics":                                  "exporte-en-ics",
		"  spaces   everywhere  ":                          "spaces-everywhere",
		"Đakovo/Kraków":                                    "dakovo-krakow",
		"---leading and trailing---":                       "leading-and-trailing",
		"UPPER Case 123":                                   "upper-case-123",
		"":                                                 "",
		"!!!":                                              "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugNCutsOnWordBoundary(t *testing.T) {
	in := "ask the Zürich datacentre team about CI capacity"
	if got, want := SlugN(in, 20), "ask-the-zurich"; got != want {
		t.Errorf("SlugN(%q, 20) = %q, want %q", in, got, want)
	}
	if got := SlugN("short", 20); got != "short" {
		t.Errorf("SlugN left a short slug alone: %q", got)
	}
	if got := SlugN("averylongsinglewordwithnohyphens", 10); got != "averylongs" {
		t.Errorf("SlugN(%q, 10) = %q", "averylongsinglewordwithnohyphens", got)
	}
}
