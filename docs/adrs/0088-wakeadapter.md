# ADR 88: Интерфейс WakeAdapter и непрозрачные цели

Статус: accepted

## Контекст

Нужен минимальный контракт wake для OpenCode, Pi, Kilo и Cursor. Остальные пять операций из ADR 0024 остаются за пределами моста и здесь не повторяются.

## Решение

WakeAdapter: bind(pointer, registry) -> Target | null, deliver(pointer, sessionID) -> Receipt, observe? и dispose?. PointerSource: next() и commit(). Центральная функция deliver получает непрозрачный sessionID. Отсутствие соответствия даёт skip, а не угадывание.

## Последствия

adapter и target это непрозрачные строки. Ни одного имени агента в internal/domain, internal/app и internal/infra/mcp. Это продолжение ADR 0006 и ADR 0024. bind вызывается один раз при регистрации Watch: для OpenCode источник _meta["ai.opencode/sessionID"], для остальных beacon/sessionID или notify.target.
