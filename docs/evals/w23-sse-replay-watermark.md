# W23_sse_replay_watermark

## Сценарий

Клиент отключается, приходят события, клиент reconnect с Last-Event-ID. Параллельно transient view.changed.

## Проверки

- События после seq доставлены один раз по порядку
- Transient не имеет seq и не в replay
- watermark не сдвинут

## Негативный контроль

Сохранить transient как stored event и показать дубли в реплее.
