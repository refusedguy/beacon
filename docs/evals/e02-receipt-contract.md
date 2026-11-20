# E02_receipt_contract

## Сценарий

Вердикт CI даёт ровно одну строку events.jsonl, плагин создаёт атомарный claim receipts/<id>.wrote, вызывает ctx.session.prompt с delivery=steer и пишет receipts.jsonl state=posted. Plugin-events.jsonl фиксирует receipt-posted.

## Проверки

- receipts/<obligationID>.wrote создан через open с флагом wx и не перезаписывается
- receipts.jsonl содержит ровно одну запись state=posted на пару (obligationID, attempt)
- store импортирует квитанцию через internal/infra/store/receipts.go
- Нет квитанции, значит indeterminate, а не delivery=steer

## Негативный контроль

Заглушка или плагин без записи квитанции не проходит verifyWake: prompt без receipts/<id>.wrote и receipts.jsonl state=posted считается провалом.
