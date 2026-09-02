// Package frontmatter parses and serialises a Markdown file with YAML
// frontmatter, preserving the body byte-for-byte and reporting every parse
// failure as path:line: reason (NFR-4).
//
// Values are handled as raw text. This package validates the shape of the
// document, never the meaning of a field: interpreting a timestamp or a status
// belongs to the caller, which knows the vocabulary.
package frontmatter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Delimiter opens and closes the frontmatter block.
const Delimiter = "---"

// Error is a failure at a known position in a known file.
type Error struct {
	Path string
	Line int
	Msg  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Msg)
}

func posErr(path string, line int, format string, args ...any) *Error {
	return &Error{Path: path, Line: line, Msg: fmt.Sprintf(format, args...)}
}

// Doc is a Markdown file split into its frontmatter mapping and its body.
type Doc struct {
	// Path is used for error messages only; it is never consulted for content.
	Path string
	// Body is everything after the closing delimiter line, verbatim.
	Body string

	mapping *yaml.Node
	// yamlOffset is the file line number of the frontmatter's first YAML line,
	// used to translate yaml.v3's fragment-relative line numbers.
	yamlOffset int
	// bodyLine is the file line number Body's first line occupies. It is zero
	// for a document this package built rather than parsed, because such a
	// document has no file to have a line in yet.
	bodyLine int
}

// BodyLine is the file line number the body's first line occupies, or zero for
// a document that was built rather than parsed. It exists so an error about
// something in the body can name the line it is on.
func (d *Doc) BodyLine() int { return d.bodyLine }

// yamlLineRe matches the "line N:" position yaml.v3 puts in its errors.
var yamlLineRe = regexp.MustCompile(`line (\d+): `)

// Parse splits raw into frontmatter and body. path appears in error messages.
func Parse(path string, raw []byte) (*Doc, error) {
	text := string(raw)
	lines := strings.SplitAfter(text, "\n")

	if len(lines) == 0 || strings.TrimRight(lines[0], "\r\n") != Delimiter {
		return nil, posErr(path, 1, "missing frontmatter: file must begin with %q", Delimiter)
	}

	closing := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r\n") == Delimiter {
			closing = i
			break
		}
	}
	if closing == -1 {
		return nil, posErr(path, len(lines), "unterminated frontmatter: no closing %q line", Delimiter)
	}

	fragment := strings.Join(lines[1:closing], "")
	doc := &Doc{
		Path:       path,
		Body:       strings.Join(lines[closing+1:], ""),
		yamlOffset: 2, // the opening delimiter is file line 1
		bodyLine:   closing + 2,
	}

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(fragment), &root); err != nil {
		return nil, doc.yamlErr(err)
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return nil, posErr(path, 1, "empty frontmatter: at least %q is required", "type")
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil, posErr(path, doc.fileLine(mapping.Line), "frontmatter must be a mapping of key to value, found %s", kindName(mapping.Kind))
	}
	doc.mapping = mapping

	// yaml.v3 accepts duplicate keys silently; a second brain must not.
	seen := map[string]int{}
	for i := 0; i < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		if first, dup := seen[k.Value]; dup {
			return nil, posErr(path, doc.fileLine(k.Line), "duplicate key %q, first defined on line %d", k.Value, doc.fileLine(first))
		}
		seen[k.Value] = k.Line
	}
	return doc, nil
}

// fileLine converts a fragment-relative line number into a file line number.
func (d *Doc) fileLine(fragmentLine int) int {
	if fragmentLine <= 0 {
		return 1
	}
	return fragmentLine + d.yamlOffset - 1
}

// yamlErr rewrites a yaml.v3 error into a path:line: reason error.
func (d *Doc) yamlErr(err error) error {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors:\n  ")
	line := 1
	if m := yamlLineRe.FindStringSubmatchIndex(msg); m != nil {
		n, convErr := strconv.Atoi(msg[m[2]:m[3]])
		if convErr != nil {
			return posErr(d.Path, 1, "%s", msg)
		}
		line = d.fileLine(n)
		msg = msg[:m[0]] + msg[m[1]:]
	}
	return posErr(d.Path, line, "%s", strings.TrimSpace(strings.ReplaceAll(msg, "\n", "; ")))
}

func kindName(k yaml.Kind) string {
	switch k {
	case yaml.SequenceNode:
		return "a list"
	case yaml.ScalarNode:
		return "a scalar"
	case yaml.AliasNode:
		return "an alias"
	default:
		return "an unknown node"
	}
}

// Keys returns the frontmatter keys in file order.
func (d *Doc) Keys() []string {
	out := make([]string, 0, len(d.mapping.Content)/2)
	for i := 0; i < len(d.mapping.Content); i += 2 {
		out = append(out, d.mapping.Content[i].Value)
	}
	return out
}

func (d *Doc) find(key string) (int, bool) {
	for i := 0; i < len(d.mapping.Content); i += 2 {
		if d.mapping.Content[i].Value == key {
			return i, true
		}
	}
	return 0, false
}

// Has reports whether key is present.
func (d *Doc) Has(key string) bool {
	_, ok := d.find(key)
	return ok
}

// Line returns the file line number of key, or 1 if it is absent.
func (d *Doc) Line(key string) int {
	i, ok := d.find(key)
	if !ok {
		return 1
	}
	return d.fileLine(d.mapping.Content[i].Line)
}

// Errorf builds a path:line: reason error anchored at key. A key that is
// absent anchors at line 1, which is the opening delimiter.
func (d *Doc) Errorf(key, format string, args ...any) error {
	return posErr(d.Path, d.Line(key), format, args...)
}

// String returns the raw scalar text of key. ok is false when the key is
// absent. A key present but holding a list or a mapping is an error.
func (d *Doc) String(key string) (value string, ok bool, err error) {
	i, found := d.find(key)
	if !found {
		return "", false, nil
	}
	v := d.mapping.Content[i+1]
	if v.Kind != yaml.ScalarNode {
		return "", true, posErr(d.Path, d.fileLine(v.Line), "key %q must be a single value, found %s", key, kindName(v.Kind))
	}
	return v.Value, true, nil
}

// Require returns the scalar text of key, erroring when it is absent or empty.
func (d *Doc) Require(key string) (string, error) {
	v, ok, err := d.String(key)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", posErr(d.Path, 1, "missing required key %q", key)
	}
	if strings.TrimSpace(v) == "" {
		return "", posErr(d.Path, d.Line(key), "key %q must not be empty", key)
	}
	return v, nil
}

// Strings returns the scalar elements of the list at key. A scalar value is
// read as a one-element list, which is how a hand-edited vault usually spells
// a single entry.
func (d *Doc) Strings(key string) (values []string, ok bool, err error) {
	i, found := d.find(key)
	if !found {
		return nil, false, nil
	}
	v := d.mapping.Content[i+1]
	switch v.Kind {
	case yaml.ScalarNode:
		if v.Value == "" || v.Tag == "!!null" {
			return nil, true, nil
		}
		return []string{v.Value}, true, nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(v.Content))
		for _, e := range v.Content {
			if e.Kind != yaml.ScalarNode {
				return nil, true, posErr(d.Path, d.fileLine(e.Line), "key %q must be a list of plain values", key)
			}
			out = append(out, e.Value)
		}
		return out, true, nil
	default:
		return nil, true, posErr(d.Path, d.fileLine(v.Line), "key %q must be a list, found %s", key, kindName(v.Kind))
	}
}

// scalar builds a value node. The tag is deliberately left empty so the
// encoder picks the plainest representation that survives a re-read; forcing
// !!str would quote every date and duration. Reads always take Node.Value, so
// the resolved YAML type never matters.
func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

// Set writes a scalar value, appending the key when it is new and leaving every
// other key, its order and its style untouched.
func (d *Doc) Set(key, value string) {
	node := scalar(value)
	if i, ok := d.find(key); ok {
		// Keep the existing style unless it cannot represent the new value.
		node.Style = d.mapping.Content[i+1].Style
		if node.Style == yaml.LiteralStyle || node.Style == yaml.FoldedStyle {
			node.Style = 0
		}
		d.mapping.Content[i+1] = node
		return
	}
	d.mapping.Content = append(d.mapping.Content, scalar(key), node)
}

// SetStrings writes a flow-style list value.
func (d *Doc) SetStrings(key string, values []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, v := range values {
		seq.Content = append(seq.Content, scalar(v))
	}
	if i, ok := d.find(key); ok {
		d.mapping.Content[i+1] = seq
		return
	}
	d.mapping.Content = append(d.mapping.Content, scalar(key), seq)
}

// Delete removes key. It reports whether the key was present.
func (d *Doc) Delete(key string) bool {
	i, ok := d.find(key)
	if !ok {
		return false
	}
	d.mapping.Content = append(d.mapping.Content[:i], d.mapping.Content[i+2:]...)
	return true
}

// Bytes serialises the document: delimiter, frontmatter, delimiter, then the
// body exactly as it was read.
func (d *Doc) Bytes() ([]byte, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(d.mapping); err != nil {
		return nil, fmt.Errorf("%s: serialising frontmatter: %w", d.Path, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("%s: serialising frontmatter: %w", d.Path, err)
	}
	var out strings.Builder
	out.WriteString(Delimiter + "\n")
	out.WriteString(sb.String())
	out.WriteString(Delimiter + "\n")
	out.WriteString(d.Body)
	return []byte(out.String()), nil
}

// New builds a document from ordered key/value pairs and a body. It is the one
// constructor used by templates and by add.
func New(path string, body string, pairs ...[2]string) *Doc {
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	d := &Doc{Path: path, Body: body, mapping: mapping, yamlOffset: 2}
	for _, p := range pairs {
		d.Set(p[0], p[1])
	}
	return d
}
