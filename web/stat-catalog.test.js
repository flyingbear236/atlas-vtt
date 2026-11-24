import assert from 'node:assert/strict';
import test from 'node:test';
import {statDependencies, collectStatDependencies} from './stat-catalog.js';

test('A derived stat requests the owner of its referenced field', () => {
 const defs = {
  'body.strength': {v04: {default: 16, bonus: {formula:'floor((value - 10)/2)'}}},
  'athletics': {v04: {default: {formula:'stat.body.strength.bonus'}}},
  'body.strengthen': {v04: {default: 12}},
 };
 assert.deepEqual(statDependencies('athletics', defs), ['body.strength']);
 assert.deepEqual(collectStatDependencies('athletics',defs,[]).needed,['body.strength']);
 assert.deepEqual(collectStatDependencies('athletics',defs,['body.strength']).needed,[]);
});

test('Chain of dependencies is sorted before the requested stat and avoids duplicates', () => {
 const defs={a:{v04:{default:{formula:'stat.b + stat.c'}}},b:{v04:{default:{formula:'stat.c'}}},c:{v04:{default:2}}};
 assert.deepEqual(collectStatDependencies('a',defs,[]).needed,['c','b']);
});

test('Nested stat IDs prefer the longest defined path', () => {
 const defs={body:{v04:{default:1}},'body.strength':{v04:{default:16}},check:{v04:{default:{formula:'stat.body.strength.bonus'}}}};
 assert.deepEqual(statDependencies('check',defs),['body.strength']);
});

// Right-hand catalogue panel must reveal all derived stat values, including the
// `bonus` declared inside the owning stat, even before that stat is assigned.
test('Strength includes its derived bonus formula and group path', async () => {
 const {catalogDerivedFields,catalogFullPath,catalogParentPath}=await import('./stat-catalog.js');
 const def={id:'body.strength',name:'Сила',v04:{default:16,bonus:{formula:'floor((value - 10) / 2)'}}};
 assert.deepEqual(catalogDerivedFields(def),[{name:'bonus',formula:'floor((value - 10) / 2)',current:undefined}]);
 assert.equal(catalogParentPath(def.id),'stat.body');
 assert.equal(catalogFullPath(def.id),'stat.body.strength');
 assert.equal(catalogFullPath('hp'),'stat.hp');
});

test('Derived fields show evaluated values and preserve independent definitions', async () => {
 const {catalogDerivedFields}=await import('./stat-catalog.js');
 const def={v04:{default:0,gain:10,ratio:{derive:'stat.body.strength.bonus / 2'}}};
 assert.deepEqual(catalogDerivedFields(def,[{name:'gain',value:7},{name:'ratio',value:2}]),[
  {name:'gain',formula:'10',current:7},
  {name:'ratio',formula:'stat.body.strength.bonus / 2',current:2},
 ]);
});

test('Action catalogue exposes roll, conditions and applied effects', async () => {
 const {catalogActionRules,catalogFullPath}=await import('./stat-catalog.js');
 const def={v04:{name:'Попадание',roll:'1d20 + stat.body.strength.bonus',success:'roll >= target.stat.armor',on:{success:{message:'Попал'}},apply:{path:'target.stat.hp.current',formula:'value - roll'}}};
 assert.deepEqual(catalogActionRules(def),[
  {path:'roll',value:'1d20 + stat.body.strength.bonus'},
  {path:'success',value:'roll >= target.stat.armor'},
  {path:'on.success.message',value:'Попал'},
  {path:'apply.path',value:'target.stat.hp.current'},
  {path:'apply.formula',value:'value - roll'},
 ]);
 assert.equal(catalogFullPath('lab.skill','action'),'action.lab.skill');
});

// The server encodes v0.4 RulesetFields as a TOML string. Iterating it as
// an object formerly rendered fake derived fields "0", "1", "2".
test('TOML-string stat definitions expose description, derived bonus and references', async () => {
 const {catalogDefinitionFields,catalogDerivedFields}=await import('./stat-catalog.js');
 const def={id:'body.strength',name:'Сила',type:'integer',v04:'name = "Сила"\ndescription = "База и эффективное значение проверяются отдельно."\ntype = "integer"\ndefault = 16\nbonus = { formula = "floor((value - 10) / 2)" }'};
 assert.deepEqual(catalogDefinitionFields(def),{
  name:'Сила',description:'База и эффективное значение проверяются отдельно.',type:'integer',default:16,bonus:{formula:'floor((value - 10) / 2)'}
 });
 assert.deepEqual(catalogDerivedFields(def,[{name:'bonus',value:5}]),[{name:'bonus',formula:'floor((value - 10) / 2)',current:5}]);
});

test('Formula default in TOML-string resource is preserved and not misread as a derived field', async () => {
 const {catalogDefinitionFields,catalogDerivedFields,statDependencies}=await import('./stat-catalog.js');
 const hp={id:'hp',type:'integer',kind:'resource',v04:'name = "Здоровье"\ndefault = { formula = "20 + stat.body.strength" }\nresource = true'};
 assert.deepEqual(catalogDefinitionFields(hp).default,{formula:'20 + stat.body.strength'});
 assert.deepEqual(catalogDerivedFields(hp),[]);
 const defs={hp,'body.strength':{id:'body.strength',v04:'default = 16'}};
 assert.deepEqual(statDependencies('hp',defs),['body.strength']);
});

test('Derived metadata and nested action rules are read from TOML strings', async () => {
 const {catalogDerivedFields,catalogActionRules}=await import('./stat-catalog.js');
 assert.deepEqual(catalogDerivedFields({v04:'default = 10\nbonus = { name = "Бонус силы", type = "integer", formula = "floor(value / 2)" }'}),[
  {name:'bonus',formula:'floor(value / 2)',current:undefined,label:'Бонус силы',type:'integer'},
 ]);
 const action={v04:'name = "Проверка"\nroll = "1d20 + stat.body.strength.bonus"\n[on.success]\nmessage = "Попадание"'};
 assert.deepEqual(catalogActionRules(action),[
  {path:'roll',value:'1d20 + stat.body.strength.bonus'},
  {path:'on.success.message',value:'Попадание'},
 ]);
});

test('TOML parser ignores prototype property assignments', async () => {
 const {catalogDefinitionFields}=await import('./stat-catalog.js');
 const def={v04:'name = "Безопасный"\n__proto__.polluted = "yes"'};
 assert.equal(catalogDefinitionFields(def).name,'Безопасный');
 assert.equal({}.polluted,undefined);
});

test('Metadata-only parent action is represented by a group, never a separate choice', async () => {
 const {catalogActionEntries}=await import('./stat-catalog.js');
 const actions={
  'weapon.strike':{id:'weapon.strike',name:'Удар оружием',v04:'name = "Удар оружием"'},
  'weapon.strike.hit':{id:'weapon.strike.hit',name:'Попадание',v04:'name = "Попадание"\nroll = "1d20"'},
  'weapon.strike.damage':{id:'weapon.strike.damage',name:'Урон',v04:'name = "Урон"\nroll = "dice(source.damage)"'},
  'lab.inspect':{id:'lab.inspect',name:'Осмотр',v04:'name = "Осмотр"\nmessage = "Осмотр предмета"'},
 };
 const entries=catalogActionEntries(actions);
 assert.deepEqual(entries.map(row=>row.def.id),['weapon.strike.hit','weapon.strike.damage','lab.inspect']);
 assert.deepEqual(entries[0].group,{id:'weapon.strike',name:'Удар оружием'});
 assert.deepEqual(entries[1].group,{id:'weapon.strike',name:'Удар оружием'});
 assert.equal(entries[2].group,null);
});

test('Executable parent remains an action even if it also has children', async () => {
 const {catalogActionEntries}=await import('./stat-catalog.js');
 const actions={
  'weapon.strike':{id:'weapon.strike',v04:'name = "Удар"\nroll = "1d20"'},
  'weapon.strike.hit':{id:'weapon.strike.hit',v04:'roll = "1d20"'},
  'lonely':{id:'lonely',v04:'name = "Без правил"'},
 };
 assert.deepEqual(catalogActionEntries(actions).map(row=>row.def.id),['weapon.strike','weapon.strike.hit','lonely']);
});

test('Whole action definition exposes the actual server TOML without expanding characters', async () => {
 const {catalogActionSource}=await import('./stat-catalog.js');
 const toml='name = "Урон"\nroll = "dice(source.damage)"\napply = { path = "target.stat.hp.current", formula = "value - roll" }';
 assert.equal(catalogActionSource({v04:toml}),toml);
 assert.match(catalogActionSource({v04:{name:'Урон',roll:'1d20'}}),/"roll": "1d20"/);
});
