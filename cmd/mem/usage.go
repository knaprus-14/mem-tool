package main

import (
	"fmt"
)

func printUsage() {
	printVersion()
	fmt.Println()
	fmt.Println(`Каждая команда работает с локальной базой .mem/ в текущей папке.
В разных папках — разные базы. Настройки эмбеддингов тоже per-project.

Использование:
  mem gui [--port N] [--no-browser] [--state-dir <папка>]
      Открыть локальную веб-библиотеку: создание/открытие баз, документы и записи.
      Поддерживает --dir и --global; существующая база открывается для чтения.
      Каталог веб-баз по умолчанию: <UserConfigDir>/mem-tool/web.

  mem serve --config <server.json>
      Запустить ту же библиотеку в серверном режиме с отдельными пользователями.
      Требует конфиг и секреты из окружения; слушает loopback за HTTPS-прокси.
      Веб-интерфейс: импорт, поиск, ответы, редактирование, карты и фоновая обработка.

  mem init
      Создать новую локальную базу .mem/ в текущей папке
      (опционально — база создаётся автоматически при первом add/index/config)

  mem version
      Показать версию программы

  mem help
      Показать эту справку

  mem add <текст> [-title "Название"] [-tags "тег1,тег2"] [-important]
      Сохранить новую запись в базу. -important — пометить как важную.
      Если .mem/ нет — создаст автоматически.

  mem search <запрос> [-limit N] [-tags "тег1,тег2"] [-tag "категория"] [-from 2026-01-01] [-to 2026-07-01] [-min-score 0.5] [-vector-only]
      Найти записи, похожие по смыслу.
      По умолчанию гибридный поиск + реранжирование (свежесть, теги, важность).
      -vector-only — отключить полнотекстовый буст, только векторы.
      -tag "X" — фильтр по КАТЕГОРИИ записи (точное совпадение с одним тегом:
      rule / decision / bug / best-practice). Отличается от -tags (любой из списка).
      Текст каждой записи выводится полностью (без обрезки).

  mem ask <вопрос> [-limit N] [-tags "тег1,тег2"] [-tag "категория"] [-from 2026-01-01] [-to 2026-07-01] [-min-score 0.5] [-vector-only] [-context-chars N]
      Сформировать локальный ответ только по найденным evidence-фрагментам.
      Каждый тезис требует точного citation ID; статусы идут в stderr,
      проверенный ответ и источники — в stdout. mem search не меняется.

  mem mindmap open [--port N] [--no-browser]
      Открыть отдельную локальную библиотеку и интерактивный редактор классических карт.

  mem mindmap create <название> [--description <текст>] [--json]
  mem mindmap list [--all] [--json]
  mem mindmap show <карта> [--json]
  mem mindmap add-node <карта> <родитель> <название> [флаги]
  mem mindmap edit-node <карта> <узел> [флаги]
  mem mindmap move-node <карта> <узел> --parent <родитель> [флаги]
  mem mindmap delete-node <карта> <узел> [--branch|--promote-children]
  mem mindmap source-add <карта> <узел> (--entry N [--excerpt <текст>] | --file <путь> | --url <адрес> | --knowledge-node <ID>)
  mem mindmap source-remove <карта> <узел> --source <ID>
  mem mindmap source-move <карта> <узел> --source <ID> --position N
  mem mindmap history <карта> [-limit N] [--json]
  mem mindmap undo <карта> [--change N] [--expect N]
  mem mindmap redo <карта> [--expect N]
  mem mindmap snapshot <карта> --reason <текст> [--expect N]
  mem mindmap snapshots <карта> [-limit N] [--json]
  mem mindmap ai-new <запрос> [AI scope flags] [--title <название>] [--json]
  mem mindmap ai-expand <карта> <узел> <запрос> [AI scope flags] [--expect N] [--json]
  mem mindmap ai-fill <карта> <узел> <запрос> [AI scope flags] [--expect N] [--json]
  mem mindmap ai-sources <карта> <узел> <запрос> [AI scope flags] [--expect N] [--json]
  mem mindmap ai-show <preview-id> [--json]
  mem mindmap ai-apply <preview-id> [--select <id,id>] [--expect N] [--json]
  mem mindmap templates [--json]
  mem mindmap template-create <шаблон> <название> [--description <текст>] [--json]
  mem mindmap ask-branch <карта> <узел> <вопрос> [--json]
  mem mindmap compare <левая-карта> <правая-карта> [--json]
  mem mindmap study <карта> <узел> [--output <путь>] [--force] [--json]
  mem mindmap export <карта> --format html|svg|png|json|opml|markdown|mermaid|obsidian --output <путь> [--force]
      AI scope flags: --document <путь|document-id>, --page-from N, --page-to N,
      --query <текст>, повторяемый --entry N, --node-sources, --without-sources,
      --limit N (1..10000). Без evidence-флагов используются все current chunks
      активной базы. Без --limit безопасный автоматический порог — 512; большая
      область отклоняется до вызова модели. Явный лимит выбирает первые N chunks
      после стабильной сортировки и не гарантирует полный корпус.
      --without-sources нельзя совмещать с evidence-флагами, а
      ai-sources всегда требует current versioned evidence.
      Отдельные классические карты мыслей: каждая карта — упорядоченное дерево,
      которое можно создать вручную, изменять и связывать с точными фрагментами
      текущих документов. Названия карт и узлов принимаются вместо внутренних ID.
      Каждая операция создаёт новую ревизию и append-only запись истории; --expect
      защищает от конкурентной правки, undo отменяет последнее доступное изменение.
      AI-команды сначала сохраняют неизменяемый preview. Новая карта или изменения
      публикуются только отдельной командой ai-apply после проверки предложений.
      Расширенная работа задаёт grounded-вопрос по current evidence ветви, создаёт
      ручные карты из шаблонов, сравнивает два дерева и собирает учебный Markdown.
      export всегда снимает полное дерево независимо от свёрнутых ветвей. По
      умолчанию существующий файл не перезаписывается; это разрешает только --force.

  mem map build <фокус> [-limit N] [-tags "тег1,тег2"] [-tag "категория"] [-from 2026-01-01] [-to 2026-07-01] [-min-score 0.5] [-vector-only] [-context-chars N]
      Извлечь типизированные узлы и связи только из versioned document evidence.
      Source-слой различает документ/раздел/тему/определение/утверждение/формулу/
      пример/процедуру; analytics — сравнение/зависимость/причину/следствие/риск/
      ограничение. Пользовательские заметки, вопросы и карточки модель не создаёт.
      Citation, координаты, ревизии, хеши и постоянные ID назначаются самим mem;
      невалидный ответ модели отклоняется целиком без частичной записи.

  mem map coverage [--document <путь|document-id>] [--pages N|N-M] [--tag <тег>] [--json]
      Измерить покрытие текущих versioned chunks объектами карты без вызова модели.
      Показывает полностью/частично/непокрытые страницы, слабый OCR, предупреждения,
      статусы объектов и stale/missing evidence. JSON включает стабильный snapshot.

  mem map eval <manifest.json> [--json]
      Применить строгий воспроизводимый quality gate к текущему coverage-снимку.
      Manifest задаёт минимальные проценты импорта/обработки/покрытия и пределы
      OCR warnings, draft, stale/missing и других счётчиков. Модель не вызывается;
      непройденный gate печатает полный отчёт и завершает команду с кодом 1.

  mem map eval-labeled <manifest.json> [--json]
      Сравнить current claims и связи contradicts с человеческим эталоном.
      Детерминированно считает TP/FP/FN, precision, recall и F1; текст можно
      уточнить алиасами, точным node_id и допустимыми citation_id. Проверка
      read-only, не вызывает модель, а непройденный gate возвращает код 1.

  mem map profile [-iterations N] [--view <имя>] [--json]
      Измерить прогретый host-side путь карты: загрузку SQLite-графа, построение
      pinned evidence snapshot, сборку live-view и сериализацию HTML. Отчёт
      содержит median/p95, размеры БД/HTML и content/state digests. Браузерный
      JavaScript, layout, paint, GPU и FPS этой командой не измеряются.

  mem map diff [--document <путь|document-id>] [--json]
      Сравнить сохранённые evidence-снимки карты с текущими versioned chunks базы.
      Показывает изменённые/исчезнувшие фрагменты и затронутые узлы/связи. Если
      есть снимок 1.57.0+, добавляет полный diff всех chunks документа.

  mem map snapshots [--document <путь|document-id>] [--json]
      Показать неизменяемые снимки прежних ревизий. Снимок создаётся автоматически
      перед атомарной заменой документа и включает полный корпус и снимок карты.

  mem map corpus-diff --document <путь|document-id> [--from <snapshot-id|revision>] [--to <snapshot-id|revision|current>] [--json]
      Полностью сравнить сохранённую ревизию со следующей исторической или текущей:
      added/changed/removed/unchanged chunks, тексты, хеши и физические координаты.

  mem map restore --document <путь|document-id> --revision <snapshot-id|revision> [--confirm <plan-digest>] [--json]
      Без --confirm выполняет только обязательный preview: diff корпуса, размеры
      текущей/целевой карты и state-pinned digest. С точным digest атомарно
      восстанавливает документ и всю карту; перед изменением создаёт точку отката.

  mem map restore --rollback <run-id> [--confirm <plan-digest>] [--json]
      Предпросмотр или подтверждённый откат операции восстановления. Использует
      точный снимок карты, созданный непосредственно перед исходной операцией.

  mem map restore-runs [--json] [-limit N]
      Показать append-only журнал восстановлений и откатов с их recovery point.

  mem map extract <фокус> [--document <путь|document-id>] [--pages N|N-M] [--tag <тег>] [-context-chars N] [-batches N] [-resume <run-id>] [--dry-run]
  mem map extract <фокус> --document <путь|document-id> --all-chunks [--dry-run]
      Пакетно обработать только новые непокрытые chunks выбранной области. Каждый
      строгий batch-result сохраняется как checkpoint; -resume продолжает точный run.
      --dry-run показывает объём без вызова модели и без изменения базы. Лимит
      -batches (1..128, по умолчанию 8) ограничивает стоимость одного запуска.

  mem map extract-runs [--json] [-limit N]
      Показать историю resumable extraction runs и состояния checkpoints.

  mem map extract-run <run-id> [--json]
      Показать один extraction run, пакеты, причины ошибок и число chunks.

  mem map analyze <фокус> [-context-chars N] [-batches N] [-resume <run-id>]
      Сравнить active claim с current evidence из разных документов и предложить
      draft-узлы contradiction/gap. Endpoints, anchors и связи назначает mem;
      -batches (1..32, по умолчанию 1) включает детерминированную пакетную обработку.
      Проверенные batch-результаты сохраняются как checkpoints; повтор команды
      продолжает run. Knowledge graph изменяется только после завершения всех пакетов.

  mem map open [--port N] [--title <текст>] [--no-browser]
      Открыть актуальную карту через локальное HTTP-рабочее пространство только на
      127.0.0.1. По умолчанию выбирается свободный порт и запускается браузер;
      обновление страницы перечитывает граф из активной базы. Раскладка закреплённых
      узлов, масштаб и положение сохраняются в базе автоматически. Текущий PDF
      открывается из карточки evidence сразу на физической странице. Готовый draft
      подтверждается или отклоняется там же без копирования ID и digest; отклонённый
      объект можно вернуть в работу, а последнее допустимое решение — отменить.
      Название и описание редактируются там же с append-only историей; последнюю
      правку можно безопасно отменить. Правка active/rejected возвращает объект в draft.
      Ctrl+C останавливает сервер.
      Для ручного открытия адреса используй --no-browser.

  mem map duplicates [--json] [-threshold 0.92] [-kind claim] [-nodes N] [-limit N]
      Найти близкие по смыслу узлы одного kind. Эмбеддится точный label+body,
      stale/missing/resolved узлы исключаются; результат ничего не объединяет.

  mem map merge-node <manifest.json>
      После ручной проверки пометить generated draft как resolved в пользу active
      канонического узла. Манифест закрепляет node/evidence digests из duplicates.

  mem map merges [--json] [-limit N]
      Показать append-only историю объединений и их текущее current/stale состояние.

  mem map runs [--json] [-limit N] [-status running|completed]
      Показать историю analysis runs, покрытие и состояния batch-checkpoints.

  mem map run <run-id> [--json]
      Показать один analysis run со всеми batch-checkpoints и причинами ошибок.

  mem map prune-runs -older-than <duration> [-keep N] [--dry-run|--yes] [--json]
      Найти завершённые runs старше duration (например 30d или 720h), сохранив
      последние N (по умолчанию 20). Без --yes выводит preview и ничего не удаляет;
      running runs никогда не удаляются.

  mem map status [--json]
      Показать draft/active/rejected/resolved, состояние current/stale/missing для каждого
      evidence anchor и очередь объектов, готовых к подтверждению.

  mem map approve <node|edge> <id> --reviewer <имя> [--comment <текст>] [--evidence-digest <sha256>] [--review-digest <sha256>]
      Подтвердить один draft-объект и записать автора/комментарий в append-only
      журнал. Без --review-digest подтверждается текущая редакция по ID;
      --review-digest закрепляет текст/структуру, --evidence-digest — источники.

  mem map approve-batch <manifest.json> (expected_evidence_digest + expected_review_digest)
      Атомарно подтвердить явный пакет объектов. Каждый объект обязан содержать
      expected_evidence_digest и expected_review_digest из map status --json;
      старый манифест без review pin нужно составить заново после проверки.

  mem map reviews [--json] [-limit N]
      Показать локальный журнал review-решений, новые записи первыми.

  mem map edits [--json] [-limit N]
      Показать append-only историю правок названий и описаний, новые записи первыми.

  mem map export
      Вывести сохранённый граф знаний в JSON (stdout).

  mem map export --format markdown|outline|opml|graphml|gexf|mermaid|obsidian --output <путь> [--title <текст>] [--force]
      Экспортировать весь текущий граф, все типизированные связи и provenance.
      GraphML/GEXF подходят для Gephi/yEd, Mermaid — для документации, OPML и
      outline — для планов, Markdown/Obsidian — для чтения и базы заметок.
      Content digest и source-state digest защищают выгрузку от конкурентного
      изменения; stale/missing источники не скрываются. Существующий файл не
      меняется без --force, активная SQLite-база и её sidecar-файлы защищены.

  mem map export-html <output.html> [--title <текст>] [--force]
      Создать автономную офлайн HTML-карту с force-layout, pan/zoom/drag,
      поиском, фильтрами и provenance-панелью. Существующий файл не меняется
      без явного --force.

  mem recent [-limit N]
      Показать последние записи

  mem show <id> | mem show --from-file <путь>
      Алиасы: get, view. Также mem source <id>.
      Показать одну запись целиком (полный текст + метаданные)
      или все чанки одного документа.

  mem add-file <путь_к_файлу> [-tags "тег1,тег2"] [-important]
      Сохранить UTF-8 текстовый файл в базу (с чанкингом).
      PDF/DjVu/DOCX автоматически перенаправляются в безопасный mem import;
      бинарные данные никогда не эмбеддятся как текст.

  mem import <document.md|document.pdf|document.djvu|document.docx|document.txt> [-title "Название"] [-tags "тег1,тег2"] [-important]
      Импортировать Markdown, PDF, DjVu, DOCX, TXT/CSV/TSV/JSON/XML с provenance.
      Для PDF/DjVu сохраняется манифест всех физических страниц, включая пустые.
      Все chunks и манифест фиксируются атомарно после успешных embeddings.

  mem import-status [--document <путь|document-id>] [--all-pages] [--json]
      Показать манифест текущей ревизии: сохранённые, пустые и ошибочные страницы,
      метод извлечения, OCR-предупреждения и число созданных chunks.

  mem import-runs [--document <путь|document-id>] [--status <статус>] [-limit N] [--json]
      Показать append-only журнал всех попыток импорта, включая ошибки, отмену и
      аварийно прерванные процессы.

  mem import-run <id> [--all-pages] [--json]
      Показать одну попытку: путь, этап, факт обновления базы и причины по
      физическим страницам.

  mem index <путь_к_папке_или_файлу>
      Проиндексировать все файлы в папке (.txt, .md, .pdf, .csv, .json)

  mem sources
      Список всех проиндексированных документов с числом чанков

  mem delete <id> | mem rm <id>
      Удалить запись из базы

  mem edit <id> <новый текст> [-title "Новый заголовок"]
      Изменить текст и/или заголовок записи

  mem retag <id> -tags "новые,теги"
      Изменить теги записи

  mem important <id> | mem imp <id>
      Переключить флаг важности записи

  mem config
      Показать текущую конфигурацию (локальную, из .mem/config.json)

  mem config set-backend <ollama|polza>
      Переключить бэкенд эмбеддингов (только для текущей базы)

  mem config set-chunk-size <символов>
      Размер чанка (100-10000, умолч. 1000)

  mem config set-chunk-overlap <символов>
      Перекрытие чанков (0-1000, умолч. 100)

  mem config set-chunk-strategy <paragraph|sentence|fixed>
      Стратегия разбивки документа на чанки

  mem config set-polza-key <api_key>
      Установить API ключ Polza AI

  mem config set-polza-model <model>
      Установить модель Polza AI

  mem config set-ollama-model <model>
      Установить модель Ollama (по умолч. bge-m3)

  mem config set-answer-model <model>
      Установить отдельную локальную chat/instruct модель для mem ask

  mem config set-answer-base-url <url>
      Установить loopback URL Ollama для mem ask

  mem config set-answer-timeout <секунды>
      Таймаут retrieval и генерации ответа

  mem config set-answer-max-tokens <число>
      Максимум токенов ответа

  mem config set-answer-context-chars <число>
      Бюджет сериализованного evidence-контекста

  mem stats
      Статистика базы

  mem where | mem current
      Показать абсолютные пути активного проекта, .mem и SQLite-файла.

  mem open <каталог_проекта|путь_к_.mem>
      Открыть существующую локальную базу и запустить TUI.
      Пример: mem open "D:\Knowledge\ProjectA"

  mem
      Без аргументов — запуск TUI (полноценный интерфейс на bubbletea).
      Если .mem/ нет — показывается эта справка.

  mem repl
      Классический readline-REPL (с историей и Tab-дополнением)

Глобальные флаги (перед командой):
  --color=always|never|auto   Включить/выключить цвета в выводе
  --no-color                  То же, что --color=never
  По умолчанию: auto (цвета в TTY, выключены в pipe).
  Управляется также env NO_COLOR=1, MEM_NO_COLOR=1.

  --global                    Переключиться на глобальную базу знаний
                              (по умолчанию ~/global-mem/.mem, путь через
                              env MEM_GLOBAL_DIR). Пример: mem --global stats
  --dir <путь>                Переключиться на базу в указанной директории.
                              Пример: mem --dir "C:/Users/ZMII/global-mem" stats
                              или mem --dir /home/user/projects/foo stats
                              Путь к установленному mem.exe на выбор базы не влияет.

Примеры:
  cd ~/projects/myapp && mem add "Сервер: 157.22.196.67"
  cd ~/projects/other && mem add "Другой факт"
  mem where                              # показать точный store.db
  mem open "D:/Knowledge/ProjectA"       # открыть локальную базу в TUI
  mem --dir "D:/Knowledge/ProjectA" recent
  mem search "IP сервера" -tags "инфраструктура"
  mem search "tui" -tag rule                 # только правила про TUI
  mem --global search "deadlock"            # поиск в глобальной базе
  mem --global search "архитектура" -tag best-practice
  mem search "архитектура" -from 2026-07-01 -to 2026-07-26
  mem search "сервер" -min-score 0.5 -vector-only
  mem ask "каков порядок запуска?" -limit 5
  mem map build "архитектура импорта" -limit 10
  mem map coverage --document "D:/Books/manual.pdf" --pages 20-45
  mem map eval .\quality-baseline.json --json
  mem map eval-labeled .\labeled-benchmark.json --json
  mem map profile -iterations 5 --json
  mem map diff --document "D:/Books/manual.pdf"
  mem map snapshots --document "D:/Books/manual.pdf"
  mem map corpus-diff --document "D:/Books/manual.pdf"
  mem map restore --document "D:/Books/manual.pdf" --revision "dhs-..."
  mem map restore-runs
  mem map extract "полный разбор документа" --document "D:/Books/manual.pdf" -batches 16
  mem map analyze "требования к рабочему давлению"
  mem map analyze "требования к рабочему давлению" -batches 8 -resume kar-0123456789abcdef0123456789abcdef
  mem map duplicates --json -kind claim -threshold 0.92
  mem map merge-node merge-node.json
  mem map merges
  mem map runs -status running
  mem map run kar-0123456789abcdef0123456789abcdef --json
  mem map prune-runs -older-than 30d -keep 20
  mem map status
  mem map approve node kn-0123456789abcdef0123456789abcdef --reviewer "Руслан"
  mem map approve-batch review-manifest.json
  mem map reviews --json
  mem map edits
  mem map export
  mem map export --format graphml --output knowledge-map.graphml --title "Карта проекта"
  mem map export --format obsidian --output knowledge-map.obsidian.md
  mem map export-html knowledge-map.html --title "Карта проекта"
  mem show 50                            # одна запись целиком
  mem show --from-file docs/arch.md      # все чанки документа
  mem add-file ./документация.txt
  mem import ./book.md
  mem import-status --document ./book.pdf
  mem import-runs --status failed
  mem import-run 12
  mem index ./проекты/
  mem edit 1 "Обновлённый текст сервера"
  mem retag 5 -tags "сервер,ubuntu,важно"
  mem important 5
  mem delete 12
  mem config set-chunk-size 800
  mem config set-chunk-strategy sentence

Интерактивные режимы:
  mem без аргументов запускает TUI; mem repl запускает readline-REPL.
  TUI поддерживает все команды выше через /команда, включая ask, import,
  import-runs, import-run, index, map и config. Введите / для палитры;
  /help показывает полный список.
  Esc возвращает на главный экран. Выход из TUI: /exit или Ctrl+C два раза.
  В REPL текст без / — сокращение для /search, Up/Down — история,
  Tab — дополнение, Ctrl-D или /exit — выход.

Больше информации: README.md и DOCUMENTATION.md`)
}

// handleConfig — CLI-обёртка над mem.SaveConfig / mem.LoadConfig
// Реализует команды `mem config set-backend`, `set-polza-key` и т.д.
