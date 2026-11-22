import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {groupSheetActionSources, shortActionFormula} from './action-groups.js';
const snapshot = {
  actions: {
    'blade.hit': {v04: true}, 'blade.damage': {v04: true},
    skill: {v04: true}, legacy: {v04: false}
  },
  actionSelectors: {'blade.hit':'select(character)', 'blade.damage':'select(character)', skill:'self'},
  effective: {actionSources: [
    {actionId:'blade.hit',sourceId:'knife1'}, {actionId:'blade.hit',sourceId:'knife2'},
    {actionId:'blade.damage',sourceId:'knife1'}, {actionId:'blade.damage',sourceId:'knife2'},
    {actionId:'skill'}
  ]}
};
test('actions from multiple definitions are grouped by exact item instance ID', () => {
  const result=groupSheetActionSources(snapshot,[{items:['action:blade.damage','action:legacy']},{items:['action:blade.hit','action:skill']}]);
  const groups=result.byEntry.get('action:blade.damage');
  assert.equal(groups.length,2);
  assert.deepEqual(groups.map(g=>g.sourceId),['knife1','knife2']);
  assert.deepEqual(groups[0].choices.map(c=>c.actionId),['blade.hit','blade.damage']);
  assert.equal(result.byEntry.has('action:blade.hit'),false);
  assert.equal(result.byEntry.get('action:skill')[0].sourceId,'');
  assert.equal(result.handled.has('action:legacy'),false);
});
test('preview summary omits unknown and non-additive values', () => {
  assert.equal(shortActionFormula({parts:[{label:'1d20'},{label:'Ловкость',value:'3'},{label:'Кольцо',value:'2'}]}),'d20 + 5');
  assert.equal(shortActionFormula({parts:[{label:'1d20'},{label:'Атлетика',value:'?'}]}),'');
  assert.equal(shortActionFormula({parts:[{label:'1d20'}],notes:['Зависит от цели']}),'');
});
test('unequipped item choices remain visible as separate sources without becoming executable grants',()=>{
 const preview={...snapshot, inactiveActionSources:[{actionId:'blade.hit',sourceId:'knife3'},{actionId:'blade.damage',sourceId:'knife3'}]};
 const groups=groupSheetActionSources(preview,[{items:['action:blade.hit','action:blade.damage']}]).byEntry.get('action:blade.hit');
 assert.deepEqual(groups.map(group=>group.sourceId),['knife1','knife2','knife3']);
 assert.deepEqual(groups[2].choices.map(choice=>choice.actionId),['blade.hit','blade.damage']);
 assert.equal(snapshot.effective.actionSources.some(row=>row.sourceId==='knife3'),false);
});
