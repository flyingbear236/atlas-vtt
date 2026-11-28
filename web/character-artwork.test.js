import test from 'node:test';
import assert from 'node:assert/strict';
import {artworkGeometry,zoomArtwork,normalizeArtworkPlacement,artworkPreferences} from './character-artwork.js';
const frame={width:350,height:80}, portrait={width:600,height:1200};
test('vertical portrait preserves proportions and feathered edges in header',()=>{
 const g=artworkGeometry(frame,portrait,{},'header');
 assert.equal(g.height/g.width,2);assert.ok(g.height>frame.height);assert.ok(g.feather>0&&g.feather<=32);
});
test('wheel zoom preserves the image point underneath the cursor',()=>{
 const p=normalizeArtworkPlacement({x:.5,y:.5,zoom:1},'header'), anchor={x:180,y:30};
 const a=artworkGeometry(frame,portrait,p,'header');const next=zoomArtwork(frame,portrait,p,'header',1.25,anchor);const b=artworkGeometry(frame,portrait,next,'header');
 assert.ok(Math.abs((anchor.x-a.left)/a.width-(anchor.x-b.left)/b.width)<1e-8);
 assert.ok(Math.abs((anchor.y-a.top)/a.height-(anchor.y-b.top)/b.height)<1e-8);
});
test('dragging cannot completely lose an image outside the viewport',()=>{
 for(const x of [-100,100])for(const y of [-100,100]){const g=artworkGeometry(frame,portrait,{x,y,zoom:.2},'header');assert.ok(g.left<frame.width&&g.left+g.width>0&&g.top<frame.height&&g.top+g.height>0);}
});
test('header and full background preferences stay independent',()=>{
 const p=artworkPreferences({mode:'background',header:{zoom:2,x:.3},background:{zoom:.75,x:.8}});assert.equal(p.header.zoom,2);assert.equal(p.background.zoom,.75);assert.equal(p.header.x,.3);assert.equal(p.background.x,.8);assert.equal(p.mode,'background');
});
test('invalid persisted values and extreme image proportions are bounded',()=>{
 const p=artworkPreferences({mode:'invalid',header:{zoom:Infinity,x:NaN,opacity:-4}});assert.equal(p.mode,'header');assert.equal(p.header.zoom,1);assert.equal(p.header.opacity,.1);
 const g=artworkGeometry(frame,{width:50000,height:1},{zoom:100},'header');assert.ok(g.width<frame.width*13);assert.ok(g.feather<=g.height*.24);assert.equal(artworkGeometry(frame,{width:0,height:0},p.header,'header'),null);
});
