#!/usr/bin/env python3
"""Аудит batch-пакета bcx.json: отсутствующие файлы, ссылки на каталоги и
пересечения owner_files внутри одной волны (фазы).

Только stdlib. Запуск из любого каталога репозитория:

    python3 docs/research/batches/fix_bcx.py [path/to/bcx.json]

Множество реальных файлов берётся из `git ls-tree -r --name-only origin/main`.
Волной считается группа карточек с одинаковым `phase`, как в batchapply и
BATCH-VALIDATION.md. Ссылки на генерируемые документы из .gitignore (IDX-11)
не считаются ошибкой: batchapply их допускает.
"""

import json
import subprocess
import sys
from collections import defaultdict
from pathlib import Path

# IDX-11: эти документы создаются генераторами и намеренно не лежат в git.
GENERATED = {
    "docs/FACTS.md",
    "docs/ROADMAP_STATUS.md",
    "docs/adrs/README.md",
}


def repo_root():
    out = subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], text=True
    )
    return Path(out.strip())


def real_files(root):
    out = subprocess.check_output(
        ["git", "ls-tree", "-r", "--name-only", "origin/main"],
        cwd=root,
        text=True,
    )
    return {line for line in out.splitlines() if line}


def is_directory_ref(path, files):
    """Путь - каталог, если он префикс реального файла с завершающим /."""
    prefix = path.rstrip("/") + "/"
    return any(f.startswith(prefix) for f in files)


def analyze(cards, files):
    """Возвращает (строки отчёта по карточке, сводку по категориям)."""
    # owner_file -> phase -> [id]
    by_file_phase = defaultdict(lambda: defaultdict(list))
    for c in cards:
        for f in c.get("owner_files", []):
            by_file_phase[f][c["phase"]].append(c["id"])

    overlaps_by_card = defaultdict(list)  # id -> [(файл, [конкуренты])]
    for f, phases in by_file_phase.items():
        for phase, ids in phases.items():
            if len(ids) > 1:
                for cid in ids:
                    others = [x for x in ids if x != cid]
                    overlaps_by_card[cid].append((f, others))

    # Сводка считает уникальные ссылки и группы (file, phase) с более чем
    # одной карточкой. Именно так считает BATCH-VALIDATION.md: 10 + 2 + 44.
    uniq = defaultdict(set)
    for c in cards:
        for f in c.get("owner_files", []):
            if f in GENERATED or f in files:
                continue
            key = "directory" if is_directory_ref(f, files) else "missing"
            uniq[key].add((c["id"], f))
    for f, phases in by_file_phase.items():
        for phase, ids in phases.items():
            if len(ids) > 1:
                uniq["overlap"].add((f"phase={phase}", f, tuple(ids)))

    lines = []
    for c in cards:
        cid, phase = c["id"], c["phase"]
        problems = []
        for f in c.get("owner_files", []):
            if f in GENERATED or f in files:
                continue
            if is_directory_ref(f, files):
                problems.append(f"directory ref: {f}")
            else:
                problems.append(f"missing file: {f}")
        for f, others in overlaps_by_card.get(cid, []):
            problems.append(f"overlap [{f}] with {', '.join(others)}")
        if problems:
            lines.append(f"{cid} (phase={phase}): " + " | ".join(problems))

    summary = (
        f" cards={len(cards)} missing={len(uniq['missing'])} "
        f"directory_refs={len(uniq['directory'])} "
        f"overlap_groups={len(uniq['overlap'])}"
    )
    return lines, summary, uniq


def main():
    root = repo_root()
    default = root / "docs/research/batches/bcx.json"
    path = Path(sys.argv[1]) if len(sys.argv) > 1 else default
    if not path.is_absolute():
        path = root / path
    data = json.loads(path.read_text(encoding="utf-8"))
    cards = data["cards"]
    files = real_files(root)

    lines, summary, uniq = analyze(cards, files)
    print(f"=== bcx audit: {path} ===")
    for line in lines:
        print(line)
    print("=== SUMMARY ===")
    print(summary)
    for cat in ("missing", "directory", "overlap"):
        print(f"-- {cat} ({len(uniq[cat])}) --")
        for row in sorted(uniq[cat]):
            flat = []
            for item in row:
                flat.extend(item) if isinstance(item, tuple) else flat.append(item)
            print("   " + " | ".join(flat))
    # Ненулевой код выхода, чтобы скрипт можно было использовать как гейт.
    return 1 if any(uniq[k] for k in ("missing", "directory", "overlap")) else 0


if __name__ == "__main__":
    sys.exit(main())
