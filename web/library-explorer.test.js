import test from 'node:test';
import assert from 'node:assert/strict';
import {File} from 'node:buffer';
import {libraryHierarchy,libraryGalleryGroups,filterLibraryImages,libraryFilesZip,ViewportPreviews,LibraryBrowser} from './library-explorer.js';

const tree={directories:['rules','rules/monsters','art','art/monsters','empty'],files:[
  {path:'rules/stats.toml',type:'definitions'},
  {path:'rules/monsters/goblin.toml',type:'definitions'},
  {path:'art/monsters/goblin.png',type:'image',assetId:'a'},
  {path:'art/orc.png',type:'image',assetId:'b'},
  {path:'character.toml',type:'character',characterId:'c'},
]};
test('Explorer derives directories and keeps folders ahead of files',()=>{
 const root=libraryHierarchy(tree);assert.deepEqual(root.children.map(x=>x.path),['art','empty','rules']);
 const rules=root.children.find(x=>x.path==='rules');assert.deepEqual(rules.children.map(x=>x.path),['rules/monsters']);
 assert.deepEqual(rules.files.map(x=>x.path),['rules/stats.toml']);assert.equal(root.files[0].path,'character.toml');
});
test('Gallery groups images only by the exact virtual parent folder',()=>{
 const groups=libraryGalleryGroups(tree);assert.deepEqual(groups.map(g=>[g.path,g.files.length]),[['art',1],['art/monsters',1]]);
});
test('Asset picker search only returns matching images',()=>{
 assert.deepEqual(filterLibraryImages(tree,'MONSTERS').map(file=>file.path),['art/monsters/goblin.png']);
 assert.deepEqual(filterLibraryImages(tree,'rules'),[]);
});
test('single-file ZIP uses user paths and preserves original file data',async()=>{
 const original='schema_version = 1\n# original comment\n[stats.strength]\ntype = "integer"\n';
 const file=new File([original],'goblin.toml');const zip=await libraryFilesZip([file],'rules/monsters/');
 const bytes=new Uint8Array(await zip.arrayBuffer());const view=new DataView(bytes.buffer);assert.equal(view.getUint32(0,true),0x04034b50);
 const nameLength=view.getUint16(26,true),name=new TextDecoder().decode(bytes.slice(30,30+nameLength));assert.equal(name,'rules/monsters/goblin.toml');
 assert.equal(new TextDecoder().decode(bytes.slice(30+nameLength,30+nameLength+file.size)),original);
 assert.equal(view.getUint32(30+nameLength+file.size,true),0x02014b50);
});
test('ZIP rejects traversal paths',async()=>{
 await assert.rejects(libraryFilesZip([new File(['x'],'evil.toml')],'../'),/Недопустимый файл/);
});
test('preview loader limits requests and revokes every retained blob URL',async()=>{
 const saved={IntersectionObserver:globalThis.IntersectionObserver,URL:globalThis.URL};
 let observer,creates=0,revokes=0,resolve=[],calls=0;
 globalThis.IntersectionObserver=class {constructor(cb){observer=this;this.cb=cb;}observe(){}disconnect(){this.disconnected=true;}};
 globalThis.URL={createObjectURL:()=>`blob:${++creates}`,revokeObjectURL:()=>{revokes++;}};
 try{
  const loader=new ViewportPreviews((_id,signal)=>{calls++;return new Promise(ok=>resolve.push(()=>ok(new Blob(['x']))));});
  const images=Array.from({length:10},(_,i)=>({dataset:{},isConnected:true,alt:'',src:'',removeAttribute(){this.src='';}}));
  for(let i=0;i<images.length;i++)loader.observe(images[i],String(i));
  observer.cb(images.map(target=>({target,isIntersecting:true})));
  assert.equal(calls,4,'at most four active requests');
  resolve.splice(0,4).forEach(x=>x());await new Promise(resolve=>setImmediate(resolve));
  assert.ok(calls<=8);
  loader.destroy();assert.equal(revokes,creates);assert.ok(observer.disconnected);
 }finally{globalThis.IntersectionObserver=saved.IntersectionObserver;globalThis.URL=saved.URL;}
});
test('stale server revision refreshes tree without optimistic movement; duplicate submit ignored',async()=>{
 let calls=0,refreshed=0,release;
 const browser=Object.create(LibraryBrowser.prototype);browser.tree={libraryRevision:4,registryRevision:5,characterRevision:6};browser.pending=false;browser.selection='a.toml';browser.expanded=new Set();browser.message=()=>{};browser.refresh=async()=>{refreshed++;};
 browser.api={operate:async()=>{calls++;await new Promise(ok=>release=ok);const error=new Error('stale');error.status=409;throw error;}};
 const first=browser.operate('moveFile','a.toml','b.toml'),second=browser.operate('moveFile','a.toml','b.toml');assert.equal(calls,1);release();await Promise.all([first,second]);
 assert.equal(browser.selection,'a.toml');assert.equal(refreshed,1);assert.equal(browser.pending,false);
});
