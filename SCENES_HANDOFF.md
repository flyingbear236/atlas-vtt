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
- Большая карта не является отдельным Scene type. Изображение до порога получает bitmap representation, крупное — tiled LOD; повёрнутый tiled raster отсекает тайлы по многоугольнику viewport.
- `elementPreview` меняет RAM/realtime без receipt и диска. Финальный `elementTransform` использует отдельный LWW reliable stream, ACK не ждёт I/O, а последнее состояние coalesce в общий flush примерно раз в 5 секунд.
- Структурные команды и свойства сохраняются до ACK. Immediate save включает pending transforms. No-op не создаёт revision или save.
- `Scene.Revision` меняется только от authoritative state этой Scene. Delivery sequence отдельна для каждого peer и растёт только для реально отправленных ему событий.
- Token остаётся отдельной сущностью, принадлежит Token Layer своего Floor и не может произвольно менять Floor со стороны Player. Transition проверяет управление токеном, source region и destination Walkable Bounds на сервере.
- Assets принадлежат Campaign и могут иметь много ссылок из разных сцен. `keep` не становится orphan; `reclaimable` без ссылок получает `orphanSince`, но физически не удаляется.
- Server Asset lifecycle не связан с bounded client RAM/IndexedDB caches. Смена Scene отменяет ненужные запросы, но не очищает весь shared cache.
- Singleton `Scene.Map` отсутствует. Startup upgrade текущего формата маркирует прежние Layers как `visual`, создаёт специальные Layers и привязывает Tokens; карту он не превращает в отдельный backend type.

## Реализовано

Реализованы logical bounds, максимум два Floors, visual/Token/Walkable Layers, произвольные SceneElements, общий порядок visual content и Tokens, drag/drop, move/resize/rotate, duplication, multiplicative opacity, двухэтажный compositing, authoritative current Floor и базовые Transitions. Player UI не показывает дерево редактора и не выбирает SceneElement.

Asset metadata содержит filename/MIME/dimensions/size/levels/render mode/retention/orphan time. Обычная загрузка создаёт reclaimable SceneElement; libvips автоматически готовит bitmap или tile pyramid. Asset discovery следует Floor-aware region state.

Persistence/reliable protocol сохраняют прежнее разделение realtime RAM, coalesced LWW transforms и durable business-команд. Graceful shutdown flush'ит pending state.

## Осталось

Запекание visual layers, анимации, Actor и Token→Actor, Asset Library, поиск/папки/previews, физический GC с grace period, ACL отдельных элементов, interaction/collision, FOV/свет/стены и перенос элементов между Scenes не реализованы. `orphanSince` уже даёт безопасную точку для будущего GC.

Клиентский Transition editor пока минимальный: создаёт прямоугольный односторонний переход через prompts. Серверная модель также принимает круг и хранит directionality, но полноценный визуальный редактор не сделан.

Плотный viewport с десятками тысяч объектов всё ещё требует их передачи и отрисовки; aggregation/LOD для самих сущностей нет. Общий server mutex и полный JSON save остаются ограничениями прототипа.

## Dependency map

`main.go` вызывает структуру/validation при startup и upload, а persistence сериализует Campaign целиком. `reliable.go` маршрутизирует команды токенов и Scene content. `scene_commands.go` использует типы/validation из `scene_content.go` и публикацию/region runtime из `scenes.go`. `web/app.js` связывает transport/cache с `web/scene-content.js` и `web/scene-tree.js`.

## Максимум пяти файлов для handoff

Для большинства следующих изменений Scene content достаточно:

1. `scene_content.go`
2. `scene_commands.go`
3. `scenes.go`
4. `web/scene-content.js`
5. `web/scene-tree.js`

Для persistence/upload дополнительно нужен `main.go`; для reliable classification/receipts — `reliable.go`; для клиентского transport и формы свойств — `web/app.js` и `web/index.html`; для CSS — `web/scenes.css`; для серверных проверок — `scene_content_test.go`; для E2E — `browser_test.go` и `browser_scenarios_test.go`; для preprocessing — `image_pipeline.go`.
