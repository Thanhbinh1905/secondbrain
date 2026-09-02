# templates

Two different things live here.

The Markdown files are the shape of every file `brain-axi add` writes, one per record type.
`board.html` and `recap.html` are not documentation: they are code, embedded by `templates/embed.go`
and rendered by `internal/board` and `internal/recap`. Each carries exactly one
`__BRAIN_AXI_DATA__` slot, and a renderer substitutes one validated JSON payload for that line and
generates nothing else. Everything a board or a recap looks like - layout, styling, pane order and
every empty-state string - is in those two files and nowhere else.

The Markdown files are documentation, not code: `internal/vault/init.go` builds records through
`internal/frontmatter`, which is what guarantees the key order and the quoting.
They exist so the vault format can be read without reading Go, and so somebody writing a file by
hand in Obsidian has something to copy.

`{{...}}` marks a value the tool fills in. A `daily` file has no body of its own; `add note`
appends timestamped bullets to it.

A `task` writes `assignee:`, `due:` and `follow_up_after:` only when it has them, so the template
shows the fullest shape rather than the commonest one. `status:` defaults to `open`, or to `waiting`
when the task has an assignee.

Any record may also carry a linked pull or merge request, added by `brain-axi link`:

```yaml
forge_url: https://github.com/owner/repo/pull/12
forge_state: open
forge_checks: passing
forge_checked_at: 2026-09-02T10:15:00+07:00
```

The last three are a cache of what the forge said and when. They are written only by
`brain-axi pr --refresh`, they are always displayed with their timestamp, and deleting them by hand
costs nothing but the next refresh.

Any record may also carry the linking layer and the fleet bridge:

```yaml
links: [platform-team-sync-20260904]     # ids of other records; `related` reads both directions
raise_with: [platform-team]              # people this is waiting to be raised with
raised: 2026-09-04                       # when it was, which takes it off their agenda
fleet_tasks: [PROJ-42]                   # external work items, written by `link fleet`
shipped_at: 2026-09-04T15:40:00+07:00    # when the work landed, written by `ship`
shipped_pr: https://github.com/owner/repo/pull/14
```

`links:` and `raise_with:` are format-validated only: an id no record claims is an unresolved link
that `doctor` reports with its line, never a parse error. A duplicate or self-referential entry is a
corrupt record. `raise_with:` is refused on a person and on a daily file. `shipped_pr:` needs
`shipped_at:` beside it, for the same reason a cached forge state needs its timestamp.
