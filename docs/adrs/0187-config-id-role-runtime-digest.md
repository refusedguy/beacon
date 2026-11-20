# ADR 187: config_id это отсортированный список role=runtime, digest обязателен

Статус: accepted

## Контекст

OpenRig определяет configuration ID как отсортированный список pod.member=runtime, например dev.build=claude-code,orch.lead=codex. Псевдонимы только подписи. registry хранит commit, digest пакета и статус listed/withdrawn. Beacon уже имеет поля config_id и digest, но без семантики канонического ID.

## Решение

Канонический config_id вычислять сервером как отсортированный список role=runtime из .beacon/agents.yaml. Регистрация с несовпавшим digest отклоняется ошибкой config_digest_mismatch, а не предупреждением. Изменение конфига создаёт новый config_id.

## Последствия

Watch не привязывается к конфигурации, которую исполнитель не видел. Нужна версия конфига в watch_bindings. Авторский YAML не содержит sessionID, watchID и токенов.
