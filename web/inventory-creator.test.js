import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {inventoryGrantPayload} from './inventory-creator.js';
import {knownActionFormula} from './action-groups.js';
test('grant existing object has no draft and keeps exact definition ID',()=>{
 assert.deepEqual(inventoryGrantPayload('hero','weapon.rune',2,false),{type:'characterItemAdd',payload:{characterId:'hero',objectId:'weapon.rune',itemCount:2,equipped:false}});
});
test('create object and grant as one command with typed properties and grants',()=>{
 const {type,payload}=inventoryGrantPayload('hero','',1,true,{name:'  Эликсир силы  ',description:'Для героя',properties:[{name:'charges',value:'3'}],actions:['lab.recover'],effects:['ring_strength']});
 assert.equal(type,'characterItemCreate');assert.equal(payload.objectDraft.name,'Эликсир силы');assert.equal(payload.objectDraft.properties.charges,'3');assert.deepEqual(payload.objectDraft.actions,['lab.recover']);assert.equal(payload.equipped,true);
});
test('inventory item authoring rejects ambiguous and unsafe input',()=>{
 assert.throws(()=>inventoryGrantPayload('hero','ring',21,false),/количество/);
 assert.throws(()=>inventoryGrantPayload('hero','',1,false),/Выберите/);
 assert.throws(()=>inventoryGrantPayload('hero','',1,false,{name:'Potion',properties:[{name:'actions',value:'override'}]}),/Некорректное/);
 assert.throws(()=>inventoryGrantPayload('hero','',1,false,{name:'Potion',properties:[{name:'damage',value:'1d8'},{name:'damage',value:'1d4'}]}),/Повторяющееся/);
});
test('known subtotal survives unresolved target and conditional effects',()=>{
 assert.deepEqual(knownActionFormula({parts:[{label:'1d20',sign:'+'},{label:'Сила',value:'4',sign:'+'},{label:'Рунная точность',value:'2',sign:'+'}],notes:['Зависит от цели']}),{text:'d20 + 6',partial:true});
 assert.deepEqual(knownActionFormula({parts:[{label:'1d20'},{label:'Сила',value:'?'},{label:'Кольцо',value:'-3',sign:'+'}]}),{text:'d20 − 3',partial:true});
 assert.equal(knownActionFormula({parts:[{label:'Сила',value:'3'}]}),null);
 assert.equal(knownActionFormula({parts:[{label:'1d20',sign:'-'}]}),null);
});
