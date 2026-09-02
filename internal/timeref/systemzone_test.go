package timeref

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSystemZoneReadsTZ: an operator who sets $TZ means it, so it is consulted
// first and taken as the answer whenever it names a real zone.
func TestSystemZoneReadsTZ(t *testing.T) {
	for _, tc := range []struct{ tz, want string }{
		{"Europe/Lisbon", "Europe/Lisbon"},
		{"Pacific/Auckland", "Pacific/Auckland"},
		{"UTC", "UTC"},
		{"EST5EDT", "EST5EDT"}, // a legacy name the database really carries
		{":Europe/Lisbon", "Europe/Lisbon"},
		{"", "UTC"}, // POSIX: TZ set to empty means UTC
	} {
		t.Setenv("TZ", tc.tz)
		got, err := SystemZone()
		if err != nil {
			t.Errorf("SystemZone with TZ=%q: %v", tc.tz, err)
			continue
		}
		if got != tc.want {
			t.Errorf("SystemZone with TZ=%q = %q, want %q", tc.tz, got, tc.want)
		}
	}
}

// TestSystemZoneRefusesAnUnloadableTZ is the whole point of not reading
// time.Local: Go resolves a $TZ that names no zone to UTC with no error at all,
// and writing UTC into a vault whose user is not in UTC corrupts every stored
// timestamp quietly. A refusal costs one command.
func TestSystemZoneRefusesAnUnloadableTZ(t *testing.T) {
	for _, tz := range []string{"Not/AZone", "<+07>-7", "gibberish"} {
		t.Setenv("TZ", tz)
		// Go itself would hand back UTC here rather than an error, which is
		// exactly the behaviour this function must not inherit.
		if got := time.Local.String(); got != "UTC" && got != tz {
			t.Logf("time.Local with TZ=%q is %q", tz, got)
		}
		got, err := SystemZone()
		if !errors.Is(err, ErrNoSystemZone) {
			t.Errorf("SystemZone with TZ=%q = (%q, %v), want ErrNoSystemZone", tz, got, err)
		}
		if got != "" {
			t.Errorf("SystemZone with TZ=%q returned a zone alongside the error: %q", tz, got)
		}
	}
}

func TestSystemZoneRefusesLocalPseudoZone(t *testing.T) {
	t.Setenv("TZ", "Local")
	got, err := SystemZone()
	if !errors.Is(err, ErrNoSystemZone) {
		t.Fatalf("SystemZone with TZ=Local = (%q, %v), want ErrNoSystemZone", got, err)
	}
	if got != "" {
		t.Errorf("SystemZone returned a zone alongside the error: %q", got)
	}
}

// TestSystemZoneErrorIsActionable: the message has to say what to do, because
// it is the only thing between the user and a vault full of wrong offsets.
func TestSystemZoneErrorIsActionable(t *testing.T) {
	msg := ErrNoSystemZone.Error()
	for _, want := range []string{"timezone", "--timezone"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not mention %q: %s", want, msg)
		}
	}
}

// TestSystemZoneFallsBackToHostConfiguration: with no $TZ the answer comes from
// the host, and whatever comes back must be a name the tool can actually load -
// never a raw path or an unvalidated string. A host with no readable
// configuration is the documented refusal, so both outcomes are accepted and
// only a wrong-shaped success is a failure.
func TestSystemZoneFallsBackToHostConfiguration(t *testing.T) {
	// t.Setenv registers the restore, then the variable is removed outright:
	// an unset TZ and a TZ set to empty are different inputs, and this test
	// wants the first.
	t.Setenv("TZ", "")
	if err := os.Unsetenv("TZ"); err != nil {
		t.Fatal(err)
	}
	got, err := SystemZone()
	if err != nil {
		if !errors.Is(err, ErrNoSystemZone) {
			t.Fatalf("SystemZone: %v", err)
		}
		t.Logf("this host has no readable zone configuration; the refusal is the documented answer")
		return
	}
	if _, err := time.LoadLocation(got); err != nil {
		t.Errorf("SystemZone returned %q, which does not load: %v", got, err)
	}
	if got == "Local" || got == "" {
		t.Errorf("SystemZone returned %q, which is not an IANA name", got)
	}
}

func TestZoneFromLocaltimePathResolvesSymlinkChain(t *testing.T) {
	root := t.TempDir()
	zoneinfo := filepath.Join(root, "zoneinfo")
	zonePath := filepath.Join(zoneinfo, "Europe", "Lisbon")
	if err := os.MkdirAll(filepath.Dir(zonePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zonePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	alternatives := filepath.Join(root, "alternatives")
	if err := os.MkdirAll(alternatives, 0o755); err != nil {
		t.Fatal(err)
	}
	intermediate := filepath.Join(alternatives, "localtime")
	if err := os.Symlink(zonePath, intermediate); err != nil {
		t.Fatal(err)
	}
	localtime := filepath.Join(root, "localtime")
	if err := os.Symlink(intermediate, localtime); err != nil {
		t.Fatal(err)
	}

	got, ok := zoneFromLocaltimePath(localtime, []string{zoneinfo + string(filepath.Separator)})
	if !ok || got != "Europe/Lisbon" {
		t.Fatalf("zoneFromLocaltimePath = (%q, %v), want Europe/Lisbon", got, ok)
	}
}
