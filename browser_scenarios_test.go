package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// Overlapping, near-square native assets exceed 64 MiB at the requested size.
// Odd heights also catch rounding mismatches that can cause endless redecodes.
func browserHeavyScene(t *testing.T, s *Server) {
	t.Helper()
	var assets []Asset
	for i := 0; i < 80; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 512, 509))
		for y := 0; y < 509; y++ {
			for x := 0; x < 512; x++ {
				img.SetRGBA(x, y, color.RGBA{uint8(i + 20), uint8(x * 7), uint8(y * 7), 255})
			}
		}
		var buf bytes.Buffer
		png.Encode(&buf, img)
		a, err := prepare(s.root, buf.Bytes(), "token")
		if err != nil {
			t.Fatal(err)
		}
		assets = append(assets, a)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.sessions {
		var scene *Scene
		for _, candidate := range ss.Scenes {
			if candidate.Published && len(candidate.Elements) > 0 {
				scene = candidate
				break
			}
		}
		if scene == nil {
			t.Fatal("browser scene with map not found")
		}
		for i, a := range assets {
			ss.Assets[a.ID] = a
			tokenID := id()
			scene.Tokens[tokenID] = Token{ID: tokenID, Name: "Cache", FloorID: firstFloorID(scene), X: scene.Bounds.Width/2 + float64(i%10), Y: scene.Bounds.Height/2 + float64(i/10), Size: 1024, Color: "#c2d89b", Asset: a.ID}
		}
		scene.Revision++
		s.dirty = true
		s.publishSceneSnapshot(ss, scene.ID)
	}
}

const browserDraftScenario = `(async()=>{
 const wait=async f=>{for(let i=0;i<200;i++){if(f())return;await new Promise(r=>setTimeout(r,50))}throw Error('draft test timeout')};
 const creds=JSON.parse(localStorage.getItem('atlas-sessions'))[0];
 const row=[...document.querySelectorAll('.token-row')].find(r=>r.textContent.includes('Browser hero'));row.click();window.heroID=row.dataset.tokenId;
 const field=document.getElementById('tokenName');field.value='Unsaved draft';field.dispatchEvent(new Event('input',{bubbles:true}));
 const joined=await fetch('/api/join',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({session:creds.session,invite:creds.invite,name:'Remote'})}).then(r=>r.json());
 const ws=new WebSocket(location.origin.replace('http','ws')+'/ws');window.remoteSocket=ws;window.remoteMessages=[];
 ws.onmessage=e=>window.remoteMessages.push(JSON.parse(e.data));await new Promise(r=>ws.onopen=r);ws.send(JSON.stringify(joined));await wait(()=>remoteMessages.some(m=>m.type==='campaignSnapshot'));const remoteHome=remoteMessages.find(m=>m.type==='campaignSnapshot'),remoteScene=remoteHome.scenes[0].id;ws.send(JSON.stringify({type:'subscribe',sceneId:remoteScene}));await wait(()=>remoteMessages.some(m=>m.type==='snapshot'));ws.send(JSON.stringify({type:'view',sceneId:remoteScene,region:{left:-10000,top:-10000,right:10000,bottom:10000}}));await wait(()=>remoteMessages.filter(m=>m.type==='snapshot').length>=2);
 await wait(()=>document.getElementById('tokenOwner').options.length===2);
 if(field.value!=='Unsaved draft')throw Error('snapshot erased draft');
 const gm=new WebSocket(location.origin.replace('http','ws')+'/ws');window.secondGM=gm;window.gmMessages=[];gm.onmessage=e=>gmMessages.push(JSON.parse(e.data));await new Promise(r=>gm.onopen=r);gm.send(JSON.stringify(creds));await wait(()=>gmMessages.some(m=>m.type==='campaignSnapshot'));const gmHome=gmMessages.find(m=>m.type==='campaignSnapshot'),gmScene=gmHome.scenes.find(s=>s.published).id;gm.send(JSON.stringify({type:'subscribe',sceneId:gmScene}));await wait(()=>gmMessages.some(m=>m.type==='snapshot'));gm.send(JSON.stringify({type:'view',sceneId:gmScene,region:{left:-10000,top:-10000,right:10000,bottom:10000}}));await wait(()=>gmMessages.filter(m=>m.type==='snapshot').length>=2);
 const player=remoteMessages.find(m=>m.type==='snapshot').you.id;
 gm.send(JSON.stringify({type:'properties',token:{id:heroID},properties:{owner:player,color:'#c2d89b'}}));await wait(()=>document.getElementById('tokenOwner').value===player);
 if(field.value!=='Unsaved draft')throw Error('property event erased draft');
 ws.send(JSON.stringify({type:'move',token:{id:heroID,x:700,y:500}}));await wait(()=>remoteMessages.some(m=>m.type==='move'&&m.id===heroID&&m.x===700));
 if(!document.querySelector('[data-token-id="'+heroID+'"]')){document.getElementById('fit').click();await wait(()=>document.querySelector('[data-token-id="'+heroID+'"]'));}const draftRow=document.querySelector('[data-token-id="'+heroID+'"]');if(!draftRow.classList.contains('selected'))draftRow.click();await wait(()=>field.value==='Unsaved draft');document.getElementById('properties').requestSubmit();await wait(()=>[...document.querySelectorAll('.token-row')].some(row=>row.textContent.includes('Unsaved draft')));
 ws.send(JSON.stringify({type:'final',client:'browser-player',seq:1,token:{id:heroID,x:800,y:600}}));await wait(()=>remoteMessages.some(m=>m.type==='ack'));
 document.getElementById('reconnect').click();await new Promise(r=>setTimeout(r,1500));await wait(()=>document.getElementById('connection').textContent.includes('В сети'));
 document.getElementById('toScenes').click();await wait(()=>!document.getElementById('campaignHome').hidden);const oldPrompt=window.prompt;window.prompt=()=> 'Reconnect rename';const published=[...document.querySelectorAll('.scene-card')].find(card=>card.classList.contains('published'));[...published.querySelectorAll('button')].find(button=>button.textContent==='Переименовать').click();document.getElementById('reconnect').click();window.prompt=oldPrompt;await wait(()=>document.getElementById('connection').textContent.includes('В сети'));await wait(()=>[...document.querySelectorAll('.scene-card')].some(card=>card.textContent.includes('Reconnect rename')));[...document.querySelectorAll('.scene-card')].find(card=>card.textContent.includes('Reconnect rename')).querySelector('.primary').click();await wait(()=>!document.getElementById('workspace').hidden);
 document.querySelector('[data-token-id="'+heroID+'"]').click();document.getElementById('board').dispatchEvent(new KeyboardEvent('keydown',{key:'Escape'}));
 const bounds=document.getElementById('board').getBoundingClientRect();return {x:bounds.x+bounds.width/2,y:bounds.y+bounds.height/2};
})()`

const browserModuleScenario = `(async()=>{
 const {Outbox,Drafts,CacheWriteBudget,imageEdge}=await import('/reliability.js');const values=new Map();const store={getItem:k=>values.get(k),setItem:(k,v)=>values.set(k,v)};const sent=[];
 const {SpatialIndex,ArtworkCache,decodedSize,fitImageSize,fitPlansToBudget,resolveImageMemoryBudgetMiB,planLRUEviction}=await import('/rendering.js');
 const {compositeFloors,transformedFromDrag}=await import('/scene-content.js');

 const floorState={floors:{lower:{id:'lower',order:0,opacity:.6,opacityWhenViewedFromBelow:0},upper:{id:'upper',order:1,opacity:.8,opacityWhenViewedFromBelow:.5}}};const fromBelow=compositeFloors(floorState,'lower'),fromAbove=compositeFloors(floorState,'upper');if(fromBelow.map(v=>v.floor.id).join(',')!=='lower,upper'||fromBelow[1].alpha!==.4||fromAbove.map(v=>v.floor.id).join(',')!=='lower,upper'||fromAbove[1].alpha!==.8)throw Error('floor compositing');floorState.floors.upper.opacityWhenViewedFromBelow=0;if(compositeFloors(floorState,'lower').length!==1)throw Error('zero-alpha floor loaded');
 const transformBase={x:10,y:20,width:100,height:50,rotation:0,pointerX:60,pointerY:-8};
 const moved=transformedFromDrag(transformBase,'move',15,-5);if(moved.x!==25||moved.y!==15)throw Error('element move transform');
 const proportional=transformedFromDrag(transformBase,'se',50,10);if(proportional.width!==150||proportional.height!==75)throw Error('element aspect transform');
 const resized=transformedFromDrag(transformBase,'se',50,10,true);if(resized.width!==150||resized.height!==60)throw Error('element resize transform');
 const rotated=transformedFromDrag(transformBase,'rotate',50,53);if(Math.abs(rotated.rotation-90)>.001)throw Error('element rotate transform');
 const small=decodedSize(32,16,512);if(small.width!==32||small.height!==16)throw Error('small asset upscaled');
 const narrow=decodedSize(512,113,128);if(narrow.width!==128||narrow.height!==28)throw Error('aspect ratio changed');
 const odd=fitImageSize(512,509,256);if(odd.width!==256||odd.height!==255)throw Error('odd size rounding');
 const budget=56*1048576,many=Array.from({length:2000},()=>({plan:{width:512,height:509,native:{width:512,height:509}}})),planned=fitPlansToBudget(many,budget);if(planned>budget)throw Error('plan budget exceeded');if(many.some(({plan})=>plan.width>512||plan.height>509||plan.width<1||plan.height<1))throw Error('invalid planned decode size');
 if(resolveImageMemoryBudgetMiB(undefined)!==128||resolveImageMemoryBudgetMiB(2)!==64||resolveImageMemoryBudgetMiB(4)!==64||resolveImageMemoryBudgetMiB(8)!==128||resolveImageMemoryBudgetMiB(16)!==256||resolveImageMemoryBudgetMiB(64)!==256)throw Error('auto image budget profile');
 if(resolveImageMemoryBudgetMiB(2,'512')!==512||resolveImageMemoryBudgetMiB(64,'64')!==64||resolveImageMemoryBudgetMiB(2,'bogus')!==64)throw Error('manual image budget override');
 const stale=new Map([['old',{size:90,used:-1}]]);if(planLRUEviction(stale,new Set(),90,100).keys.length)throw Error('age-only image eviction');
 const lruEntries=new Map([['visible',{size:30,used:1}],['old',{size:30,used:2}],['new',{size:30,used:3}]]),lru=planLRUEviction(lruEntries,new Set(['visible']),120,100);if(lru.keys.join(',')!=='old,new'||lru.remaining>85)throw Error('LRU/wanted hysteresis');
 const shrunk=planLRUEviction(lruEntries,new Set(),90,64);if(shrunk.keys.join(',')!=='visible,old'||shrunk.remaining>Math.floor(64*.85))throw Error('reduced image budget eviction');
 const boundedEntries=new Map(Array.from({length:100},(_,i)=>[String(i),{size:10,used:i}])),bounded=planLRUEviction(boundedEntries,new Set(),1000,100);if(bounded.remaining>85||bounded.keys.length===0)throw Error('image cache is not bounded under pressure');
 const index=new SpatialIndex();for(let i=0;i<2000;i++)index.set({id:String(i),x:i*100,y:0,size:40});if(index.query(-30,-30,30,30).length!==1)throw Error('spatial query');index.set({id:'0',x:-1000,y:0,size:40});if(index.query(-30,-30,30,30).length)throw Error('stale spatial position');index.remove('0');if(index.query(-1100,-50,-900,50).length)throw Error('spatial delete');
 const artwork=new ArtworkCache(16);artwork.begin(['a','b']);const a=artwork.get('a',2,2,()=>{});if(artwork.get('b',2,2,()=>{})!==null||artwork.get('a',2,2,()=>{})!==a)throw Error('visible artwork churn');artwork.clear();if(artwork.bytes!==0||a.width!==0)throw Error('artwork not released');
 const box=new Outbox(store,'test',cmd=>{sent.push(cmd);return true},()=>{},()=>{});
 box.enqueue('final',{token:{id:'a',x:3,y:4}});box.reconnect();if(sent.length!==2||sent[0].seq!==sent[1].seq)throw Error('retry identity lost');
 const restored=new Outbox(store,'test',cmd=>{sent.push(cmd);return true},()=>{},()=>{});restored.reconnect();if(sent[2].client!==sent[0].client)throw Error('reload identity lost');restored.ack({seq:1});if(restored.data.queue.length)throw Error('ack did not clear queue');
 const positionStore=new Map(),positionStorage={getItem:k=>positionStore.get(k),setItem:(k,v)=>positionStore.set(k,v)},positionSent=[];
 const positions=new Outbox(positionStorage,'positions',cmd=>{positionSent.push(cmd);return true},()=>{},()=>{});positions.enqueue('final',{token:{id:'a',x:10,y:10}});positions.enqueue('final',{token:{id:'a',x:20,y:20}});if(!positions.hasQueuedBehindInflight())throw Error('queued final did not block preview');positions.ack({seq:1});if(positions.hasQueuedBehindInflight())throw Error('preview remained blocked after queued final was sent');if(positionSent.length!==2||positionSent[1].seq!==2)throw Error('next final was not sent after ack');
 const d=new Drafts();d.set('a','name','one');d.set('a','name','two');d.confirm('a',{name:'one'});if(d.get('a').name!=='two')throw Error('ack erased newer draft');
 const writes=new CacheWriteBudget(2,10);const releaseA=writes.acquire(6);if(!releaseA||writes.acquire(5)!==null)throw Error('write byte budget');const releaseB=writes.acquire(4);if(!releaseB||writes.acquire(1)!==null)throw Error('write count budget');releaseA();const releaseC=writes.acquire(5);if(!releaseC||writes.bytes!==9)throw Error('write budget release');releaseB();releaseC();if(writes.count||writes.bytes)throw Error('write budget leaked');
 for(const count of [1,64,65,100,1000,2000]){const edge=imageEdge(count,64*1048576);if(count*edge*edge*4>64*1048576)throw Error('image budget exceeded')}
 return true;
})()`
