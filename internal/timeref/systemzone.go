package timeref

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoSystemZone is returned when the host's IANA zone name cannot be
// determined. It is deliberately not a fallback: the vault timezone stamps an
// explicit UTC offset onto every stored record, so guessing one writes a wrong
// offset into every timestamp and the mistake surfaces weeks later, one record
// at a time. Refusing costs one command.
var ErrNoSystemZone = errors.New("cannot determine this machine's timezone; pass --timezone <IANA name>, for example --timezone Europe/Lisbon")

// zoneinfoDirs are the path prefixes an /etc/localtime symlink resolves
// through. The zone name is whatever follows one of them.
var zoneinfoDirs = []string{
	"/usr/share/zoneinfo/",
	"/usr/lib/zoneinfo/",
	"/usr/share/lib/zoneinfo/",
	"/etc/zoneinfo/",
	"/var/db/timezone/zoneinfo/", // macOS
}

// SystemZone reports the host's IANA timezone name.
//
// It never guesses. Every candidate is round-tripped through
// time.LoadLocation before it is returned, so a host whose configuration is
// missing or unreadable produces ErrNoSystemZone rather than a plausible wrong
// answer. In particular it does not read time.Local, which is "Local" when $TZ
// is unset and silently "UTC" when $TZ names a zone that does not exist.
//
// The order is the same one the C library and Go itself use: $TZ first, because
// an operator who sets it means it, then the host's own configuration.
func SystemZone() (string, error) {
	if tz, ok := os.LookupEnv("TZ"); ok {
		// POSIX: TZ set to the empty string means UTC.
		if tz == "" {
			return "UTC", nil
		}
		// A leading colon is a POSIX spelling Go also strips.
		name := strings.TrimPrefix(tz, ":")
		if !loadableZoneName(name) {
			// $TZ may hold a POSIX rule such as "EST5EDT" rather than an IANA
			// name. That is a valid way to run a process and a wrong thing to
			// write into a vault, so it is reported, not worked around.
			return "", ErrNoSystemZone
		}
		return name, nil
	}
	if name, ok := zoneFromLocaltimeLink(); ok {
		return name, nil
	}
	if name, ok := zoneFromTimezoneFile(); ok {
		return name, nil
	}
	return "", ErrNoSystemZone
}

func loadableZoneName(name string) bool {
	if name == "" || name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// zoneFromLocaltimeLink reads /etc/localtime, which is a symlink into the
// zoneinfo database on Linux and macOS.
func zoneFromLocaltimeLink() (string, bool) {
	return zoneFromLocaltimePath("/etc/localtime", zoneinfoDirs)
}

func zoneFromLocaltimePath(localtime string, dirs []string) (string, bool) {
	target, err := filepath.EvalSymlinks(localtime)
	if err != nil {
		return "", false
	}
	target = filepath.Clean(target)
	for _, dir := range dirs {
		name, ok := strings.CutPrefix(target, dir)
		if !ok || name == "" {
			continue
		}
		if !loadableZoneName(name) {
			continue
		}
		return name, true
	}
	return "", false
}

// zoneFromTimezoneFile reads /etc/timezone, which holds the bare zone name on
// Debian and its derivatives.
func zoneFromTimezoneFile() (string, bool) {
	raw, err := os.ReadFile("/etc/timezone")
	if err != nil {
		return "", false
	}
	name := strings.TrimSpace(string(raw))
	if name == "" {
		return "", false
	}
	if !loadableZoneName(name) {
		return "", false
	}
	return name, true
}
