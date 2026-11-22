# CBX-17. Явный разбор иерархического ID с ограничением глубины

Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/taskstore/store.go, internal/infra/taskstore/edges.go, internal/infra/taskstore/store_test.go
Зависимости: 
Ссылка: https://github.com/gastownhall/beads (коммит 78624109904655d7a4609f27d34eb0982cd8963f)
Агент: нет
Время: нет
Baseline: beads: internal/types/id_generator.go:103-125, MaxHierarchyDepth равен 3; в beacon exactIDRE в internal/infra/taskstore/store.go:118-120 принимает ID с любым числом точек, а CheckHierarchyDepth отсутствует.

Цель: Не давать дочерним карточкам уходить глубже трёх уровней и не путать точки хеш-суффикса с уровнями иерархии.

Контекст: Источник, реальный код beads. GenerateChildID на internal/types/id_generator.go:43-50 делает parent.N. ParseHierarchicalID на :52-90 возвращает root, parent и depth, считая точки; ExtractPrefix на :92-101 отделяет префикс до первого дефиса. MaxHierarchyDepth на :103-105 равен трём, а CheckHierarchyDepth на :107-127 отклоняет родителя, у которого уже достигнут предел. Разрыв в beacon: набор регулярных выражений в internal/infra/taskstore/store.go:96-125 различает idRE, dotIDRE и hashIDRE, но не содержит ни парсера глубины, ни проверки предела; AddEdge в internal/infra/taskstore/edges.go:103-107 принимает любой ID, прошедший IsID.

Шаги: Добавить ParseCardID, возвращающий root, parent и depth, с отдельной веткой для hash-формы T<g>.<n>.<base36>. Ввести константу предельной глубины и CheckHierarchyDepth. Вызывать проверку при создании дочерней карточки и при добавлении ребра parent-child. Сохранить точное совпадение существующих ID-форм.

Проверка: Измеримая. Цепочка bd-af78e9a2.1.2 принимается, попытка bd-af78e9a2.1.2.3 возвращает код валидации. Парсер для bd-af78e9a2.1.2 возвращает root bd-af78e9a2, parent bd-af78e9a2.1, depth 2. ID T3.17.k9x2 даёт depth 0. Все шесть существующих семейств ID из store.go:97-113 продолжают парситься. Число ложных отказов на корпусе из 1000 реальных ID равно нулю.

Приёмка: CB-01 владеет адаптивной длиной хеша; здесь только иерархия и предел глубины. Формат файлов карточек не меняется.
