# Scenes handoff

## Архитектура

`Session` остаётся внутренним именем Campaign. Campaign владеет участниками, ключами, receipts, `CampaignRevision`, общим `Assets` и набором `Scenes`. Клиент находится на Campaign Home либо подписан максимум на одну Scene.

Scene владеет независимыми `Bounds`, `Revision`, `Floors`, `Layers`, `Elements`, `Tokens` и `Transitions`. Текущий продуктовый лимит — два Floor. У каждого Floor есть `opacity`, `opacityWhenViewedFromBelow`, ровно один `tokens` Layer и один функциональный `walkable` Layer; остальные Layers имеют `kind=visual`. `SceneElement` может принадлежать только visual Layer, Token хранит ссылку на Token Layer своего Floor. Карта является обычным SceneElement; asset pipeline автоматически выбирает bitmap либо tiled LOD.

Подсистема разделена так:

- `scene_content.go` — типы, значения по умолчанию, двухэтажные/compositing/walkable invariants, миграция текущей Layer-модели, геометрия переходов и campaign-wide asset reference tracking;
- `scene_commands.go` — авторитетные команды bounds/floors/layers/elements/transitions/retention, rollback, revision и realtime публикация;
- `scenes.go` — подписка, snapshots, region/spatial runtime, asset discovery и authorization;
- `web/scene-content.js` — общий Floor/Layer render stack, bitmap/tiled LOD, отсечение тайлов повернутых элементов, walkable overlay, hit-test и transform-геометрия;
- `web/scene-tree.js` — GM UI дерева и его команды.

`main.go`, `reliable.go` и `web/app.js` являются интеграционными границами с persistence/upload, reliable transport и общим клиентским runtime.

## Invariants

- Campaign Home не получает state, revisions или asset metadata сцен. GM видит все summaries, Player — только published.
- Peer подписан максимум на одну Scene. Все snapshot/event/ACK содержат `sceneId`; клиент отбрасывает ответы другой Scene и при переключении очищает scene-local state.
- Scene open отдаёт metadata и entry point. Для Player `activeTokenId` хранится на конкретном WS peer, заново проверяется по ownership/hidden при выборе и reconnect и определяет current Floor. Snapshot отдельно содержит безопасные locators всех собственных нескрытых токенов без asset ID, поэтому список служит навигацией между этажами без eager loading. После bounded `view` сервер определяет участвующие в compositing Floors, пересекающие region Tokens/SceneElements и metadata только их assets.
- Для нижнего Floor верхний загружается только при `upper.opacity * upper.opacityWhenViewedFromBelow > 0`; для верхнего нижний загружается только при `lower.opacity > 0`. Нулевой effective alpha не создаёт asset request/decode.
- Невидимые Player элементы, элементы полностью вне `Scene.Bounds` и их assets скрываются как через snapshot/realtime, так и через binary endpoint. Знание asset ID не является разрешением.
- Runtime uniform-grid indexes и public/all asset reference counts по Floor не персистентны и восстанавливаются из authoritative Scene state.
- Scene logical bounds не определяют размер Canvas и не перемещают/удаляют содержимое. Walkable Bounds отдельно ограничивают Player movement; GM может поставить токен снаружи.
- Render order детерминирован: Floor composition, затем общий Layer order, где visual и Token Layer чередуются, затем element `zOrder` с ID как tie-breaker. Отдельного финального token pass нет.
- Большая карта не является отдельным Scene type. Все raster SceneElement используют `kind=scene` и одну `representationPolicy`: bitmap при оценке декодированной памяти до 8 МиБ и стороне до 8192 px, иначе tiled LOD. Старый входной alias `kind=map` нормализуется в `scene`. Повёрнутый tiled raster отсекает тайлы по многоугольнику viewport.
- Исходник и подготовленное представление имеют разные идентичности: `sourceId` — SHA-256 исходных байтов, а Asset ID включает роль, render mode и `representationVersion`. Поэтому одинаковое представление переиспользуется всеми SceneElement, а изменение алгоритма, формата или политики создаёт новый immutable URL без порчи старого browser cache.
- Fix Rotation — отдельная reliable business-команда. Под mutex она фиксирует recipe, затем использует общий ограниченный libvips worker вне mutex и перед commit повторно сверяет права, target и recipe hash. Scene revision целиком не является условием commit. ACK означает durable completion; `job: processing/completed/error` отделяет состояние долгой операции. Retry одного `client/seq` не запускает второй worker, а shutdown отменяет и дожидается активной job.
- Derived raster хранит server-only provenance на исходный Asset и recipe. Reference tracking проходит по этой цепочке, поэтому исходник не становится orphan. Для sparse tiled output `tilePresence` является компактным побитовым manifest по уровням; клиент не запрашивает known-empty tiles, а произвольный 404 по-прежнему считается ошибкой.
- `elementPreview` меняет RAM/realtime без receipt и диска. Финальный `elementTransform` использует отдельный LWW reliable stream, ACK не ждёт I/O, а последнее состояние coalesce в общий flush примерно раз в 5 секунд.
- Структурные команды и свойства сохраняются до ACK. Immediate save включает pending transforms. No-op не создаёт revision или save.
- `Scene.Revision` меняется только от authoritative state этой Scene. Delivery sequence отдельна для каждого peer и растёт только для реально отправленных ему событий.
- Token остаётся отдельной сущностью, принадлежит Token Layer своего Floor и не может произвольно менять Floor со стороны Player. Transition проверяет управление токеном, source region и destination Walkable Bounds на сервере.
- Assets принадлежат Campaign и могут иметь много ссылок из разных сцен. `keep` не становится orphan; `reclaimable` без ссылок получает `orphanSince`, но физически не удаляется.
- Server Asset lifecycle не связан с bounded client RAM/IndexedDB caches. Смена Scene отменяет ненужные запросы, но не очищает весь shared cache.
- Singleton `Scene.Map` отсутствует. Startup upgrade текущего формата маркирует прежние Layers как `visual`, создаёт специальные Layers и привязывает Tokens; карту он не превращает в отдельный backend type.

## Реализовано

Реализованы logical bounds, максимум два Floors, visual/Token/Walkable Layers, произвольные SceneElements, общий порядок visual content и Tokens, drag/drop, move/resize/rotate, duplication, multiplicative opacity, двухэтажный compositing, authoritative current Floor и базовые Transitions. Player UI не показывает дерево редактора и не выбирает SceneElement.

Asset metadata содержит source/representation identity, filename/MIME/dimensions/size/levels/render mode/retention/orphan time. Обычная загрузка создаёт reclaimable SceneElement; libvips автоматически готовит bitmap или tile pyramid. Token portrait остаётся специализированным bitmap до 512 px и не используется как визуальный SceneElement. Asset discovery следует цепочке visible Floors → bounded region → SceneElements/Tokens → asset metadata → binary requests; невидимые и внеэкранные изображения не инициируют загрузку.

Порог проверен на 512², 1024², 2048², 4096², длинном 8192×128 и целевом 11220×11516. На тестовой Windows-машине bitmap/tiled занимали соответственно 22/28 мс для 512², 33/61 мс для 1024², 67/179 мс для 2048² и 169/564 мс для 4096²; tiled-подготовка 11220×11516 заняла около 4,9 с и создала 688 файлов. Порог 8 МиБ выбран как ограничение удерживаемой decoded RAM: стоимость подготовки платится один раз, а загрузка и декодирование происходят по viewport.

Persistence/reliable protocol сохраняют прежнее разделение realtime RAM, coalesced LWW transforms и durable business-команд. Graceful shutdown flush'ит pending state. Реализована явная фиксация rotation одного SceneElement: anisotropic scale выполняется до rotate по ограниченной pixel-density policy, центр и world-space bounding footprint сохраняются, а итог снова проходит общую bitmap/tiled representation policy.

## Осталось

Derived-asset bake visual layers, анимации, Actor и Token→Actor, Asset Library, поиск/папки/previews, физический GC с grace period, ACL отдельных элементов, interaction/collision, FOV/свет/стены и перенос элементов между Scenes не реализованы. `orphanSince` уже даёт безопасную точку для будущего GC. Повторная фиксация уже derived raster сохраняет provenance chain, но пока обрабатывает предыдущий derived bitmap, а не сворачивает всю цепочку affine transforms к самому первому source.

Для Fix Rotation не выполнен отдельный peak-RSS замер на raster около предела 150 Мп. Лимит временных данных оценивается до запуска, job имеет timeout и общий concurrency limit, но это не hard RAM cap для libvips. UI отмены фиксации отсутствует; исходник и provenance сохраняются, поэтому будущая реализация не потребует менять сохранённую модель.

Клиентский Transition editor пока минимальный: создаёт прямоугольный односторонний переход через prompts. Серверная модель также принимает круг и хранит directionality, но полноценный визуальный редактор не сделан.

Плотный viewport с десятками тысяч объектов всё ещё требует их передачи и отрисовки; aggregation/LOD для самих сущностей нет. Общий server mutex и полный JSON save остаются ограничениями прототипа.

## Dependency map

`image_pipeline.go` владеет выбором и immutable identity raster representation. `derived_pipeline.go` строит recipe, геометрию и derived resource, а `rotation_jobs.go` владеет asynchronous reliable lifecycle и commit. `main.go` вызывает preprocessing, структуру/validation при startup и upload, а persistence сериализует Campaign целиком. `reliable.go` маршрутизирует команды токенов и Scene content. `scene_commands.go` использует типы/validation из `scene_content.go` и публикацию/region runtime из `scenes.go`. `web/app.js` связывает transport/cache с `web/scene-content.js` и `web/scene-tree.js`.

## Максимум пяти файлов для handoff

Для большинства следующих изменений Scene content достаточно:

1. `scene_content.go`
2. `scene_commands.go`
3. `scenes.go`
4. `web/scene-content.js`
5. `web/scene-tree.js`

Для persistence/upload дополнительно нужен `main.go`; для reliable classification/receipts — `reliable.go`; для клиентского transport и формы свойств — `web/app.js` и `web/index.html`; для CSS — `web/style.css`; для серверных проверок — `scene_content_test.go`; для E2E — `browser_test.go` и `browser_scenarios_test.go`; для preprocessing — `image_pipeline.go`, `derived_pipeline.go`, `rotation_jobs.go` и `rotation_jobs_test.go`.
