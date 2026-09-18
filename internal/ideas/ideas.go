// Package ideas builds the versioned ideas review surface.
//
// Build is the one assembly path for the existing text and JSON listing and
// the HTML page. The page markup lives in templates/ideas.html; this package
// only validates and injects the model that every renderer reads.
package ideas

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Thanhbinh1905/secondbrain/internal/payload"
	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
	"github.com/Thanhbinh1905/secondbrain/templates"
)

// Schema is the versioned HTML payload contract.
const Schema = "brain-ideas.v1"

// Slot is where the built page keeps its payload.
var Slot = payload.Slot{Marker: templates.DataSlot, ElementID: "ideas-data"}

// Filter records the exact existing ideas filters used to build the surface.
// Empty values mean the corresponding filter was not requested.
type Filter struct {
	Status string `json:"status"`
	Stale  string `json:"stale"`
}

// Row is one idea and the context needed to review it at a glance.
type Row struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	AgeDays     int    `json:"age_days"`
	HorizonDays int    `json:"horizon_days"`
	PastHorizon bool   `json:"past_horizon"`
	Created     string `json:"created"`
	Touched     string `json:"touched"`
	ShippedAt   string `json:"shipped_at"`
	ShippedPR   string `json:"shipped_pr"`
	Path        string `json:"path"`
}

// Model is the complete review surface. Rows is always a list, including when
// the selected filters match nothing.
type Model struct {
	Schema    string `json:"schema"`
	Generated string `json:"generated"`
	Timezone  string `json:"timezone"`
	Filter    Filter `json:"filter"`
	Ideas     []Row  `json:"ideas"`
}

// Build runs the existing ideas query and assembles the one model used by the
// command's text, JSON and HTML renderers.
func Build(e *query.Engine, f query.IdeaFilter) (Model, error) {
	rows, err := e.Ideas(f)
	if err != nil {
		return Model{}, err
	}
	m := Model{
		Schema:    Schema,
		Generated: timeref.Format(e.Now),
		Timezone:  e.Vault.Zone.Name(),
		Filter:    Filter{Status: f.Status},
		Ideas:     make([]Row, 0, len(rows)),
	}
	if f.HasStale {
		m.Filter.Stale = f.Stale.String()
	}
	for _, r := range rows {
		touched := r.Record.Created.String()
		if r.Record.HasTouched {
			touched = r.Record.Touched.String()
		}
		row := Row{
			ID: r.Record.ID, Title: r.Record.Title, Status: r.Record.Status,
			AgeDays: r.AgeDays, HorizonDays: r.HorizonDays, PastHorizon: r.PastHorizon,
			Created: r.Record.Created.String(), Touched: touched, Path: r.Record.Rel,
		}
		if r.Record.HasShipped {
			row.ShippedAt = timeref.Format(r.Record.ShippedAt)
			row.ShippedPR = r.Record.ShippedPR
		}
		m.Ideas = append(m.Ideas, row)
	}
	return m, nil
}

// Validate checks a payload against brain-ideas.v1 and reports path:line:
// reason for every refusal.
func Validate(path string, raw []byte, lineOffset int) (Model, error) {
	if err := requireFields(path, raw, lineOffset); err != nil {
		return Model{}, err
	}
	var m Model
	if err := payload.Decode(path, raw, lineOffset, &m); err != nil {
		return Model{}, err
	}
	if m.Schema != Schema {
		found := m.Schema
		if found == "" {
			found = "(none)"
		}
		return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "schema", lineOffset),
			"unsupported ideas schema %s: this build writes and reads %s", found, Schema)
	}
	if _, err := timeref.ParseStored(m.Generated); err != nil {
		return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "generated", lineOffset), "generated: %v", err)
	}
	if strings.TrimSpace(m.Timezone) == "" {
		return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "timezone", lineOffset), "timezone must not be empty")
	}
	if m.Filter.Status != "" && !known(vault.IdeaStatuses, m.Filter.Status) {
		return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "status", lineOffset),
			"unknown idea status %q: valid statuses are %s", m.Filter.Status, strings.Join(vault.IdeaStatuses, ", "))
	}
	if m.Filter.Stale != "" {
		if _, err := timeref.ParseSpan(m.Filter.Stale); err != nil {
			return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "stale", lineOffset), "stale: %v", err)
		}
	}
	for i, row := range m.Ideas {
		if strings.TrimSpace(row.ID) == "" || strings.TrimSpace(row.Title) == "" {
			return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "ideas", lineOffset),
				"idea row %d needs an id and a title", i+1)
		}
		if !known(vault.IdeaStatuses, row.Status) {
			return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "status", lineOffset),
				"idea row %d has status %q: valid statuses are %s", i+1, row.Status, strings.Join(vault.IdeaStatuses, ", "))
		}
		for key, value := range map[string]string{
			"created": row.Created, "touched": row.Touched, "path": row.Path,
		} {
			if strings.TrimSpace(value) == "" {
				return Model{}, payload.Errorf(path, payload.LineOfKey(raw, key, lineOffset),
					"idea row %d has no %s", i+1, key)
			}
		}
	}
	return m, nil
}

func requireFields(path string, raw []byte, lineOffset int) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	if err := required(path, raw, lineOffset, root, "schema", "generated", "timezone", "filter", "ideas"); err != nil {
		return err
	}
	var filter map[string]json.RawMessage
	if json.Unmarshal(root["filter"], &filter) == nil {
		if err := required(path, raw, lineOffset, filter, "status", "stale"); err != nil {
			return err
		}
	}
	var rows []json.RawMessage
	if json.Unmarshal(root["ideas"], &rows) == nil {
		for _, rowRaw := range rows {
			var row map[string]json.RawMessage
			if json.Unmarshal(rowRaw, &row) != nil {
				continue
			}
			if err := required(path, raw, lineOffset, row,
				"id", "title", "status", "age_days", "horizon_days", "past_horizon",
				"created", "touched", "shipped_at", "shipped_pr", "path"); err != nil {
				return err
			}
		}
	}
	return nil
}

func required(path string, raw []byte, lineOffset int, object map[string]json.RawMessage, fields ...string) error {
	for _, field := range fields {
		value, ok := object[field]
		if !ok || string(value) == "null" {
			return payload.Errorf(path, payload.LineOfKey(raw, field, lineOffset), "%s is required and must not be null", field)
		}
	}
	return nil
}

func known(vocab []string, value string) bool {
	for _, candidate := range vocab {
		if candidate == value {
			return true
		}
	}
	return false
}

// RenderHTML builds the self-contained page. The committed template owns all
// markup and style; this function only substitutes the validated payload.
func RenderHTML(m Model) ([]byte, error) {
	raw, err := payload.Marshal(m)
	if err != nil {
		return nil, err
	}
	if _, err := Validate("<ideas payload>", raw, 0); err != nil {
		return nil, err
	}
	page, err := payload.Inject(templates.Ideas, Slot, payload.Escape(raw))
	if err != nil {
		return nil, err
	}
	back, line, err := payload.Extract(page, Slot)
	if err != nil {
		return nil, fmt.Errorf("the built ideas surface cannot be read back: %w", err)
	}
	if _, err := Validate("<built ideas surface>", back, line); err != nil {
		return nil, fmt.Errorf("the built ideas surface does not carry a readable payload: %w", err)
	}
	return []byte(page), nil
}
