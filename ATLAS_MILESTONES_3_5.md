# Atlas: уточнённое ТЗ для Milestone 3–5

## Как использовать документ

Это самостоятельное ТЗ для оставшейся raster/representation/bake части исходного плана. Оно заменяет исходные пункты 9–27 и уточняет их взаимодействие с Floors, opacity, permissions и persistence. Оно не является командой реализовать все milestones одновременно: выполнять только milestone, выбранный пользователем.

Требования исходных Milestone 1–2 сохраняются. Не считать их полностью выполненными по названию milestone или по handoff: проверить связанные исходники и существующие тесты. В исходном документе пункт 8 встречается дважды; актуальна расширенная версия с endpoint A/B, круглыми зонами, направлением и визуальным созданием, а не прежний source-region через prompts.

Репозиторий и AGENTS.md обязательны. Не переделывать работающие подсистемы. SCENES_HANDOFF.md может отставать от кода; актуализировать только связанные с выполненной работой сведения.

## 1. Сохраняемые инварианты

- Campaign владеет Assets и Scenes. Клиент находится на Campaign Home либо подписан максимум на одну Scene.
- У Scene максимум два Floor; третий запрещён UI и сервером. Формат не должен исключать расширение в будущем.
- У каждого Floor ровно один Token Layer, один Walkable Layer и обычные visual Layers.
- Token Layer участвует в порядке слоёв своего Floor. Финальный общий проход всех Tokens поверх Floors запрещён.
- Token остаётся отдельной сущностью; нельзя помещать его в visual Layer, а SceneElement — в Token Layer. Token/Walkable Layer нельзя удалить или запечь.
- WalkableBounds — прямоугольное ограничение anchor токена, проверяемое сервером для Player. GM может редактировать снаружи с явной индикацией.
- Scene.Bounds — логическая область, не размер Canvas. Изменение bounds не уничтожает содержимое. Полностью внешнее содержимое недоступно Player, включая выдачу его бинарных ресурсов, если нет другого разрешённого использования этого Asset.
- Player currentFloor определяется authoritative scene-view/activeToken. Выбор собственного нескрытого токена проверяется сервером. Смена activeToken не перемещает сам Token между Floors.
- Floor 1 снизу: alpha Floor 1 = его opacity; alpha Floor 2 = его opacity × opacityWhenViewedFromBelow. Сверху: alpha каждого Floor = его opacity.
- Для каждого visual element alpha = effectiveFloorAlpha × Layer.opacity × Element.opacity. Tokens получают Floor alpha. Walkable editor overlay не получает Floor alpha.
- Это поэлементная alpha, а не Photoshop group opacity. В этой задаче семантику не менять.
- Floor с нулевым effective alpha не инициирует загрузку или декодирование визуальных ресурсов. Служебная навигация по собственным токенам не должна требовать их изображений.
- Player не получает editor tree, Asset list, BakeRecipe, resize/rotation handles и обычный SceneElement selection.
- Неоткрытая Scene не инициирует загрузку image bytes. Floor-aware delivery остаётся региональной, без full-floor eager loading.
- Scene events и ACK из прежней Scene не меняют новую. Reliable receipts, reconnect и независимые revisions сохраняются.
- Движения/transform остаются realtime RAM + coalesced persistence; структурные изменения подтверждаются по существующим durable-правилам. Обычный disconnect не теряет RAM-state сервера.

## 2. Milestone 3: единая representation policy и lazy loading

### Модель

Карта, мебель и декорация — SceneElement со ссылкой на raster Asset. Не вводить singleton Scene.Map или новый фундаментальный тип Map.

Централизовать representationPolicy(metadata): маленькие raster → bitmap, крупные → tiled LOD. Для visual SceneElements renderer выбирается по representation metadata, а не по semantic kind map/object. Существующие специализированные портреты токенов не переписывать без необходимости.

Основные признаки: width, height, pixelCount, estimatedDecodedBytes = width × height × 4. Compressed file size не является основным критерием. Не добавлять intermediate resolution variants без измеренной необходимости.

Выбор representation производится при подготовке. Масштаб камеры выбирает существующий LOD и размер клиентского decode, но не запускает серверный preprocessing.

### Идентичность и кэши

Исходный Asset и подготовленное representation различимы логически. Ресурс по immutable URL никогда не меняется. Изменение processing algorithm, разрешения или кодирования создаёт новый representation ID/ключ. Не обязательно вводить новую сущность в БД: отдельный ключ или версия URL достаточны.

Один Asset, используемый многими SceneElements, переиспользует подготовленные bytes и совместимые декодированные изображения. Floor, Layer и положение экземпляра не должны без необходимости дублировать cache entries. Authorization проверяется независимо от наличия ресурса в кэше.

### Загрузка

Порядок: visible Floors → relevant spatial region → elements/tokens → разрешённые asset references → binary resources/tiles.

Клиентский loaded region шире viewport. Наличие элемента в loaded state само по себе не означает немедленный fetch его bitmap: сначала viewport culling либо явно ограниченный prefetch.

RAM decoded cache, compressed IndexedDB cache, in-flight downloads/decode и очереди записи имеют отдельные ограничения. Не увеличивать лимиты ради прохождения тестов. Не очищать весь кэш при каждом переключении Scene. Не запускать повторный decode только потому, что элемент снова прошёл render traversal.

### Подбор threshold

Сравнить 512², 1024², 2048², 4096², существующую большую target-карту и хотя бы одно длинное узкое изображение. Измерить preparation time, decode time/count, decoded bytes, HTTP requests/bytes, pan/zoom, холодное и повторное открытие.

Порог хранить в одном месте. Если полноценный benchmark слишком дорог, выбрать консервативный порог с явным обоснованием и указать, что это tuning parameter, а не доказанный оптимум.

Acceptance: оба representations работают через общий SceneElement path; hidden/unopened/offscreen вне prefetch не вызывают fetch; повторное использование не дублирует работу; immutable URLs не переиспользуются для новых bytes; существующие permission/reliability проверки проходят.

## 3. Milestone 4: фиксация ротации одного элемента

### UI

Backend поддерживает rotation для любого SceneElement. Для самого нижнего visual Layer текущего Floor rotation handle по умолчанию скрыт. «Разрешить вращение» включает его для выбранного элемента в текущей editing session. Имя «Основа» не имеет backend-семантики. Существующая rotation сохраняется при перестановке слоёв.

«Зафиксировать ротацию» — отдельная явная GM-команда для статичного raster. Не запускать после pointermove. Это не команда «Запечь слой».

### Геометрия

Создать derived raster, визуально эквивалентный исходному элементу; после успешного commit rotation элемента = 0. Сохранить мировое положение, footprint, собственную opacity и порядок.

Явно определить координатное преобразование, pivot, pixel density и world offset результата. Учесть отрицательные координаты, trim и неравномерный scale. Простое rotate исходника → stretch результата не всегда эквивалентно stretch → rotate.

Не запекать runtime scale без необходимости; если для точной эквивалентности при anisotropic scale требуется resampling, выполнить его по явной ограниченной pixel-density policy. Не привязывать выходное разрешение к текущему zoom/DPR браузера.

Не запекать Floor/Layer opacity. Повторные операции по возможности вычислять по исходным bytes и совокупному transform, избегая цепочки повторного resampling. Хранить достаточное provenance: source references и processing recipe.

### Общий preprocessing

Переиспользовать существующий libvips pipeline. Общая низкоуровневая цепочка: source transforms/composition → rasterization → output bounds/offset → derived resource → representationPolicy → optional tiled LOD → sparse handling.

Fix Rotation и Bake Layer остаются разными бизнес-командами. Не писать отдельный image processor для каждого действия.

### Ограничения ресурсов

До запуска рассчитать output dimensions, pixel count и оценку временного диска. Явно задать общие бюджеты, timeout и максимальное число одновременных jobs. Использовать один ограниченный scheduler; не создавать неограниченную очередь процессов.

Запрещены полный decoded raster в Go и Canvas размером Scene/Floor. Libvips cache limit не считать жёстким пределом RSS. Не обещать hard RAM cap без механизма его обеспечения; измерять worker RSS на граничном сценарии.

### Жизненный цикл задачи

1. Под блокировкой проверить права и снять неизменяемый recipe входных данных.
2. Обработать вне глобального mutex во временном месте.
3. Перед commit проверить, что target существует, права актуальны и relevant recipe hash не изменился.
4. Опубликовать полностью готовые immutable ресурсы; durable-сохранить новую ссылку и состояние, затем сообщить успех.

Общая sceneRevision сама по себе не должна инвалидировать job из-за движения постороннего токена. Повтор reliable-команды не запускает дублирующий job. Явно различать ACK принятия задачи и durable завершение, если используются оба; существующий command protocol не оставлять в неопределённом состоянии после restart.

При ошибке/устаревшем результате прежняя сцена остаётся рабочей. Частичный результат не выдаётся клиентам. Неиспользованный готовый результат reclaimable, временные файлы очищаются. Disconnect клиента не должен сам по себе повреждать job; остановка сервера должна корректно остановить или завершить его. Перезапуск не обязан возобновлять вычисление, но должен однозначно обрабатывать retry.

### Sparse tiles

Полностью прозрачные tiles не хранить и не запрашивать. Manifest отличает known-empty tile от missing/corrupt tile. Не маскировать произвольный 404 как прозрачность.

Manifest компактный; при необходимости разделён по уровням/областям. Не передавать полный огромный перечень тайлов с каждым snapshot. Пустоту определять для каждого результирующего LOD; полупрозрачные края не удалять по произвольному threshold.

При chunked rasterization учитывать filter halo/overlap, затем обрезать его, чтобы не создавать швы. Альтернатива — подтверждённо корректная фильтрация самим общим pipeline до нарезки.

Acceptance: сравнение до/после с заданным допуском resampling для 0°, 90° и произвольного угла; negative position, anisotropic scale, прозрачные края и tile seams. Пустые tiles не создают fetch/retry. Concurrent edit/delete, duplicate command и processing failure не повреждают сцену.

## 4. Milestone 5: bake visual Layer

### Recipe и runtime

Запекать только visual Layer. Lock запрещает редактирование, bake оптимизирует представление; это разные действия.

BakeRecipe хранится на сервере и содержит версию, стабильные ID элементов, source asset references, transforms, opacity, visibility и детерминированный render order. Сохранить также существующие свойства, необходимые для восстановления редактора, а не только данные для rasterization. Recipe — источник восстановления исходных элементов, не копия активного runtime content.

В baked runtime исходные элементы не присутствуют в active spatial index, player snapshots и asset discovery. Player получает только результирующие representations. Невидимые source elements сохраняются для unbake, но не попадают в baked pixels.

### Порядок static/dynamic

Статичные элементы можно объединять только в непрерывные участки render order. При static A → dynamic B → static C результат должен оставаться baked A → dynamic B → baked C. Нельзя собрать A+C в один raster поверх/под B.

Пока анимаций нет, не создавать animation subsystem. Достаточно не закреплять обязательное ограничение «один baked asset на весь Layer» и явно проверять eligibility содержимого. Минимальная реализация вправе отклонять неподдерживаемую смешанную композицию; это ограничение указать в UI и handoff.

### Прозрачность: обязательное ограничение первой версии

Сохранить текущую поэлементную alpha-семантику. Не вводить group opacity и дополнительные Floor buffers в рамках bake.

Несколько перекрывающихся элементов с общей alpha нельзя в общем случае заменить одним raster с той же общей alpha: compositing не эквивалентен.

Для первой реализации использовать консервативное условие bake visual Layer:

- Layer.opacity = 1 и Floor.opacity = 1;
- для верхнего Floor opacityWhenViewedFromBelow допускается только 0 или 1;
- индивидуальная Element.opacity может быть любой: она учитывается внутри raster composition.

При невыполнении условия bake отклоняется с понятной причиной. Это намеренное ограничение, а не изменение opacity-настроек пользователя.

Если последующая команда opacity или перестановки Floors делает уже baked Layer несовместимым с условием, сервер в той же durable-операции выполняет unbake затронутого Layer. UI предупреждает о таком результате. Нельзя оставлять визуально неверный baked результат, молча менять opacity или автоматически запускать rebake.

Проверить переходы 1 → 0.5, directional alpha 1 → 0.5 и изменение порядка Floors. Возможное снятие ограничения через group compositing — отдельное продуктовое решение, меняющее исходную визуальную семантику.

### Размер и плотность результата

Выходная область — bounds участвующей композиции, не всей Scene. Pixel density выбирается детерминированной общей policy, учитывающей source resolution и world transforms, не текущей камерой.

До rasterization проверить бюджеты output dimensions/pixels/temp disk. Два маленьких элемента далеко друг от друга не должны запускать огромную обработку без проверки. Sparse tiles не являются доказательством ограниченного RAM/I/O во время построения.

Первая версия вправе понятно отклонять чрезмерно разреженную/большую композицию. Не добавлять виртуальную мозаику и новую систему хранения ради прохождения такого кейса.

### Edit/unbake

Для первой версии выбрать глобальный unbake: команда GM «Редактировать слой» durable-восстанавливает исходные элементы для runtime всей сцены. Не создавать отдельную ветку GM draft и отдельный published bake для Player.

После unbake и GM, и Player получают только разрешённую региональную часть исходных элементов; их изображения по-прежнему загружаются лениво. Полный recipe не передаётся Player. Изменённая композиция не запускает rebake автоматически; GM выбирает «Запечь слой» заново.

Внутренние исходные ID сохраняются. Не дублировать исходные элементы вместе с baked representation в одном runtime. Unbake возвращает также скрытые элементы для GM.

### Hash, reuse и commit

Канонический recipe hash включает все влияющие на pixels данные: source content/representation identity, transforms, opacity, visibility, render order с tie-breaker, pixel density, processing version и параметры кодирования. Не использовать случайный порядок Go map при вычислении hash. Не включать не влияющие на pixels координаты камеры, названия и посторонние scene revisions.

Различать revision редактируемого recipe и ключ вычисления pixels: переименование не должно заставлять повторно rasterize неизменную композицию. Одинаковые jobs могут переиспользовать готовый compatible derived resource; авторизация и business commit всё равно выполняются отдельно.

Использовать lifecycle и race-check Milestone 4. Не связывать успешный preprocessing с немедленным уничтожением source assets.

### References и authorization

Оригиналы, derived assets, recipe и pyramids хранятся на серверном диске. Не удерживать исходные binaries в server RAM.

Recipe и provenance являются reference sources для retention/GC. Source Asset не orphan, пока нужен recipe, восстановлению, другой Scene или активной обработке. После delete/unbake/rebake старые derived assets могут стать orphan по общему lifecycle.

Retention reference не даёт права скачивания Player. Авторизация исходит из текущего разрешённого runtime view. Recipe source, используемый также в другом видимом объекте, доступен по этому отдельному использованию.

Не вводить physical GC в этом milestone, если безопасного общего механизма ещё нет. Не удалять файлы немедленно. Уже скачанные bytes невозможно отозвать из клиентского кэша задним числом.

Acceptance: bake/unbake сохраняют порядок, ID и разрешённый внешний вид; повторный bake переиспользует compatible результат; изменение во время job не затирается; hidden recipe не раскрывается Player; references предотвращают преждевременный orphan; перестановка/opacity не ломают картинку; крупная разреженная композиция завершается контролируемым отказом.

## 5. Проверки и границы каждой итерации

Перед изменениями зафиксировать текущий baseline и релевантные измерения. Не повторять уже соответствующие требованиям реализации. Одна итерация — один выбранный milestone с работающим вертикальным срезом.

Проверки: gofmt для изменённых Go-файлов, релевантные tests, go test ./..., go vet ./..., браузерный сценарий для изменённого UI/render/cache. Для pixels использовать fixture-сравнения с явным допуском, не только проверки JSON. Для производительности записать реальные параметры среды и до/после; не заявлять 60 FPS без измерения.

Обязательные граничные кейсы: нулевой effective Floor alpha; перекрытие с прозрачностью; разные роли/Scenes; reconnect/retry; concurrent edit/delete при job; разреженный oversized bake; горячий кэш и многократный pan/zoom без постоянного роста памяти.

Не добавлять Floor 3, FOV, light, collision, combat, полноценные animations, Asset Library, event sourcing, новую БД и автоматическое rebake. Не увеличивать существующие runtime cache limits ради тестов.

После milestone обновить handoff: выполненное, ограничения, invariants, зависимости и проверенные сценарии. Diff формировать по актуальным AGENTS.md; snapshot относительно /dev/null не называть обратимым diff итерации.
