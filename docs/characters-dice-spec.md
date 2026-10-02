# Atlas VTT: персонажи, пресеты, actions, владение токенами, Player View и кубики

## 1. Цель

Нужно добавить в существующий Atlas VTT первую полноценную итерацию системы персонажей и бросков кубиков.

В рамках этой итерации должны появиться:

- определения характеристик персонажей (`StatDefinition`);
- определения действий (`ActionDefinition`);
- пресеты персонажей/существ (`CharacterPresetDefinition`);
- конкретные экземпляры персонажей (`CharacterInstance`);
- связь токена с экземпляром персонажа;
- редактируемый character sheet;
- несколько владельцев одного токена;
- отдельный Player View для GM;
- ручные броски кубиков через `dice-box-threejs`;
- сервер-авторитетный результат броска;
- журнал бросков;
- локальная настройка показа анимаций;
- lifecycle и GC временных экземпляров персонажей.

Основная цель первой итерации — получить рабочую, понятную и расширяемую модель без попытки сразу реализовать полноценный rules engine D&D/Pathfinder.

Полноценный DSL правил, вычисляемые характеристики, pipeline урона, сложные условия вроде `damage > target.armor`, автоматическое применение урона, эффекты, бафы и подобные механики в эту задачу не входят.

Архитектура при этом не должна закрывать возможность добавить их позже.

---

# 2. Основные архитектурные принципы

## 2.1. Definitions и instances должны быть разделены

Использовать следующую модель:

```text
definitions
    StatDefinition
    ActionDefinition
    CharacterPresetDefinition

instances
    CharacterInstance

scene objects
    Token
```

`Definition` описывает переиспользуемую сущность.

`CharacterInstance` содержит состояние конкретного персонажа.

`Token` является объектом сцены и может ссылаться на `CharacterInstance`.

Токен не должен сам становиться хранилищем статблока персонажа.

---

## 2.2. Один экземпляр персонажа может иметь несколько токенов

Допускается:

```text
CharacterInstance "Sir Lancelot"
        ↑
        ├── token on scene A
        └── token on scene B
```

Все такие токены представляют одного персонажа.

Изменение характеристик через любой из токенов должно менять один `CharacterInstance`.

При создании нового токена из пресета всегда создаётся новый `CharacterInstance`.

Например, создание десяти волков из пресета `wolf` должно породить:

```text
wolf preset

    ↓

wolf instance #1
wolf instance #2
wolf instance #3
...
wolf instance #10
```

Их HP и остальные изменяемые данные независимы.

---

# 3. Реестры definitions

Definitions могут происходить из двух источников:

```text
Ruleset registry
Campaign registry
```

Ruleset registry содержит определения, поставляемые установленным ruleset.

Например:

```text
strength
dexterity
hp
fireball
goblin
wolf
```

Campaign registry содержит определения, созданные пользователем внутри конкретной кампании:

```text
хуй
homosexuality
eno_fireball
red_wolf
```

Для остальной программы они должны выглядеть как единый namespace.

Например поиск:

```text
stat "strength"
```

может найти определение из ruleset, а:

```text
stat "хуй"
```

из campaign registry.

При этом campaign-level изменение не должно физически изменять исходный установленный ruleset.

---

# 4. StatDefinition

Минимальная структура:

```text
StatDefinition
    id
    name
    type
```

Допустимые типы первой итерации:

```text
number
integer
boolean
string
```

Опционально может существовать:

```text
default
```

Однако `default` не означает, что эта характеристика автоматически присутствует у каждого персонажа.

Наличие:

```text
StatDefinition "strength"
```

означает только:

> движок знает, что означает ключ `strength` и какого он типа.

Персонаж может вообще не иметь значения `strength`.

---

# 5. Автоматическое создание StatDefinition

Нужно поддержать следующий UX.

Есть персонаж:

```toml
[characters.eno.stats]
hp = 12
"хуй" = 19
homosexuality = true
```

`hp` уже известен ruleset.

`хуй` и `homosexuality` неизвестны.

При сохранении движок должен создать в Campaign registry:

```toml
[stats."хуй"]
type = "integer"

[stats.homosexuality]
type = "boolean"
```

После этого эти определения доступны всей кампании.

Если второй персонаж получает:

```toml
"хуй" = 7
```

используется существующий `StatDefinition`.

Не создавать второе одноимённое определение.

### Конфликт типов

Если уже существует:

```toml
[stats."хуй"]
type = "integer"
```

то:

```toml
"хуй" = true
```

должен приводить к ошибке валидации.

Молча изменять тип существующего определения нельзя.

### Опечатки

Автоматическое создание неизвестных характеристик сохраняется, поскольку это желаемое поведение.

Однако UI желательно показать, что пользователь только что создал новое глобальное campaign definition.

Например:

```text
Создана характеристика "strenght"
```

Если несложно, при похожем существующем имени можно показать предупреждение:

```text
Возможно, имелось в виду "strength"
```

Это предупреждение не должно блокировать создание.

---

# 6. ActionDefinition

Action является самостоятельной переиспользуемой definition.

Пример:

```toml
[actions.sword_attack]
name = "Удар мечом"
description = "Обычная атака мечом."
tags = ["attack", "melee"]
```

В первой итерации Action может дополнительно содержать один или несколько простых RollSpec:

```toml
[[actions.sword_attack.rolls]]
id = "attack"
name = "Попадание"
count = 1
sides = 20
modifier_stat = "strength_mod"

[[actions.sword_attack.rolls]]
id = "damage"
name = "Урон"
count = 1
sides = 10
modifier_stat = "strength_mod"
```

Допустимые поля RollSpec:

```text
id
name
count
sides
modifier_stat
modifier_fixed
```

`modifier_stat` опционален.

`modifier_fixed` опционален.

Не надо в этой задаче строить универсальный expression language.

То есть пока не требуется:

```text
1d10 + floor((strength - 10) / 2)
```

или:

```text
if damage > target.armor ...
```

В дальнейшем `RollSpec` можно будет расширить выражением или заменить rules engine без изменения базовой модели ActionDefinition.

---

# 7. Кастомизация actions

Action definitions глобальны в рамках объединённого registry.

Не вводить локальные ActionDefinition внутри персонажей.

Если пользователь хочет кастомизировать `fireball` только для одного персонажа, нормальный сценарий:

```text
fireball
    ↓ Duplicate
eno_fireball
```

`eno_fireball` создаётся в Campaign registry как независимая ActionDefinition.

После этого она назначается персонажу.

Изменение `eno_fireball` не должно изменять исходный `fireball`.

Не реализовывать наследование:

```text
eno_fireball extends fireball
```

в этой итерации.

---

# 8. CharacterPresetDefinition

Персонаж, NPC и монстр на этом уровне используют одну сущность.

```text
CharacterPresetDefinition
```

У неё можно иметь информационное поле:

```text
kind = "character" | "npc" | "monster"
```

но механически все три варианта одинаковы.

Минимальная структура:

```text
CharacterPresetDefinition
    id
    name
    kind
    avatar
    stats
    action_ids
```

Пример:

```toml
[presets.goblin]
name = "Гоблин"
kind = "monster"
actions = ["scimitar"]

[presets.goblin.stats]
hp = 7
strength = 8
dexterity = 14
armor = 15
```

Или:

```toml
[presets.wolf]
name = "Волк"
kind = "monster"
actions = ["bite"]

[presets.wolf.stats]
hp = 11
strength = 12
dexterity = 15
```

---

# 9. CharacterInstance

CharacterInstance представляет конкретного персонажа.

Минимально:

```text
CharacterInstance
    id
    preset_id?
    name
    avatar_asset_id?
    stat_overrides
    added_action_ids
    removed_action_ids
    persistent
```

`preset_id` может отсутствовать.

Это позволяет создать полностью кастомного персонажа без пресета.

---

# 10. Наследование данных от пресета

Instance с preset должен вычислять effective state следующим образом:

```text
preset values
    +
instance overrides
    =
effective character
```

Например:

```text
wolf preset:
    hp = 11
    strength = 12
    dexterity = 15

instance "Рыжий волк":
    hp override = 4
    custom stat "fur_color" = "red"
```

Effective:

```text
hp = 4
strength = 12
dexterity = 15
fur_color = "red"
```

Модифицированный волк остаётся instance пресета `wolf`.

Он не превращается автоматически в новый preset.

---

# 11. Поведение при изменении пресета

Если значение у instance не переопределено, изменение preset должно отражаться на instance.

Например:

```text
wolf preset:
armor = 10
```

Создано пятьдесят волков.

Позже GM исправляет preset:

```text
armor = 11
```

Все волки, у которых нет собственного override `armor`, получают effective `armor = 11`.

Если конкретному волку было задано:

```text
armor = 15
```

он продолжает иметь 15.

Для поля с override желательно предусмотреть в UI действие:

```text
Сбросить к значению пресета
```

которое просто удаляет override.

---

# 12. Создание CharacterInstance

Новый обычный Token изначально может существовать без персонажа:

```text
token.character_instance_id = null
```

Сам факт создания токена не должен создавать CharacterInstance.

CharacterInstance создаётся при первом осмысленном действии с персонажем:

1. GM назначает токену preset;
2. GM назначает токену ранее сохранённого персонажа;
3. GM выбирает «Создать пустого персонажа»;
4. GM начинает создавать для токена кастомный character sheet.

---

# 13. Назначение preset токену

При выборе:

```text
Назначить персонажа
    → Из пресета
    → Волк
```

создать:

```text
new CharacterInstance
    preset_id = "wolf"
    persistent = false
```

и связать его с токеном.

Повторное использование preset создаёт новый instance.

Десять токенов с `wolf` не должны ссылаться на один CharacterInstance.

---

# 14. Назначение существующего персонажа

GM должен иметь возможность выбрать:

```text
Назначить персонажа
    → Сохранённые персонажи кампании
    → Сэр Ланцелот
```

В этом случае новый CharacterInstance не создаётся.

Токен получает ссылку на существующий CharacterInstance Сэра Ланцелота.

Это нужно, чтобы персонаж мог:

- покинуть одну сцену;
- некоторое время вообще не иметь токена;
- позже снова появиться на этой или другой сцене;
- сохранить HP, кастомные статы, avatar и actions.

---

# 15. Persistent CharacterInstance

У CharacterInstance должно быть:

```text
persistent = true | false
```

В UI назвать это:

```text
Сохранить в кампании
```

Это лучше, чем «Сохранить как персонажа», поскольку instance уже является персонажем.

Поведение:

```text
persistent = false
```

CharacterInstance считается временным.

```text
persistent = true
```

CharacterInstance входит в постоянный roster кампании и существует даже при отсутствии токенов.

Изменение этого флага доступно GM.

---

# 16. GC CharacterInstance

GC нужен только для CharacterInstance.

Не удалять автоматически:

- StatDefinition;
- ActionDefinition;
- CharacterPresetDefinition.

Они являются частью registry и могут понадобиться позже.

### Условие удаления CharacterInstance

После удаления ссылки Token → CharacterInstance проверить:

```text
persistent == false
AND
количество токенов во всех сценах кампании, ссылающихся на instance == 0
```

Если оба условия выполняются, CharacterInstance удалить.

Проверять ссылки по всей кампании, а не только по текущей сцене.

### Пример

Созданы:

```text
Wolf #1
Wolf #2
Red Wolf
Sir Lancelot
Gordey
```

`Wolf #1`, `Wolf #2`, `Red Wolf`:

```text
persistent = false
```

`Sir Lancelot`:

```text
persistent = true
```

`Gordey`:

```text
persistent = true
```

Во время боя волки потеряли HP, а Red Wolf получил дополнительные custom stats.

После удаления их последних токенов:

```text
Wolf #1       DELETE
Wolf #2       DELETE
Red Wolf      DELETE
```

Наличие повреждений или overrides не должно спасать transient instance от GC.

Если удалён последний токен Sir Lancelot:

```text
Sir Lancelot  KEEP
```

GM позже может снова назначить его новому токену.

### Реализация GC

GC не должен работать каждый frame или через периодическое полное сканирование кампании.

Запускать проверку после операций, способных удалить последнюю ссылку:

- удаление token;
- unlink CharacterInstance от token;
- удаление scene;
- массовое удаление tokens.

Если существующий persistence слой позволяет дешёво проверить наличие ссылок по `character_instance_id`, использовать его.

Допустима дополнительная reconciliation-проверка при загрузке кампании, но она не должна попадать в hot path.

---

# 17. Token ownership

Сейчас владение одним token нужно расширить до many-to-many.

Вместо концепции:

```text
owner_user_id
```

должно поддерживаться:

```text
owner_user_ids = [...]
```

или эквивалентная структура существующей архитектуры.

Один token может принадлежать одновременно нескольким игрокам.

Например:

```text
token Wolf Companion
owners:
    user_A
    user_B
```

Оба пользователя имеют право управлять этим token.

GM имеет доступ ко всем token независимо от owner list.

Если сейчас хранится один owner, нужна совместимая миграция:

```text
old owner A
    ↓
owners = [A]
```

---

# 18. Разрешения на CharacterInstance

Игрок, являющийся владельцем Token, может:

- выбирать этот token;
- перемещать его в пределах обычных player movement restrictions;
- открывать его character sheet;
- изменять значения stats CharacterInstance;
- менять avatar CharacterInstance;
- запускать actions/rolls этого персонажа.

Игрок не может:

- назначать token другой CharacterInstance;
- менять preset;
- менять `persistent`;
- редактировать глобальные ActionDefinition;
- редактировать CharacterPresetDefinition;
- менять ownership token.

Эти операции принадлежат GM.

Если CharacterInstance представлен несколькими token с разными владельцами, пользователь считается имеющим право редактировать instance, если он владеет хотя бы одним связанным token, к которому у него есть доступ.

Все permission checks должны выполняться на сервере.

Нельзя полагаться только на скрытие кнопок в UI.

---

# 19. GM mode и Player View

У GM должен появиться переключатель:

```text
GM
Player View
```

Разместить его в существующем GM интерфейсе там, где он не конфликтует с основными инструментами.

Не перестраивать ради этого весь layout.

## Player View для GM

При включении Player View GM должен получить интерфейс и ограничения взаимодействия максимально близкие к реальному player client:

- применяются player rendering restrictions;
- работают `renderBounds`;
- токен нельзя перемещать за пределы `walkable`;
- фоновые SceneElement/assets нельзя выбирать кликом;
- недоступны build/edit инструменты;
- canvas interaction соответствует player mode;
- правая и левая панели выглядят как у игрока.

GM при этом остаётся GM на сервере.

Это UI/interaction preview, а не смена серверной роли пользователя.

Переключение обратно в GM должно быть доступно всегда.

---

# 20. Токены GM в Player View

Обычный player в левой панели видит список token, которыми он владеет.

GM в Player View считается имеющим право управления всеми token текущей сцены.

Следовательно в player-style списке token у GM можно показывать все token сцены как доступные ему для выбора.

Это позволяет GM переключаться между ними и проверять player UX.

Canvas при этом продолжает использовать player rendering restrictions.

---

# 21. Левая боковая панель: вкладка «Токены»

Когда открыта вкладка:

```text
Токены
```

перенести туда существующие настройки token, которые сейчас находятся в правой панели:

- изображение token;
- прозрачность;
- существующие настройки перемещения;
- ownership.

Не дублировать state.

Нужно именно переместить существующие UI controls и сохранить существующую модель данных, если изменение модели не требуется данной задачей.

Ownership должен позволять выбрать несколько игроков.

---

# 22. Выбор активного token игроком

В player mode в левой панели показывать token пользователя.

При выборе token он становится active token.

Active token используется для:

- character sheet справа;
- action buttons;
- ручных бросков, если бросок ассоциируется с персонажем;
- движения токена.

Если доступен только один token, он может быть выбран автоматически.

Если token не связан с CharacterInstance, правая панель не должна создавать instance автоматически только из-за выбора.

---

# 23. Правая боковая панель

Для выбранного token, связанного с CharacterInstance, правая панель содержит:

```text
Character Sheet

Actions

Dice

Roll Journal
```

Порядок можно адаптировать к текущему UI, но зона Dice должна находиться в нижней части панели.

---

# 24. Character Sheet

Character Sheet должен отображать effective character:

```text
preset
+
instance overrides
```

Пользователь с правом редактирования может менять stat values.

При изменении унаследованного значения создаётся instance override.

При удалении override снова используется preset value.

Character Sheet должен позволять добавлять новую характеристику.

Например пользователь вводит:

```text
хуй = 19
```

Если definition отсутствует, создаётся Campaign StatDefinition согласно правилам выше.

---

# 25. Avatar персонажа

CharacterInstance должен иметь avatar, независимый от изображения token.

В character sheet должна быть возможность изменить avatar.

Не хранить огромный исходный файл как avatar.

Использовать существующий asset/image pipeline проекта, если он уже подходит.

Для avatar создать ограниченную производную версию:

```text
max width/height: 512 px
```

Исходное гигантское изображение после генерации avatar для этой цели хранить не требуется, если оно не было отдельно добавлено пользователем как обычный campaign asset.

Нужно также установить разумный предел входного файла и/или pixel count, чтобы загрузка случайного изображения огромного разрешения не приводила к чрезмерному расходу RAM.

Не добавлять новый параллельный image cache, если существующий asset pipeline можно переиспользовать.

Изменение avatar не обязано менять token image.

---

# 26. Actions в Character Sheet

Character sheet отображает effective actions персонажа.

Они складываются из:

```text
preset actions
+
instance added actions
-
instance removed actions
```

GM может добавить персонажу ActionDefinition из registry.

GM может убрать inherited action у конкретного instance через `removed_action_ids`.

Если нужно изменить сам ActionDefinition только для данного персонажа, UI должен использовать сценарий:

```text
Duplicate Action
→ создать campaign ActionDefinition
→ назначить копию персонажу
```

Не реализовывать field-level inheritance одного action от другого.

---

# 27. Dice UI

В нижней части правой панели добавить компактную ручную зону броска.

Примерно:

```text
[-] [  1  ] [+]    [d20]

           [Бросить]
```

Поле количества является обычным числовым input.

Кнопки:

```text
-
+
```

уменьшают/увеличивают количество.

Минимум:

```text
1
```

Поддерживаемые типы:

```text
d4
d6
d8
d10
d12
d20
```

Кнопка текущего dice типа работает так:

```text
ЛКМ:
d4 → d6 → d8 → d10 → d12 → d20 → d4

ПКМ:
d4 ← d6 ← d8 ← d10 ← d12 ← d20 ← d4
```

На ПКМ нужно подавить browser context menu.

---

# 28. dice-box-threejs

Использовать `dice-box-threejs` только как renderer/animation layer.

Он не должен быть источником authoritative random result.

Архитектура:

```text
Client
    RollRequest
        ↓
Server
    generates dice results
        ↓
RollEvent
        ↓ broadcast
Clients
        ↓
dice-box-threejs renders predetermined results
```

Библиотека должна получать заранее известные значения отдельных dice.

Например сервер определил:

```text
10d10
results:
1, 7, 3, 10, 4, 4, 8, 2, 9, 6
```

клиент должен анимировать именно эти результаты.

---

# 29. Server-authoritative rolls

Запрос клиента:

```text
RollRequest
    count
    sides
    optional character_instance_id
    optional action_id
    optional roll_spec_id
```

Не принимать от обычного клиента authoritative результаты dice.

Сервер генерирует каждый die result.

Для Go желательно вынести RNG за интерфейс, чтобы unit tests могли использовать deterministic implementation.

Production implementation может использовать `crypto/rand`, поскольку объём бросков мал и стоимость здесь практически несущественна.

---

# 30. RollEvent

После вычисления сервер создаёт событие примерно следующей структуры:

```text
RollEvent
    id
    user_id
    author_name
    character_instance_id?
    character_name?
    action_id?
    action_name?
    count
    sides
    results[]
    modifier
    total
    timestamp
```

Например:

```text
author: Gordey
character: Eno Morius
dice: 3d10
results: [7, 4, 10]
modifier: 2
total: 23
```

Событие рассылается подключённым участникам соответствующей сцены/кампании через существующий realtime transport.

Не создавать отдельное websocket-соединение только для dice, если уже имеется общий WS event bus.

---

# 31. Ограничения dice requests

Сервер обязан валидировать:

```text
count >= 1
sides ∈ {4, 6, 8, 10, 12, 20}
```

Ввести разумный hard limit количества dice на один RollRequest.

Например:

```text
MAX_DICE_PER_ROLL = 100
```

Значение вынести в одну константу.

Обычный `10d10` должен полностью поддерживаться.

---

# 32. Анимация большого количества dice

3D-анимация не должна позволять одному пользователю положить клиент остальных игроков.

Ввести отдельный client-side animation limit, например:

```text
MAX_ANIMATED_DICE = 30
```

Если authoritative RollEvent содержит больше dice:

- результаты всё равно показываются в журнале полностью;
- итог сохраняется;
- клиент может показать сокращённую визуализацию либо пропустить 3D-анимацию.

Не отбрасывать сам RollEvent.

---

# 33. Очередь анимаций

Не запускать несколько конфликтующих `dice-box-threejs.roll()` одновременно.

Сделать последовательную animation queue.

Если броски приходят быстрее, чем проигрывается анимация:

```text
RollEvent
    ↓
journal immediately
    ↓
animation queue
```

Журнал не должен ждать окончания animation.

Очередь должна иметь разумный предел.

Если очередь переполнена, можно пропустить старую/лишнюю animation, но нельзя терять RollEvent или запись в журнале.

---

# 34. Настройка «Анимации бросков»

У каждого пользователя должна быть локальная настройка:

```text
Анимации бросков:

Все
Только мои
Выключены
```

Она влияет только на текущий клиент.

### Все

Анимировать:

- свои броски;
- чужие броски.

### Только мои

Анимировать только RollEvent, где:

```text
event.user_id == current_user.id
```

### Выключены

Не запускать `dice-box-threejs`.

Все RollEvent по-прежнему отображаются в журнале.

Настройку желательно сохранять локально между сессиями пользователя.

Если roll animation сопровождается звуком, sound должен подчиняться той же политике, чтобы режим «Выключены» действительно не создавал dice spam.

---

# 35. Журнал бросков

Добавить скрываемый/раскрываемый Roll Journal.

Для каждого броска показывать минимум:

```text
автор
персонаж, если есть
что бросалось
результаты
модификатор
итог
```

Например:

```text
Gordey · Eno Morius
2d10 + 3
[7, 4] + 3 = 14
```

Или для ручного броска без персонажа:

```text
Vova
1d20
[17] = 17
```

Journal и animation являются независимыми системами.

Режим:

```text
Анимации: Выключены
```

не скрывает журнал.

---

# 36. Ограничение журнала

Не создавать бесконечно растущий массив RollEvent на клиенте или сервере.

Для первой итерации достаточно bounded history.

Например:

```text
последние 500 RollEvent
```

на кампанию/активную игровую сессию.

Постоянная многомесячная история бросков в эту задачу не входит.

При reconnect желательно получить последние события из bounded server history, если это нормально ложится на существующую архитектуру.

---

# 37. Action rolls

Если у ActionDefinition есть RollSpec, action button может использовать тот же серверный RollRequest.

Например:

```text
Sword Attack
    Попадание
    Урон
```

Кнопка `Попадание` создаёт RollRequest на основании RollSpec.

Если указан:

```text
modifier_stat = "strength_mod"
```

клиент не должен сам доверенно вычислять окончательный modifier.

Сервер должен разрешить CharacterInstance, получить значение нужного stat и сформировать authoritative modifier.

Если stat отсутствует, вернуть нормальную validation error.

Сложные derived stats в этой задаче не реализовывать.

---

# 38. TOML

Definitions должны иметь человекочитаемое TOML-представление.

Основные требования:

- файл должен нормально читаться человеком;
- файл должен легко генерироваться LLM;
- ссылки должны идти по стабильным ID, а не по filesystem path;
- не использовать обязательные `import` внутри каждого персонажа;
- не создавать глубокое наследование;
- структура должна быть детерминированной;
- один и тот же ID означает одну и ту же definition;
- source ruleset и campaign extensions должны оставаться различимы.

Пример:

```toml
schema_version = 1

[stats.strength]
name = "Сила"
type = "integer"

[stats.dexterity]
name = "Ловкость"
type = "integer"

[stats.hp]
name = "HP"
type = "integer"

[stats."хуй"]
name = "Хуй"
type = "integer"


[actions.sword_attack]
name = "Удар мечом"
description = "Обычная атака мечом."
tags = ["attack", "melee"]

[[actions.sword_attack.rolls]]
id = "attack"
name = "Попадание"
count = 1
sides = 20
modifier_stat = "strength_mod"

[[actions.sword_attack.rolls]]
id = "damage"
name = "Урон"
count = 1
sides = 10
modifier_stat = "strength_mod"


[presets.goblin]
name = "Гоблин"
kind = "monster"
actions = ["sword_attack"]

[presets.goblin.stats]
hp = 7
strength = 8
dexterity = 14


[presets.wolf]
name = "Волк"
kind = "monster"
actions = ["bite"]

[presets.wolf.stats]
hp = 11
strength = 12
dexterity = 15
```

Campaign-level extensions могут использовать ту же форму.

Не обязательно хранить runtime state именно одним большим TOML-файлом, если существующая persistence архитектура проекта для runtime instances устроена иначе.

Однако модель должна сериализоваться и восстанавливаться однозначно.

Не вводить второй параллельный persistence backend только ради TOML.

---

# 39. Unicode TOML keys

Учесть, что произвольные пользовательские названия могут требовать quoted TOML keys.

Например:

```toml
[stats."хуй"]
type = "integer"

[presets."рыжий_волк"]
...
```

Serializer должен корректно экранировать такие значения.

Не строить формат на предположении, что все пользовательские ID ASCII.

---

# 40. UI редактирования definitions

Нужен минимально достаточный редактор.

GM должен иметь возможность:

### Stats

- увидеть известные campaign stats;
- создать stat;
- переименовать display name;
- задать тип;
- удалить definition, только если это безопасно либо после проверки ссылок.

### Actions

- создать;
- редактировать name/description;
- задать RollSpec;
- duplicate;
- удалить после проверки references.

### Character presets

- создать;
- редактировать stats;
- назначить actions;
- изменить avatar;
- duplicate.

Не надо в этой задаче строить отдельный сложный ruleset IDE.

Редактирование может быть встроено в существующий UI кампании там, где это наименее разрушительно для текущей архитектуры.

---

# 41. Удаление definitions

Не удалять definition автоматически только потому, что сейчас её никто не использует.

При ручном удалении проверить references.

Например нельзя молча удалить:

```text
ActionDefinition sword_attack
```

если её использует preset `goblin`.

UI должен либо:

- запретить удаление и показать references;
- либо потребовать явного удаления/замены references.

---

# 42. Player rendering restrictions

Player View должен использовать уже существующие механизмы проекта.

Не дублировать отдельную реализацию:

```text
walkable
renderBounds
player visibility
movement validation
```

Нужно переиспользовать player interaction path.

GM Player View должен проходить по максимально тому же коду, что и реальный player.

Особенно важно, чтобы после появления Player View не возникли две расходящиеся реализации movement validation.

---

# 43. Background interaction

В Player View:

- SceneElement/background assets не должны получать selection;
- не показывать handles;
- не показывать build affordances;
- click по background должен вести себя как у обычного player.

Tokens при этом остаются интерактивными согласно permissions.

---

# 44. Server authority

Следующие вещи должны проверяться сервером:

- право пользователя двигать token;
- право изменять CharacterInstance;
- право менять ownership;
- право менять preset;
- право сохранять character permanently;
- dice parameters;
- dice results;
- stat type validation.

Player View на клиенте сам по себе не является security boundary.

---

# 45. Синхронизация

Изменения CharacterInstance должны синхронизироваться realtime существующим механизмом проекта.

Например два игрока владеют одним token.

Игрок A меняет:

```text
hp 10 → 7
```

Игрок B должен получить обновлённый character sheet без reload.

То же касается:

- avatar;
- actions;
- stat overrides;
- token ownership;
- linkage Token ↔ CharacterInstance.

Не добавлять polling, если проект уже использует WebSocket events.

---

# 46. Performance

Новая подсистема не должна попадать в render hot path без необходимости.

Нельзя:

- пересобирать весь registry каждый frame;
- сканировать все CharacterInstance каждый frame;
- выполнять GC каждый frame;
- декодировать avatar заново при каждом открытии sheet;
- держать исходные огромные avatar images в RAM;
- бесконечно накапливать RollEvent;
- бесконечно накапливать dice animations.

Effective Character state можно пересчитывать при изменении instance/preset или при открытии sheet; его стоимость мала относительно scene rendering.

Не вводить cache до появления реальной необходимости.

---

# 47. Миграция существующих token

Существующие token должны продолжить работать.

Для них:

```text
character_instance_id = null
```

до тех пор, пока GM не назначит персонажа.

Если сейчас существует единственный owner:

```text
owner_user_id = X
```

мигрировать логически в:

```text
owner_user_ids = [X]
```

Не требовать от пользователя вручную пересоздавать существующие токены.

---

# 48. Не входящее в эту задачу

Не реализовывать сейчас:

- универсальный expression DSL;
- полноценную D&D 5e rules implementation;
- derived stats engine;
- автоматический hit/miss;
- armor/save rules;
- автоматическое применение damage;
- resistances/immunities;
- buffs/debuffs;
- initiative;
- conditions;
- action inheritance;
- preset inheritance;
- script execution;
- циклы/функции пользовательского языка;
- произвольные пользовательские JS scripts;
- перманентную историю всех бросков за всю жизнь кампании.

Архитектура должна позволять развивать систему позже, но эти вещи не должны раздувать текущий патч.

---

# 49. Ключевые сценарии приёмки

## Сценарий A. Пять волков

GM создаёт пять token.

Каждому назначает preset `wolf`.

Должны появиться пять разных CharacterInstance:

```text
wolf #1
wolf #2
wolf #3
wolf #4
wolf #5
```

Изменение HP одного волка не меняет остальных.

---

## Сценарий B. Рыжий волк

GM создаёт ещё одного `wolf`.

Меняет:

```text
fur_color = "red"
hp = 20
```

`fur_color` раньше отсутствовал.

Campaign registry получает новый StatDefinition.

CharacterInstance остаётся:

```text
preset_id = wolf
```

и содержит overrides.

Новый CharacterPreset `red_wolf` автоматически не создаётся.

---

## Сценарий C. Изменение wolf preset

GM меняет у preset:

```text
dexterity 15 → 16
```

Все wolf instances без dexterity override получают effective `dexterity = 16`.

Instance с собственным dexterity override сохраняет своё значение.

---

## Сценарий D. GC волков

Все tokens этих волков удалены со всех сцен.

У instances:

```text
persistent = false
```

Все они удаляются, включая повреждённого и кастомизированного рыжего волка.

---

## Сценарий E. Sir Lancelot

GM создаёт NPC `Sir Lancelot`.

Включает:

```text
Сохранить в кампании
```

То есть:

```text
persistent = true
```

Ланцелот получает damage.

Его единственный token удаляется.

CharacterInstance остаётся.

На другой сцене GM создаёт token:

```text
Назначить персонажа
→ Сохранённые персонажи
→ Sir Lancelot
```

Появляется token того же instance с прежним состоянием.

---

## Сценарий F. Два владельца

Token принадлежит:

```text
Alice
Bob
```

Оба могут:

- двигать token;
- выбирать его;
- редактировать character stats;
- бросать dice от этого character.

Charlie не может.

GM может всегда.

---

## Сценарий G. Player View

GM находится в обычном GM mode.

Переключается на:

```text
Player View
```

После переключения:

- build assets не выбираются;
- GM видит player rendering restrictions;
- token нельзя увести за walkable;
- справа отображается player character sheet;
- слева показывается player-style token selector;
- GM может выбрать любой token сцены для проверки.

После возврата в GM mode редактор работает как раньше.

---

## Сценарий H. 10d10

Игрок выбирает:

```text
count = 10
dice = d10
```

Нажимает:

```text
Бросить
```

Client отправляет RollRequest.

Server генерирует, например:

```text
[1, 7, 3, 10, 4, 4, 8, 2, 9, 6]
```

Создаёт RollEvent.

Все клиенты получают именно эти значения.

`dice-box-threejs` визуализирует predetermined outcomes.

Журнал показывает автора и итог.

---

## Сценарий I. Отключение чужих анимаций

Alice:

```text
Анимации бросков = Только мои
```

Bob бросает 5d20.

Alice получает RollEvent и видит его в журнале.

3D animation у Alice не запускается.

Alice бросает 2d6.

Её собственная animation запускается.

---

## Сценарий J. Полностью выключенные animation

Пользователь выбирает:

```text
Анимации бросков = Выключены
```

Все RollEvent продолжают приходить.

Журнал работает.

`dice-box-threejs` и dice sound для этих событий не запускаются.

---

# 50. Проверки

После реализации выполнить минимально достаточные проверки.

Обязательно нужны unit/integration tests на:

- merge Ruleset registry + Campaign registry;
- auto-create неизвестного StatDefinition;
- конфликт типов stat;
- создание CharacterInstance из preset;
- instance overrides;
- reset override;
- multiple token owners;
- server permission check владельца;
- создание нескольких independent instances из одного preset;
- persistent character;
- GC transient character после удаления последней ссылки;
- отсутствие GC, пока существует token на другой scene;
- RollRequest validation;
- диапазон результата dice;
- корректный total;
- server-generated results;
- websocket/realtime broadcast RollEvent;
- migration single owner → owner list.

Для dice RNG предусмотреть deterministic test implementation, а production RNG не тестировать статистическим stress test.

Для Player View выполнить хотя бы реалистичный browser/e2e сценарий:

```text
GM mode
→ Player View
→ выбрать token
→ попытаться выйти за walkable
→ убедиться, что движение запрещено
→ убедиться, что background asset не выбирается
→ вернуться в GM mode
```

Не запускать тяжёлые browser-memory/stress/performance suites без обнаруженной зависимости этой доработки от них.

---

# 51. Требования к реализации в существующем проекте

Перед изменениями определить минимальный набор реально затрагиваемых файлов:

- token model;
- campaign persistence;
- websocket events;
- auth/permissions;
- left sidebar;
- right sidebar;
- player/GM interaction mode;
- asset/avatar handling;
- dice integration.

Не переписывать соседние подсистемы.

Не делать попутный рефакторинг.

Переиспользовать существующие:

- Token;
- scene state;
- WebSocket;
- movement validation;
- walkable;
- renderBounds;
- asset pipeline;
- persistence layer.

Если текущая архитектура уже имеет подходящую сущность вместо названной в ТЗ, адаптировать решение к существующей архитектуре, а не создавать её дубль только ради совпадения названий.

---

# 52. Итоговая концептуальная схема

```text
Ruleset registry
    │
    ├── StatDefinition
    ├── ActionDefinition
    └── CharacterPresetDefinition
            │
            │
Campaign registry
    │
    ├── custom StatDefinition
    ├── custom ActionDefinition
    └── custom CharacterPresetDefinition
            │
            │ merged namespace
            ▼
    CharacterInstance
        preset_id?
        stat overrides
        action additions/removals
        avatar
        persistent
            │
            │ 1:N
            ▼
          Token
        scene position
        token image
        opacity
        movement settings
        owners[]
```

Отдельно:

```text
Player
    ↓
RollRequest
    ↓
Server RNG
    ↓
RollEvent
    ├── journal
    └── dice-box-threejs predetermined animation
```

---

# 53. Главные инварианты

После реализации должны соблюдаться следующие правила:

```text
Definition != value.

Preset != instance.

Instance != token.

Новый token из одного preset создаёт новый instance.

Один существующий instance может иметь несколько token.

Instance overrides не создают новый preset.

Неизвестный stat создаёт campaign StatDefinition.

Campaign definition не мутирует установленный ruleset.

Action customization делается через отдельную ActionDefinition, а не скрытое inheritance.

Удаление последнего token удаляет только transient CharacterInstance.

persistent CharacterInstance живёт без token.

Один token может принадлежать нескольким users.

Server проверяет permissions.

Server определяет dice results.

dice-box-threejs только визуализирует predetermined results.

Roll journal не зависит от animation visibility.

Player View переиспользует реальные player restrictions.
```

Именно эти инварианты считать источником истины при выборе между несколькими возможными вариантами реализации.