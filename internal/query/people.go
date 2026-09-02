package query

import (
	"sort"

	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// AgendaItem is one thing waiting to be raised with somebody: the record
// itself, plus how long it has been waiting.
type AgendaItem struct {
	Record *vault.Record
	// WaitingDays counts calendar days since the record was last touched, or
	// since it was created when the kind carries no touched date. It is the
	// only honest measure of "how long has this been on the list".
	WaitingDays int
}

// PersonProfile is everything the vault knows about one person: what is
// assigned to them, what has closed, and what is waiting to be raised.
//
// Every part of it is derived from the other records. Nothing about a person's
// workload is stored in the people/ file, because a copy of a fact in a second
// place is a copy that can disagree with the first - the same reason a task's
// assignee lives on the task rather than in a list on the profile.
type PersonProfile struct {
	ID string
	// Record is the people/ record, or nil when other records name an id that
	// no profile claims. A missing profile is reported, never invented.
	Record *vault.Record
	// Open and Closed are the tasks handed to this person, most urgent first
	// and most recently touched first respectively.
	Open   []TaskRow
	Closed []TaskRow
	// Agenda is what is waiting to be raised with them, longest-waiting first.
	Agenda []AgendaItem
	// Events are the occurrences this person is named in, over whatever window
	// the caller asked about. It is nil unless the caller filled it.
	Events []vault.Occurrence
}

// Person assembles one person's profile.
func (e *Engine) Person(id string) (PersonProfile, error) {
	records, err := e.Vault.Walk()
	if err != nil {
		return PersonProfile{}, err
	}
	profile := PersonProfile{ID: id}
	for _, r := range records {
		if r.Kind == vault.KindPerson && r.ID == id {
			profile.Record = r
			break
		}
	}
	tasks, err := e.Tasks(TaskFilter{Assignee: id})
	if err != nil {
		return PersonProfile{}, err
	}
	for _, row := range tasks {
		if vault.TaskIsOpen(row.Record.Status) {
			profile.Open = append(profile.Open, row)
			continue
		}
		profile.Closed = append(profile.Closed, row)
	}
	profile.Agenda = e.agendaFor(records, id)
	return profile, nil
}

// PersonAgenda is what is waiting to be raised with one person, which is what
// is wanted in the thirty seconds before walking into the room.
func (e *Engine) PersonAgenda(id string) ([]AgendaItem, error) {
	records, err := e.Vault.Walk()
	if err != nil {
		return nil, err
	}
	return e.agendaFor(records, id), nil
}

// PersonAgendas is every person's agenda in one pass, keyed by person id. The
// agenda surfaces beside an event, so today, week and the board would
// otherwise walk the vault once per attendee.
func (e *Engine) PersonAgendas() (map[string][]AgendaItem, error) {
	records, err := e.Vault.Walk()
	if err != nil {
		return nil, err
	}
	out := map[string][]AgendaItem{}
	for _, r := range records {
		if !r.WaitingToBeRaised() {
			continue
		}
		item := AgendaItem{Record: r, WaitingDays: e.Vault.AgeDays(r, e.Now)}
		for _, who := range r.RaiseWith {
			out[who] = append(out[who], item)
		}
	}
	for who := range out {
		sortAgenda(out[who])
	}
	return out, nil
}

func (e *Engine) agendaFor(records []*vault.Record, id string) []AgendaItem {
	var out []AgendaItem
	for _, r := range records {
		if !r.WaitingToBeRaised() || !hasString(r.RaiseWith, id) {
			continue
		}
		out = append(out, AgendaItem{Record: r, WaitingDays: e.Vault.AgeDays(r, e.Now)})
	}
	sortAgenda(out)
	return out
}

// sortAgenda puts the longest-waiting item first, ties broken by id so the
// order never depends on filesystem order.
func sortAgenda(items []AgendaItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].WaitingDays != items[j].WaitingDays {
			return items[i].WaitingDays > items[j].WaitingDays
		}
		return items[i].Record.ID < items[j].Record.ID
	})
}

// AttendeeAgendas is the agenda of every person named in an agenda's events,
// so a day's or a week's answer can carry what to raise with whom.
func (e *Engine) AttendeeAgendas(ag Agenda) (map[string][]AgendaItem, error) {
	all, err := e.PersonAgendas()
	if err != nil {
		return nil, err
	}
	out := map[string][]AgendaItem{}
	for _, occ := range ag.Occurrences {
		for _, who := range occ.Record.With {
			if items, ok := all[who]; ok {
				out[who] = items
			}
		}
	}
	return out, nil
}
