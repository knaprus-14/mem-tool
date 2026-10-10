# Проверка alpha.37 — исправления код-ревью

Дата: 09.10.2026. Версия исходников: **1.71.0-alpha.37**.

Исправлены семь дефектов R1–R7 из [отчёта код-ревью](../review/2026-10-09/REPORT_RU.html).
Дополнительно закрыт связанный с R2 обход при повторной генерации графа.
Исходные данные пользователя и история Git не изменялись. Исправления внесены
поверх существующего рабочего дерева, включая ранее неотслеживаемые файлы.

## Что исправлено и чем проверено

| Дефект | Поведение alpha.37 | Основные регрессии |
| --- | --- | --- |
| **R1, P1. Перепривязка старой цитаты к другому физическому оригиналу** | Новая ревизия учитывает извлечённое содержимое, SHA-256 исходных байтов и сохранённый логический путь. Конфликт прежнего fingerprint отклоняет всю транзакцию. Одинаковый текст при изменившемся PDF больше не означает ту же физическую редакцию. | `TestPhysicalRevisionPinsBytesAndOriginalLocator`, `TestPhysicalFingerprintRejectsEditionMutationAtomically`, `TestPhysicalImportChangedBytesWithSameTextStalesOldEvidence`, `TestPhysicalSameTextUploadRestoreKeepsOriginalFingerprint` |
| **R2, P1. Подтверждение непросмотренной формулировки** | Веб и batch-план требуют `expected_review_digest` вместе с evidence. Проверяются точные label/body, kind, origin и концы связи в той же транзакции, что записывает решение. Нет pin → 400; несовпадение → 409. Правило действует и на reject/reopen/undo. | `TestKnowledgeApprovalPinsNodeTextAndEdgeStructure`, `TestKnowledgeApprovalMissingPinHTTPAndLegacyAudit`, `TestKnowledgeReviewDecisionsRejectChangedContent`, `TestMaintenanceApprovalRequiresReviewedContent` |
| **R3, P2. Worker перезаписывает отмену задания** | Захват выполняется условным `UPDATE … WHERE status = 'queued'`; worker начинает работу только при успешном изменении строки. | `TestWebOperationClaimPreservesCancellation`, существующие cancel/restart recovery tests |
| **R4, P2. Исчерпание кеша графов после повторных входов** | Выход и expiry освобождают сеанс. Поздний запрос не создаёт его заново. Закрытие БД ждёт активные HTTP-запросы и фоновые runners; обычное LRU сохраняет задания и доступные результаты. | `TestGraphSessionsReleaseOnLogoutAndExpiry`, `TestGraphLogoutDrainsActiveRequestAndRejectsStaleSession`, `TestGraphLRUPreservesPinnedJobsAndEvictsIdleHandlers`, набор `TestSharedGraph…` |
| **R5, P2. Недоступная соседняя база блокирует доступные** | В локальном режиме отсутствующий/недоступный `.mem` не ломает подсчёт квоты других баз; интерфейс отмечает базу и предлагает повторить проверку. Обращение к самой недоступной базе остаётся ошибкой. Серверный режим запрещает новые записи при ошибке учёта. | `TestOfflineWorkspaceLocalRecoveryAndHostedQuotaFailClosed` |
| **R6, P2. Evidence недоступен при полной квоте** | POST `/evidence` классифицируется как чтение. Просмотр работает, запрет новых записей сохраняется. | `TestEvidenceRemainsReadableAtStorageLimit` |
| **R7, P2. Legacy index меняет web identity документа** | `mem index` сохраняет managed `web:` ID. Прямая подмена ID и индексация устаревшего retained original отклоняются транзакционно. | `TestLegacyIndexPreservesManagedIdentityAndRejectsOldOriginal`, `TestManagedIdentityCannotChangeThroughDirectReplacement` |

Тесты физического источника находятся в
[`pkg/ingest/physical_revision_test.go`](../pkg/ingest/physical_revision_test.go) и
[`pkg/mem/physical_source_regression_test.go`](../pkg/mem/physical_source_regression_test.go);
подтверждений — в [`graph_review_pin_test.go`](../pkg/mem/graph_review_pin_test.go);
жизненного цикла — в [`graph_lifecycle_test.go`](../internal/webapp/graph_lifecycle_test.go)
и [`web_graph_lifecycle_test.go`](../pkg/mem/web_graph_lifecycle_test.go).

### Дополнительная защита подтверждений

Stable ID сгенерированного узла может остаться прежним при изменении регистра
формулы. Теперь повторная генерация сохраняет прежний review-статус только
при точном совпадении эффективного содержимого/структуры и evidence.
Изменение возвращает объект в draft; зависимые подтверждённые связи также
теряют подтверждение. Сохранённые ручные правки учитываются до сравнения.

Это проверяют четыре `TestGeneratedUpsert…` в `graph_review_pin_test.go`:
изменение формулы при том же ID, сохранение ручного override, прежние состояния
active/rejected/resolved и изменение структуры связи.

Проверка настоящих JS-обработчиков форм дополнительно обнаружила и устранила
несовпадение полей `/api/workspace/create` со строгой серверной структурой.
[`knowledgeForms.test.cjs`](../pkg/mem/web/knowledgeForms.test.cjs) выполняет
обработчики отправки: все четыре review-действия передают просмотренный pin,
а форма создания отправляет только разрешённые сервером поля.

## Совместимость и границы гарантий

- Старые anchors, fingerprints и review-записи не переписываются. Утраченная
  историческая привязка оригинала автоматически не восстанавливается.
  Явный реимпорт legacy-документа создаёт физическую ревизию; старые ссылки
  могут стать `stale` и требуют повторной сверки.
- Portable-перенос и retained rechunk сохраняют логический путь исходника.
  Windows drive/UNC-пути нормализуются одинаково независимо от ОС; POSIX-пути
  сохраняют чувствительность к регистру. Это проверяется отдельно.
- Новый pin хранится в append-only `knowledge_review_pins`. У прежних audit-записей
  digest остаётся неизвестным; доказательство просмотра им не приписывается.
- Старые batch JSON без `expected_review_digest` нужно пересоздать из свежего
  `mem map status --json`. Открытые страницы графа нужно обновить после установки.
- CLI `mem map approve` без `--review-digest` явно подтверждает текущую редакцию
  по ID внутри транзакции. Для подтверждения ранее просмотренной редакции нужен
  `--review-digest` из её status JSON. Это не равнозначные режимы.
- Undo дополнительно закрепляет последнюю запись истории. Прочие решения
  проверяют текущую редакцию/статус/evidence; они не обещают обнаруживать
  промежуточные решения, после которых состояние полностью вернулось к прежнему.
- Выход или истечение авторизации отменяют фоновые задания этого сеанса.
  Последующие запросы старой сессии получают 401; БД закрывается только после
  выхода runners. Долговечность результатов после logout не обещается.

## Выполненные проверки

Среда: Windows/amd64, Go 1.26.5; проект объявляет Go 1.25.0. Использован доступный
кеш `build/go-cache`. Тесты работают на временных БД и файлах.

| Проверка | Результат |
| --- | --- |
| `go test ./... -count=1 -timeout=300s` после последних Go-изменений | PASS: 13 пакетов с тестами, ещё 2 без тестов; `pkg/mem` 83.787 s |
| `go vet ./...` | PASS |
| Целевые workspace/approval/view tests после окончательной правки HTML-шаблона | PASS, `pkg/mem` 2.121 s |
| `node pkg/mem/web/knowledgeForms.test.cjs` | PASS, 2 теста |
| `node pkg/mem/web/knowledgeSourcePreview.test.cjs` | PASS, 2 теста |
| `node pkg/mem/web/knowledgeTopics.test.cjs` | PASS, 8 тестов; 2 проверки частного корпуса пропущены |
| `node --check` для app.js, operations.js и script из HTML-шаблона | PASS |
| `gofmt -l` по изменённым Go-файлам | Нет неформатированных файлов |
| `git diff --check` | PASS; Git предупреждает об ожидаемом преобразовании CRLF/LF |

Журналы: [`build/web-alpha/alpha37`](../build/web-alpha/alpha37/).
Основные файлы: `go-test.log`, `go-vet.log`, `go-test-final-template.log`,
`js-forms-test.log`, `js-preview-test.log`, `js-topics-test.log`.
`source-before.json` и `source-changes.json` отделяют эти исправления от
существовавших до них изменений. Старые overlay-probes в каталоге ревью являются
материалами воспроизведения alpha.36; для alpha.37 используются штатные регрессии.

Go сообщал отказ записи telemetry token за пределами workspace; завершение
тестов и vet успешное. Это предупреждение не скрыто и сохранено в журналах.

Не выполнялись: race detector (нет C-компилятора в PATH), новый полный browser E2E,
Linux/VDS deployment, обращения к реальным LLM/OCR-сервисам и восстановление
пользовательской БД. Успех временных тестов не подменяет эти проверки.

## Сборка и установка

Все три программы собраны для `windows/amd64`, `CGO_ENABLED=0`, с `-trimpath`
в [`build/web-alpha/alpha37/bin`](../build/web-alpha/alpha37/bin/).
Команда `version` для каждой успешно вернула **1.71.0-alpha.37**.
Метаданные сохранены в `staged-binaries.json`, `smoke.log` и `SHA256SUMS.txt`.

| Файл | SHA-256 |
| --- | --- |
| mem.exe | `7D30A0925F1A46CC7D586117B01825A836D9666A856FE6EBA9A6F8644F7FA92C` |
| mem-index.exe | `158607E55CF3F7228B52789D823A3BEF3276BDC9DBA4E3C68F7BC0F5E8E1DD00` |
| mem-bot.exe | `C41C5E0C937EDB26241C05100F240219D74311DA36E45503D3244DDD2218D4C5` |

Сборка завершилась с кодом 0. Помимо telemetry, Go выдал предупреждение об отказе
записи stat cache модуля в `C:\Users\ZMII\go\pkg\mod\cache\download`; оно сохранено
в `build.log` и не помешало созданию и запуску трёх EXE.

После сборки выполнена штатная попытка `deploy/windows.ps1`. Она остановлена
на предварительной проверке назначения:

```text
Deployment target 'C:\Users\ZMII\bin' is not writable.
Access to the path 'C:\Users\ZMII\bin\.mem-deploy-probe-b887aac943c54083a9aca0ac72ffedc8' is denied.
```

Это ограничение записи текущего сеанса. Установленные файлы не заменены;
повторно проверенные `version` и SHA-256 сохранены в `installed-binaries-after.json`.
В `C:\Users\ZMII\bin` остаётся **1.71.0-alpha.35**, подготовленная alpha.37
не считается установленной. Работающий `mem.exe` PID 1508 не останавливался.

Чтобы завершить установку, закройте запущенные программы mem-tool и выполните
из обычного PowerShell с доступом к каталогу назначения:

```powershell
& 'C:\Users\ZMII\Documents\ChatGPT\mem-tool\deploy\windows.ps1'
mem version
mem-index version
mem-bot version
```

Скрипт заново выполнит проверки и staging build, затем проверит хеши и версии
всех трёх установленных программ. После запуска обновлённого приложения
перезагрузите открытые страницы графа.

## Что остаётся вне этого исправления

Пункты функционального развития и паритета CLI/web из первоначального отчёта
не входят в R1–R7 и этим выпуском не объявляются завершёнными. Коммит, push,
обновление целевого сервера и изменение реальных баз не выполнялись.
