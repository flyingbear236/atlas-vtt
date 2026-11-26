import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mediaFilter,mediaSelection,mediaGridWindow,mediaShortcut,mediaPasteFolder,mediaWidthLimits} from './media-library.js';
const files=[{path:'Props/Barrels/prop_0042.png',assetId:'a',addedAt:2,size:45},{path:'Props/Barrels/prop_0007.png',assetId:'b',addedAt:3,size:10},{path:'Nature/Oak.png',assetId:'c',addedAt:1,size:100},{path:'Tavern/prop_0042.png',assetId:'a',addedAt:4,size:45}];
test('parent and filename search, multiword and local scope',()=>{
 assert.deepEqual(mediaFilter(files,{folder:'Nature',query:'BARRELS 0042'}).map(x=>x.assetId),['a']);
 assert.equal(mediaFilter(files,{folder:'Nature',scope:'folder',query:'barrels'}).length,0);
 assert.equal(mediaFilter(files,{folder:'Props'}).length,2);
 assert.equal(mediaFilter(files,{folder:'@fav',favorites:['a'],query:'oak'}).length,1);
 assert.equal(mediaFilter(files,{folder:''}).length,0);
 assert.equal(mediaFilter(files,{folder:'@fav',favorites:['a']}).length,2);
 assert.deepEqual(mediaFilter(files,{folder:'@recent',recent:['c','b']}).map(x=>x.assetId),['c','b']);
 assert.equal(mediaFilter(files,{sort:'size'})[0].assetId,'c');
});
test('Ctrl and Shift selection uses sorted visible paths, not DOM window',()=>{
 const paths=files.map(f=>f.path);let s=mediaSelection(new Set(),paths,paths[0],'');
 s=mediaSelection(s.selection,paths,paths[2],s.anchor,{shiftKey:true});assert.deepEqual([...s.selection],paths.slice(0,3));
 s=mediaSelection(s.selection,paths,paths[1],s.anchor,{ctrlKey:true});assert.deepEqual([...s.selection],[paths[0],paths[2]]);
 s=mediaSelection(s.selection,paths,paths[3],s.anchor,{metaKey:true});assert.equal(s.selection.size,3);
});
test('10,000 links keep DOM window bounded and reach last row',()=>{
 for(const tile of [88,120,190]){
  const a=mediaGridWindow(10000,960,700,0,tile);assert.ok(a.end<150);
  const z=mediaGridWindow(10000,960,700,a.total-700,tile);assert.equal(z.end,10000);assert.ok(z.end-z.start<150);
 }
});

test('keyboard shortcuts use physical keys with Cyrillic layouts and preserve Alt/Shift combinations',()=>{
 assert.equal(mediaShortcut({key:'с',code:'KeyC',ctrlKey:true}),'c');
 assert.equal(mediaShortcut({key:'м',code:'KeyV',ctrlKey:true}),'v');
 assert.equal(mediaShortcut({key:'ф',code:'KeyA',metaKey:true}),'a');
 assert.equal(mediaShortcut({key:'v',ctrlKey:true}),'v');
 assert.equal(mediaShortcut({key:'v',code:'KeyV',ctrlKey:true,altKey:true}),'');
 assert.equal(mediaShortcut({key:'c',code:'KeyC'}),'');
});
test('paste into all images uses root; smart lists are not folder targets',()=>{
 assert.equal(mediaPasteFolder('@all'),'');
 assert.equal(mediaPasteFolder(''),'');
 assert.equal(mediaPasteFolder('Props/Barrels'),'Props/Barrels');
 assert.equal(mediaPasteFolder('@fav'),null);
 assert.equal(mediaPasteFolder('@recent'),null);
});
test('media dock keeps room for map and clamps maximum width',()=>{
 assert.deepEqual(mediaWidthLimits(1440,260),{min:330,max:900});
 assert.deepEqual(mediaWidthLimits(900,400),{min:280,max:280});
 assert.deepEqual(mediaWidthLimits(600,400),{min:0,max:0});
});
