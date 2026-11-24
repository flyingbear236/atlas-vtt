import test from 'node:test';
import assert from 'node:assert/strict';
import {buildStatTraceModel,traceSourceEditKey} from './character-trace.js';

const snapshot={
 statDefinitions:{
  'body.strength':{id:'body.strength',name:'Сила',type:'integer',v04:'name = "Сила"\ndescription = "База и эффективное значение проверяются отдельно."\ntype = "integer"\ndefault = 16\nbonus = { formula = "floor((value - 10) / 2)" }'},
  hp:{id:'hp',name:'Здоровье',type:'integer',kind:'resource',v04:'name = "Здоровье"\ndefault = { formula = "20 + stat.body.strength" }\nresource = true'}
 },
 effective:{stats:{'body.strength':{type:'integer',value:20}},resources:{hp:{current:{type:'integer',value:24},initial:{type:'integer',value:40}}}},
 statFields:{'body.strength':{base:16,derived:[{name:'bonus',value:5}]},hp:{base:40}},
 statTraces:{'stat.body.strength':{initial:16,result:20,effects:[{effectId:'ring_strength',sourceId:'ring-a',item:true,before:16,after:18,editable:true,value:2},{effectId:'ring_strength',sourceId:'ring-b',item:true,before:18,after:20,editable:true,value:2}]},'stat.hp':{initial:40}},
 itemNames:{'ring-a':'Кольцо испытателя','ring-b':'Кольцо испытателя'},instance:{inventory:[{id:'ring-a',objectId:'lab.ring'},{id:'ring-b',objectId:'lab.ring'}]}
};
test('Strength inspector exposes the actual two instance grants and the derived bonus',()=>{
 const m=buildStatTraceModel(snapshot,'body.strength');
 assert.equal(m.name,'Сила');assert.equal(m.current,20);assert.equal(m.sources.length,3);
 assert.equal(m.sources[1].itemId,'ring-a');assert.equal(m.sources[1].effectId,'ring_strength');assert.equal(m.sources[1].value,2);assert.equal(m.sources[1].editable,true);
 assert.equal(m.derived[0].formula,'floor((value - 10) / 2)');assert.equal(m.derived[0].value,5);
});
test('Health shows current and computed maximum, but does not invent derived fields',()=>{
 const m=buildStatTraceModel(snapshot,'hp');
 assert.equal(m.current,24);assert.equal(m.maximum,40);assert.equal(m.baseFormula,'20 + stat.body.strength');
 assert.equal(m.derived.length,0);assert.deepEqual(m.sources.map(s=>s.kind),['resourceCurrent']);
});
test('Untraced values do not create a fake editable inspector',()=>{
 assert.equal(buildStatTraceModel({...snapshot,statTraces:null},'body.strength'),null);
});

test('Computed default is a formula, not an editable character source',()=>{
 const snapshot={
  statDefinitions:{mastery:{id:'mastery',name:'Мастерство',type:'integer',v04:'name = "Мастерство"\ntype = "integer"\ndefault = { formula = "2 + floor((stat.level - 1) / 4)" }'}},
  effective:{stats:{mastery:{type:'integer',value:3}},statValues:{'stat.mastery':3,'stat.level':6}},
  statFields:{mastery:{base:3}},statTraces:{'stat.mastery':{initial:3}},instance:{inventory:[]}
 };
 const m=buildStatTraceModel(snapshot,'mastery');
 assert.equal(m.baseFormula,'2 + floor((stat.level - 1) / 4)');
 assert.equal(m.current,3);
 assert.deepEqual(m.sources,[]);
});

test('Derived effects retain original value and the ordered before/after chain',()=>{
 const snapshot={
  statDefinitions:{signal:{id:'signal',name:'Сигнал',type:'integer',v04:'name = "Сигнал"\ntype = "integer"\ndefault = 0\ngain = 10'}},
  effective:{stats:{signal:{type:'integer',value:0}}},
  statFields:{signal:{base:0,derived:[{name:'gain',value:7}]}},
  statTraces:{'stat.signal':{initial:0},'stat.signal.gain':{
   initial:10,result:7,effects:[
    {effectId:'signal_boost',sourceId:'hero',item:false,before:10,after:15,formula:'target + 5'},
    {effectId:'signal_reduce',sourceId:'hero',item:false,before:15,after:7,formula:'floor(target / 2)'}
   ]}},instance:{inventory:[]}
 };
 const m=buildStatTraceModel(snapshot,'signal');
 assert.equal(m.derived.length,1);assert.equal(m.derived[0].formula,'10');
 assert.equal(m.derived[0].value,7);
 assert.equal(m.derived[0].calculation.initial,10);
 assert.deepEqual(m.derived[0].effects.map(x=>[x.before,x.after,x.formula]),[
  [10,15,'target + 5'],[15,7,'floor(target / 2)']
 ]);
 assert.equal(m.derived[0].effects[1].editable,false);
});

test('Editable constant is explicitly a per-character override, not shared default',()=>{
 const m=buildStatTraceModel(snapshot,'body.strength');
 assert.equal(m.sources[0].kind,'statBase');
 assert.equal(m.sources[0].label,'Исходное значение персонажа');
 assert.match(m.sources[0].description,/Только для этого персонажа/);
 assert.equal(m.sources[0].editable,true);
});


test('Integer literal derived field edits one character without changing formulas or effect results',()=>{
 const source={
  statDefinitions:{signal:{id:'signal',name:'Сигнал',type:'integer',v04:'name = "Сигнал"\ntype = "integer"\ndefault = 0\ngain = 10'}},
  effective:{stats:{signal:{type:'integer',value:0}}},
  statFields:{signal:{base:0,derived:[{name:'gain',value:7}]}},
  statTraces:{'stat.signal':{initial:0},'stat.signal.gain':{
   initial:10,result:7,effects:[
    {effectId:'boost',sourceId:'hero',item:false,before:10,after:15,formula:'target + 5'},
    {effectId:'reduce',sourceId:'hero',item:false,before:15,after:7,formula:'floor(target / 2)'}
   ]}},instance:{inventory:[]}
 };
 const model=buildStatTraceModel(source,'signal');
 assert.deepEqual({kind:model.derived[0].constant.kind,value:model.derived[0].constant.value,target:model.derived[0].constant.target},
   {kind:'statField',value:10,target:'stat.signal.gain'});
 const key=traceSourceEditKey(model.derived[0].constant);
 assert.deepEqual(key.split(':'),['statField','signal','','','stat.signal.gain']);
 assert.deepEqual(model.derived[0].effects.map(x=>[x.before,x.after]),[[10,15],[15,7]]);
 const custom={...source,instance:{inventory:[],statFieldOverrides:{'stat.signal.gain':12}},
  effective:{stats:{signal:{type:'integer',value:0}}},
  statTraces:{...source.statTraces,'stat.signal.gain':{initial:12,result:8,effects:[]}}};
 assert.equal(buildStatTraceModel(custom,'signal').derived[0].constant.value,12);
});
test('Computed derived formula and non-numeric literals cannot be edited as stat fields',()=>{
 const source={statDefinitions:{strength:{id:'strength',type:'integer',v04:'default = 16\nbonus = { formula = "floor((value - 10) / 2)" }\nlabel = "motto"'}},
  effective:{stats:{strength:{type:'integer',value:16}}},
  statFields:{strength:{base:16,derived:[{name:'bonus',value:3},{name:'label',value:'motto'}]}},
  statTraces:{'stat.strength':{initial:16},'stat.strength.bonus':{initial:3},'stat.strength.label':{initial:'motto'}},instance:{inventory:[]}};
 const model=buildStatTraceModel(source,'strength');
 assert.equal(model.derived.find(x=>x.field==='bonus').constant,null);
 assert.equal(model.derived.find(x=>x.field==='label').constant,null);
});
