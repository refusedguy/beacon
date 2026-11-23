# Ветвление исполнителя после карточки todo

Режим документа: explanation. Схема показывает, что происходит с карточкой в статусе `todo` после premise-check. Четыре выхода из ромба описаны в отчёте ночной смены 06-card-closing. Пятая стрелка ведёт в блокер и означает ожидание, а не завершение.

## Схема

<svg viewBox="0 0 1240 1150" width="1240" height="1150" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Схема четырёх веток после premise-check, блокер, слой идемпотентности и счётчики">
  <title>Ветвление исполнителя после карточки todo</title>
  <desc>Ромб premise-check имеет четыре подписанных выхода и пунктирную стрелку в блокер. Внизу показаны слой идемпотентности и полосы со счётчиками.</desc>
  <defs>
    <marker id="arrow-green" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="#3fb950"/></marker>
    <marker id="arrow-yellow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="#d29922"/></marker>
    <marker id="arrow-blue" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="#58a6ff"/></marker>
    <marker id="arrow-gray" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="#9aa7b4"/></marker>
    <marker id="arrow-muted" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="#bc8cff"/></marker>
    <style>
      .flow{fill:none;stroke-width:2.5;animation:pulse 2.4s ease-in-out infinite}
      .flow.green{stroke:#3fb950;marker-end:url(#arrow-green)}
      .flow.yellow{stroke:#d29922;marker-end:url(#arrow-yellow)}
      .flow.blue{stroke:#58a6ff;marker-end:url(#arrow-blue)}
      .flow.gray{stroke:#9aa7b4;marker-end:url(#arrow-gray)}
      .flow.blocked{stroke:#bc8cff;stroke-dasharray:3 6;animation:none;marker-end:url(#arrow-muted)}
      .flow.reaper{stroke:#d29922;marker-end:url(#arrow-yellow)}
      @keyframes pulse{0%,100%{opacity:1}50%{opacity:.7}}
      @media (prefers-reduced-motion: reduce){.flow{animation:none}}
      .lbl{font:13px Arial,sans-serif;fill:#e6edf3}
      .edge{font:12px Arial,sans-serif}
      .ttl{font:700 16px Arial,sans-serif;fill:#e6edf3}
      .mut{font:12px Arial,sans-serif;fill:#9aa7b4}
    </style>
  </defs>

  <rect x="0" y="0" width="1240" height="1150" fill="#0d1117"/>
  <text x="24" y="28" fill="#9aa7b4" font-family="Arial, sans-serif" font-size="13">Замер 2026-11-22 · база 29cda75b · источник: отчёт 06-card-closing</text>

  <rect x="470" y="52" width="300" height="56" rx="10" fill="#1c2330" stroke="#58a6ff" stroke-width="2"/>
  <text x="620" y="86" text-anchor="middle" fill="#e6edf3" font-family="Arial, sans-serif" font-size="17" font-weight="700">Карточка todo</text>

  <line x1="620" y1="108" x2="620" y2="138" stroke="#58a6ff" stroke-width="2.5" marker-end="url(#arrow-blue)"/>

  <polygon points="620,140 830,240 620,340 410,240" fill="#1c2330" stroke="#58a6ff" stroke-width="2"/>
  <text x="620" y="200" text-anchor="middle" fill="#e6edf3" font-family="Arial, sans-serif" font-size="17" font-weight="700">Premise-check</text>
  <text x="620" y="228" text-anchor="middle" fill="#9aa7b4" font-family="Arial, sans-serif" font-size="13">коммит в main?</text>
  <text x="620" y="252" text-anchor="middle" fill="#9aa7b4" font-family="Arial, sans-serif" font-size="13">открытый PR / OwnerFiles?</text>
  <text x="620" y="276" text-anchor="middle" fill="#9aa7b4" font-family="Arial, sans-serif" font-size="13">требование живо?</text>

  <path class="flow green" d="M 410 240 C 300 260, 170 330, 165 485" stroke="#3fb950" marker-end="url(#arrow-green)"/>
  <text x="205" y="370" class="edge" fill="#3fb950">merge-base exit 0</text>
  <text x="205" y="396" class="ttl" fill="#3fb950">161</text>

  <path class="flow yellow" d="M 530 320 C 480 360, 455 410, 450 485" stroke="#d29922" marker-end="url(#arrow-yellow)"/>
  <text x="445" y="395" text-anchor="end" class="edge" fill="#d29922">открытый PR или</text>
  <text x="445" y="413" text-anchor="end" class="edge" fill="#d29922">OwnerFiles заняты</text>
  <text x="445" y="447" text-anchor="end" class="ttl" fill="#d29922">7</text>

  <path class="flow blue" d="M 620 340 L 735 485" stroke="#58a6ff" marker-end="url(#arrow-blue)"/>
  <text x="755" y="425" class="edge" fill="#58a6ff">чистая карточка</text>

  <path class="flow gray" d="M 830 240 C 950 270, 1040 340, 1045 485" stroke="#9aa7b4" marker-end="url(#arrow-gray)"/>
  <text x="1000" y="390" text-anchor="end" class="edge" fill="#9aa7b4">требование устарело</text>
  <text x="1000" y="410" text-anchor="end" class="edge" fill="#9aa7b4">ST-05</text>

  <path class="flow blocked" d="M 770 180 L 898 170" stroke="#bc8cff" stroke-dasharray="3 6" marker-end="url(#arrow-muted)"/>
  <text x="895" y="128" text-anchor="end" class="edge" fill="#bc8cff">PR #1837 / #1878</text>
  <text x="895" y="146" text-anchor="end" class="edge" fill="#bc8cff">ещё открыт</text>

  <rect x="900" y="64" width="310" height="150" rx="10" fill="#1c2330" stroke="#bc8cff" stroke-width="2"/>
  <text x="916" y="98" class="ttl" fill="#bc8cff">Блокер: ждём, не завершение</text>
  <text x="916" y="125" class="lbl">#1872 CCx-23 · #1894 CV-10</text>
  <text x="916" y="149" class="mut">PR #1837 в apply.go, PR #1878 в schema.go</text>
  <text x="916" y="178" class="lbl">Карточка остаётся todo, ждём</text>

  <rect x="30" y="485" width="280" height="120" rx="10" fill="#161b22" stroke="#3fb950" stroke-width="2"/>
  <text x="45" y="515" class="ttl" fill="#3fb950">Docs-only close</text>
  <text x="45" y="541" class="lbl">статус + SHA + Причина</text>
  <text x="45" y="582" fill="#3fb950" font-family="Arial, sans-serif" font-size="22" font-weight="700">161</text>

  <rect x="315" y="485" width="270" height="120" rx="10" fill="#161b22" stroke="#d29922" stroke-width="2"/>
  <text x="330" y="515" class="ttl" fill="#d29922">Docs-only close</text>
  <text x="330" y="541" class="lbl">duplicateOf / OwnerFiles</text>
  <text x="330" y="582" fill="#d29922" font-family="Arial, sans-serif" font-size="22" font-weight="700">7</text>

  <rect x="600" y="485" width="270" height="56" rx="10" fill="#161b22" stroke="#58a6ff" stroke-width="2"/>
  <text x="735" y="519" text-anchor="middle" fill="#e6edf3" font-family="Arial, sans-serif" font-size="14" font-weight="700">Реальный код в OwnerFiles</text>

  <line x1="735" y1="541" x2="735" y2="563" stroke="#58a6ff" stroke-width="2.5" marker-end="url(#arrow-blue)"/>
  <rect x="600" y="565" width="270" height="44" rx="10" fill="#161b22" stroke="#58a6ff" stroke-width="2"/>
  <text x="735" y="593" text-anchor="middle" class="lbl">CI</text>

  <line x1="735" y1="609" x2="735" y2="631" stroke="#58a6ff" stroke-width="2.5" marker-end="url(#arrow-blue)"/>
  <rect x="600" y="633" width="270" height="44" rx="10" fill="#161b22" stroke="#58a6ff" stroke-width="2"/>
  <text x="735" y="661" text-anchor="middle" class="lbl">Merge</text>

  <line x1="735" y1="677" x2="735" y2="699" stroke="#58a6ff" stroke-width="2.5" marker-end="url(#arrow-blue)"/>
  <rect x="600" y="701" width="270" height="52" rx="10" fill="#161b22" stroke="#58a6ff" stroke-width="2"/>
  <text x="735" y="733" text-anchor="middle" class="lbl">Авто-close по cardID</text>

  <rect x="885" y="485" width="325" height="120" rx="10" fill="#161b22" stroke="#9aa7b4" stroke-width="2"/>
  <text x="900" y="515" class="ttl" fill="#9aa7b4">Docs-only close</text>
  <text x="900" y="541" class="lbl">false premise</text>
  <text x="900" y="565" class="mut">ST-05 · файл удалён в #1622</text>
  <text x="900" y="585" class="mut">возврат файла откатил бы IDX-11</text>

  <text x="30" y="800" fill="#e6edf3" font-family="Arial, sans-serif" font-size="18" font-weight="700">Слой идемпотентности</text>

  <path class="flow reaper" d="M 40 865 L 18 865 L 18 80 L 468 80" stroke="#d29922" marker-end="url(#arrow-yellow)"/>
  <text x="34" y="76" class="edge" fill="#d29922">1465: in_progress → todo</text>

  <rect x="40" y="825" width="300" height="80" rx="10" fill="#161b22" stroke="#d29922" stroke-width="2"/>
  <text x="55" y="855" class="ttl" fill="#d29922">#1465 TTL-reaper</text>
  <text x="55" y="879" class="mut">нет живого Watch и процесса</text>

  <path class="flow blue" d="M 600 865 L 560 865 L 560 775 L 170 775 L 170 607" stroke="#58a6ff" marker-end="url(#arrow-blue)"/>
  <text x="365" y="766" text-anchor="middle" class="edge" fill="#58a6ff">1966: повторный close → created=false</text>

  <rect x="600" y="825" width="300" height="80" rx="10" fill="#161b22" stroke="#58a6ff" stroke-width="2"/>
  <text x="615" y="855" class="ttl" fill="#58a6ff">#1966 created flag</text>
  <text x="615" y="879" class="mut">первый close и повтор различаются</text>

  <text x="30" y="950" fill="#e6edf3" font-family="Arial, sans-serif" font-size="18" font-weight="700">Счётчики за сутки</text>

  <text x="180" y="989" text-anchor="end" class="lbl">влито PR</text>
  <rect x="195" y="971" width="900" height="26" rx="5" fill="#58a6ff"/>
  <text x="1107" y="989" fill="#e6edf3" font-family="Arial, sans-serif" font-size="13" font-weight="700">728</text>

  <text x="180" y="1033" text-anchor="end" class="lbl">карточных PR</text>
  <rect x="195" y="1015" width="242" height="26" rx="5" fill="#bc8cff"/>
  <text x="449" y="1033" fill="#e6edf3" font-family="Arial, sans-serif" font-size="13" font-weight="700">196</text>

  <text x="180" y="1077" text-anchor="end" class="lbl">закрыто карточек</text>
  <rect x="195" y="1059" width="252" height="26" rx="5" fill="#3fb950"/>
  <text x="459" y="1077" fill="#e6edf3" font-family="Arial, sans-serif" font-size="13" font-weight="700">204</text>

  <text x="180" y="1121" text-anchor="end" class="lbl">доказательное закрытие</text>
  <rect x="195" y="1103" width="199" height="26" rx="5" fill="#d29922"/>
  <text x="406" y="1121" fill="#e6edf3" font-family="Arial, sans-serif" font-size="13" font-weight="700">161</text>
</svg>

## Четыре выхода из ромба

| Ребро | Действие агента | Счётчик или пример |
| --- | --- | --- |
| merge-base exit 0 | Меняет только карточку: статус, полный SHA, причину. Код не пишет. | 161 |
| открытый PR или OwnerFiles заняты | Закрывает карточку с `duplicateOf` или причиной занятых файлов. | 7 |
| требование устарело | Закрывает карточку как false premise, код не трогает. | ST-05 |
| чистая карточка | Правит OwnerFiles, проходит CI, мержится, получает авто-close по cardID. | код и CI на PR |

ST-05 просила сверять числа в `docs/ROADMAP_STATUS.md`. IDX-11 (#1622) удалил файл и добавил его в `.gitignore`. Выполнение требования откатило бы IDX-11, поэтому карточку закрыли как ложную premise.

Две карточки ждут открытых PR. #1872 (CCx-23) ждёт PR #1837 в `apply.go`. #1894 (CV-10) ждёт PR #1878 в `schema.go`. Обе остаются `todo`, и пунктирная стрелка показывает это ожидание.

## Счётчики

Замер сделан 2026-11-22 на базе `29cda75b`. Источник: отчёт ночной смены 06-card-closing и набор `merged24_full.json`. За сутки влилось 728 PR, из них 196 карточных. 168 карточных PR содержат `closed`: 161 точечное закрытие по шаблону `already landed` и 7 закрытий с другой причиной. Четыре массовых PR закрыли ещё 36 карточек, итого 204 закрытые карточки.

## Идемпотентность

TTL-reaper из #1465 возвращает карточку из `in_progress` в `todo`, когда нет живого Watch и процесса. Поле `created` из #1966 отделяет первый close от повторного. Повторный close получает `created=false` и не меняет результат, если прерванная запись уже договорила работу. Именно поэтому стрелка от #1966 уходит в блок Docs-only close, а не в блок с реальным кодом.

Правила закрытия живут в [CLOSE-PROCEDURE.md](../runbooks/CLOSE-PROCEDURE.md). Схема их не меняет.
