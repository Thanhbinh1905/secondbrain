// Package unitext handles Unicode text the way the vault needs it, treating
// non-ASCII as the case that has to be right rather than an afterthought.
//
// Two problems recur. Searching must match with and without diacritics in both
// directions, so "zurich" finds "Zürich". And padding a column must count
// display cells, not bytes: "Zürich" is seven bytes and six cells, and "東京"
// is six bytes and four cells, so byte padding would push a frame out of true
// in both directions.
package unitext

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/width"
)

// Fold reduces text to a lower-case, diacritic-free form for comparison.
//
// Decomposing and dropping combining marks handles every accented vowel.
// The stroked d does not decompose - U+0111 has no canonical decomposition -
// so it is mapped by hand, which is why "dakovo" finds "Đakovo".
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		switch r {
		case 'đ', 'Đ':
			r = 'd'
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Contains reports whether haystack contains needle, ignoring case and
// diacritics on both sides.
func Contains(haystack, needle string) bool {
	return strings.Contains(Fold(haystack), Fold(needle))
}

// Width is the number of terminal cells the string occupies. Combining marks
// take none; East Asian wide and fullwidth runes take two.
func Width(s string) int {
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		switch width.LookupRune(r).Kind() {
		case width.EastAsianWide, width.EastAsianFullwidth:
			n += 2
		default:
			n++
		}
	}
	return n
}

// PadRight pads s on the right to n display cells. A string already at or over
// the width is returned unchanged; truncating is the caller's decision.
func PadRight(s string, n int) string {
	if pad := n - Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// PadLeft pads s on the left to n display cells.
func PadLeft(s string, n int) string {
	if pad := n - Width(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}

// Truncate shortens s to at most n display cells, marking the cut with a
// single-cell ellipsis when anything was removed.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if Width(s) <= n {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := Width(string(r))
		if used+w > n-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

// Slug turns a title into a filename- and id-safe ASCII string: folded, with
// every run of non-alphanumeric characters collapsed to a single hyphen.
func Slug(s string) string {
	folded := Fold(s)
	var b strings.Builder
	b.Grow(len(folded))
	lastHyphen := true // suppress a leading hyphen
	for _, r := range folded {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// SlugN is Slug limited to n characters, cut on a hyphen boundary so a word is
// never left half-written.
func SlugN(s string, n int) string {
	slug := Slug(s)
	if n <= 0 || len(slug) <= n {
		return slug
	}
	cut := slug[:n]
	if i := strings.LastIndexByte(cut, '-'); i > 0 {
		cut = cut[:i]
	}
	return strings.Trim(cut, "-")
}
