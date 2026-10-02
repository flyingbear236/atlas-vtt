# Atlas VTT: последовательные промпты реализации персонажей и кубиков

Исходное ТЗ: [characters-dice-spec.md](characters-dice-spec.md). Оно сохранено без изменений. Этот документ содержит предложения по уточнению ТЗ и 22 последовательных промпта; фактическое состояние выполнения фиксируется в журнале C.

## Как пользоваться

Выполнять промпты 01–22 по порядку. Каждый блок `text` — один готовый промпт для новой сессии модели в этом репозитории. Дописывать к нему историю разговора не требуется: источниками контекста служат два этих файла, код, тесты, git diff и журнал ниже. Промпты предполагают принятие предложенных уточнений раздела A; если какое-то решение не подходит, сначала изменить его здесь.

Разбиение рассчитано на ограниченный scope за один запуск Sol 5.6 medium, но гарантировать расход токенов без численного бюджета и фактических результатов тестов невозможно. Этап считается завершённым по критериям приёмки, а не по исчерпанию бюджета. При вынужденной остановке продолжать тот же номер по журналу, не переходить к следующему.

Пути новых модулей в промптах — рекомендуемые. Если предыдущий этап уже выбрал другое разумное имя, использовать существующий модуль и записать соответствие в журнале. Не создавать дубли.

### Контрольные точки интерфейса

Atlas должен собираться и запускаться после каждого этапа, но 01–04 почти не дают нового UI. Осмысленные ручные просмотры: после 05 — несколько владельцев и список доступных токенов; после 10 — итоговое размещение token controls и opacity; после 11 — назначение preset/сохранённого персонажа и roster; после 12 — первый полный character vertical slice (sheet, overrides, avatar, actions); после 14 — полный GM editor definitions/presets и оба TOML-сценария; после 16 — Player View; после 19 — серверные броски и journal без 3D; после 21 — готовые 3D-анимации и локальные настройки; после 22 — итоговая сквозная приёмка. На этих этапах перед переходом дальше полезно запускать Atlas и оценивать UX вручную.

## A. Уточнения ТЗ, принятые за основу этих промптов

### A1. Реальные точки входа

- Backend — Go 1.26, один package `main`, модуль `planar`; frontend — нативные ES modules без сборщика.
- Кампания уже называется `Session`: `main.go`, `Session`, `Token`, `newServer`, `saveLocked`, `ws`, `uploadAsset`, `asset`. Не вводить вторую сущность Campaign.
- `reliable.go`: `Command`, `Properties`, `Receipt`, `command`, `sceneCommand`, `commandPersistence`, `publish`. Business-команды пишут JSON до ACK; движение имеет отдельный coalesced stream.
- `scenes.go`: `snapshotCampaign`, `snapshotSceneAtFloor`, `snapshotSceneForPeer`, `ownedTokenLocators`, `assetVisibleToAtToken`, spatial runtime. Полные токены/ассеты загружаются по региону.
- `scene_content.go`: `activeTokenForMember`, `currentFloorForPeer`, `validateSceneStructure`, `refreshAssetOrphans`; `scene_commands.go`: структурные изменения сцены.
- `web/app.js`: `queueCommand`, обработчик WS, `canMove`, `activatePlayerToken`, `navigationToken`, `renderPanels`, `fillProperties`, `processTokenDrag`, `clearSceneState`.
- `web/reliability.js`: `Outbox`, `Drafts`; `web/scenes.js`: навигация; `web/scene-content.js`, `web/movement-geometry.js`, `web/token-renderer.js`: существующие пути отображения и перемещения.
- `image_pipeline.go`: `prepareReader`, `representationID`, обработка через libvips; `scene_content.go`: общий учёт ссылок на ассеты.
- Проверки: `reliable_test.go`, `scenes_test.go`, `scene_content_test.go`, `scene_catalog_test.go`, `walkable_commands_test.go`, `image_pipeline_test.go`, `browser_test.go`, `browser_scenarios_test.go`.

На момент разведки пользовательские изменения: изменён `AGENTS.md`, удалены `changes.diff`, `snapshot.diff`, `игровая_зона.txt`. Их не восстанавливать и не включать в работу эпика. Существующий документ `docs/walkable-renderbounds-plan.md` не менять.

### A2. Definitions и эффективные значения

1. Namespace единый между ruleset и campaign **внутри вида definition**. Stat `wolf` и preset `wolf` могут сосуществовать. ID неизменяемый, регистрозависимый, Unicode допустим; display name можно менять. Не превращать ID в путь файловой системы. Применять одинаковую проверку ID в JSON, TOML и UI.
2. Campaign definition с тем же ID целиком перекрывает ruleset definition того же вида. Field-level merge definitions не нужен. Для stat нельзя менять тип существующего ruleset definition. Удаление campaign override возвращает ruleset definition; перед удалением проверить валидность результирующего registry и ссылок.
3. `number` принимает конечные числа, в том числе целые; `integer` — целые в диапазоне безопасных JS integers. Не терять точность при JSON round-trip. Тип неизвестного stat выводится из значения; JSON `12`/`12.0` считать одним математически целым значением. `false`, `0`, `""` — присутствующие значения. `null` не является stat value.
4. Автоматическое создание stat + изменение персонажа/пресета — одна атомарная операция. Игрок может создавать неизвестный stat только через разрешённое изменение собственного персонажа; прямой CRUD registry остаётся GM-only. Конфликт любого поля отменяет всю операцию.
5. Effective stats = preset stats + overrides. `default` применяется только при явном добавлении характеристики без введённого значения, не заполняет всех персонажей. Reset удаляет override. Удаление локального stat удаляет override; удаление inherited stat из одного персонажа в этой итерации не вводить — отдельного tombstone в ТЗ нет.
6. Effective actions = `(preset actions ∪ added) − removed`, без дублей, со стабильным порядком: сначала порядок preset, затем добавленные. Удаление имеет приоритет; команды нормализуют пересечения списков. Duplicate создаёт независимую definition с новым ID.
7. Имя instance копируется при создании и дальше самостоятельно. Аватар наследуется от preset при отсутствии override; операция reset удаляет avatar override. Не добавлять неявное наследование имени. Token image остаётся независимым.
8. RollSpec первой итерации принимает только `sides ∈ {4, 6, 8, 10, 12, 20}` и `count = 1..100`, как общий dice contract ТЗ. Не разрешать импортировать action, которую текущий сервер потом не сможет выполнить. Статические ссылки definitions должны разрешаться в registry; `modifier_stat` ссылается на numeric definition, но наличие значения у конкретного instance проверяется при броске. Пример TOML из ТЗ неполон (`bite`, `strength_mod`): тестовые и стартовые данные должны содержать необходимые определения/значения.
9. Редактирование definition с references требует проверки совместимости. Смена типа используемого stat запрещена; новый тип можно задать только неиспользуемому campaign stat. Удаление используемых definitions блокируется со списком/ограниченной выборкой references. Не реализовывать каскадное удаление или массовое преобразование типов.

### A3. Хранение, TOML и транзакции

1. Runtime остаётся в `sessions.json`. TOML — формат definitions и обмена, не второй backend. `schema_version = 1`; явное соответствие TOML `actions` и runtime `actionIds`. Канонический экспорт с сортировкой ключей и корректным quoting.
2. Готового ruleset subsystem нет. Ruleset устанавливается **в конкретную Session** из TOML через `preview → explicit Apply` и сохраняется там как отдельный immutable snapshot. В первой итерации установка разрешена, только пока ruleset registry кампании пуст; replace/uninstall после появления ruleset snapshot не делать. Это оставляет понятный путь для будущей миграции ruleset без неявного преобразования живых characters. Изменение исходного файла после установки не меняет кампанию.
3. Необязательный серверный `-ruleset <file.toml>` — только удобный default для автоматически создаваемых новых кампаний и использует тот же parser/validator/snapshot contract. Без него новая кампания начинает с пустым ruleset и GM может установить его через UI. Никаких менеджеров пакетов, hot reload и сетевых установщиков ruleset.
4. Ruleset TOML и campaign TOML используют общий `schema_version = 1`, но разные операции. Для ruleset обязательна metadata `ruleset.id`, `ruleset.name`, `ruleset.version`; она информационная и не включает resolver зависимостей или semver-логику. Ruleset обязан быть внутренне замкнут: его actions/presets ссылаются на definitions того же ruleset, а не на заранее существующие campaign extensions. Затем отдельно валидируется объединённый registry. Ruleset install заменяет только пустой ruleset registry целиком. Campaign import делает upsert перечисленных campaign definitions; отсутствующие definitions не удаляются и ruleset snapshot не меняется.
5. TOML не передавать внутри существующего WS: его hard limit 16 КиБ меньше реалистичного ruleset. Использовать отдельные GM-only bounded HTTP `preview` и `apply`, максимум 1 МиБ. `apply` получает тот же документ, ожидаемую registry revision, digest preview и client-generated idempotency key; сервер заново вычисляет digest и полностью валидирует итог. Результат операции/receipt сохраняется вместе с registry до ответа, повтор того же key+digest возвращает прежний результат, другой digest с тем же key отклоняется. После успешного save существующий WS сообщает новую revision и заставляет заинтересованных клиентов обновить registry/sheets. Не хранить server-side временный upload между preview и apply.
6. Portable ruleset TOML первой итерации не содержит бинарные assets и не может ссылаться на campaign-local `avatar_asset_id`. Ruleset presets импортируются без avatar; GM назначает avatar позже campaign overlay/редактором. Campaign TOML может ссылаться только на уже существующий asset этой Session, иначе импорт отклоняется. Относительные filesystem paths, URL и base64-изображения в TOML не поддерживаются.
7. Нужны явные лимиты размеров ID, строк, числа stats/actions/roll specs и входного TOML/JSON. Definition document имеет отдельный предел 1 МиБ; общий WS limit не увеличивать. Ошибка валидации не оставляет частично созданных definitions.
8. Patch-команды меняют отдельные поля/stat keys, не присылают целиком effective character. Разные поля двух владельцев не затирают друг друга; одно поле — порядок принятия сервером, как в текущем проекте. Полная замена редактируемой definition использует ожидаемую registry revision с понятной ошибкой stale revision.
9. ACK, rollback и broadcast сохраняют существующие гарантии. После ошибки записи откатываются сущности, связи, receipt, revisions и производные индексы. Не клонировать всю кампанию для каждого изменения одного stat.

### A4. Владение, GC и подписки

1. Runtime/wire поле `ownerIds`, миграция старого JSON `owner` в список. Новый список имеет приоритет, если присутствуют оба поля, включая явно пустой список. Дедупликация, стабильный порядок, проверка Members. Legacy-поле читается на границе загрузки; поддерживать два параллельных источника владения не нужно.
2. `Token` сейчас сравнивается через `t != old`. После добавления slice нужен явный equality с `slices.Equal`, а также copy-on-write списка перед изменением. Проверить другие места сравнения и копирования: поверхностная копия slice/map не обеспечивает rollback.
3. Игрок имеет доступ к character через хотя бы один не-hidden принадлежащий ему token на опубликованной сцене этой кампании; viewport и текущий этаж не отзывают права на sheet. Persistent character без доступных токенов доступен только GM. Права пересчитываются при ownership/link/hidden/publication/role/delete.
4. Для GC нужен неперсистентный индекс `characterID → ссылки (sceneID, tokenID)`, восстановленный при загрузке. Обновлять при структурных изменениях ссылок, а не при каждом перемещении. Проверка последней ссылки не сканирует всю кампанию. Reference-проверки редкого ручного удаления definitions могут сканировать registry/instances.
5. GC выполняется после завершения unlink/relink/delete token/delete scene и других существующих операций, удаляющих токены. Замена связи удаляет старый transient instance только после установки новой. `persistent: true → false` без ссылок тоже запускает GC. Asset references обновляются вслед за итоговым состоянием.
6. Полные character sheets не добавлять во все region snapshots. На peer достаточно одной подписки на открытый character; сервер отправляет разрешённый snapshot и изменения независимо от viewport. GM editor получает definitions/roster отдельным запросом. На reconnect подписка восстанавливается; на revoke приходит удаление/закрытие и очищается локальный state.
7. Список токенов — отдельные лёгкие locators, без картинок и character sheets. Переиспользовать `ownedTokens`/`navigationToken`, расширить для GM preview. Каталог нельзя получать через загрузку всех полных токенов/ассетов. Изменения вне viewport тоже должны обновлять locators; уход из region не равен удалению из списка.
8. Новые campaign/character сообщения обрабатывать до существующего spatial `sceneId/delivery` фильтра. Не заставлять character/roll events выглядеть как token events. Сохранить непрерывность существующего delivery stream; отдельный snapshot/revision нового состояния допустим в том же WS.

### A5. UI, Player View и аватары

1. Разделить фактическое `isGM` для авторизации и `isEditorView`/player interaction для UI. Не подменять `Member.role` или `Member.gm`.
2. На сервере сейчас GM запрещён `activeToken`. Добавить transient per-peer preview context в существующий WS, чтобы snapshot/render visibility и active floor могли использовать player projection с GM-доступом к выбору любого токена. Это не новая роль и не impersonation конкретного игрока.
3. В preview использовать те же movement predicates, walkable и renderBounds, что у player. Для move/final preview-контекст ограничивает GM-перемещение тем же серверным валидатором; вне preview обычные GM-возможности сохраняются. Preview — реальное управление сценой: движения и stat edits сохраняются.
4. GM preview selector содержит токены всей текущей сцены, включая hidden для диагностики; canvas всё равно соблюдает player visibility. Выбор hidden token не делает его видимым. При переключении отменять/завершать текущую операцию согласованно, снимать build selection/tools и обновлять projection; старые очереди не должны отправляться с неоднозначной семантикой режима.
5. Перенести существующие token controls в левую панель без второго state/обработчиков. Отдельного token opacity сейчас нет: добавить `opacity` 0…1, legacy default 1, применение в renderer. Отдельных per-token movement settings не изобретать; сохранять реально существующие свойства/ограничения.
6. Avatar: отдельная роль/representation в существующем asset pipeline, PNG/JPEG, предлагаемые лимиты 10 MiB / 25 млн pixels / сторона 8192, результат максимум 512×512. Worker/лимитер/хэширование переиспользуются; исходник avatar-only не сохраняется. На загрузке и перед назначением повторно проверить character permissions.
7. Включить avatars instances и campaign presets в `refreshAssetOrphans` и разрешение чтения, включая persistent roster без сцены. Portable ruleset snapshot по A3 avatar assets не содержит. Не использовать обход GM-only upload для всех изображений. Доступ к исходникам/чужим scene assets не расширять. Если campaign TOML ссылается на отсутствующий avatar asset, отклонять импорт с объяснением; упаковка бинарных ассетов в TOML не требуется.

### A6. Dice protocol и ограниченные ресурсы

1. Два взаимоисключающих режима запроса: manual (`count`, `sides`, optional character) и action (`characterId`, `actionId`, `rollSpecId`). В action режиме count/sides/modifier берутся только с сервера; конфликтующие присланные значения отклоняются. Проверить, что action входит в effective actions. Один запрос запускает один RollSpec.
2. `modifier = modifier_fixed + modifier_stat value`, отсутствующий optional компонент = 0. Отсутствующее обязательное stat value, нечисловое значение и не конечный/непредставимый итог — validation error. Автор/имена/время/результаты всегда серверные.
3. Предлагаемые лимиты: 100 dice на запрос, 30 на анимацию, 8 ожидающих анимаций, последние 500 RollEvent суммарно на кампанию. Лимиты вынести в именованные константы. Большой бросок полностью попадает в журнал; 3D можно целиком пропустить.
4. Броски публичные в рамках сцены, где выполнены, и доставляются её текущим подписчикам; не фильтруются viewport. Связанный персонаж должен иметь подходящий token в этой сцене. GM должен понимать, что имя связанного скрытого персонажа при публичном броске тоже публикуется; отдельные secret rolls в scope не добавляются.
5. История ограничена 500 событиями на Session, сохраняется вместе с прочим runtime. Reconnect получает только доступные события текущей сцены; удалённые/недоступные сцены не раскрываются. История — snapshot имён и результатов, не ссылка для GC characters/definitions. Старые записи переживают rename/delete.
6. Roll идёт через существующий reliable business stream. Event и receipt сохраняются атомарно до broadcast/ACK. Receipt последней команды stream при необходимости содержит исходный RollEvent: потеря ACK/reconnect/restart возвращает тот же результат, даже если событие уже вытеснено из журнала. Никакого повторного RNG для уже принятой команды. Гарантия ограничена существующим lifecycle receipts, не обещает вечную идемпотентность после их pruning.
7. Клиент дедуплицирует по event ID; журнал обновляется сразу. History/replayed receipt не анимируются заново. Переполненная animation queue отбрасывает только анимацию. Очереди, dedup state и DOM имеют верхнюю границу.
8. Анимация lazy-loaded и изолирована адаптером. `all/self/off` сохраняется локально; off не создаёт renderer и не запускает звук. Смена режима очищает неподходящие ожидающие задачи; смена сцены/кампании останавливает текущую анимацию и освобождает её ресурсы. Timeout/отказ WebGL/ошибка библиотеки не останавливают журнал и Outbox.
9. Точный пакет — `@3d-dice/dice-box-threejs`. Upstream документирует predetermined notation `Box.roll("6d6@4,4,4,4,4,4")`: https://github.com/3d-dice/dice-box-threejs . Зафиксировать версию, проверить её фактический lifecycle API; не предполагать наличие `dispose()`. Vendor assets должны входить в Go embed и работать локально без CDN. Не переводить весь frontend на Vite ради одной зависимости; допустим отдельный воспроизводимый скрипт сборки vendor bundle с lockfile и лицензиями.

### A7. Где оптимизация нужна, а где лишняя

- **High:** повторный RNG при lost ACK меняет исход действия даже у двух клиентов. Исправление — result-bearing receipt + атомарный bounded journal. Цена: немного дополнительного bounded storage и существующая запись JSON на бросок.
- **High:** раздача всей кампании в region snapshot ломает spatial contract и может раскрыть чужие sheets; стоимость растёт с токенами × подписчиками × refresh. Исправление — отдельные locators и одна sheet-подписка. Цена: явный subscribe/revoke lifecycle.
- **Medium:** GC с обходом всех сцен на каждое unlink при массовом удалении становится квадратичным. Исправление — индекс ссылок и пакетное завершение GC. Цена: восстановление индекса при загрузке/rollback.
- **High:** новый GPU renderer на каждый roll или неограниченная очередь на длинной сессии удерживает ресурсы. Исправление — ленивый единственный активный renderer, bounded queue, проверяемый cleanup. Цена: адаптер и lifecycle-тесты.
- **Low / без отдельной оптимизации:** effective state выбранного sheet, поиск references при редком удалении definition и crypto/rand для ≤100 dice. Достаточно простого кода; caches, криптографические benchmarks и второй persistence backend не нужны.
- Существующий общий mutex и полная JSON-запись — известное ограничение. Этот эпик не переписывает storage. Не добавлять к нему новые O(вся кампания) копии и сохранение истории при каждом frame.

## B. Общий контракт исполнения каждого промпта

Прочитать `AGENTS.md`, разделы A и B этого файла, соответствующие разделы исходного ТЗ, журнал C. Затем проверить `git status`, текущий diff и конкретные зависимости этапа поиском. При продолжении не переписывать завершённую работу.

Реализовать только указанный этап. Новые подсистемы выносить в небольшие Go/ES-модули, `web/app.js` оставлять местом интеграции; не устраивать рефакторинг соседнего кода. Не подключать субагентов без отдельной инструкции пользователя. Не выполнять commit автоматически.

Перед завершением: форматирование изменённых Go-файлов, компиляция, релевантные unit/integration/browser checks и просмотр diff. Для frontend проверять реальное поведение, а не только поиск строк. Новые browser tests по возможности выделять отдельными именованными Go-тестами, используя существующий CDP harness; не заставлять каждый этап запускать все memory/stress scenarios. `ATLAS_CHROME` включает реальные Chrome/Edge проверки; без браузера явно отмечать skipped. Для image checks нужен libvips.

Обычные команды: `gofmt -w <изменённые Go-файлы>`, `go test ./... -run '<релевантные тесты>'`, `go build ./...`. Test regex подбирать по реально существующим именам. На финальном этапе полный обычный suite; opt-in профилирование не включать без причины. Отсутствующую инфраструктуру проверки описывать честно, не считать skipped тест пройденным.

После этапа обновить только его запись в журнале C: статус, конкретные файлы/символы, wire/storage решения, запущенные проверки и результаты, открытые блокеры/оставшаяся работа. Достаточно 5–10 строк; не пересказывать весь diff и не создавать отдельные отчёты. При изменении межэтапного контракта обновить соответствующее уточнение A и зависимый будущий промпт. Не менять исходное ТЗ молча.

В ответе пользователю кратко назвать изменения, проверки, ограничения и следующий номер. Не отмечать этап завершённым при заглушках вместо его обязательных результатов.

## C. Журнал передачи контекста

Исходное состояние: выполнена разведка; реализации эпика нет. Все этапы 01–22 ожидают выполнения. После каждого этапа добавлять здесь запись `### Этап NN — статус` по контракту B. Не удалять записи предыдущих этапов.

### Этап 01 — завершён

- Добавлены `characters.go` и `characters_test.go`: definitions, типизированные stat values, instances и раздельные ruleset/campaign registries.
- Реализованы чистые merge/lookup/effective и copy-on-write override/reset helpers; campaign definitions перекрывают ruleset целиком, конфликт типа stat отклоняется.
- Введены общие доменные лимиты, Unicode ID validation, safe-integer/finite-number validation и атомарное выведение неизвестных campaign stats.
- Effective state не материализует defaults, сохраняет отсутствие отдельно от `0`/`false`/`""`, стабильно объединяет и удаляет actions без дублей.
- Wire/storage/Session/Token не менялись; persistence, TOML, WS, UI, rules engine и cache остаются за пределами этапа.
- Проверки: `gofmt`, релевантные unit tests, `go build ./...`, полный `go test ./...` — успешно; Go вывел нефатальное предупреждение о недоступном telemetry token.
- Открытых блокеров этапа нет. Следующий этап: 02 (TOML codec и validation импорта).

### Этап 02 — завершён

- Добавлены `definitions_toml.go` и `definitions_toml_test.go`; подключён `github.com/pelletier/go-toml/v2` v2.4.3 по официальному Go-модулю.
- Реализованы ограниченный 16 KiB strict parse для `schema_version = 1` и канонический export с библиотечным quoting Unicode/точек и сортировкой map keys.
- TOML `presets.*.actions` явно преобразуется в runtime `ActionIDs`; поддержаны все четыре stat type, defaults, actions/RollSpec и presets.
- `PreviewCampaignDefinitionsImport` чисто upsert-ит campaign definitions, сохраняет пропущенные записи, возвращает сортированный UI diff и валидирует итоговый ruleset overlay.
- Неизвестные поля, дубли, неверные schema/type/value/reference, небезопасная смена типа используемого stat и oversized input отклоняются без частичного результата.
- Исправлен используемый codec-ом `clonePresetDefinition` этапа 01: preset stats теперь действительно глубоко копируются, а не теряются.
- Storage, Session, endpoints, WS и UI не менялись. `gofmt`, `go mod tidy`, релевантные и полные `go test ./...`, `go build ./...`, `go vet ./...` — успешно.
- Открытых блокеров этапа нет. Следующий этап: 03 (persistence персонажей, ruleset snapshot и миграция связи).

### Этап 03 — завершён

- `Session` сохраняет отдельные `rulesetSnapshot` с metadata, `campaignDefinitions` и `characterInstances`; `Token` получил необязательный `characterInstanceId`.
- Legacy JSON без новых полей мигрирует в пустые registry/maps без создания characters; typed `StatValue` хранится с type tag и сохраняет `number(12)` отдельно от `integer(12)`.
- Добавлены обязательные ruleset metadata `id/name/version`, внутренне замкнутая validation и bounded `-ruleset <file.toml>` как immutable default только для новых Session.
- Existing Session использует сохранённый snapshot после restart и не перечитывает изменившийся ruleset-файл; campaign overlay и instance references валидируются перед любой записью.
- Неперсистентный индекс `character → (scene, token)` восстанавливается при загрузке и инкрементально обновляется при create/delete token и delete scene; generic create не назначает character.
- Duplicate JSON keys/IDs, неверные stat types и ссылки отклоняются до открытия `sessions.json.tmp`; повреждённый файл не перезаписывается. Политика scene model version не менялась.
- Лимит definition document приведён к 1 МиБ и RollSpec sides к `{4,6,8,10,12,20}` по актуальному A2–A3; storage/WS CRUD и UI не добавлялись.
- `gofmt`, релевантные storage tests, полный `go test ./...`, `go build ./...`, `go vet ./...` — успешно. Следующий этап: 04 (несколько владельцев).

### Этап 04 — завершён

- `Token.owner` заменён серверным runtime/wire полем `ownerIds`; custom JSON boundary читает legacy `owner`, причём присутствующий `ownerIds`, включая `[]`, имеет приоритет.
- При загрузке ownership стабильно дедуплицируется, проверяется по `Session.Members` и при необходимости атомарно пересохраняется; неизвестный владелец останавливает загрузку без перезаписи corrupted state.
- Добавлены общие helpers владения, управления token и доступа к character через visible token опубликованной сцены; GM role и существующие hidden/unpublished ограничения сохранены.
- Movement, active token, floor selection, entry point, locators, snapshots и fixtures переведены на membership в ownerIds; владельцев по-прежнему меняет только GM.
- Сравнение Token стало явным через `slices.Equal`, а mutation/rollback используют copy-on-write owner slices; failed save восстанавливает исходных владельцев.
- Серверные тесты покрывают Alice/Bob/Charlie/GM, пустой список, stable dedup, неизвестного member, legacy restart/precedence, rollback, character access и общий token на другом этаже.
- UI и browser multi-select не менялись и остаются этапу 05. `gofmt`, полный `go test ./...`, `go build ./...`, `go vet ./...` — успешно. Следующий этап: 05.

### Этап 05 — завершён

- Клиентские проверки владения переведены на membership в `ownerIds`; существующий control стал multi-select участников без переноса из правой панели.
- `Drafts`, сравнение исходных значений и queued-команд сравнивают массивы по содержимому, поэтому ACK старой ownership-команды не очищает более новый ввод.
- `ownedTokens` использует отдельный `TokenLocator` без artwork/character данных; snapshot по-прежнему содержит полный доступный каталог независимо от viewport.
- Добавлены отдельные `tokenLocatorUpsert/Move/Delete` вне spatial `delivery`: выдача, revoke, hidden, удаление и движение вне региона обновляют список узкими событиями, а spatial eviction его не удаляет.
- Revoke отменяет drag/selection клиента; выбор удалённого locator переиспользует существующий `activeToken`/focus запрос и только затем загружает региональные token/assets.
- Интеграционный тест покрывает двух владельцев, далёкий token, движение без artwork/delivery, focus, revoke, hidden/unhidden и delete; Chrome-сценарий покрывает multi-select и array draft ACK.
- `gofmt`, релевантные тесты, реальный `TestBrowser` в headless Chrome, полный `go test ./...`, `go build ./...`, `go vet ./...` — успешно. Следующий этап: 06.

### Этап 06 — завершён

- Добавлены `definitions_commands.go` и серверные GM-only reliable WS-команды create/update/duplicate/delete campaign definitions; они используют отдельную registry revision и работают без открытой сцены.
- Preset-команда атомарно создаёт неизвестные stat definitions; overlay/reset, независимое duplicate, опасная смена типа и удаление используемых definitions проверяются на полном effective registry с ограниченным списком references.
- Добавлены GM read API и раздельные bounded HTTP preview/apply для ruleset install и campaign import: предел 1 МиБ, digest, expected revision, повторная validation и отсутствие server-side preview state.
- Ruleset устанавливается только в пустой immutable snapshot с metadata; campaign import остаётся upsert-операцией и не меняет ruleset. WS-предел 16 КиБ и scene snapshots не расширялись полным registry.
- Persisted operation receipts обеспечивают идемпотентный replay key+digest после restart; key с другим digest/kind, replace ruleset и stale apply отклоняются. Изменения и notifications публикуются только после успешного атомарного save.
- `Session` хранит `registryRevision` и bounded `definitionOperations`; загрузка и save нормализуют legacy state, а rollback восстанавливает definitions, revision, receipts/results и dirty-state.
- Тесты покрывают player denial, campaign без scenes, lost ACK, restart, >16 КиБ HTTP, install/import replay, overlay reset, references, stale preview, 1 МиБ limit и отказ записи. `gofmt`, целевые и полный `go test ./...` — успешно. Следующий этап: 07.

### Этап 07 — завершён

- Добавлен `character_commands.go`: reliable-команды явного создания instance из preset/пустого, link/relink/unlink, persistent, stat set/reset, avatar set/reset и action add/remove; server-generated character ID сохраняется в receipt для lost-ACK replay.
- Linkage, preset/persistent/actions остаются GM-only; stat/avatar разрешены GM либо владельцу через campaign-wide `memberCanAccessCharacter`, независимо от current scene, floor и viewport. Выбор/чтение token instance не создают.
- Unknown stat definition и instance override сохраняются одной транзакцией с registry revision; команды меняют отдельные поля, а reset удаляет только override. После успешного save разрешённые co-owners получают лёгкий `characterChanged` без sheet payload.
- Reference index обновляется только при структурных link changes. GC transient instances подключён к unlink/relink, `persistent=false`, обычному token delete и scene delete; cross-scene ссылка и persistent flag защищают instance.
- Rollback восстанавливает instances, campaign definitions, token linkage, scene/registry revisions, receipts, asset state и перестраивает reference index; failed command можно безопасно повторить.
- `refreshAssetOrphans` теперь учитывает campaign preset и instance avatar references; definition/character mutations обновляют orphan state только после итоговой модели и откатывают его при ошибке записи.
- `character_commands_test.go` покрывает сценарии A–F, двух владельцев/Charlie, независимые wolves, preset inheritance, unknown stat, avatar/actions/reset, cross-scene GC, relink, persistent без ссылок, scene delete и disk failure. `gofmt`, связанные и полные тесты, `go build ./...`, `go vet ./...`, `git diff --check` — успешно. Следующий этап: 08.

## D. Промпты

### 01. Доменная модель и чистая логика

```text
Выполни этап 01 эпика Atlas VTT. Прочитай AGENTS.md и docs/characters-dice-prompts.md: разделы A, B, C; следуй им. Исходник: docs/characters-dice-spec.md §§2–11, 24, 26, 38–41, 53. Остальные этапы не реализуй.

Точки входа: main.go (Session/Token), go.mod; новые компактные characters.go/characters_test.go или эквивалент.

Создай StatDefinition, типизированные stat values, ActionDefinition/RollSpec, CharacterPresetDefinition, CharacterInstance и раздельные ruleset/campaign registries. Реализуй чистые merge/lookup/validation/effective helpers по A2. Не добавляй storage, WS и UI. Effective merge не мутирует preset и сохраняет отсутствие значения отдельно от 0/false/пустой строки. Введи централизованные доменные лимиты. Не реализуй expression DSL, rules engine или cache.

Приёмка: тесты merge precedence, разных namespace, Unicode ID, numeric boundaries, type conflict, defaults без автоматического заполнения, overrides/reset, action order/dedup/removal. Два независимо созданных instance не разделяют mutable maps/slices. Изменение preset влияет только на непереопределённые значения. Проверки и журнал — по B.
```

### 02. TOML codec и validation импорта

```text
Выполни этап 02. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C, используй завершённый этап 01. Исходное ТЗ: docs/characters-dice-spec.md §§38–41. Только этот этап.

Точки входа: доменный модуль этапа 01, go.mod/go.sum; рекомендуемый новый definitions_toml.go.

Добавь maintained Go TOML dependency, проверив её официальную документацию. Измени отдельный предел definition document на 1 МиБ; существующий WS limit 16 КиБ не трогай. Приведи domain validation RollSpec к поддерживаемым в ТЗ граням `{4,6,8,10,12,20}` до приёма TOML. Реализуй ограниченный по размеру parse, schema_version=1, канонический детерминированный export, проверку duplicates/неизвестных полей/ссылок. Различай ruleset document с обязательной metadata `ruleset.id/name/version` и campaign definitions document; portable ruleset должен быть внутренне замкнут и не принимает avatar asset references по A3. Явно отображай TOML actions в runtime actionIds. Unicode, кавычки, точки и управляющие символы обрабатываются библиотекой и общей ID validation, не ручной конкатенацией.

Добавь чистую функцию preview campaign import: upsert без удаления пропущенных definitions, проверка всего итогового registry и diff для UI. Не записывай файлы кампаний и не делай endpoint/UI. Fixtures должны быть самосогласованными, включая bite и strength_mod.

Приёмка: round-trip обоих видов документов, всех definition видов и stat типов, одинаковый export независимо от map insertion order, quoted keys, invalid/missing ruleset metadata, ruleset avatar rejection, invalid schema/type/reference, oversize input и all-or-nothing preview. Проверки и журнал — по B.
```

### 03. Persistence персонажей, ruleset snapshot и миграция связи

```text
Выполни этап 03. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; опирайся на 01–02. Исходное ТЗ: §§9, 12, 16, 38, 47 документа docs/characters-dice-spec.md.

Точки входа: main.go (Session, Token, newServer, create, saveLocked, main), scene_content.go (validateSceneStructure), новые доменные модули.

Добавь в существующий JSON storage раздельный ruleset snapshot с metadata, campaign definitions и instances; Token получает необязательный characterInstanceId. Старые сохранения без этих полей загружаются с пустыми коллекциями и без создания characters. Добавь необязательный -ruleset default для новых Session по A3 через тот же parser/validator; existing Session не перепривязываются к файлу при restart.

Создай неперсистентный campaign index ссылок character→tokens и восстановление при загрузке. Проверь ссылки, типы и duplicate IDs до изменения сохранения; corrupted state не перезаписывать. Не меняй существующую политику отклонения старых scene model versions. Не добавляй команды CRUD и UI; ownership мигрирует на следующем этапе.

Приёмка: legacy/current JSON round-trip, ruleset-file изменения не меняют существующую кампанию, восстановление cross-scene references, неверная ссылка/тип приводит к понятной ошибке без перезаписи. Проверки и журнал — по B.
```

### 04. Несколько владельцев: сервер и миграция

```text
Выполни этап 04. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; предпосылка 03. Исходное ТЗ: §§17–18, 44, 47.

Точки входа: main.go Token/newServer; reliable.go Properties/command/publish; scenes.go ownedTokenLocators/sceneEntryPoint; scene_content.go activeTokenForMember/currentFloorForPeer/validateSceneStructure. Найди ВСЕ реальные использования Owner и сравнения Token.

Переведи сервер на ownerIds по A4, мигрируй legacy owner. Введи общие helpers ownership/доступа без изменения GM role. Замени несовместимые сравнения Token и обеспечь copy-on-write owner slices в rollback. Обнови snapshots, validation, movement, active token, floor selection и существующие тестовые fixtures. Владельцы меняются только GM; hidden/unpublished restrictions сохраняются.

Приёмка: Alice и Bob могут управлять одним token, Charlie не может; GM может. Проверить [] owners, duplicates, неизвестного member, старый save/restart, failed-save rollback, токен на другом этаже. Браузерный multi-select будет в 05: здесь только серверный контракт. Проверки и журнал — по B.
```

### 05. Владельцы на клиенте и актуальные token locators

```text
Выполни этап 05. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; предпосылка 04. Исходное ТЗ: §§17–18, 20, 22, 45.

Точки входа: web/app.js canMove/navigationToken/activatePlayerToken/renderTokenList/fillProperties/WS handler, web/reliability.js Drafts, web/index.html; scenes.go ownedTokenLocators, reliable.go publish.

Замени одиночный selector owner на multi-select существующих участников и все owner comparisons на membership. Черновики массивов сравнивай по содержимому, не по ссылке; старый ACK не должен очищать новый ввод. Пока оставь controls на прежнем месте — перенос будет в 10.

Раздели locator updates и spatial token events. Доступные токены вне viewport должны появляться/исчезать/обновляться в selector при ownership, hidden, удалении и перемещении; spatial eviction не означает потерю владения. Locators не включают изображения/character sheets. Не рассылай весь locator list на каждый move, используй узкие изменения.

Приёмка: два владельца, revoke во время выбора, выданный вдали token появляется без reload, его выбор вызывает существующий activeToken/focus путь; за пределами региона не загружается artwork. Проверки и журнал — по B.
```

### 06. Серверный CRUD registry

```text
Выполни этап 06. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 01–05. Исходное ТЗ: §§3–8, 38–41, 44–45.

Точки входа: reliable.go Command/sceneCommand/Receipt, main.go ws/saveLocked, доменные/TOML модули. Новый definitions_commands.go допустим.

Добавь GM-only reliable WS-команды создания/редактирования/duplicate/удаления небольших отдельных definitions. Campaign-команды работают и без открытой сцены. Для ruleset install и campaign TOML import реализуй bounded authenticated HTTP preview/apply по A3: максимум 1 МиБ, expected revision, preview digest, persisted idempotency key/result и повторная полная validation на apply. Ruleset install допустим только для пустого ruleset registry, сохраняет immutable snapshot с metadata и не смешивается с campaign import. Не увеличивай WS limit и не храни временный preview на сервере. Ruleset overlay, запрет опасной смены типа, auto-create неизвестных stat при сохранении preset и reference checks — по A2–A3.

Добавь GM read API registry и отдельные HTTP preview ruleset install / campaign import, достаточные будущему editor; полный registry не помещай в каждый scene snapshot. Авторизованные изменения публикуются после успешного save. Error и rollback не оставляют частичных definitions/receipts/import-operation results.

Приёмка: player запрет, campaign без scenes, документ больше 16 КиБ проходит HTTP без расширения WS, установка ruleset в пустую кампанию, повтор той же apply операции идемпотентен, same key/different digest и replace отклонены, duplicate независим, удаление referenced definition отклонено, overlay reset валиден, stale preview/import и save failure атомарны, lost ACK не дублирует отдельное создание. UI пока не делай. Проверки и журнал — по B.
```

### 07. Команды instances, связи, permissions и GC

```text
Выполни этап 07. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 03–06. Исходное ТЗ: §§9–18, 24, 26, 44–45.

Точки входа: character domain/index этапа 03, reliable.go token delete/sceneDelete/command, scene_commands.go структурные операции, scene_content.go refreshAssetOrphans. Новый character_commands.go допустим.

Реализуй GM-команды: создать из preset/пустой и атомарно связать с token, привязать существующий, unlink/relink, persistent, назначение/удаление actions. Разреши владельцу только stat patches/reset и подготовь разрешённое изменение avatar reference. Используй character access по A4, не только current scene или loaded region. Не создавай instance при чтении/выборе token.

Unknown stat definition и value сохраняются одной транзакцией. Сохраняй field-level patch semantics. Подключи reference index и GC после удаления последних ссылок во всех существующих путях; на save failure восстанови данные и index. Не сканируй кампанию на каждый move.

Приёмка: A–F исходного ТЗ серверными тестами; cross-scene ссылка защищает transient; relink, persistent=false без ссылок, удаление scene и отказ диска; Charlie не меняет character, owner не меняет preset/persistent/actions/ownership. Проверки и журнал — по B.
```

### 08. Character subscriptions и reconnect

```text
Выполни этап 08. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; предпосылки 06–07. Исходное ТЗ: §§18, 22–26, 45–46.

Точки входа: main.go peer/ws/send, scenes.go snapshots/publishCampaign, character commands; web/app.js WS dispatch и clearSceneState; новый web/characters.js для состояния/подписки.

Реализуй один открытый character watch на peer: разрешённый effective snapshot, instance/preset изменения, unsubscribe, reconnect. GM roster/registry read остаётся отдельным; игрок не получает все sheets кампании. Предоставляй только нужные definitions/action metadata. При revocation очищай sheet, pending requests и локальные данные; повторно проверяй права при каждой mutation.

Character events должны работать между владельцами в разных сценах и вне viewport, не ломая scene delivery. На клиенте отдели их от token event handler; задай revisions и защиту от позднего ответа старой подписки. Пока не рисуй sheet.

Приёмка: два владельца в разных сценах получают один stat update; preset меняет effective state; revoke/unpublish/delete закрывают watch; reconnect восстанавливает актуальные данные; repeated subscriptions не накапливаются; snapshot персонажа не раскрывается постороннему. Проверки и журнал — по B.
```

### 09. Аватары через существующий image pipeline

```text
Выполни этап 09. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 07–08. Исходное ТЗ: §25 и связанные §§18, 40, 46.

Точки входа: main.go uploadAsset/asset/auth, image_pipeline.go prepareReader/representationID, scene_content.go refreshAssetOrphans, scenes.go assetVisibleToAtToken, character permissions; image_pipeline_test.go.

Добавь avatar representation и авторизованный upload/assignment для instance и GM preset. Переиспользуй streaming input, libvips worker, concurrency limiter, asset identity и storage. Применяй лимиты A5, max side output 512, не сохраняй оригинал avatar-only. Обнови startup validation новых asset kinds. Доступ и назначение повторно проверяются после обработки; удалённый character/revoked owner не получает запись.

Учти inherited/persistent avatars в существующих references и read authorization, даже без открытой сцены у GM. Отмена/ошибка не оставляет опубликованного broken asset; не вводи cache полного исходника. Не меняй token image при avatar change. UI upload подключится в 12/14.

Приёмка: PNG/JPEG и большие входы, превышение лимита, отмена, permission revoke во время worker, persistent character без token, общий avatar двух characters, failed save и очистка временных файлов. Проверки и журнал — по B.
```

### 10. Перенос token controls и opacity

```text
Выполни этап 10. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 05 и 09. Исходное ТЗ: §§21–23, 47.

Точки входа: web/index.html/style.css/app.js properties handlers/fillProperties/selection, web/token-renderer.js; main.go Token/newServer, reliable.go Properties, scene_content.go validation.

Перенеси существующие свойства token и multi-owner controls в левую вкладку «Токены», сохранив один Drafts state и обработчики. Не дублируй форму и не переписывай общий layout. Сохрани ограничения редактирования и существующие floor/size/image/hidden настройки.

Добавь отсутствующее token opacity 0…1: совместимая загрузка старых tokens со значением 1, корректное явное 0, field patch, snapshot и отрисовка вместе с floor/hidden alpha. Не включай opacity в artwork cache key, если достаточно compositing alpha. Новых per-token movement mechanics не вводи.

Приёмка: old token выглядит как раньше; opacity 0/0.5/1 сохраняется после reconnect; ownership сохраняется; несохранённая форма не сбрасывается от move или selection update. Правая панель готова для character UI, build inspector не сломан. Проверки и журнал — по B.
```

### 11. Назначение персонажа и постоянный roster

```text
Выполни этап 11. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 07–10. Исходное ТЗ: §§12–16, 22–24 и сценарии A/E.

Точки входа: web/characters.js этапа 08, web/app.js selection/queueCommand, web/index.html/style.css, read/command API characters.

Добавь в правую панель GM действия: назначить из preset (новый instance), назначить сохранённого (существующий), создать пустого, unlink/relink, «Сохранить в кампании». Roster получает summary persistent instances, не все character sheets/assets. Если набор большой, ограничь выдачу страницами; выбранный sheet загружается отдельно.

Token без character показывает empty state; выбор не создаёт instance. Обычный player не видит GM mutation controls. Обновления linkage и GC закрывают устаревший sheet; при замене token selection поздний ответ не открывает предыдущего персонажа. Пустой персонаж создаётся только явным действием начала редактирования.

Приёмка через UI: пять tokens из wolf → пять IDs; один persistent Lancelot назначается на двух scenes с одним ID; удаление последнего token не удаляет его; player не перепривязывает character. Редактор stats и definitions не реализуй здесь. Проверки и журнал — по B.
```

### 12. Character sheet, черновики, avatar и actions

```text
Выполни этап 12. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 08–11. Исходное ТЗ: §§24–26, 45 и сценарии B/C/F.

Точки входа: web/characters.js, web/app.js integration, web/reliability.js Drafts, avatar API этапа 09, character patch commands.

Реализуй типизированное отображение effective stats, inherited/override indication, редактирование, reset и добавление stat. Показывай уведомление о новом campaign definition. Не отправляй полный effective map. Черновики привязаны к character ID и field; входящий update/ACK старого ввода не затирает более новый локальный ввод. После отклонения показывай ошибку, не подменяй server state.

Добавь avatar upload/reset и effective actions list. GM добавляет/убирает actions через существующие commands; RollSpec можно отобразить, но до dice этапа не добавляй фиктивных локальных бросков. Права UI следуют server snapshot. Переиспользуй существующий bounded image lifecycle; не создавай unbounded avatar cache.

Приёмка: Alice/Bob меняют разные stats без потери данных; reset возвращает актуальный preset; false/0/пустая строка корректны; конфликт типов отклонён; avatar независим от token; revoke закрывает редактор и не отправляет черновики. Проверки и журнал — по B.
```

### 13. GM editor характеристик и actions

```text
Выполни этап 13. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 06 и 12. Исходное ТЗ: §§4–7, 26, 40–41.

Точки входа: definitions read/commands этапа 06, web/index.html/style.css, новый web/definitions.js, web/characters.js.

Добавь компактный campaign editor без нового framework: список/создание stat, display name/type/default с серверными ограничениями; создание/редактирование action name/description/tags и нескольких RollSpec. Показывай источник ruleset/campaign и результат overlay. Reference errors должны быть читаемыми; stale revision не перезаписывает новые данные молча.

Реализуй Duplicate Action с новым ID и назначением копии текущему character через уже существующие команды. Не делай inheritance или локальные ActionDefinition. Разделяй успех создания копии и её назначения при частичном пользовательском сценарии, не обещай одну транзакцию, если API использует две.

Приёмка: редактирование campaign action не меняет ruleset; duplicate независим; referenced deletion/type change заблокированы; Unicode IDs; игрок не открывает GM editor и сервер отклоняет обход UI. Preset editor/TOML UI — следующий этап. Проверки и журнал — по B.
```

### 14. GM editor presets и TOML import/export

```text
Выполни этап 14. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 02, 06, 09, 11–13. Исходное ТЗ: §§8, 11, 25, 38–41.

Точки входа: web/definitions.js, registry/TOML API, avatar API, character subscriptions.

Добавь создание/редактирование/duplicate preset: name/kind/avatar/stats/actions. Выбор known actions использует IDs. Unknown stats создаются атомарно сервером. Изменение preset обновляет открытые sheets через готовый realtime, без массовой перезаписи instances.

Подключи два явно разных TOML-сценария. «Установить ruleset» принимает ruleset document, показывает metadata/diff/errors и доступен только при пустом ruleset snapshot; Apply повторно отправляет тот же файл через HTTP с digest preview, expected revision и устойчивым client operation ID. «Импортировать расширения кампании» делает upsert campaign definitions через тот же HTTP preview/apply contract. Export позволяет отдельно выгрузить установленный ruleset snapshot и campaign extensions, не включает runtime instances. Не добавляй replace/uninstall ruleset, raw script execution, imports filesystem paths или второй backend. Некорректный TOML/metadata/reference либо отсутствующий campaign asset не оставляет половину registry.

Приёмка: UI устанавливает ruleset в существующую пустую кампанию и после этого не предлагает молча заменить его; редактирование campaign overlay wolf меняет inherited dexterity и сохраняет override; duplicate preset независим; round-trip Unicode TOML обоих видов через UI; stale preview требует обновления; campaign import не удаляет отсутствующие definitions и не меняет ruleset. Проверки и журнал — по B.
```

### 15. Серверный контекст Player View

```text
Выполни этап 15. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 04–08. Исходное ТЗ: §§19–20, 42–44.

Точки входа: main.go peer/ws (activeToken сейчас запрещён GM), scene_content.go activeTokenForMember/currentFloorForPeer, scenes.go snapshotSceneAtFloor/ownedTokenLocators/asset visibility, reliable.go move/final/publish.

Добавь transient per-peer preview context по A5, не меняя GM flag/role. GM в preview может выбирать любой token текущей сцены; получает player render projection и лёгкие locators всех токенов. Используй существующие predicates player visibility/floors/renderBounds и server movement validation; не копируй геометрические алгоритмы. Hidden token может быть в GM selector, но не становится видимым на canvas.

Определи переключение режима относительно queued/in-flight move/final; snapshot подтверждает режим и active floor. Reconnect/subscribe должны иметь однозначное восстановление. Обычный player не может получить GM projection или изменить права через preview command. Редактирование GM вне preview работает как раньше.

Приёмка: два peer одного GM могут иметь разные режимы; preview move блокируется walkable; normal GM move сохраняет прежнее поведение; distant token выбирается без полной загрузки сцены; hidden/background data фильтруются player projection. UI переключатель пока не нужен. Проверки и журнал — по B.
```

### 16. Player View в интерфейсе и canvas

```text
Выполни этап 16. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 10–12 и 15. Исходное ТЗ: §§19–23, 42–43, сценарий G.

Точки входа: web/app.js isGM/renderPanels/activatePlayerToken/processTokenDrag/pointer/keyboard/draw, web/scenes.js, web/scene-tree.js, web/scene-content.js, web/movement-geometry.js, web/index.html/style.css.

Добавь переключатель GM / Player View с всегда доступным возвратом. Раздели authorizing role и effective interaction mode; не меняй все isGM механически. Используй общий player path для canvas, bounds, movement, active token, sidebar и sheet. Запрети build selection/handles/tools/keyboard edits в preview. При переключении корректно заверши/cancel drag, убери selection и drafts геометрии; серверный mode ACK/snapshot определяет готовность нового режима.

Сохрани локальный viewport/active selection предсказуемо; не загружай всю сцену для selector. GM preview edits остаются реальными изменениями. Не делай impersonation другого пользователя.

Приёмка настоящим browser test: GM → preview → token → drag за walkable запрещён → background не выбирается → другой этаж/token → возврат → build работает; отдельно реальный player path. Проверки и журнал — по B.
```

### 17. Серверная логика бросков и deterministic RNG

```text
Выполни этап 17. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 01 и 07. Исходное ТЗ: §§27–31, 37, 44.

Точки входа: character effective helpers/permissions, ActionDefinition/RollSpec; новый dice.go/dice_test.go.

Создай RollRequest/RollEvent, строгую validation и сервис выполнения manual/action roll по A6. Action request разрешается сервером через effective character/actions/roll spec; count/sides/modifier нельзя подменить. Проверяй наличие доступного связанного token в сцене. RollEvent фиксирует серверные имена, ID, timestamp, результаты, modifier и total.

RNG инъецируется интерфейсом; production использует crypto/rand с равномерным диапазоном. Контролируй integer count, поддерживаемые sides, 100 dice, missing/wrong stat и конечность/точность total. Не добавляй WS/storage/UI в этом этапе и не строй expression parser.

Приёмка: deterministic 10d10, все supported sides, count 0/1/100/101, отрицательный/fractional numeric modifier, sum двух модификаторов, отсутствующий stat/action, подмена count/results/author, RNG error без частичного event. Статистический stress test RNG не нужен. Проверки и журнал — по B.
```

### 18. Reliable rolls, bounded journal и realtime

```text
Выполни этап 18. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 08 и 17. Исходное ТЗ: §§29–36, 45, 50.

Точки входа: reliable.go Command/Receipt/command/commandPersistence, main.go Session/newServer/ws/saveLocked, scene subscription lifecycle; новый dice_commands.go.

Подключи roll к существующему reliable business stream. Сохраняй bounded campaign history 500 и result-bearing receipt атомарно до broadcast/ACK. Повтор подтверждённого request не вызывает RNG и возвращает тот же event даже после restart или вытеснения event из history, пока существует receipt. Не рассылай event при failed save; rollback восстанавливает историю/revision/receipt. Не заводи отдельный WebSocket.

Рассылай новые события подписчикам сцены независимо от viewport; history/reconnect возвращает доступную историю текущей сцены с явным признаком replay. Не ломай scene delivery и существующие command seq. История не удерживает character от GC и сохраняет snapshot имён после rename/delete.

Приёмка: lost ACK, reconnect, restart, same seq/different payload, history eviction, replay dedup contract, disk failure, две сцены, unpublish/delete/access checks. Гарантии после pruning receipts явно ограничены существующим transport. UI ещё не делай. Проверки и журнал — по B.
```

### 19. Dice controls, action buttons и журнал

```text
Выполни этап 19. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 12 и 18. Исходное ТЗ: §§23, 27, 30, 34–37.

Точки входа: web/app.js WS dispatch/queueCommand, web/characters.js, web/index.html/style.css; новый web/dice.js. Реальных 3D-анимаций здесь ещё нет.

Добавь в нижнюю часть правой панели count input, +/- и dice selector с циклом ЛКМ/ПКМ и подавлением context menu. Manual roll работает без character; при выбранном связанном token может включать character ID. Action buttons отправляют IDs, не вычисленный доверенный modifier. Server errors показываются без зависания Outbox.

Добавь раскрываемый journal: автор, character/action/roll spec, notation, все results, modifier, total. Bounded 500 записей, bounded DOM/dedup; replay/reconnect не дублирует строки. Late events старой сцены не загрязняют текущую. Добавь local all/self/off setting и точку передачи только новых событий будущему animator. Текст пользователя выводи через textContent.

Приёмка: 10d10, границы input, ПКМ цикл, action modifier с сервера, roll без token, duplicate event/history, 501 events, off не влияет на журнал. Проверки и журнал — по B.
```

### 20. dice-box-threejs: локальная зависимость и адаптер

```text
Выполни этап 20. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужен 19. Исходное ТЗ: §§28, 32–34, 46.

Точки входа: Go embed web/* в main.go, web/dice.js, scripts; рекомендуемый web/dice-renderer.js. Проверь README и исходники конкретной версии официального @3d-dice/dice-box-threejs.

Зафиксируй dependency и транзитивные версии, добавь локальный vendor bundle/необходимые assets/license и воспроизводимый способ обновить bundle. Не переводить весь app на сборщик и не требовать CDN во время игры. Реализуй lazy adapter init/renderPredetermined/cleanup с проверенным фактическим API. Не предполагай существование dispose, пока не увидишь исходник.

Предопределённые значения строятся исключительно из server event; библиотечный result не меняет журнал/total. Покажи доказуемо корректный 10d10 и d4/d6/d8/d10/d12/d20. Установи соответствие order/multiset server results фактическим результатам renderer; не скрывай несоответствие заменой чисел в журнале. Overlay не перехватывает canvas input. Queue/policies подключатся в 21.

Приёмка в реальном browser: packaged Go app обслуживает bundle/assets локально, forced outcomes совпадают, повтор init/cleanup не оставляет canvas/listeners/RAF/GPU resources. Ошибка WebGL возвращается адаптером без нарушения основного app. Проверки и журнал — по B.
```

### 21. Очередь анимаций, настройки и cleanup

```text
Выполни этап 21. Прочитай AGENTS.md и docs/characters-dice-prompts.md A–C; нужны 19–20. Исходное ТЗ: §§32–36, 46 и сценарии H–J.

Точки входа: web/dice.js, web/dice-renderer.js, web/app.js clearSceneState/reconnect/leave, local animation setting.

Подключи animator к новым RollEvent: журнал немедленно, animation отдельно. Максимум один roll in flight, 8 pending, >30 dice пропускает 3D целиком. При overflow отбрасывай только animation. all/self/off фильтрует также звук, off не создаёт renderer. Replayed/history события не анимируются.

Введи timeout/error recovery, cancellation при смене сцены/кампании и очистку очереди при смене политики. Не запускай новый roll поверх зависшего старого renderer; сначала остановка/cleanup и только затем восстановление. Не держи постоянный render loop при отсутствии анимации. Очередь и dedup освобождаются по понятному lifecycle.

Приёмка: burst больше queue limit, 100 dice в журнале без 3D, only-self для Alice/Bob, off до init и во время roll, reconnect без повтора, смена сцены, timeout/rejection, WebGL failure. Mock tests проверяют orchestration, хотя бы один real-browser тест проверяет библиотечный путь. Проверки и журнал — по B.
```

### 22. Сквозная приёмка и завершение эпика

```text
Выполни этап 22. Прочитай AGENTS.md, docs/characters-dice-prompts.md целиком, исходное docs/characters-dice-spec.md §§49–53 и журнал всех предыдущих этапов. Это интеграционная приёмка, не новый scope.

Сопоставь каждый сценарий A–J и обязательные проверки §50 с фактическими тестами/поведением. Добавь недостающие интеграционные проверки и исправь только дефекты эпика. Особенно проверь цепочки: пустая existing campaign→ruleset preview/install→restart→immutable snapshot→campaign overlay; legacy save→owners migration→restart; пять независимых wolves→preset edit→GC; persistent character→две scenes→ownership revoke; два владельца→concurrent stat drafts→reconnect; GM→Player View→walkable/background→возврат; roll→lost ACK→restart→тот же event→journal/animation policy.

Проверь edge cases: character удалён при открытом sheet/загрузке avatar, token вне viewport, unpublish сцены, failed persistence, 100 dice и переполнение history/queue. Убедись, что definitions не GC, avatars защищены references, journal не удерживает characters, hidden sheets не уходят игрокам, новых scans/cache work в render/move path нет.

Запусти gofmt для изменённых Go-файлов, go build ./..., полный обычный go test ./... и реальные targeted Chrome/Edge сценарии с ATLAS_CHROME. Согласуй test timeout с уже существующим harness. Не включай тяжёлые opt-in profile/stress suites без найденной зависимости. Проверки libvips/3D не заменяй mock-only результатом; недоступные честно отметь.

Просмотри итоговый diff, не трогай исходные пользовательские удаления. Запиши завершение/оставшиеся реальные блокеры в журнал C. Дай краткий итог реализации и проверок. Не объявляй эпик завершённым при непроверенном обязательном сценарии, failing tests или заглушке вместо функции.
```
