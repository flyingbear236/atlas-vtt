# Walkable geometry и render bounds: индекс реализации

## Общие архитектурные факты

- `Scene` определён в `scenes.go`, а `Floor`, `Layer`, `SceneBounds`, `WalkableBounds` и проверка структуры сцены — в `scene_content.go`. Сейчас прямоугольный `WalkableBounds` принадлежит специальному `Layer(kind=walkable)`, а не `Floor`.
- Отдельных persistence-структур нет: `main.go:newServer` напрямую загружает `map[string]*Session` из `data/sessions.json`, вызывает `ensureSceneStructure`/`validateSceneStructure`, а `Server.saveLocked` атомарно пишет полный JSON через `.tmp` и замену файла.
- Клиент ставит durable-команды в `web/reliability.js:Outbox`; `main.go:Server.ws` передаёт их в `reliable.go:Server.command`, затем scene-content команды идут в `scene_commands.go:Server.contentCommand`. `Scene.Revision`, persisted `Receipt{Seq,Digest}` и peer-local `delivery` уже обеспечивают revision, dedup/reconnect и фильтрованный realtime-поток.
- Движение токена проверяется в `reliable.go:Server.command`, ветка `move/final`. Для Player сейчас проверяется только конечный центр через `scene_content.go:positionInWalkable`; GM обходит ограничение, а переход между этажами определяется отдельно через `transitionForMove`.
- Главный Canvas-проход находится в `web/app.js:draw`; этажи и слои рисует `web/scene-content.js:drawSceneStack`, токены — `web/token-renderer.js:drawTokens`. Сетка пока рисуется в `draw` после `drawSceneStack`.
- Сервер выбирает региональные `Token`/`SceneElement` через `scenes.go:sceneRuntime.query/queryElements` и выдаёт только нужные asset metadata в `snapshotSceneAtFloor`. Клиент выбирает LOD/тайлы в `web/scene-content.js:drawTiled`, планирует запросы в `web/app.js:requestImage/scheduleImages`, загружает и декодирует в `loadImage` через `createImageBitmap`.
- Hit testing элементов выполняют `web/scene-content.js:elementsAtPoint/hitElement/elementHandleAt`; токены сначала отбираются `web/rendering.js:SpatialIndex`, затем точно проверяются в `web/app.js:canvas.onpointerdown`.
- GM pointer/drag infrastructure сосредоточена в `web/app.js:canvas.onpointerdown/onpointermove/endDrag`; локальные transform-preview уже не требуют durable-команды на каждый `pointermove`. Панельные команды Floor/Layer и старых bounds находятся в `web/scene-tree.js:SceneTreeRuntime`.
- `Scene.Bounds` сейчас задаёт прямоугольник фона, начальную/fit-камеру, публичность элементов через `elementIntersectsSceneBounds`, исходный `WalkableBounds`, metadata snapshot и автоматически принимает размер первой карты. Редактируется командой `boundsUpdate` из `SceneTreeRuntime.saveBounds`.
- Можно переиспользовать: `Scene.Revision`, отдельную будущую geometry revision на `Floor`, `Outbox`, `Receipt/Digest`, `publishSceneSnapshot`, rollback внутри `contentCommand`, `SceneRegion`, server `sceneRuntime`, client `SpatialIndex`, OBB/AABB-проверки `elementIntersectsRegion/elementIntersectsView`, ограниченные image caches и отмену `pending` загрузок по множеству `wanted`.

## Последовательные итерации
### 0. Разведка архитектуры
**Цель**  
Зафиксировать реальные точки интеграции без изменения приложения; результатом служит этот файл.
**Читать**  
`scenes.go`: `Scene`, `sceneRuntime`, snapshots; `scene_content.go`: `Floor`, bounds; `main.go`: load/save/WS; `reliable.go` и `scene_commands.go`: команды; `web/app.js`, `web/scene-content.js`, `web/rendering.js`, `web/token-renderer.js`, `web/reliability.js`, `web/scene-tree.js`.
**Менять**  
Только `docs/walkable-renderbounds-plan.md`.
**Использовать**  
Поиск по конкретным типам и функциям; текущий git diff как источник истины.
**Не трогать**  
Код, тесты, зависимости, ассеты, profiling и несвязанный технический долг.
**Проверка**  
Проверить существование перечисленных файлов и основных символов.
### 1. Модели данных
**Цель**  
Добавить `Point`, `Polygon`, `WalkableComponent` и поля `walkableMode`, `walkableComponents`, `renderBounds`, `geometryRevision` на `Floor`. Не создавать второй несовместимый тип точки рядом с существующим `ScenePoint`.
**Читать**  
`scene_content.go`: `Floor`, `WalkableBounds`, `validateSceneStructure`; `scenes.go`: `ScenePoint`, `Scene`, `SceneMetadata`; `main.go:newServer`.
**Менять**  
`scene_content.go`, `scenes.go`, новый `geometry.go`, `scene_content_test.go`.
**Использовать**  
Прямую JSON-сериализацию текущих моделей; `validNumber`, `maxSceneDimension`, `sceneModelVersion`.
**Не трогать**  
Команды, движение, renderer, clipping, старый `WalkableBounds` и `Scene.Bounds`.
**Проверка**  
Unit-тест JSON round trip и различимость `unrestricted`, `restricted + []`, `renderBounds == null`.
### 2. Базовое геометрическое ядро
**Цель**  
Реализовать квантование, AABB, площадь/ориентацию, point-in-polygon с holes, segment intersection, simple-polygon validation и точную нормализацию контуров.
**Читать**  
`geometry.go` после этапа 1; `scene_content.go:validNumber/maxSceneDimension`; `scene_content.go:elementIntersectsRegion` только как ориентир принятой числовой модели.
**Менять**  
`geometry.go`, новый `geometry_test.go`.
**Использовать**  
Единые `Point`/`Polygon`, world coordinates и согласованный фиксированный шаг квантования.
**Не трогать**  
Boolean clipping, Floor state, WS, renderer и client geometry.
**Проверка**  
Тесты на concave polygon, holes, точки/отрезки на границе, повторные и коллинеарные вершины, self-intersection.
### 3. Adapter библиотеки polygon clipping
**Цель**  
Выбрать Go-библиотеку union/difference и изолировать её API за adapter, принимающим нормализованные Atlas `Polygon`. Выбор сделать после проверки holes, multi-component, касаний, integer safety и лицензии.
**Читать**  
`geometry.go` после этапа 2; `go.mod`; документацию и лицензию кандидата — уточнить после этапа 2.
**Менять**  
Новый `polygon_clip.go`, `polygon_clip_test.go`, `go.mod`, `go.sum`.
**Использовать**  
Квантование, normalization и limits hooks из `geometry.go`.
**Не трогать**  
Floor, команды, persistence, UI и движение.
**Проверка**  
Union пересекающихся/соседних прямоугольников, corner touch без merge, hole и difference со split.
### 4. `addWalkableRect`
**Цель**  
Добавить чистую серверную операцию: validate rect, AABB shortlist, union, normalization, deterministic component IDs и атомарный новый результат.
**Читать**  
`geometry.go`, `polygon_clip.go`; `scene_content.go:Floor`; `main.go:id`.
**Менять**  
Новый `walkable.go`, `walkable_test.go`; при необходимости только модель в `scene_content.go`.
**Использовать**  
Geometry AABB/limits, clipping adapter и существующий генератор стабильных случайных ID.
**Не трогать**  
WS, receipts, persistence lifecycle, движение и UI.
**Проверка**  
Г-форма, общее ребро, касание угла, contained rect, отдельный остров и добавление внутри hole.
### 5. `subtractWalkableRect`
**Цель**  
Добавить difference по затронутым AABB-компонентам и deterministic ID assignment после split, не сохраняя историю операций.
**Читать**  
`walkable.go:addWalkableRect`, `polygon_clip.go`, правила component IDs из этапа 4.
**Менять**  
`walkable.go`, `walkable_test.go`.
**Использовать**  
Тот же normalization/limits pipeline и atomically produced result.
**Не трогать**  
Команды, UI, renderer, movement и исходные прямоугольники операций.
**Проверка**  
Hole, срез края, split, полное удаление, no-op вне геометрии и один rect против нескольких компонентов.
### 6. move/delete/setMode для walkable
**Цель**  
Добавить перемещение компонента по `id + delta`, последующий локальный union, удаление и явное переключение `unrestricted/restricted`.
**Читать**  
`walkable.go`; `scene_content.go:Floor`; `geometry.go` translation helpers.
**Менять**  
`walkable.go`, `walkable_test.go`.
**Использовать**  
ID rules этапов 4–5, AABB shortlist и общий union pipeline.
**Не трогать**  
Токены, ассеты, transitions, WS и client preview.
**Проверка**  
Move без merge/с merge, перенос holes, delete last component при restricted и отсутствие автоматического unrestricted.
### 7. Versioning, conflict handling и deduplication операций
**Цель**  
Подключить walkable operations как immediate durable business-команды с `expectedGeometryRevision`. Использовать существующий `client/seq + Receipt.Digest`, не добавляя второй operation log.
**Читать**  
`reliable.go:Command/commandPersistence/Receipt`; `scene_commands.go:sceneContentCommand/contentCommand`; `web/reliability.js:Outbox`; `walkable.go`.
**Менять**  
`reliable.go`, `scene_commands.go`, `walkable.go`, `reliable_test.go` или новый `walkable_commands_test.go`.
**Использовать**  
Rollback/save/ACK pattern `contentCommand`, `Scene.Revision`, `Floor.geometryRevision`, `publishSceneSnapshot`.
**Не трогать**  
Coalesced token/element transforms, delivery semantics и UI.
**Проверка**  
Stale geometry revision, exact retry, reused seq with different digest, limit rejection и отсутствие partial state.
### 8. Persistence walkable
**Цель**  
Включить startup validation и выбранную политику старого прямоугольного `WalkableBounds`; сложную миграцию не делать. Поля моделей уже попадают в JSON автоматически.
**Читать**  
`main.go:newServer/saveLocked`; `scene_content.go:ensureSceneStructure/validateSceneStructure`; persisted model после этапов 1 и 7.
**Менять**  
`main.go`, `scene_content.go`, `scene_content_test.go` или `walkable_commands_test.go`.
**Использовать**  
`sceneModelVersion`, fail-fast повреждённого `sessions.json`, atomic `.tmp` save и graceful final save.
**Не трогать**  
Asset persistence, caches, renderer и runtime spatial indexes.
**Проверка**  
Create/edit, save, restart, normalized equality и сохранение component IDs/revisions/mode.
### 9. Проверка движения по walkable
**Цель**  
Ввести серверный `CanMoveTokenSegment` для всего отрезка, holes и нескольких компонентов. Текущая модель использует центр токена; закрепить это явно, пока не появится отдельное правило footprint.
**Читать**  
`reliable.go:Server.command` ветка `move/final`; `scene_content.go:positionInWalkable/transitionForMove`; `web/app.js:canvas.onpointermove`.
**Менять**  
Новый `movement_geometry.go` и тест, `reliable.go`; `web/app.js` и `web/scene-content.js` только для удаления старого rectangle clamp/advisory preview.
**Использовать**  
Geometry predicates этапа 2, authoritative server move, текущий GM bypass и отдельный transition pipeline.
**Не трогать**  
Teleport/transition destination rules, renderBounds и pathfinding.
**Проверка**  
Внутри, через hole/разрыв/concave edge, между компонентами и попытка Player двигать уже внешний токен.
### 10. Серверный `renderBounds`
**Цель**  
Добавить immediate durable команды `setRenderBounds`/`clearRenderBounds` с проверкой одного простого polygon без holes и отдельным geometry revision conflict.
**Читать**  
`geometry.go`; `scene_content.go:Floor/validateSceneStructure`; command integration этапа 7.
**Менять**  
`reliable.go`, `scene_commands.go`, новый либо общий `geometry_commands.go`, `geometry_test.go`/`walkable_commands_test.go`.
**Использовать**  
Simple polygon validation, normalization, limits, Receipt/Digest, rollback и `publishSceneSnapshot`.
**Не трогать**  
Canvas clipping, resource loading, hit testing и старый `Scene.Bounds`.
**Проверка**  
Rectangle, concave polygon, self-intersection, zero area, holes rejection, clear и stale revision.
### 11. Проверка движения с `renderBounds`
**Цель**  
Расширить `CanMoveTokenSegment`: независимо проверить walkable и renderBounds, не строя и не сохраняя их boolean intersection.
**Читать**  
`movement_geometry.go` после этапа 9; `geometry.go`; `scene_content.go:transitionForMove`.
**Менять**  
`movement_geometry.go`, его тесты; при необходимости одна точка вызова в `reliable.go`.
**Использовать**  
Тот же segment-containment predicate и `renderBounds == nil` как отсутствие ограничения.
**Не трогать**  
Renderer, clipping library, UI, camera и resource culling.
**Проверка**  
Walkable шире bounds, bounds шире walkable, их частичное пересечение и null bounds.
### 12. GM UI add/subtract walkable
**Цель**  
Добавить два rectangle tools с полностью локальным drag preview и одной durable-командой на `pointerup`; reject/Escape возвращает authoritative snapshot.
**Читать**  
`web/app.js:canvas.onpointerdown/onpointermove/endDrag`; `web/scene-tree.js:editWalkable`; `web/reliability.js:Outbox`; client contracts этапа 7.
**Менять**  
`web/app.js`, `web/scene-content.js`, `web/scene-tree.js`, `web/index.html`, `web/style.css`, `browser_test.go`.
**Использовать**  
Существующий `drag`, GM checks, world coordinate conversion, `queueCommand`, toast/error/snapshot recovery.
**Не трогать**  
Component drag, renderBounds editor, player UI и Canvas clipping.
**Проверка**  
Browser: ноль WS-команд во время drag, ровно одна на release, Escape и server reject cleanup.
### 13. GM UI перемещения walkable components
**Цель**  
Добавить component selection, локальное смещение outer+holes и одну команду `componentId + delta`; после ответа всегда принять server topology/IDs.
**Читать**  
UI этапа 12; `web/app.js` drag lifecycle; snapshot/event contract этапа 7.
**Менять**  
`web/app.js`, `web/scene-content.js`, возможно новый `web/geometry.js`, `browser_test.go`.
**Использовать**  
Client point-in-polygon только для выбора/preview, authoritative server merge и geometry revision.
**Не трогать**  
Токены, ассеты, transitions и vertex editing renderBounds.
**Проверка**  
Drag без merge/с merge, holes, Escape, stale revision и замена IDs из ответа.
### 14. Базовый редактор `renderBounds`
**Цель**  
Добавить rectangle creation, polygon clicks, завершение первой вершиной/Enter, clear и Escape. Локальная validation помогает UX, сервер остаётся авторитетным.
**Читать**  
GM tools этапов 12–13; `web/app.js` transition draft как пример multi-step gesture; server contract этапа 10.
**Менять**  
`web/app.js`, `web/scene-content.js`, `web/scene-tree.js`, `web/index.html`, `web/style.css`, `browser_test.go`.
**Использовать**  
Draft lifecycle, world coordinates, `queueCommand`, geometry revision и client geometry helpers — уточнить после этапа 13.
**Не трогать**  
Vertex editing, clipping, hit testing, tile loading и old bounds removal.
**Проверка**  
Rectangle, concave clicks, Enter/first-point finish, invalid preview, clear и Escape.
### 15. Редактирование вершин `renderBounds`
**Цель**  
Добавить drag вершины/полигона, insert на edge и delete vertex; один `setRenderBounds` отправляется только после завершения жеста.
**Читать**  
Редактор этапа 14; `web/scene-content.js:elementHandleAt/transformedFromDrag` как пример handles.
**Менять**  
Файлы редактора этапа 14 — уточнить после этапа 14; соответствующий browser test.
**Использовать**  
Локальный draft, server revalidation, geometry revision и authoritative rollback.
**Не трогать**  
Walkable topology, renderer clipping, loading и SceneElement editor.
**Проверка**  
Все четыре жеста, минимум три вершины, invalid edit, Escape и conflict response.
### 16. Canvas clipping
**Цель**  
Обрезать единый world pass по `renderBounds`: visual layers, token layer, grid и world effects. GM edit mode видит внешний мир; player preview использует тот же cache без копии сцены.
**Читать**  
`web/app.js:draw`; `web/scene-content.js:drawSceneStack`; `web/token-renderer.js:drawTokens`; editor state этапов 14–15.
**Менять**  
`web/app.js`, `web/scene-content.js`, возможно `web/geometry.js`, `browser_scenarios_test.go`.
**Использовать**  
`ctx.save/clip/restore`, `compositeFloors`; кэшировать `Path2D` по `floorId + geometryRevision`.
**Не трогать**  
Resource selection/decode, server authorization, movement и image caches.
**Проверка**  
Частичный объект, concave bounds, tokens/grid под clip и изменение bounds без reload.
### 17. Hit testing с учётом `renderBounds`
**Цель**  
До существующего Player hit-test отклонять world point вне bounds; GM edit mode сохраняет доступ к внешнему содержимому.
**Читать**  
`web/scene-content.js:elementsAtPoint/hitElement/elementHandleAt`; `web/app.js:canvas.onpointerdown`; `web/rendering.js:SpatialIndex`.
**Менять**  
`web/scene-content.js`, `web/app.js`, `browser_scenarios_test.go`/`browser_test.go`.
**Использовать**  
Client point-in-polygon и текущий reverse render-order selection.
**Не трогать**  
Server asset authorization, clipping, geometry commands и spatial index layout.
**Проверка**  
Токен/объект у края: клик внутри видимой части работает, снаружи clip — нет; GM edit остаётся доступен.
### 18. Замена старых scene bounds
**Цель**  
После появления рабочей замены распределить роли `Scene.Bounds`: visual restriction передать Floor renderBounds, fit/start camera считать от его AABB, старый editable rectangle удалить. Fallback камеры при null bounds уточнить здесь.
**Читать**  
Все найденные usages `Scene.Bounds`: `scene_content.go`, `scenes.go`, `scene_commands.go`, `main.go`, `web/app.js`, `web/scene-tree.js`, `web/index.html`; результаты этапов 10 и 16.
**Менять**  
Перечисленные файлы и `scene_content_test.go`; точный набор уточнить после этапа 17.
**Использовать**  
Polygon AABB, content extents/entry point и текущие camera helpers `initializeSceneCamera/fit`.
**Не трогать**  
Walkable state, asset dimensions, viewport `SceneRegion` и arbitrary camera movement.
**Проверка**  
Поиск оставшихся semantic usages, new-floor/default-map behavior, camera fit/start и сборка.
### 19. Culling тайлов до HTTP/decode
**Цель**  
До `requestImage` отбрасывать tiled chunks вне renderBounds: viewport/preload, AABB, затем точный tile-polygon intersection. Boundary tile загружается целиком и режется Canvas clip.
**Читать**  
`web/scene-content.js:drawTiled/rectIntersectsPolygon/tileAvailable`; `web/app.js:requestImage/scheduleImages/loadImage`; bounds cache этапа 16.
**Менять**  
`web/scene-content.js`, возможно `web/geometry.js`, `browser_scenarios_test.go` и browser network test.
**Использовать**  
Существующий LOD, one-row prefetch, `imagePlans/wanted`, AbortController и bounded/WeakMap metadata caches.
**Не трогать**  
HTTP asset API, IndexedDB policy, decode budgets, LOD fallback и server sceneRuntime.
**Проверка**  
Счётчики requests/decode внутри, снаружи и обратно; полностью внешние тайлы не запрашиваются.
### 20. Culling остальных дорогих ресурсов
**Цель**  
После замера добавить ранний reject для bitmap SceneElement и token artwork, только где он предотвращает реальный fetch/decode. Каждое видимое использование общего asset оценивается отдельно.
**Читать**  
`web/scene-content.js:drawElement/drawVisualLayer`; `web/token-renderer.js:drawTokens`; `web/app.js:requestImage`; результаты этапа 19.
**Менять**  
`web/scene-content.js`, `web/token-renderer.js`, browser tests — уточнить после этапа 19.
**Использовать**  
Bounds AABB/exact intersection, existing viewport culling и shared `requestImage` planning.
**Не трогать**  
Asset GC/authorization, cache limits, server snapshots и невизуальные сущности.
**Проверка**  
Один asset у нескольких объектов, partially visible bitmap/token и отсутствие лишних request/decode.
### 21. Lifecycle и invalidation при изменении bounds
**Цель**  
По новой geometry revision инвалидировать Path2D и bounded tile classifications; следующий render сам пересчитает `wanted` и отменит ставший ненужным pending preload. Decoded LRU не очищать целиком.
**Читать**  
`web/app.js` snapshot/event handling и `scheduleImages/pending/wanted`; caches этапов 16 и 19.
**Менять**  
`web/app.js`, cache-owning файл этапа 19 — уточнить после этапа 19, browser tests.
**Использовать**  
Geometry revision keys, AbortController, `LimitedMap`/WeakMap и текущий bounded image LRU.
**Не трогать**  
IndexedDB contents, global cache purge, server geometry и LOD policy.
**Проверка**  
Частые bounds edits: stale classification исчезает, ненужный preload отменяется, массового re-decode нет.
### 22. Геометрические лимиты
**Цель**  
После появления реальных операций закрепить coordinate range, input/result vertices, component count и command bytes. Проверять лимиты до публикации и по возможности до дорогой аллокации.
**Читать**  
`geometry.go`, `polygon_clip.go`, `walkable.go`, geometry command decoding; benchmark/profile результаты предыдущих этапов.
**Менять**  
Geometry/walkable modules и tests — точные файлы уточнить после этапа 7.
**Использовать**  
Normalization counters, adapter integer range, HTTP/WS JSON size knowledge и atomic rollback.
**Не трогать**  
Общие server rate limits, user quotas, database/WAL и unrelated scene limits.
**Проверка**  
Worst realistic geometry и каждый limit+1: полный reject, неизменные revision/state, bounded time/RAM.
### 23. Ограниченная интеграционная проверка
**Цель**  
Проверить только собранную вертикаль: topology, movement, persistence, player clipping/hit test и large tiled map resource behavior.
**Читать**  
`scene_content_test.go`, `reliable_test.go`, `scenes_test.go`, `browser_scenarios_test.go`, `browser_test.go`; profiling helpers только если нужны измерения.
**Менять**  
Только соответствующие test files; production — лишь для найденных регрессий.
**Использовать**  
Существующие real HTTP/WS fixtures, Chrome/Edge test, diagnostics requests/decode/RAM и restart helpers.
**Не трогать**  
Новые игровые функции, общую benchmark infrastructure и отдельный отчёт, если регрессии не требуют их.
**Проверка**  
Add, hole, split, move+merge, concave bounds, move token, restart, clip/hit test, camera inside/outside/back; полный suite только если менялась общая инфраструктура.
