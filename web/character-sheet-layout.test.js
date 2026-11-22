import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {buildSheet,normalizeLayout,moveEntry,sheetStatGroup} from './character-sheet-layout.js';
const entries=[{key:'stat:hp',type:'stat',name:'Health',resource:true},{key:'stat:power',type:'stat',name:'Power',resource:false,group:'Атрибуты'},{key:'action:slash',type:'action',name:'Slash',group:'Атаки'}];
test('default layout groups deterministically and contains three built-in tabs',()=>{
 const result=buildSheet(normalizeLayout(null),entries);
 assert.deepEqual(result.tabs.map(t=>t.id),['stats','actions','inventory']);
 assert.equal(result.tabs[0].sections.find(s=>s.name==='Характеристики').items[0],'stat:hp');
 assert.equal(result.tabs[1].sections.find(s=>s.name==='Атаки').items[0],'action:slash');
});
test('movement, new definitions and corrupted layout',()=>{
 let layout=normalizeLayout(null);layout.tabs[0].sections.push({id:'user:one',name:'Main',view:'list',collapsed:false,items:[]});
 layout=moveEntry(layout,'action:slash','stats','user:one');
 const merged=buildSheet(layout,[...entries,{key:'stat:new',name:'New',type:'stat'}]);
 assert.deepEqual(merged.tabs[0].sections.find(s=>s.id==='user:one').items,['action:slash']);
 assert.ok(merged.tabs[0].sections.some(s=>s.items.includes('stat:new')));
 assert.deepEqual(normalizeLayout({version:999,tabs:[{id:'x'}]}).tabs.map(t=>t.id),['stats','actions','inventory']);
});
test('duplicate assignments are removed and hidden entries stay hidden',()=>{
 const layout=normalizeLayout({version:1,tabs:[{id:'stats',sections:[{id:'user:a',items:['stat:hp']},{id:'user:b',items:['stat:hp']}]},{id:'actions',sections:[]}],hidden:['action:slash']});
 const full=buildSheet(layout,entries);
 assert.equal(full.tabs[0].sections.flatMap(s=>s.items).filter(x=>x==='stat:hp').length,1);
 assert.equal(full.tabs.flatMap(t=>t.sections.flatMap(s=>s.items)).includes('action:slash'),false);
});

test('Unicode groups and definition IDs with spaces are stable',()=>{
 const items=[{key:'stat:магия смерти',name:'Магия',type:'stat',group:'Внутренняя магия'}];
 const fresh=buildSheet(normalizeLayout(null),items);
 const section=fresh.tabs[0].sections[0];
 assert.equal(section.name,'Внутренняя магия');
 const stored=normalizeLayout(fresh);
 assert.equal(stored.tabs[0].sections[0].items[0],'stat:магия смерти');
});

test('inventory is a built-in tab, saved custom tabs and assignments survive',()=>{
 const legacy={version:1,tabs:[{id:'actions',sections:[{id:'user:acts',name:'Attacks',view:'list',items:['action:slash']}]},{id:'stats',sections:[]},{id:'user:own',name:'Custom',sections:[]}],hidden:[]};
 const restored=normalizeLayout(legacy);
 assert.deepEqual(restored.tabs.map(t=>t.id),['actions','stats','user:own','inventory']);
 assert.deepEqual(restored.tabs[0].sections[0].items,['action:slash']);
 const inventory=restored.tabs.find(t=>t.id==='inventory');
 inventory.sections.push({id:'user:bag',name:'Панель',view:'list',items:[]});
 const withItem=moveEntry(restored,'stat:power','inventory','user:bag');
 assert.deepEqual(withItem.tabs.find(t=>t.id==='inventory').sections[0].items,['stat:power']);
});

test('nested stat paths form display groups while manual placements win',()=>{
 assert.equal(sheetStatGroup('stat.basic.strength'),'basic');
 assert.equal(sheetStatGroup('stat."Базовые".сила'),'Базовые');
 assert.equal(sheetStatGroup('stats."Базовые".сила'),'Базовые');
 assert.equal(sheetStatGroup('stat.body.strength','Мои характеристики'),'Мои характеристики');
 assert.equal(sheetStatGroup('stat.hp'),'');
 const grouped=[{key:'stat:body.strength',type:'stat',name:'Сила',group:sheetStatGroup('stat.body.strength')},{key:'stat:body.dexterity',type:'stat',name:'Ловкость',group:sheetStatGroup('stat.body.dexterity')}];
 const initial=buildSheet(normalizeLayout(null),grouped);
 assert.deepEqual(initial.tabs[0].sections[0].items,['stat:body.dexterity','stat:body.strength']);
 assert.equal(initial.tabs[0].sections[0].name,'body');
 const layout=normalizeLayout({version:1,tabs:[{id:'stats',sections:[{id:'custom:one',name:'Своя папка',items:['stat:body.strength']}]}]});
 const moved=buildSheet(layout,grouped);
 assert.deepEqual(moved.tabs[0].sections.find(s=>s.id==='custom:one').items,['stat:body.strength']);
 assert.deepEqual(moved.tabs[0].sections.filter(s=>s.id!=='custom:one').flatMap(s=>s.items),['stat:body.dexterity']);
});
