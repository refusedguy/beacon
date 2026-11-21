# roadmap-sync

Reconcile `docs/tasks/*.md` status lines and `docs/ROADMAP_STATUS.md` counts with the commits merged into main.

The tool reads `git log`, keeps only commits with a `(#NNNN)` PR suffix, and extracts card IDs from those subjects. It recognizes `BS.11`, `HD-070`, `T3.235`, `T5.55`, `S1.008`, `UI-01`, `WIRING-001`, `CLEAN-01` and `ARDH-23`, plus slugged filenames such as `T4.7-rollback-drill.md`. Commits without a PR suffix do not count as merged.

## Status rules

A status is decided from three signals in this order: dropped, done, todo.

- A card already marked `dropped` stays dropped, even when a merged commit mentions it.
- A card already marked `done` stays done.
- A merged commit that names the card sets the status to done.
- Everything else stays or becomes `todo`.

The tool never downgrades a done or dropped card.

## Usage

Run from the repository root:

```bash
go run ./tools/roadmap-sync
```

Without flags the tool prints the proposed changes and the final counts. It writes nothing.

```bash
go run ./tools/roadmap-sync --write
```

`--write` changes the `Статус:` line in each affected card and replaces the done, dropped and todo counts and the priority lines in `docs/ROADMAP_STATUS.md`. The priority lines count todo cards only and are grouped by their `Фаза:` field.

Other flags:

- `--repo <dir>` selects the repository root. Default is `.`.
- `--git-log <file>` reads commit subjects from a file instead of running git. Use `-` to read standard input. This is how tests and offline runs feed a fixed commit list.

```bash
go run ./tools/roadmap-sync --repo /path/to/beacon --git-log /tmp/commits.txt
```

## Output

```text
roadmap-sync: dry run, merged commits 362, merged cards 241, cards 899, changed 250
docs/tasks/BS.11.md: todo -> done
docs/tasks/HD-070.md: todo -> done
ROADMAP_STATUS.md: Закрыто done 110 -> 357
ROADMAP_STATUS.md: P1 17 -> 12
done: 357
dropped: 332
todo: 210
priorities (todo only): P0: 8 P1: 12 P2: 11 P3: 78 P4: 7 P5: 21 P6: 2 P8: 71
```

## Scope

The tool reads and writes only card status lines and the count and priority lines in `docs/ROADMAP_STATUS.md`. It leaves the todo registry table alone.

Wiring cards (`WIRING-*.md`) and `.beacon/wiring-pending` are excluded. The wiring gate owns them.

## Tests

```bash
go test ./tools/roadmap-sync
```

Tests use a fixed commit list, so they do not need a git checkout or network access. They cover ID extraction, the dropped and done priority rules, counts, dry-run output and the files that must stay untouched.
