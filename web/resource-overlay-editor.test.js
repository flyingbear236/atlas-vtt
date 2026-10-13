import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {arrangeResourceOverlays,resourceOverlayLabel,resourceOverlaySample} from './resource-overlay-editor.js';
import {resourceOverlayPresentation,truncateOverlayText} from './token-renderer.js';

const overlay=(id,position,slot=1)=>({statId:id,position,slot,enabled:true,showName:true,color:'#ef963d',mode:'text'});

test('four indicators are assigned unique slots, preserving each side order',()=>{
 const input=[overlay('hp','bottom'),overlay('stamina','top'),overlay('mana','bottom'),overlay('luck','top')];
 const output=arrangeResourceOverlays(input);
 assert.deepEqual(output.map(({statId,position,slot})=>[statId,position,slot]),[['stamina','top',1],['luck','top',2],['hp','bottom',1],['mana','bottom',2]]);
 assert.deepEqual(input.map(i=>i.slot),[1,1,1,1]);
});

test('third indicator on a side is rejected, including when total is below four',()=>{
 assert.throws(()=>arrangeResourceOverlays([overlay('a','top'),overlay('b','top'),overlay('c','top')]),/не больше двух/);
});

test('boolean and string resources show current value rather than current/initial pairs',()=>{
 assert.equal(resourceOverlayLabel({type:'boolean'}, {current:{value:false},initial:{value:true}}),'Нет');
 assert.equal(resourceOverlayLabel({type:'string'}, {current:{value:'Защитная'},initial:{value:'Обычная'}}),'Защитная');
 assert.equal(resourceOverlayPresentation({type:'boolean',name:'Невидимость'}, {current:{value:false},initial:{value:true}},overlay('status','bottom')).text,'Невидимость: Нет');
 assert.equal(resourceOverlayPresentation({type:'string',name:'Стойка'}, {current:{type:'string',value:'Защитная'},initial:{type:'string',value:'Обычная'}},overlay('stance','bottom')).text,'Стойка: Защитная');
 assert.equal(resourceOverlaySample({type:'number'}).initial.value,20);
});

test('long text is clipped to the label maximum and remains readable',()=>{
 const fakeContext={measureText:text=>({width:[...text].length*9})};
 const cropped=truncateOverlayText(fakeContext,'Защитная сверхъестественная стойка с очень длинным описанием',172);
 assert.ok(cropped.endsWith('…'));
 assert.ok(fakeContext.measureText(cropped).width<=172);
 assert.equal(truncateOverlayText(fakeContext,'Коротко',172),'Коротко');
});
