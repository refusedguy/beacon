# ADR 194: Checkpoint по событиям и replay как часть обязательства

Статус: accepted

## Контекст

CrewAI пишет чекпоинт после task_completed, хранит не более max_checkpoints снимков и умеет Crew.from_checkpoint и crewai replay -t task_id. LangGraph разделяет checkpointer для одного thread и store для данных между thread. OpenHands TaskToolSet сохраняет разговор ребёнка на диск и продолжает по task_id.

## Решение

Обязательство фиксирует event-triggered checkpoint с ограничением числа снимков. Replay начинается с указанной задачи и сохраняет контекст предыдущих. Данные одного обязательства и общие факты хранятся раздельно.

## Последствия

Прерванный прогон не повторяет завершённые задачи. Старые снимки не съедают хранилище. Общие факты не зависят от lifetime одного thread.
