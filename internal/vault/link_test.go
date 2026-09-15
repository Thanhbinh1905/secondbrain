package vault

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
)

// TestLinkIsAFirstClassKind: the saved-link kind has to be valid everywhere a
// kind is asked about, or a hand-written links/ file fails to parse.
func TestLinkIsAFirstClassKind(t *testing.T) {
	if DefaultDirFor(KindLink) != LinksDir {
		t.Errorf("a link has no directory, so ParseRecord would reject the type")
	}
	if !allowed(RecordDirs, LinksDir) {
		t.Errorf("links/ is not walked, so a saved link would be invisible to every query")
	}
	if got := StatusesFor(KindLink); got != nil {
		t.Errorf("link statuses = %v, want none: a link has nothing to complete", got)
	}
	found := false
	for _, k := range Kinds {
		if k == KindLink {
			found = true
		}
	}
	if !found {
		t.Error("KindLink is not in Kinds, so the unknown-type error would not list it")
	}
}

// TestValidateURLAcceptsOnlyOpenableAddresses: a saved link is something to
// open later, so anything a browser cannot open is refused before it is stored.
func TestValidateURLAcceptsOnlyOpenableAddresses(t *testing.T) {
	for _, raw := range []string{
		"https://example.com/a-good-read",
		"http://example.com/a-good-read",
		"https://datatracker.ietf.org/doc/html/rfc5545#section-3",
	} {
		if err := ValidateURL(raw); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want nil", raw, err)
		}
	}
	for _, raw := range []string{
		"",
		"not a url",
		"ftp://example.com/a-good-read",
		"mailto:someone@example.com",
		"https://",
	} {
		if err := ValidateURL(raw); err == nil {
			t.Errorf("ValidateURL(%q) = nil, want a refusal", raw)
		}
	}
}

// linkVault builds a throwaway vault holding one saved link.
func linkVault(t *testing.T) *Vault {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if _, err := Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := timeref.ParseDateOnly("2026-09-01")
	if err != nil {
		t.Fatal(err)
	}
	rel, doc, err := v.BuildLink(NewLink{
		ID: "a-good-read", Title: "a good read", URL: "https://example.com/a-good-read",
		Body: "why it is worth re-reading", Created: created,
		Tags: []string{"reading"}, Links: []string{"customer-referral"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rel, LinksDir+"/") {
		t.Errorf("a link was built at %q, want it under %s/", rel, LinksDir)
	}
	if err := v.Save(rel, doc); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestBuildLinkWritesADescribedBookmark: the address plus the user's own
// description round-trips through the file, with no status anywhere.
func TestBuildLinkWritesADescribedBookmark(t *testing.T) {
	v := linkVault(t)
	r, err := v.Find("a-good-read")
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindLink {
		t.Errorf("kind = %q, want link", r.Kind)
	}
	if r.Status != "" {
		t.Errorf("status = %q, want none: a link has nothing to complete", r.Status)
	}
	if !r.HasURL || r.URL != "https://example.com/a-good-read" {
		t.Errorf("url = %q, want the saved address", r.URL)
	}
	if !r.HasTouched || r.Touched.String() != "2026-09-01" {
		t.Errorf("touched = %v, want the creation date so age is visible", r.Touched)
	}
	if strings.Join(r.Tags, ",") != "reading" {
		t.Errorf("tags = %v", r.Tags)
	}
	if strings.Join(r.Links, ",") != "customer-referral" {
		t.Errorf("links = %v", r.Links)
	}
	if !strings.Contains(r.Body, "worth re-reading") {
		t.Errorf("the description did not survive: %q", r.Body)
	}
}

// TestBuildLinkRefusesABadAddress keeps the address honest at the write path
// as well as the read path. A stray status cannot arrive through the build -
// there is no field for one - so the corrupt fixture asserts the read path.
func TestBuildLinkRefusesABadAddress(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if _, err := Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := timeref.ParseDateOnly("2026-09-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.BuildLink(NewLink{ID: "x", Title: "x", URL: "ftp://example.com/x", Created: created}); err == nil {
		t.Error("a non-http address was accepted")
	}
}

// TestSavedLinkKeepsResurfacingUntilDeleted: a link never closes - there is no
// status to complete - so it stays on the missed list until it is removed
// outright, rather than being filed away by reading it.
func TestSavedLinkKeepsResurfacingUntilDeleted(t *testing.T) {
	if IsClosed(KindLink, "saved") || IsClosed(KindLink, "read") || IsClosed(KindLink, "archived") {
		t.Error("a link reads as closed, so it would stop resurfacing before it is deleted")
	}
	v := goodVault(t)
	r, err := v.Find("read-later-rfc-5545")
	if err != nil {
		t.Fatal(err)
	}
	// Untouched since 2026-09-01 against the vault's 14d default: fresh on the
	// 2nd, missed by the 16th.
	if got := v.AgeDays(r, at(t, v, "2026-09-02T12:00")); got != 1 {
		t.Errorf("age = %d, want 1", got)
	}
	if v.PastHorizon(r, at(t, v, "2026-09-02T12:00")) {
		t.Error("a day-old link is already past its horizon")
	}
	if !v.PastHorizon(r, at(t, v, "2026-09-16T12:00")) {
		t.Error("a 15-day-unread link is not past its 14d horizon")
	}
}
