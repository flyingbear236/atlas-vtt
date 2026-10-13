import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {buildSheet,normalizeLayout,moveEntry} from './character-sheet-layout.js';
const entries=[{key:'stat:hp',type:'stat',name:'Health',resource:true},{key:'stat:power',type:'stat',name:'Power',resource:false,group:'Атрибуты'},{key:'action:slash',type:'action',name:'Slash',group:'Атаки'}];
test('default layout groups deterministically and contains both tabs',()=>{
 const result=buildSheet(normalizeLayout(null),entries);
 assert.deepEqual(result.tabs.map(t=>t.id),['stats','actions']);
 assert.equal(result.tabs[0].sections.find(s=>s.name==='Характеристики').items[0],'stat:hp');
 assert.equal(result.tabs[1].sections.find(s=>s.name==='Атаки').items[0],'action:slash');
});
test('movement, new definitions and corrupted layout',()=>{
 let layout=normalizeLayout(null);layout.tabs[0].sections.push({id:'user:one',name:'Main',view:'list',collapsed:false,items:[]});
 layout=moveEntry(layout,'action:slash','stats','user:one');
 const merged=buildSheet(layout,[...entries,{key:'stat:new',name:'New',type:'stat'}]);
 assert.deepEqual(merged.tabs[0].sections.find(s=>s.id==='user:one').items,['action:slash']);
 assert.ok(merged.tabs[0].sections.some(s=>s.items.includes('stat:new')));
 assert.deepEqual(normalizeLayout({version:999,tabs:[{id:'x'}]}).tabs.map(t=>t.id),['stats','actions']);
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
