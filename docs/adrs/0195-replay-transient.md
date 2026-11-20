# ADR 195: Журнал событий с replay и transient события

Статус: accepted

## Контекст

RigEvent в OpenRig append-only в SQLite. withNotifyEnvelope публикует сохранённые строки после commit. view.changed transient и несёт seq породившего события, не имея своего seq.

## Решение

Beacon ведёт журнал с курсором replay и отделяет долгоживущие события от transient уведомлений об изменении проекций.

## Последствия

Reconnect продолжает с Last-Event-ID. Transient события не смещают watermark и не попадают в replay.
