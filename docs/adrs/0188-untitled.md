# ADR 188: Пул агентов на проект настраивается, а не фиксируется константой

Статус: accepted

## Контекст

У OpenRig нетproduct-wide квоты на число агентов. Есть pool.max/min, dispatch_after_s, idle_grace_s, cooldown_s, dispatch_tag, cwd_template и process_limits.tasks_max 1..4194304. Shipped teams: starter 2, factory 7, workshop 4. Beacon имеет 15+ агентов и лимиты 50/200 watch.

## Решение

Перенести параметры пула в .beacon/agents.yaml: max_agents_per_project по умолчанию 20, max_watches_per_client 50, max_watches_global 200, max_capabilities_per_agent 32, max_config_bytes 64 KiB. Дополнительные исполнители клонируются с модели и сливаются после idle_grace_s.

## Последствия

Лимиты проверяются до регистрации. Превышение это типизированный отказ с полем limits. Конфиг меняется без рестарта сервера, но действующие watch сохраняют свой config_id.
