import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {itemActionsForInventory, nestInventoryFields} from './inventory-presentation.js';
test('item actions are selected by exact sourceId with hit before damage', () => {
 const snapshot={actionSelectors:{'knife.hit':'self','knife.damage':'self'},effective:{actionSources:[{actionId:'knife.damage',sourceId:'one'},{actionId:'knife.hit',sourceId:'two'}]},inactiveActionSources:[{actionId:'knife.hit',sourceId:'one'}]};
 assert.deepEqual(itemActionsForInventory(snapshot,'one').map(x=>x.actionId),['knife.hit','knife.damage']);
 assert.deepEqual(itemActionsForInventory(snapshot,'two').map(x=>x.actionId),['knife.hit']);
});
test('nested fields retain duplicate names under separate groups', () => {
 const tree=nestInventoryFields([{path:['магазин','прочность'],value:'40'},{path:['магазин','патроны'],value:'12'},{path:['рукоять','прочность'],value:'80'},{path:['вес'],value:'3'}]);
 assert.deepEqual(tree.fields,[{name:'вес',value:'3'}]);
 assert.deepEqual(tree.groups.get('магазин').fields.map(f=>f.name),['прочность','патроны']);
 assert.equal(tree.groups.get('рукоять').fields[0].value,'80');
});
