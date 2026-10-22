# Requests for Comments

TEAM DECISION: RFC — обсуждаемые предложения, не source of truth и не разрешение реализовывать вариант как принятый. Схема из `feature/db-rfc` имеет предварительный статус до отдельного согласования.

Статусы: DRAFT → PROPOSED → UNDER_REVIEW → ACCEPTED либо REJECTED; заменённый RFC → SUPERSEDED со ссылкой. Нумерация последовательная; существующие имена из другой ветки сверить при merge, не переименовывая чужие файлы автоматически.

Минимальные разделы: title/number, status, authors, created/updated; Context, Problem, Goals, Non-goals, Options considered (pros/cons), Evaluation criteria, Evidence/Experiments, Proposed solution, Consequences, Risks, Open questions, Decision, Related.

Решение требует RFC, если затрагивает несколько компонентов, API/хранение/качество, имеет существенные tradeoffs и не может быть честно описано одной строкой TBD. Здесь два DRAFT RFC — vector storage и generation lifecycle. Ни один вариант не принят. Для мелких TBD достаточно [реестра](../planning/decision-register.md).

После ACCEPTED обновить source-of-truth документы, создать ADR при архитектурной значимости и связать документы. REJECTED сохраняет краткую причину; не удалять историю. Merge одного RFC в main не означает его принятие, если статус остаётся DRAFT/PROPOSED/UNDER_REVIEW.
