import assert from 'node:assert/strict';
import test from 'node:test';
import {actionReferenceModel} from './action-reference.js';

const damage={
 id:'weapon.strike.damage',name:'Урон',
 v04:'name = "Урон"\ntarget = "select(character)"\nroll = "dice(source.damage) + stat.body.strength.bonus"\napply = { path = "target.stat.hp.current", formula = "value - roll" }'
};

test('Floating inspector and catalogue use the full same definition model',()=>{
 const model=actionReferenceModel(damage,{id:'weapon.strike',name:'Удар оружием'});
 assert.equal(model.name,'Урон');
 assert.equal(model.id,'action.weapon.strike.damage');
 assert.deepEqual(model.group,{id:'action.weapon.strike',name:'Удар оружием'});
 assert.match(model.source,/apply = \{ path = "target.stat.hp.current"/);
 assert.deepEqual(model.rules,[
  {path:'target',value:'select(character)'},
  {path:'roll',value:'dice(source.damage) + stat.body.strength.bonus'},
  {path:'apply.path',value:'target.stat.hp.current'},
  {path:'apply.formula',value:'value - roll'}
 ]);
});

test('Definition without TOML keeps available description and no invented rules',()=>{
 const model=actionReferenceModel({id:'rest',name:'Отдых',description:'Перерыв'},null);
 assert.equal(model.name,'Отдых');assert.equal(model.description,'Перерыв');
 assert.equal(model.source,'');assert.deepEqual(model.rules,[]);
 assert.equal(model.group,null);
});

test('Action text includes nested conditional outcomes from TOML',()=>{
 const sample={id:'lab.aim',name:'Пробный выпад',v04:'roll = "12"\n[on.success]\nmessage = "Успех"\napply = { path = "target.stat.hp.current", formula = "value - 3" }'};
 const result=actionReferenceModel(sample);
 assert.deepEqual(result.rules,[
  {path:'roll',value:'12'},
  {path:'on.success.message',value:'Успех'},
  {path:'on.success.apply.path',value:'target.stat.hp.current'},
  {path:'on.success.apply.formula',value:'value - 3'}
 ]);
});
