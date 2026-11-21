# roadmap-sync

Reconcile `docs/tasks/*.md` status lines and `docs/ROADMAP_STATUS.md` counts with the commits merged into main. Also rebuild `docs/adrs/README.md` from the ADR files.

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

`--write` changes the `Статус:` line in each affected card, replaces the done, dropped and todo counts and the priority lines in `docs/ROADMAP_STATUS.md`, and rebuilds `docs/adrs/README.md`. The priority lines count todo cards only and are grouped by their `Фаза:` field.

## ADR index

The index is generated from `docs/adrs/*.md`. The number comes from the filename prefix. The topic comes from the first heading of the ADR, for example `# ADR 260: Fixer как отдельная роль подписчика`. The status comes from the `Статус:` field. Rows sort by number, then by filename, so the two ADR 0260 files keep a stable order.

`docs/adrs/README.md` is derived and gitignored (IDX-11). A fresh checkout has no index. `--write` creates it when ADR files exist and leaves a tree without ADR files alone. A dry run reports the change without writing the file.

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
docs/adrs/README.md: update from 304 ADR files
done: 357
dropped: 332
todo: 210
priorities (todo only): P0: 8 P1: 12 P2: 11 P3: 78 P4: 7 P5: 21 P6: 2 P8: 71
```

## Scope

The tool reads and writes only card status lines, the count and priority lines in `docs/ROADMAP_STATUS.md`, and the generated ADR index. It leaves the todo registry table alone.

Wiring cards (`WIRING-*.md`) and `.beacon/wiring-pending` are excluded. The wiring gate owns them.

## Tests

```bash
go test ./tools/roadmap-sync
```

Tests use a fixed commit list, so they do not need a git checkout or network access. They cover ID extraction, the dropped and done priority rules, counts, dry-run output, the files that must stay untouched, and ADR index generation with topic, status, duplicate order and no-write behavior.
