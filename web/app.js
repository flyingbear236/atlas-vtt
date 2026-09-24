import {SpatialIndex,ArtworkCache,decodedSize,fitImageSize,fitPlansToBudget,resolveImageMemoryBudgetMiB,planLRUEviction} from './rendering.js';
import {drawTokens} from './token-renderer.js';
import {Outbox, Drafts, CacheWriteBudget} from './reliability.js';
import {ScenesRuntime} from './scenes.js';
import {compositeFloors,drawSceneStack,drawTransitionOverlay,drawWalkableOverlay,elementHandleAt,elementsAtPoint,hitElement,inversePoint,orderedFloors,orderedLayers,transitionHit,transformedFromDrag,walkableBounds} from './scene-content.js';
import {SceneTreeRuntime} from './scene-tree.js';
const $ = id => document.getElementById(id);
const CONTENT_COMMANDS=new Set(['move','final','properties','create','delete','boundsUpdate','floorCreate','floorUpdate','floorDelete','layerCreate','layerUpdate','layerDelete','elementCreate','elementUpdate','elementDelete','elementPreview','elementTransform','transitionCreate','transitionUpdate','transitionDelete','assetRetention']);
const UPLOAD_LIMIT_BYTES=256*1024*1024;
const canvas = $('board'), ctx = canvas.getContext('2d');
const saveStatus=document.createElement('span');saveStatus.id='saveStatus';saveStatus.className='status';$('connection').after(saveStatus);
let storageDegraded='';
function commitStatus(message){saveStatus.textContent=message;saveStatus.classList.toggle('danger',!!storageDegraded);saveStatus.style.display=storageDegraded?'inline':'';}
function fatal(message){stopped=true;clearTimeout(reconnectTimer);clearTimeout(commandTimer);clearTimeout(positionCommandTimer);clearTimeout(saveRetry);clearTimeout(positionSaveRetry);socket?.close();$('connection').textContent=message;$('connection').className='status';commitStatus('Восстановление остановлено');toast(message);}
function updateCommandStatus(){if(storageDegraded){commitStatus(storageDegraded);return;}const count=(outbox?.data.queue.length||0)+(positionOutbox?.data.queue.length||0);commitStatus(count?`Ожидают подтверждения: ${count}`:'Изменения приняты');}
function initOutbox(){
  try {
    const make=(key,position)=>new Outbox(sessionStorage,key,cmd=>{
      if(!send(cmd))return false;
      if(position){clearTimeout(positionCommandTimer);positionCommandTimer=setTimeout(()=>socket?.close(),8000);}
      else{clearTimeout(commandTimer);commandTimer=setTimeout(()=>socket?.close(),8000);}
      return true;
    },updateCommandStatus,(cmd,error)=>{
      dirty=true;
      if(error){
        toast(error);commitStatus(error);
        if(cmd.type==='transitionCreate'&&transitionDraft?.phase==='awaitingCreate'){transitionDraft={...transitionDraft,phase:'editingB'};delete transitionDraft.priorIds;fillTransitionProperties();}
        if(cmd.type==='transitionUpdate')send({type:'sync'});
      }else if(cmd.type==='properties'){drafts.confirm(cmd.token.id,cmd.properties);if(cmd.sceneId===state?.scene?.id)fillProperties();}
    });
    outbox=make(`atlas-outbox/${credentials.session}/${credentials.key}`,false);
    positionOutbox=make(`atlas-position-outbox/${credentials.session}/${credentials.key}`,true);
    updateCommandStatus();
  }catch(e){fatal('Не удалось прочитать очередь команд браузера');}
}
function queueCommand(type,payload){if(stopped||!outbox)return false;if(CONTENT_COMMANDS.has(type)){if(!state?.scene?.id)return false;payload={...payload,sceneId:state.scene.id};}try{outbox.enqueue(type,payload);return true;}catch(e){fatal('Не удалось сохранить команду в браузере. Изменение не отправлено');return false;}}
function queueTransform(type,payload){if(stopped||!positionOutbox||!state?.scene?.id)return false;try{positionOutbox.enqueue(type,{...payload,sceneId:state.scene.id});return true;}catch(e){fatal('Не удалось сохранить положение в браузере. Изменение не отправлено');return false;}}
function queuePosition(payload){return queueTransform('final',payload);}
for(const [id,field] of [['tokenName','name'],['tokenSize','size'],['tokenColor','color'],['tokenOwner','owner'],['tokenHidden','hidden'],['tokenFloor','floorId']]){
  $(id).addEventListener('input',()=>{if(selected)drafts.set(selected,field,id==='tokenHidden'?$(id).checked:id==='tokenSize'?Number($(id).value):$(id).value);});
}
let credentials, state, campaign, desiredSceneID='', socket, reconnectTimer, selected, selectedElement, selectedTransition='', transitionDraft=null, transitionCursor=null, currentFloorID='', pendingActiveFocus='', drag, inviteCode, outbox, positionOutbox, commandTimer, positionCommandTimer, saveRetry, positionSaveRetry, outboxNeedsResume=true; const drafts=new Drafts();
const uploadControllers=new Set();
const scenes=new ScenesRuntime({homeAdd:$('homeSceneAdd'),back:$('toScenes'),home:$('campaignHome'),workspace:$('workspace'),footer:$('appFooter'),cards:$('sceneCards'),empty:$('emptyScenes'),campaignName:$('campaignName'),sceneTabs:$('sceneTabs')},{open:openScene,home:goCampaignHome,queue:queueCommand});
const sceneTree=new SceneTreeRuntime({tree:$('sceneTree'),addFloor:$('addFloor'),addLayer:$('addLayer'),boundsWidth:$('boundsWidth'),boundsHeight:$('boundsHeight'),boundsSave:$('boundsSave'),addTransition:$('addTransition'),controls:[$('addFloor').parentElement,$('boundsSave').parentElement,$('mapUpload').parentElement]},{queue:queueCommand,selectElement,selectTransition,beginTransition,setFloor,getFloor:()=>currentFloorID});
let camera = {x: 0, y: 0, scale: .75}, viewport = {w: 1, h: 1}, grid = true;
const REGION_PREFETCH=.5,MAX_REGION_SPAN=524288,INITIAL_SCENE_SCALE=.75,INITIAL_REGION_WORLD_SPAN=6144,VIEWPORT_SAVE_INTERVAL=10000;let regionInFlight=false,sceneCameraInitialized=false,viewportSaveTimer,viewportSaveDirty=false,viewportLastSave=0;
let dirty = true, lastFrame = 0, frameTime = 0, rendered = 0, fps = 0, lastMove = 0;
const SIDEBAR_WIDTH_KEY='atlas-sidebar-width-v1',SIDEBAR_COLLAPSED_KEY='atlas-sidebar-collapsed-v1';
const sidebarFrame=$('sidebarFrame'),sidebarResize=$('sidebarResize'),sidebarToggle=$('sidebarToggle');
const sidebarSections={map:{button:$('mapTab'),panel:$('sidebarMap')},objects:{button:$('objectsTab'),panel:$('sidebarObjects')},members:{button:$('membersTab'),panel:$('sidebarMembers')}};
let sidebarWidth=250,sidebarDrag=null;
function sidebarLimits(){const width=window.innerWidth;return {min:width<=560?140:width<=760?170:190,max:Math.max(220,Math.min(420,Math.floor(width*.45)))};}
function storeSidebarSetting(key,value){try{localStorage.setItem(key,value);}catch{}}
function setSidebarWidth(value,persist=true){const {min,max}=sidebarLimits();sidebarWidth=Math.max(min,Math.min(max,Number(value)||250));sidebarFrame.style.setProperty('--sidebar-width',`${sidebarWidth}px`);if(persist)storeSidebarSetting(SIDEBAR_WIDTH_KEY,String(Math.round(sidebarWidth)));}
function setSidebarCollapsed(collapsed,persist=true){sidebarFrame.classList.toggle('collapsed',collapsed);if(!collapsed)setSidebarWidth(sidebarWidth,false);sidebarToggle.textContent=collapsed?'›':'‹';const action=collapsed?'Развернуть':'Свернуть';sidebarToggle.title=`${action} левую панель`;sidebarToggle.setAttribute('aria-label',`${action} левую панель`);if(persist)storeSidebarSetting(SIDEBAR_COLLAPSED_KEY,collapsed?'1':'0');dirty=true;}
function showSidebarSection(name,expand=false){for(const [key,section] of Object.entries(sidebarSections)){const active=key===name;section.panel.hidden=!active;section.button.classList.toggle('active',active);section.button.setAttribute('aria-pressed',String(active));}if(expand&&sidebarFrame.classList.contains('collapsed'))setSidebarCollapsed(false);}
for(const [name,section] of Object.entries(sidebarSections))section.button.onclick=()=>showSidebarSection(name,true);
sidebarToggle.onclick=()=>setSidebarCollapsed(!sidebarFrame.classList.contains('collapsed'));
try{const saved=Number(localStorage.getItem(SIDEBAR_WIDTH_KEY));if(Number.isFinite(saved)&&saved>0)sidebarWidth=saved;}catch{}
setSidebarWidth(sidebarWidth,false);
try{setSidebarCollapsed(localStorage.getItem(SIDEBAR_COLLAPSED_KEY)==='1',false);}catch{setSidebarCollapsed(false,false);}
showSidebarSection('map');
sidebarResize.onpointerdown=e=>{if(e.button!==0)return;sidebarDrag={pointerId:e.pointerId,startX:e.clientX,startWidth:sidebarFrame.getBoundingClientRect().width,collapse:false};sidebarResize.setPointerCapture(e.pointerId);document.body.classList.add('sidebar-resizing');e.preventDefault();};
sidebarResize.onpointermove=e=>{if(!sidebarDrag||sidebarDrag.pointerId!==e.pointerId)return;const raw=sidebarDrag.startWidth+e.clientX-sidebarDrag.startX,{min}=sidebarLimits();sidebarDrag.collapse=raw<min;setSidebarWidth(Math.max(raw,min),false);};
function finishSidebarDrag(e){if(!sidebarDrag||sidebarDrag.pointerId!==e.pointerId)return;const {collapse,startWidth}=sidebarDrag;sidebarDrag=null;document.body.classList.remove('sidebar-resizing');if(collapse){setSidebarWidth(startWidth,false);setSidebarCollapsed(true);}else{setSidebarWidth(sidebarWidth);setSidebarCollapsed(false);}}
sidebarResize.onpointerup=finishSidebarDrag;sidebarResize.onpointercancel=finishSidebarDrag;
sidebarResize.onkeydown=e=>{if(e.key==='ArrowLeft'){const {min}=sidebarLimits();if(sidebarWidth<=min)setSidebarCollapsed(true);else setSidebarWidth(sidebarWidth-16);e.preventDefault();}else if(e.key==='ArrowRight'){setSidebarCollapsed(false);setSidebarWidth(sidebarWidth+16);e.preventDefault();}};
window.addEventListener('resize',()=>{if(!sidebarFrame.classList.contains('collapsed'))setSidebarWidth(sidebarWidth,false);});
let bytesIn = 0, bytesOut = 0, tileBytes = 0, stopped = false, toastTimer, retry = 0;
let wanted = new Set(), activeLoads = 0, cacheHits = 0;
const visuals = new Map(), memory = new Map(), failures = new Map(), pending = new Map();
const MIB=1024*1024,DISK_LIMIT=128*MIB,IMAGE_MEMORY_SETTING_KEY='atlas-image-memory-budget-v1',IMAGE_MEMORY_SETTINGS=new Set(['auto','64','128','256','512']);
const DISK_WRITE_QUEUE_LIMIT=32*1024*1024,DISK_WRITE_QUEUE_COUNT=64;
const ARTWORK_LIMIT=8*MIB;const tokenIndex=new SpatialIndex(),movingTokens=new Set(),artwork=new ArtworkCache(ARTWORK_LIMIT);
function readImageMemorySetting(){try{const value=localStorage.getItem(IMAGE_MEMORY_SETTING_KEY)||'auto';return IMAGE_MEMORY_SETTINGS.has(value)?value:'auto';}catch{return 'auto';}}
let imageMemorySetting=readImageMemorySetting(),memoryLimit=resolveImageMemoryBudgetMiB(navigator.deviceMemory,imageMemorySetting)*MIB,assetLimit=memoryLimit-ARTWORK_LIMIT;
const tokenRows=new Map(), diskTouches=new Map();
let memoryBytes = 0, diskBytes = 0, diskEnabled = true, cacheQueue = Promise.resolve();
let diskReads=0,diskWrites=0,diskEvictions=0,diskDrops=0,decodeCount=0,decodeMs=0;
const diskWriteBudget=new CacheWriteBudget(DISK_WRITE_QUEUE_COUNT,DISK_WRITE_QUEUE_LIMIT);
const dbPromise = new Promise(resolve => {
  if (!globalThis.indexedDB) { diskEnabled = false; resolve(null); return; }
  const req = indexedDB.open('atlas-assets-v1', 1);
  req.onupgradeneeded = () => req.result.createObjectStore('assets', {keyPath:'key'}).createIndex('used', 'used');
  req.onsuccess = () => resolve(req.result);
  req.onerror = () => { diskEnabled = false; resolve(null); };
});
async function measureDisk(){
  const db=await dbPromise;if(!db)return;
  await new Promise(resolve=>{const tx=db.transaction('assets'),req=tx.objectStore('assets').openCursor();let total=0;
    req.onsuccess=()=>{const cursor=req.result;if(cursor){total+=cursor.value.size||cursor.value.blob?.size||0;cursor.continue();}else diskBytes=total;};
    tx.oncomplete=tx.onerror=tx.onabort=()=>resolve();
  });
}
const diskReady=measureDisk();
async function diskGet(key) {
  const db = await dbPromise; if (!db || !diskEnabled) return null;
  await diskReady;diskReads++;
  const now=Date.now(),touch=(diskTouches.get(key)||0)<now-60000;diskTouches.set(key,now);
  return new Promise(resolve => { const tx = db.transaction('assets',touch?'readwrite':'readonly'), store = tx.objectStore('assets'), req = store.get(key);
    let blob = null;
    req.onsuccess = () => { if(req.result){blob=req.result.blob;if(touch)store.put({...req.result,used:now});} };
    tx.oncomplete = () => resolve(blob); tx.onerror = tx.onabort = () => resolve(null);
  });
}
function diskPut(key, blob) {
  const release=diskWriteBudget.acquire(blob.size);if(!release){diskDrops++;return;}
  cacheQueue = cacheQueue.then(async () => {
    const db = await dbPromise; if (!db || !diskEnabled) return;await diskReady;
    await new Promise(resolve => {const tx = db.transaction('assets','readwrite'), store = tx.objectStore('assets'), get=store.get(key);let total=diskBytes;
      get.onsuccess=()=>{const old=get.result;total+=blob.size-(old?.size||old?.blob?.size||0);store.put({key,blob,size:blob.size,used:Date.now()});diskWrites++;
        if(total<=DISK_LIMIT)return;
        const cursorReq=store.index('used').openCursor();cursorReq.onsuccess=()=>{const cursor=cursorReq.result;if(!cursor||total<=DISK_LIMIT)return;const row=cursor.value;if(row.key!==key){total-=row.size||row.blob?.size||0;cursor.delete();diskEvictions++;}cursor.continue();};
      };
      tx.oncomplete=()=>{diskBytes=Math.max(0,total);resolve();};tx.onerror=tx.onabort=()=>{diskEnabled=false;resolve();};
    });
  }).catch(()=>{diskEnabled=false;}).finally(release);
}
function toast(message) { $('toast').textContent=message;$('toast').hidden=false;clearTimeout(toastTimer);toastTimer=setTimeout(()=>$('toast').hidden=true,5000); }
function applyImageMemorySetting(value,persist=true){
  if(!IMAGE_MEMORY_SETTINGS.has(value))value='auto';
  imageMemorySetting=value;memoryLimit=resolveImageMemoryBudgetMiB(navigator.deviceMemory,value)*MIB;assetLimit=memoryLimit-ARTWORK_LIMIT;
  const budgetSelect=$('imageCacheBudget'),autoBudget=resolveImageMemoryBudgetMiB(navigator.deviceMemory,'auto');
  budgetSelect.querySelector('option[value="auto"]').textContent=`Auto (${autoBudget} MiB)`;budgetSelect.value=value;budgetSelect.title=value==='auto'?`Auto: ${memoryLimit/MIB} MiB`:`Лимит: ${memoryLimit/MIB} MiB`;
  if(persist){try{localStorage.setItem(IMAGE_MEMORY_SETTING_KEY,value);}catch{toast('Браузер не сохранил настройку RAM-кэша.');}}
  trimImages();dirty=true;
}
applyImageMemorySetting(imageMemorySetting,false);
$('imageCacheBudget').onchange=()=>applyImageMemorySetting($('imageCacheBudget').value);
function sessions(){try{return JSON.parse(localStorage.getItem('atlas-sessions')||'[]');}catch{return [];}}
function remember(c){const list=sessions().filter(x=>x.session!==c.session);list.unshift(c);try{localStorage.setItem('atlas-sessions',JSON.stringify(list.slice(0,12)));}catch{toast('Браузер не сохранил ключ доступа. Не закрывайте вкладку.');}}
async function api(path,body,raw=false,signal) {
  const headers={};if(credentials)headers.Authorization=`Bearer ${credentials.key}`;
  if(!raw)headers['Content-Type']='application/json';
  const res=await fetch(path,{method:'POST',headers,body:raw?body:JSON.stringify(body),signal});
  if(!res.ok)throw new Error(await res.text());return res.json();
}
const invitation=new URLSearchParams(location.hash.slice(1));
if(invitation.has('invite')){$('nameLabel').textContent='Ваше имя';$('sessionName').placeholder='Как вас зовут?';$('startButton').textContent='Присоединиться к столу ↗';}
for(const saved of sessions()){const row=document.createElement('div');row.className='saved-campaign';const button=document.createElement('button');button.textContent=`Продолжить: ${saved.name||'Игровой стол'}`;button.onclick=()=>enter(saved);const remove=document.createElement('button');remove.className='danger';remove.textContent='Убрать';remove.title='Убрать вход только из этого браузера';remove.onclick=()=>{const list=sessions().filter(item=>item.session!==saved.session);localStorage.setItem('atlas-sessions',JSON.stringify(list));row.remove();};row.append(button,remove);$('resumeList').append(row);}
$('start').onsubmit=async e=>{e.preventDefault();$('startButton').disabled=true;try{const name=$('sessionName').value.trim();let c;if(invitation.has('invite'))c=await api('/api/join',{session:invitation.get('session'),invite:invitation.get('invite'),name});else c=await api('/api/sessions',{name});c.name=name||'Новая история';remember(c);enter(c);}catch(e){toast(e.message);}finally{$('startButton').disabled=false;}};
function enter(c){credentials=c;stopped=false;initOutbox();if(stopped)return;inviteCode=c.invite;$('lobby').hidden=true;$('app').hidden=false;resize();connect();}
function send(value){
 if(socket?.readyState!==WebSocket.OPEN)return false;
 if(CONTENT_COMMANDS.has(value.type)&&!value.sceneId){if(!state?.scene?.id)return false;value={...value,sceneId:state.scene.id};}
 if(value.type==='move'){value={...value,client:positionOutbox.data.client,after:positionOutbox.data.seq};}
 const message=JSON.stringify(value);bytesOut+=message.length;socket.send(message);return true;
}
function viewportStorageKey(sceneID){return `atlas-viewport-v1/${credentials.session}/${credentials.key}/${sceneID}`;}
function activeTokenStorageKey(sceneID){return `atlas-active-token-v1/${credentials.session}/${credentials.key}/${sceneID}`;}
function readSavedActiveToken(sceneID){try{return localStorage.getItem(activeTokenStorageKey(sceneID))||'';}catch{return '';}}
function rememberActiveToken(sceneID,tokenID){try{if(tokenID)localStorage.setItem(activeTokenStorageKey(sceneID),tokenID);else localStorage.removeItem(activeTokenStorageKey(sceneID));}catch{}}
function readSavedViewport(sceneID,mapID){try{const saved=JSON.parse(localStorage.getItem(viewportStorageKey(sceneID))||'null');if(!saved||saved.map!==(mapID||'')||![saved.cx,saved.cy,saved.scale].every(Number.isFinite)||saved.scale<.015||saved.scale>4)return null;return saved;}catch{return null;}}
function saveViewportNow(){clearTimeout(viewportSaveTimer);viewportSaveTimer=null;if(!viewportSaveDirty||!state?.scene?.id||!credentials)return;const scale=camera.scale,cx=camera.x+viewport.w/(2*scale),cy=camera.y+viewport.h/(2*scale);try{localStorage.setItem(viewportStorageKey(state.scene.id),JSON.stringify({cx,cy,scale,map:''}));viewportSaveDirty=false;viewportLastSave=Date.now();}catch{}}
function scheduleViewportSave(){if(!state?.scene?.id)return;viewportSaveDirty=true;if(viewportSaveTimer)return;const delay=Math.max(0,VIEWPORT_SAVE_INTERVAL-(Date.now()-viewportLastSave));viewportSaveTimer=setTimeout(()=>{viewportSaveTimer=null;saveViewportNow();},delay);}
function initializeSceneCamera(snapshot){
 const saved=readSavedViewport(snapshot.scene.id,'');let cx,cy,scale;
 if(saved){({cx,cy,scale}=saved);}else{const bounds=snapshot.scene.bounds,entry=snapshot.entry,fitScale=Math.min(2,(viewport.w-70)/bounds.width,(viewport.h-70)/bounds.height),regionScale=Math.max(viewport.w,viewport.h)*(1+REGION_PREFETCH*2)/INITIAL_REGION_WORLD_SPAN;scale=Math.min(2,Math.max(INITIAL_SCENE_SCALE,regionScale,fitScale));cx=entry?.x??bounds.width/2;cy=entry?.y??bounds.height/2;}
 camera={x:cx-viewport.w/(2*scale),y:cy-viewport.h/(2*scale),scale};viewportLastSave=Date.now();viewportSaveDirty=true;scheduleViewportSave();dirty=true;
}
window.addEventListener('pagehide',saveViewportNow);
function regionContains(region,view){return !!region&&region.left<=view.left&&region.top<=view.top&&region.right>=view.right&&region.bottom>=view.bottom;}
function boundedRegion(view){
 const width=view.right-view.left,height=view.bottom-view.top,cx=(view.left+view.right)/2,cy=(view.top+view.bottom)/2;
 const targetWidth=Math.min(MAX_REGION_SPAN,width*(1+REGION_PREFETCH*2)),targetHeight=Math.min(MAX_REGION_SPAN,height*(1+REGION_PREFETCH*2));
 return {left:cx-targetWidth/2,top:cy-targetHeight/2,right:cx+targetWidth/2,bottom:cy+targetHeight/2};
}
function regionTooLarge(region,view){return !!region&&((region.right-region.left)>(view.right-view.left)*4||(region.bottom-region.top)>(view.bottom-view.top)*4);}
function ensureSceneRegion(view){
 if(!state?.scene?.id||!view)return;
 const loadedEnough=regionContains(state.region,view)&&!regionTooLarge(state.region,view);
 if(loadedEnough||regionInFlight)return;
 const region=boundedRegion(view);if(send({type:'view',sceneId:state.scene.id,viewFloorId:currentFloorID,region}))regionInFlight=true;
}
function currentViewRegion(){return {left:camera.x,top:camera.y,right:camera.x+viewport.w/camera.scale,bottom:camera.y+viewport.h/camera.scale};}
function pruneAssetMetadata(assetID){if(!assetID)return;for(const token of Object.values(state?.tokens||{}))if(token.asset===assetID)return;for(const element of Object.values(state?.elements||{}))if(element.assetId===assetID)return;delete state.assets?.[assetID];}
function openScene(sceneID){if(!sceneID||state?.scene?.id===sceneID)return;desiredSceneID=sceneID;clearSceneState();$('connection').textContent='Открываем сцену…';send({type:'subscribe',sceneId:sceneID,activeTokenId:readSavedActiveToken(sceneID)});}
function goCampaignHome(){desiredSceneID='';clearSceneState();if(campaign)scenes.showHome();send({type:'subscribe',sceneId:''});}
function connect(){
 if(stopped)return;saveViewportNow();clearTimeout(reconnectTimer);storageDegraded='';updateCommandStatus();$('connection').textContent='Подключение…';$('connection').className='status';state=null;regionInFlight=false;sceneCameraInitialized=false;outboxNeedsResume=true;
 socket=new WebSocket(`${location.protocol==='https:'?'wss':'ws'}://${location.host}/ws`);
 socket.onopen=()=>socket.send(JSON.stringify(credentials));
 socket.onmessage=e=>{
  bytesIn+=e.data.length;const msg=JSON.parse(e.data);
  if(msg.type==='fatal'){fatal(msg.message);return;}
  if(msg.type==='ack'){const position=msg.client===positionOutbox?.data.client,target=position?positionOutbox:outbox;clearTimeout(position?positionCommandTimer:commandTimer);try{target.ack(msg);}catch(e){fatal('Не удалось сохранить очередь команд в браузере');}return;}
  if(msg.type==='saveError'){const position=msg.client===positionOutbox?.data.client,target=position?positionOutbox:outbox;clearTimeout(position?positionCommandTimer:commandTimer);commitStatus(msg.message);if(position){clearTimeout(positionSaveRetry);positionSaveRetry=setTimeout(()=>target.retry(),2000);}else{clearTimeout(saveRetry);saveRetry=setTimeout(()=>target.retry(),2000);}return;}
  if(msg.type==='storageError'){const wasDegraded=!!storageDegraded;storageDegraded=msg.message||'Сервер временно не может сохранять изменения на диск';updateCommandStatus();if(!wasDegraded)toast(storageDegraded);return;}
  if(msg.type==='storageRecovered'){storageDegraded='';updateCommandStatus();return;}
  if(msg.type==='presence'&&state){
   if(msg.member)state.members[msg.member.id]=msg.member;
   if(msg.member)state.online[msg.member.id]=!!msg.online;
   renderMembers();if(selected)fillProperties();return;
  }
  if(msg.type==='campaignSnapshot'){
   campaign=msg;scenes.update(campaign);
   retry=0;$('connection').textContent='● В сети';$('connection').className='status connected';$('sessionTitle').textContent=msg.name;$('roleBadge').textContent=msg.you.role==='gm'?'ВЕДУЩИЙ':'ИГРОК';document.querySelectorAll('.gm').forEach(el=>el.hidden=msg.you.role!=='gm');
   if(outboxNeedsResume){outboxNeedsResume=false;outbox.reconnect();positionOutbox.reconnect();}
   const active=state?.scene?.id,summary=msg.scenes.find(scene=>scene.id===active),accessible=id=>msg.scenes.some(scene=>scene.id===id);
   if(active&&summary){state.scene={...state.scene,name:summary.name,published:summary.published};$('sceneLabel').textContent=summary.name;scenes.showScene();return;}
   if(active&&!accessible(active))desiredSceneID='';
   if(!state&&desiredSceneID&&accessible(desiredSceneID)){const target=desiredSceneID;desiredSceneID='';openScene(target);return;}
   desiredSceneID='';clearSceneState();scenes.showHome();
   return;
  }
  if(msg.type==='snapshot'){
   if(!desiredSceneID||msg.scene.id!==desiredSceneID)return;
	  const draggedFloor=drag?.type==='token'?state?.tokens?.[drag.id]?.floorId:'';const initialRegion=msg.region==null;state=msg;if(!initialRegion)regionInFlight=false;desiredSceneID=msg.scene.id;scenes.update(campaign);scenes.showScene();retry=0;
   $('connection').textContent='● В сети';$('connection').className='status connected';$('sessionTitle').textContent=msg.name;$('roleBadge').textContent=msg.you.role==='gm'?'ВЕДУЩИЙ':'ИГРОК';document.querySelectorAll('.gm').forEach(el=>el.hidden=msg.you.role!=='gm');
   if(!sceneCameraInitialized){initializeSceneCamera(msg);sceneCameraInitialized=true;}
	  state.ownedTokens=state.ownedTokens||{};currentFloorID=msg.currentFloorId||orderedFloors(state)[0]?.id||'';if(state.you.role==='player')rememberActiveToken(state.scene.id,state.activeTokenId||'');resolveCreatedTransition();if(selectedTransition&&!state.transitions?.[selectedTransition])selectedTransition='';if(drag?.type==='token'&&draggedFloor&&state.tokens?.[drag.id]?.floorId!==draggedFloor)drag=null;
   if(pendingActiveFocus&&pendingActiveFocus===state.activeTokenId){const token=navigationToken(pendingActiveFocus);if(token){camera.x=token.x-viewport.w/(2*camera.scale);camera.y=token.y-viewport.h/(2*camera.scale);scheduleViewportSave();}pendingActiveFocus='';}
   for(const id of visuals.keys())if(!state.tokens[id])visuals.delete(id);
	  rebuildTokenIndex();if(selected&&!state.tokens[selected])select(null);if(selectedElement&&!state.elements?.[selectedElement])selectElement(null);renderPanels();dirty=true;ensureSceneRegion(currentViewRegion());if(!outbox.inflight)outbox.flush();if(!positionOutbox.inflight)positionOutbox.flush();return;
  }
  if(msg.type==='error'){regionInFlight=false;pendingActiveFocus='';toast(msg.message);drag=null;if(!state&&desiredSceneID){desiredSceneID='';scenes.showHome();}return;}
  if(!state)return;
  if(msg.sceneId!==state.scene.id)return;
  if(!Number.isSafeInteger(msg.delivery)){send({type:'sync'});return;}
   if(msg.delivery<=state.delivery)return;
   if(msg.delivery!==state.delivery+1){send({type:'sync'});return;}
   if(msg.type.startsWith('element')){
    const old=state.elements?.[msg.id];let next=msg.element;if(msg.asset?.id)state.assets[msg.asset.id]=msg.asset;if(msg.type==='elementTransform'){if(!old){send({type:'sync'});return;}next={...old,transform:msg.transform};}
    state.revision=msg.revision;state.delivery=msg.delivery;
    if(msg.type==='elementDelete'){delete state.elements[msg.id];if(selectedElement===msg.id)selectElement(null);if(old?.assetId)pruneAssetMetadata(old.assetId);}else if(next)state.elements[next.id]=next;
    renderPanels();dirty=true;return;
   }
   const old=state.tokens[msg.id];let next=msg.token;if(msg.asset?.id)state.assets[msg.asset.id]=msg.asset;
  if(msg.type==='move'){
   if(!old){send({type:'sync'});return;}
   next={...old,x:msg.x,y:msg.y};
  }
   const propertiesChanged=!old||!next||old.name!==next.name||old.size!==next.size||old.rotation!==next.rotation||old.floorId!==next.floorId||old.color!==next.color||old.owner!==next.owner||old.hidden!==next.hidden||old.asset!==next.asset;
  state.revision=msg.revision;
  state.delivery=msg.delivery;
  if(state.you.role==='player'){
   const prior=state.ownedTokens?.[msg.id];if(msg.type==='delete'||(next&&next.owner!==state.you.id)||next?.hidden){delete state.ownedTokens[msg.id];}else if(next?.owner===state.you.id){state.ownedTokens[msg.id]={...next,asset:''};}else if(msg.type==='move'&&prior){state.ownedTokens[msg.id]={...prior,x:msg.x,y:msg.y};}
  }
	  if(drag?.type==='token'&&msg.id===drag.id&&msg.type==='upsert'&&old&&(next?.floorId!==old.floorId||next?.x!==drag.x||next?.y!==drag.y))drag=null;
	  if(msg.type==='delete'){
   delete state.tokens[msg.id];visuals.delete(msg.id);tokenIndex.remove(msg.id);movingTokens.delete(msg.id);removeTokenRow(msg.id);
   }else if(next){
    state.tokens[msg.id]=next;tokenIndex.set(next);if(!old||old.x!==next.x||old.y!==next.y)movingTokens.add(msg.id);if(state.you.role==='player'){const listed=state.ownedTokens[msg.id];if(listed)updateTokenRow(listed);else removeTokenRow(msg.id);}else if(next.floorId===currentFloorID&&propertiesChanged)updateTokenRow(next);else if(next.floorId!==currentFloorID)removeTokenRow(next.id);
  }
  $('revision').textContent=`Ревизия ${state.revision}`;
  if(propertiesChanged&&selected===msg.id)fillProperties();
  if(old?.asset&&old.asset!==next?.asset)pruneAssetMetadata(old.asset);
  if(selected&&!state.tokens[selected])select(null);
  dirty=true;
 };
 socket.onclose=()=>{clearTimeout(commandTimer);clearTimeout(positionCommandTimer);abortUploads();if(stopped)return;outbox.inflight=false;positionOutbox.inflight=false;if(drag?.type==='token'||drag?.type==='element')endDrag();drag=null;dirty=true;$('connection').textContent='● Нет связи · повтор…';$('connection').className='status';reconnectTimer=setTimeout(connect,Math.min(1000*2**retry++,10000));};
 socket.onerror=()=>socket.close();
}
$('leaveCampaign').onclick=()=>{saveViewportNow();stopped=true;socket?.close();location.href='/';};
$('invite').onclick=async()=>{const url=`${location.origin}/#session=${credentials.session}&invite=${inviteCode}`;if(!inviteCode){toast('Приглашение доступно в исходной вкладке ведущего.');return;}try{await navigator.clipboard.writeText(url);toast('Ссылка для игроков скопирована');}catch{prompt('Скопируйте ссылку для игроков',url);}};
function canMove(t){return state&&(state.you.role==='gm'||(t.owner===state.you.id&&!t.hidden));}
function navigationToken(id){return state?.tokens?.[id]||state?.ownedTokens?.[id];}
function activatePlayerToken(token,focus){
 if(!token||state?.you.role!=='player'||token.owner!==state.you.id||token.hidden)return false;
 if(focus){pendingActiveFocus=token.id;camera.x=token.x-viewport.w/(2*camera.scale);camera.y=token.y-viewport.h/(2*camera.scale);scheduleViewportSave();}
 state.activeTokenId=token.id;state.currentFloorId=token.floorId;currentFloorID=token.floorId;state.movementBounds=null;rememberActiveToken(state.scene.id,token.id);select(token.id);
 const region=boundedRegion(currentViewRegion());if(send({type:'activeToken',sceneId:state.scene.id,activeTokenId:token.id,focus,region}))regionInFlight=true;
 renderPanels();dirty=true;return true;
}
function abortUploads(){for(const controller of uploadControllers)controller.abort();uploadControllers.clear();}
function clearSceneState(){saveViewportNow();clearTimeout(viewportSaveTimer);viewportSaveTimer=null;viewportSaveDirty=false;abortUploads();select(null);selectElement(null);selectedTransition='';transitionDraft=null;transitionCursor=null;state=null;currentFloorID='';pendingActiveFocus='';regionInFlight=false;sceneCameraInitialized=false;tokenIndex.clear();visuals.clear();movingTokens.clear();for(const id of [...tokenRows.keys()])removeTokenRow(id);wanted.clear();imagePlans.clear();for(const controller of pending.values())controller.abort();$('emptyMap').hidden=false;dirty=true;}
function rebuildTokenIndex(){tokenIndex.clear();movingTokens.clear();for(const t of Object.values(state.tokens)){tokenIndex.set(t);const v=visuals.get(t.id);if(v&&(v.x!==t.x||v.y!==t.y))movingTokens.add(t.id);}}
function renderMembers(){
 $('members').replaceChildren();let online=0;
 for(const m of Object.values(state.members)){const row=document.createElement('div');row.className='member';const dot=document.createElement('span');dot.className='live-dot'+(state.online[m.id]?'':' offline');const name=document.createElement('span');name.textContent=m.name;const role=document.createElement('small');role.textContent=m.role==='gm'?'ведущий':'игрок';row.append(dot,name,role);$('members').append(row);if(state.online[m.id])online++;}
 $('memberCount').textContent=online;
}
function renderPanels(){
 const gm=state.you.role==='gm';$('mapTab').hidden=!gm;if(!gm&&!sidebarSections.map.panel.hidden)showSidebarSection('objects');
 $('noSelection').querySelector('p').innerHTML=gm?'Выберите токен,<br>элемент или переход':'Выберите токен';
 $('emptyMap').hidden=!!Object.keys(state.elements||{}).length;$('sceneLabel').textContent=state.floors?.[currentFloorID]?`${state.scene.name} · ${state.floors[currentFloorID].name}`:state.scene.name;
	 renderMembers();renderTokenList();fillProperties();fillElementProperties();fillTransitionProperties();sceneTree.update(state,selectedElement,selectedTransition);
}
function createTokenRow(id){
 const row=document.createElement('button');row.dataset.tokenId=id;const swatch=document.createElement('span');swatch.className='swatch';const name=document.createElement('span');name.className='row-name';const extra=document.createElement('small');row.append(swatch,name,extra);
 row.onclick=()=>{const current=navigationToken(id);if(!current)return;if(activatePlayerToken(current,true))return;select(id);camera.x=current.x-viewport.w/(2*camera.scale);camera.y=current.y-viewport.h/(2*camera.scale);scheduleViewportSave();dirty=true;};
 tokenRows.set(id,row);$('tokens').append(row);return row;
}
function updateTokenRow(t){
 let row=tokenRows.get(t.id);if(!row)row=createTokenRow(t.id);
 row.className='token-row'+(t.id===selected?' selected':'')+(t.id===state?.activeTokenId?' active':'');const [swatch,name,extra]=row.children;swatch.style.background=t.color;swatch.textContent=t.name.slice(0,1)||'◆';name.textContent=t.name;extra.textContent=t.hidden?'◌':t.id===state?.activeTokenId?'●':canMove(t)?'↗':'';
 $('emptyTokens').hidden=tokenRows.size>0;
}
function removeTokenRow(id){const row=tokenRows.get(id);if(row){row.remove();tokenRows.delete(id);}$('emptyTokens').hidden=tokenRows.size>0;}
function renderTokenList(){
 if(!state)return;$('revision').textContent=`Ревизия ${state.revision}`;const listed=state.you.role==='player'?Object.values(state.ownedTokens||{}):Object.values(state.tokens).filter(t=>t.floorId===currentFloorID),ids=new Set(listed.map(t=>t.id));
 for(const id of tokenRows.keys())if(!ids.has(id))removeTokenRow(id);
 for(const t of listed)updateTokenRow(t);
 $('emptyTokens').hidden=ids.size>0;
}
function hasTransitionSelection(){return state?.you.role==='gm'&&(!!transitionDraft||!!state?.transitions?.[selectedTransition]);}
function updateInspectorState(){const visible=!!state?.tokens?.[selected]||!!state?.elements?.[selectedElement]||hasTransitionSelection();$('noSelection').hidden=visible;document.querySelector('.inspector').classList.toggle('has-selection',visible);}
function cancelTransitionDraft(){transitionDraft=null;transitionCursor=null;if(drag?.type?.startsWith('transition'))drag=null;fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition);dirty=true;}
function beginTransition(){if(state?.you.role!=='gm')return;select(null);selectElement(null);selectedTransition='';transitionDraft={phase:'placingA',endpointA:null,endpointB:null,direction:'bidirectional'};transitionCursor=null;fillTransitionProperties();sceneTree.update(state,selectedElement,selectedTransition);canvas.focus();toast('Поставьте точку A перехода');dirty=true;}
function select(id){const previous=selected;selected=id;if(id){selectedElement=null;selectedTransition='';transitionDraft=null;}if(previous&&tokenRows.has(previous))tokenRows.get(previous).classList.remove('selected');if(id&&tokenRows.has(id))tokenRows.get(id).classList.add('selected');fillProperties();fillElementProperties();fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition);dirty=true;}
function selectElement(id){if(id&&state?.you.role!=='gm')return;if(id){select(null);selectedTransition='';transitionDraft=null;}selectedElement=id;fillProperties();fillElementProperties();fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition);dirty=true;}
function selectTransition(id){if(id&&state?.you.role!=='gm')return;select(null);selectedElement=null;transitionDraft=null;selectedTransition=id||'';fillProperties();fillElementProperties();fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition);dirty=true;}
function setFloor(id,preserveTransition=false){if(!state?.floors?.[id]||state.you.role!=='gm'||id===currentFloorID)return;if(!preserveTransition){select(null);selectElement(null);selectedTransition='';transitionDraft=null;}const region=boundedRegion(currentViewRegion());if(send({type:'view',sceneId:state.scene.id,viewFloorId:id,region}))regionInFlight=true;}
function fillProperties(){const source=state?.tokens[selected];const t=source?{...source,...drafts.get(selected)}:null;$('properties').hidden=!t;updateInspectorState();if(!t)return;$('tokenName').value=t.name;$('tokenSize').value=t.size;$('tokenColor').value=/^#[0-9a-f]{6}$/i.test(t.color)?t.color:'#c2d89b';$('tokenHidden').checked=t.hidden;$('tokenPreview').style.background=t.color;$('tokenPreview').textContent=t.name.slice(0,1);$('tokenOwner').replaceChildren();const add=(id,name)=>{const option=document.createElement('option');option.value=id;option.textContent=name;$('tokenOwner').append(option);};add('','Только ведущий');for(const m of Object.values(state.members))if(m.role==='player')add(m.id,m.name);$('tokenOwner').value=t.owner;$('tokenFloor').replaceChildren();for(const floor of orderedFloors(state)){const option=document.createElement('option');option.value=floor.id;option.textContent=floor.name;$('tokenFloor').append(option);}$('tokenFloor').value=t.floorId;for(const el of $('properties').querySelectorAll('input,select'))el.disabled=state.you.role!=='gm';}
function fillElementProperties(){const element=state?.you.role==='gm'?state?.elements?.[selectedElement]:null;$('elementProperties').hidden=!element;updateInspectorState();if(!element)return;const t=element.transform;$('elementName').value=element.name;$('elementX').value=t.x;$('elementY').value=t.y;$('elementWidth').value=t.width;$('elementHeight').value=t.height;$('elementRotation').value=t.rotation;$('elementOpacity').value=element.opacity;$('elementVisible').checked=element.visible;$('elementLocked').checked=element.locked;$('elementLayer').replaceChildren();for(const layer of orderedLayers(state,element.floorId).filter(layer=>layer.kind==='visual')){const option=document.createElement('option');option.value=layer.id;option.textContent=layer.name;$('elementLayer').append(option);}$('elementLayer').value=element.layerId;for(const control of $('elementProperties').querySelectorAll('input,select,button'))control.disabled=state.you.role!=='gm';}
function transitionForInspector(){return transitionDraft||state?.transitions?.[selectedTransition]||null;}
function fillTransitionProperties(){const transition=state?.you.role==='gm'?transitionForInspector():null;$('transitionProperties').hidden=!transition;updateInspectorState();if(!transition)return;const draft=!!transitionDraft,waiting=draft&&transition.phase==='awaitingCreate',a=transition.endpointA,b=transition.endpointB;$('transitionDirection').value=transition.direction||'bidirectional';for(const [select,endpoint]of [[$('transitionFloorA'),a],[$('transitionFloorB'),b]]){select.replaceChildren();for(const floor of orderedFloors(state)){const option=document.createElement('option');option.value=floor.id;option.textContent=floor.name;select.append(option);}select.value=endpoint?.floorId||currentFloorID;select.disabled=!endpoint||waiting;}$('transitionRadiusA').value=a?.radius??'';$('transitionRadiusB').value=b?.radius??'';$('transitionRadiusA').disabled=!a||waiting;$('transitionRadiusB').disabled=!b||waiting;$('transitionGoA').disabled=!a;$('transitionGoB').disabled=!b;$('transitionDirection').disabled=draft;$('transitionConfirm').hidden=!(draft&&(transition.phase==='editingA'||transition.phase==='editingB'));$('transitionCancel').hidden=!draft||waiting;$('transitionSave').hidden=draft;$('deleteTransition').hidden=draft;}
function currentTransitionEndpoint(key){return transitionForInspector()?.[key]||null;}
function cloneTransitionEndpoint(endpoint){return endpoint?{...endpoint,position:{...endpoint.position}}:endpoint;}
function clampTransitionPosition(floorId,position){const bounds=walkableBounds(state,floorId);if(!bounds)return {...position};return {x:Math.max(bounds.x,Math.min(bounds.x+bounds.width,position.x)),y:Math.max(bounds.y,Math.min(bounds.y+bounds.height,position.y))};}
function navigateTransitionEndpoint(key){const endpoint=currentTransitionEndpoint(key);if(!endpoint)return;camera.x=endpoint.position.x-viewport.w/(2*camera.scale);camera.y=endpoint.position.y-viewport.h/(2*camera.scale);scheduleViewportSave();if(endpoint.floorId!==currentFloorID)setFloor(endpoint.floorId,true);dirty=true;}
function sameEndpoint(a,b){return a&&b&&a.floorId===b.floorId&&a.radius===b.radius&&a.position.x===b.position.x&&a.position.y===b.position.y;}
function resolveCreatedTransition(){if(!transitionDraft||transitionDraft.phase!=='awaitingCreate')return;const prior=new Set(transitionDraft.priorIds||[]),found=Object.values(state?.transitions||{}).find(item=>!prior.has(item.id)&&sameEndpoint(item.endpointA,transitionDraft.endpointA)&&sameEndpoint(item.endpointB,transitionDraft.endpointB));if(found){selectedTransition=found.id;transitionDraft=null;transitionCursor=null;}}
function confirmTransitionEndpoint(){if(!transitionDraft)return;if(transitionDraft.phase==='editingA'){transitionDraft.phase='placingB';transitionCursor={x:transitionDraft.endpointA.position.x,y:transitionDraft.endpointA.position.y};toast('Поставьте точку B перехода');}else if(transitionDraft.phase==='editingB'){const payload={name:'Переход',endpointA:cloneTransitionEndpoint(transitionDraft.endpointA),endpointB:cloneTransitionEndpoint(transitionDraft.endpointB),direction:'bidirectional'};if(queueCommand('transitionCreate',{transition:payload}))transitionDraft={...transitionDraft,phase:'awaitingCreate',priorIds:Object.keys(state?.transitions||{})};}fillTransitionProperties();dirty=true;}
$('transitionConfirm').onclick=confirmTransitionEndpoint;
$('transitionCancel').onclick=cancelTransitionDraft;
$('transitionGoA').onclick=()=>navigateTransitionEndpoint('endpointA');
$('transitionGoB').onclick=()=>navigateTransitionEndpoint('endpointB');
$('transitionFloorA').onchange=()=>{if(transitionDraft?.endpointA){const floorId=$('transitionFloorA').value;transitionDraft.endpointA={...transitionDraft.endpointA,floorId,position:clampTransitionPosition(floorId,transitionDraft.endpointA.position)};dirty=true;}};
$('transitionFloorB').onchange=()=>{if(transitionDraft?.endpointB){const floorId=$('transitionFloorB').value;transitionDraft.endpointB={...transitionDraft.endpointB,floorId,position:clampTransitionPosition(floorId,transitionDraft.endpointB.position)};dirty=true;}};
$('transitionRadiusA').oninput=()=>{if(transitionDraft?.endpointA){const radius=Math.max(8,Number($('transitionRadiusA').value)||8);transitionDraft.endpointA={...transitionDraft.endpointA,radius};dirty=true;}};
$('transitionRadiusB').oninput=()=>{if(transitionDraft?.endpointB){const radius=Math.max(8,Number($('transitionRadiusB').value)||8);transitionDraft.endpointB={...transitionDraft.endpointB,radius};dirty=true;}};
$('transitionProperties').onsubmit=e=>{e.preventDefault();const transition=state?.transitions?.[selectedTransition];if(!transition)return;const floorA=$('transitionFloorA').value,floorB=$('transitionFloorB').value,next={...transition,direction:$('transitionDirection').value,endpointA:{...transition.endpointA,floorId:floorA,position:clampTransitionPosition(floorA,transition.endpointA.position),radius:Math.max(8,Number($('transitionRadiusA').value)||8)},endpointB:{...transition.endpointB,floorId:floorB,position:clampTransitionPosition(floorB,transition.endpointB.position),radius:Math.max(8,Number($('transitionRadiusB').value)||8)}};queueCommand('transitionUpdate',{transition:next});};
$('deleteTransition').onclick=()=>{const transition=state?.transitions?.[selectedTransition];if(transition&&confirm('Удалить переход?')){queueCommand('transitionDelete',{transition:{id:transition.id}});selectTransition(null);}};
$('properties').onsubmit=e=>{e.preventDefault();const source=state?.tokens[selected];if(!source)return;const draft={...drafts.get(selected)},properties={},unchanged={};for(const [field,value] of Object.entries(draft))(source[field]===value?unchanged:properties)[field]=value;if(Object.keys(unchanged).length)drafts.confirm(selected,unchanged);if(Object.keys(properties).length)queueCommand('properties',{token:{id:selected},properties});else fillProperties();};
$('delete').onclick=()=>{if(selected)queueCommand('delete',{token:{id:selected}});select(null);};
$('elementProperties').onsubmit=e=>{e.preventDefault();const element=state?.elements?.[selectedElement];if(!element)return;const layerId=$('elementLayer').value,layer=state.layers[layerId],properties={name:$('elementName').value.trim(),floorId:layer.floorId,layerId,visible:$('elementVisible').checked,locked:$('elementLocked').checked,opacity:Number($('elementOpacity').value)},transform={x:Number($('elementX').value),y:Number($('elementY').value),width:Number($('elementWidth').value),height:Number($('elementHeight').value),rotation:Number($('elementRotation').value)};queueCommand('elementUpdate',{element:{id:element.id},elementProperties:properties});queueTransform('elementTransform',{element:{id:element.id,transform}});};
$('deleteElement').onclick=()=>{if(selectedElement&&confirm('Удалить элемент сцены?'))queueCommand('elementDelete',{element:{id:selectedElement}});selectElement(null);};
$('duplicateElement').onclick=()=>{const element=state?.elements?.[selectedElement];if(!element)return;queueCommand('elementCreate',{element:{...element,id:'',name:`${element.name} копия`,transform:{...element.transform,x:element.transform.x+30,y:element.transform.y+30}}});};
function addToken(asset='',x=camera.x+viewport.w/(2*camera.scale),y=camera.y+viewport.h/(2*camera.scale)){if(!queueCommand('create',{token:{name:asset?'Новый персонаж':'Искатель',floorId:currentFloorID,x,y,size:80,rotation:0,color:'#c2d89b',owner:'',hidden:false,asset}}))toast('Дождитесь подключения к серверу');}
$('add').onclick=()=>addToken();
async function upload(file,kind){if(!file)return;if(!state?.scene?.id)throw new Error('Сначала откройте сцену');if(file.size>UPLOAD_LIMIT_BYTES)throw new Error('Размер файла не должен превышать 256 МиБ');const sceneID=state.scene.id,controller=new AbortController(),activeElement=state.elements?.[selectedElement],layer=activeElement?.layerId||orderedLayers(state,currentFloorID).find(item=>item.kind==='visual')?.id||'';uploadControllers.add(controller);toast('Подготовка изображения на сервере…');try{const query=new URLSearchParams({session:credentials.session,scene:sceneID,kind,name:file.name,floor:currentFloorID,layer});const a=await api(`/api/upload?${query}`,file,true,controller.signal);if(controller.signal.aborted||state?.scene?.id!==sceneID)throw new DOMException('Scene changed','AbortError');toast(kind==='map'?(a.renderMode==='tiled'?'Изображение добавлено. Загружаются видимые тайлы.':'Изображение добавлено.'):'Изображение токена загружено');if(kind==='token')addToken(a.id);return a;}finally{uploadControllers.delete(controller);}}
for(const kind of ['map','token'])$(`${kind}Upload`).onchange=async e=>{const input=e.target;input.disabled=true;try{await upload(input.files[0],kind);}catch(e){if(e.name!=='AbortError')toast(e.message);}finally{input.disabled=false;input.value='';}};
function resize(){const rect=$('stage').getBoundingClientRect();viewport={w:Math.max(1,rect.width),h:Math.max(1,rect.height)};const dpr=Math.min(devicePixelRatio||1,2);canvas.width=Math.round(viewport.w*dpr);canvas.height=Math.round(viewport.h*dpr);dirty=true;}
new ResizeObserver(resize).observe($('stage'));
function fit(){const bounds=state?.scene?.bounds;if(bounds){camera.scale=Math.max(.015,Math.min(2,(viewport.w-70)/bounds.width,(viewport.h-70)/bounds.height));camera.x=bounds.width/2-viewport.w/2/camera.scale;camera.y=bounds.height/2-viewport.h/2/camera.scale;}else{camera={x:-viewport.w,y:-viewport.h,scale:.75};}scheduleViewportSave();dirty=true;}
function zoom(factor,x=viewport.w/2,y=viewport.h/2){const wx=camera.x+x/camera.scale,wy=camera.y+y/camera.scale;camera.scale=Math.max(.015,Math.min(4,camera.scale*factor));camera.x=wx-x/camera.scale;camera.y=wy-y/camera.scale;scheduleViewportSave();dirty=true;}
$('fit').onclick=fit;$('plus').onclick=()=>zoom(1.25);$('minus').onclick=()=>zoom(.8);$('grid').onclick=()=>{grid=!grid;$('grid').classList.toggle('active',grid);dirty=true;};
const point=e=>{const r=canvas.getBoundingClientRect();return{x:e.clientX-r.left,y:e.clientY-r.top};};
const alphaCanvas=typeof OffscreenCanvas!=='undefined'?new OffscreenCanvas(1,1):document.createElement('canvas');alphaCanvas.width=1;alphaCanvas.height=1;const alphaCtx=alphaCanvas.getContext('2d',{willReadFrequently:true});
function sampleBitmapAlpha(bitmap,x,y){if(!bitmap||x<0||y<0||x>=bitmap.width||y>=bitmap.height)return 0;try{alphaCtx.clearRect(0,0,1,1);alphaCtx.drawImage(bitmap,Math.max(0,Math.min(bitmap.width-1,x)),Math.max(0,Math.min(bitmap.height-1,y)),1,1,0,0,1,1);return alphaCtx.getImageData(0,0,1,1).data[3];}catch{return null;}}
function elementAlphaAt(element,x,y){const asset=state?.assets?.[element.assetId];if(!asset)return null;const local=inversePoint(element.transform,x,y),u=local.x/element.transform.width*asset.width,v=local.y/element.transform.height*asset.height;if(u<0||v<0||u>=asset.width||v>=asset.height)return 0;if(asset.renderMode==='tiled'||(asset.kind==='map'&&asset.levels>0&&!asset.renderMode)){for(let z=0;z<asset.levels;z++){const unit=512*2**z,tx=Math.floor(u/unit),ty=Math.floor(v/unit),entry=memory.get(cacheKey(`${asset.id}/${z}_${tx}_${ty}.png`));if(!entry)continue;const spanX=Math.min(unit,asset.width-tx*unit),spanY=Math.min(unit,asset.height-ty*unit),px=(u-tx*unit)/spanX*entry.bitmap.width,py=(v-ty*unit)/spanY*entry.bitmap.height;return sampleBitmapAlpha(entry.bitmap,px,py);}return null;}const path=asset.kind==='token'?'token.png':'image.png',entry=memory.get(cacheKey(`${asset.id}/${path}`));if(!entry)return null;return sampleBitmapAlpha(entry.bitmap,u/asset.width*entry.bitmap.width,v/asset.height*entry.bitmap.height);}
function pickTransitionFloor(x,y){const floors=[...compositeFloors(state,currentFloorID)].reverse();for(const {floor}of floors)for(const element of elementsAtPoint(state,floor.id,x,y)){const alpha=elementAlphaAt(element,x,y);if(alpha!==null&&alpha>0)return floor.id;}return currentFloorID||orderedFloors(state)[0]?.id||'';}
function transitionDefaultRadius(){return Math.max(24,Math.min(250,50/camera.scale));}
function draftTransitionHit(x,y){if(!transitionDraft)return null;const key=transitionDraft.phase==='editingA'?'endpointA':transitionDraft.phase==='editingB'?'endpointB':'';const endpoint=transitionDraft[key];if(!endpoint||endpoint.floorId!==currentFloorID)return null;const hit=10/camera.scale,radiusPoint={x:endpoint.position.x+endpoint.radius,y:endpoint.position.y};if(Math.hypot(x-radiusPoint.x,y-radiusPoint.y)<=hit)return {key,part:'radius'};if(Math.hypot(x-endpoint.position.x,y-endpoint.position.y)<=hit)return {key,part:'center'};const offset=endpoint.radius+18/camera.scale,size=14/camera.scale,confirmX=endpoint.position.x+offset,cancelX=confirmX+21/camera.scale,controlY=endpoint.position.y-14/camera.scale;if(Math.abs(x-confirmX)<=size/2&&Math.abs(y-controlY)<=size/2)return {key,part:'confirm'};if(Math.abs(x-cancelX)<=size/2&&Math.abs(y-controlY)<=size/2)return {key,part:'cancel'};return null;}
canvas.onwheel=e=>{e.preventDefault();const p=point(e);zoom(Math.exp(-e.deltaY*.001),p.x,p.y);};
canvas.onpointerdown=e=>{if(!state)return;canvas.focus();canvas.setPointerCapture(e.pointerId);const p=point(e),wx=camera.x+p.x/camera.scale,wy=camera.y+p.y/camera.scale,gm=state.you.role==='gm';if(gm&&transitionDraft&&transitionDraft.phase!=='awaitingCreate'&&e.button===0){if(transitionDraft.phase==='placingA'||transitionDraft.phase==='placingB'){const key=transitionDraft.phase==='placingA'?'endpointA':'endpointB',floorId=pickTransitionFloor(wx,wy),endpoint={floorId,position:clampTransitionPosition(floorId,{x:wx,y:wy}),radius:transitionDefaultRadius()};transitionDraft={...transitionDraft,[key]:endpoint,phase:key==='endpointA'?'editingA':'editingB'};drag={type:'transitionDraftRadius',key,start:{x:wx,y:wy},moved:false};fillTransitionProperties();dirty=true;return;}const hit=draftTransitionHit(wx,wy);if(hit?.part==='confirm'){confirmTransitionEndpoint();return;}if(hit?.part==='cancel'){cancelTransitionDraft();return;}if(hit){drag={type:hit.part==='radius'?'transitionDraftRadius':'transitionDraftCenter',key:hit.key,startEndpoint:cloneTransitionEndpoint(transitionDraft[hit.key]),start:{x:wx,y:wy},moved:false};return;}dirty=true;return;}const transitionHitResult=gm?transitionHit(state,currentFloorID,wx,wy,camera.scale,selectedTransition):null;if(transitionHitResult&&e.button===0&&!e.altKey){if(transitionHitResult.transition.id!==selectedTransition){selectTransition(transitionHitResult.transition.id);}else if(transitionHitResult.part==='center'||transitionHitResult.part==='radius'){drag={type:transitionHitResult.part==='center'?'transitionCenter':'transitionRadius',id:selectedTransition,key:transitionHitResult.key,startEndpoint:cloneTransitionEndpoint(transitionHitResult.transition[transitionHitResult.key]),start:{x:wx,y:wy},moved:false};}dirty=true;return;}const t=tokenIndex.query(wx,wy,wx,wy).reverse().find(t=>(gm?t.floorId===currentFloorID:canMove(t))&&Math.hypot(wx-t.x,wy-t.y)<=t.size/2),chosen=state.elements?.[selectedElement],handle=gm?elementHandleAt(chosen,wx,wy,camera.scale):null,element=gm?(handle?chosen:hitElement(state,currentFloorID,wx,wy)):null;if(t&&e.button===0&&!e.altKey){if(!gm)activatePlayerToken(t,false);else select(t.id);if(canMove(t))drag={type:'token',id:t.id,floorId:t.floorId,dx:wx-t.x,dy:wy-t.y,x:t.x,y:t.y,moved:false};}else if(element&&e.button===0&&!e.altKey){selectElement(element.id);const layer=state.layers[element.layerId];if(!element.locked&&!layer.locked)drag={type:'element',id:element.id,mode:handle||'move',start:{...element.transform,pointerX:wx,pointerY:wy},pointerX:wx,pointerY:wy,transform:{...element.transform},moved:false};}else{select(null);selectElement(null);selectedTransition='';fillTransitionProperties();drag={type:'pan',px:p.x,py:p.y,x:camera.x,y:camera.y};}dirty=true;};
canvas.onpointermove=e=>{const p=point(e),wx=camera.x+p.x/camera.scale,wy=camera.y+p.y/camera.scale;if(transitionDraft?.phase==='placingB'){transitionCursor={x:wx,y:wy};dirty=true;}if(!drag)return;if(drag.type==='pan'){camera.x=drag.x-(p.x-drag.px)/camera.scale;camera.y=drag.y-(p.y-drag.py)/camera.scale;scheduleViewportSave();}else if(drag.type==='token'){let x=wx-drag.dx,y=wy-drag.dy;if(state.you.role!=='gm'){const bounds=walkableBounds(state,currentFloorID);if(bounds){x=Math.max(bounds.x,Math.min(bounds.x+bounds.width,x));y=Math.max(bounds.y,Math.min(bounds.y+bounds.height,y));}}if(x!==drag.x||y!==drag.y)drag.moved=true;drag.x=x;drag.y=y;if(drag.moved&&performance.now()-lastMove>50&&!positionOutbox?.hasQueuedBehindInflight()){send({type:'move',token:{id:drag.id,floorId:drag.floorId,x:drag.x,y:drag.y}});lastMove=performance.now();}}else if(drag.type==='element'){const transform=transformedFromDrag(drag.start,drag.mode,wx-drag.pointerX,wy-drag.pointerY,e.shiftKey);drag.transform=transform;drag.moved=true;state.elements[drag.id]={...state.elements[drag.id],transform};if(performance.now()-lastMove>50&&!positionOutbox?.hasQueuedBehindInflight()){send({type:'elementPreview',element:{id:drag.id,transform}});lastMove=performance.now();}}else if(drag.type==='transitionDraftRadius'){const endpoint=transitionDraft?.[drag.key];if(endpoint){const radius=Math.max(8,Math.hypot(wx-endpoint.position.x,wy-endpoint.position.y));if(Math.abs(radius-endpoint.radius)>.01)drag.moved=true;transitionDraft[drag.key]={...endpoint,radius};}}else if(drag.type==='transitionDraftCenter'){const start=drag.startEndpoint,dx=wx-drag.start.x,dy=wy-drag.start.y;transitionDraft[drag.key]={...start,position:clampTransitionPosition(start.floorId,{x:start.position.x+dx,y:start.position.y+dy})};drag.moved=true;}else if(drag.type==='transitionRadius'||drag.type==='transitionCenter'){const transition=state.transitions?.[drag.id],start=drag.startEndpoint;if(transition){const endpoint=drag.type==='transitionRadius'?{...start,radius:Math.max(8,Math.hypot(wx-start.position.x,wy-start.position.y))}:{...start,position:clampTransitionPosition(start.floorId,{x:start.position.x+wx-drag.start.x,y:start.position.y+wy-drag.start.y})};state.transitions[drag.id]={...transition,[drag.key]:endpoint};drag.moved=true;}}dirty=true;};
function endDrag(){if(drag?.type==='token'&&drag.moved)queuePosition({token:{id:drag.id,floorId:drag.floorId,x:drag.x,y:drag.y}});if(drag?.type==='element'&&drag.moved)queueTransform('elementTransform',{element:{id:drag.id,transform:drag.transform}});if((drag?.type==='transitionRadius'||drag?.type==='transitionCenter')&&drag.moved){const transition=state?.transitions?.[drag.id];if(transition)queueCommand('transitionUpdate',{transition});}drag=null;fillElementProperties();fillTransitionProperties();dirty=true;}
canvas.onpointerup=endDrag;canvas.onpointercancel=endDrag;canvas.onlostpointercapture=endDrag;canvas.oncontextmenu=e=>e.preventDefault();
canvas.onkeydown=e=>{if(e.key==='Escape'){if(transitionDraft)cancelTransitionDraft();else{select(null);selectElement(null);selectedTransition='';fillTransitionProperties();}}if(e.key==='Enter'&&transitionDraft&&(transitionDraft.phase==='editingA'||transitionDraft.phase==='editingB'))confirmTransitionEndpoint();if(e.key==='Delete'&&state?.you.role==='gm'){if(selected)$('delete').click();else if(selectedElement)$('deleteElement').click();else if(selectedTransition)$('deleteTransition').click();}if(e.key==='f')fit();};
$('continuous').onchange=()=>{dirty=true;};$('reconnect').onclick=()=>socket?.close();
$('stress').onclick=()=>{if(!state)return;for(let i=0;i<100;i++)addToken('',camera.x+(i%10)*100,camera.y+Math.floor(i/10)*100);toast('Добавлено 100 тестовых токенов');};
$('clearCache').onclick=async()=>{await cacheQueue;const db=await dbPromise;if(db){await new Promise(resolve=>{const tx=db.transaction('assets','readwrite');tx.objectStore('assets').clear();tx.oncomplete=tx.onerror=tx.onabort=resolve;});}diskBytes=0;diskEnabled=!!db;for(const entry of memory.values())entry.bitmap.close();memory.clear();memoryBytes=0;artwork.clear();dirty=true;toast('Кэш очищен. Сцена сохранена.');};

let decodeEdge=512, imagePlans=new Map();const nativeSizes=new Map();
function cacheKey(path){return `${credentials.session}/${path}`;}
function nativeSize(path){
 const key=cacheKey(path);if(nativeSizes.has(key))return nativeSizes.get(key);
 const [asset,file]=path.split('/'),a=state?.assets[asset];let width=512,height=512;
 if(file==='token.png'&&a){width=a.width;height=a.height;while(width>512||height>512){width=Math.ceil(width/2);height=Math.ceil(height/2);}}
 else if(file==='image.png'&&a){width=a.width;height=a.height;}
 else if(a){const match=file.match(/^(\d+)_(\d+)_(\d+)\.png$/);if(match){const [,z,x,y]=match.map(Number);width=Math.min(512,Math.ceil(a.width/2**z)-x*512);height=Math.min(512,Math.ceil(a.height/2**z)-y*512);}}
 return {width:Math.max(1,width),height:Math.max(1,height)};
}
function requestImage(path,screenEdge,priority='visible'){
 const key=cacheKey(path),native=nativeSize(path);
 if(screenEdge===undefined){const z=Number(path.split('/')[1].split('_')[0]);screenEdge=Math.max(native.width,native.height)*2**z*camera.scale*(canvas.width/viewport.w);}
 const entry=memory.get(key),currentEdge=entry?Math.max(entry.bitmap.width,entry.bitmap.height):0,size=decodedSize(native.width,native.height,screenEdge,currentEdge),old=imagePlans.get(key),rank=priority==='prefetch'?1:0;
 if(!old)imagePlans.set(key,{...size,native,priority:rank});
 else {old.priority=Math.min(old.priority,rank);if(size.width>old.width||size.height>old.height){old.width=size.width;old.height=size.height;}}
 if(entry&&rank===0){entry.used=performance.now();memory.delete(key);memory.set(key,entry);}
 return entry?.bitmap;
}
function releaseImage(key){const e=memory.get(key);if(e){e.bitmap.close();memoryBytes-=e.size;memory.delete(key);}}
function evictImages(projectedBytes=memoryBytes){const plan=planLRUEviction(memory,wanted,projectedBytes,assetLimit);for(const key of plan.keys)releaseImage(key);return plan.remaining;}
function trimImages(){evictImages();}
const imageBytes=p=>p.width*p.height*4;
function scheduleImages(){
 const visible=[],prefetch=[];for(const [key,plan]of imagePlans)(plan.priority===0?visible:prefetch).push({key,plan});
 const visibleBytes=fitPlansToBudget(visible,assetLimit),admitted=new Map();for(const item of visible)admitted.set(item.key,item.plan);
 let prefetchBudget=Math.min(Math.max(0,assetLimit-visibleBytes),Math.floor(assetLimit*.20));
 // Reuse already-decoded neighbours first, then admit cheap new prefetches.
 prefetch.sort((a,b)=>(memory.has(b.key)?1:0)-(memory.has(a.key)?1:0)||imageBytes(a.plan)-imageBytes(b.plan));
 for(const {key,plan}of prefetch){const bytes=imageBytes(plan);if(bytes<=prefetchBudget){admitted.set(key,plan);prefetchBudget-=bytes;}}
 wanted=new Set(admitted.keys());decodeEdge=0;for(const p of admitted.values())decodeEdge=Math.max(decodeEdge,p.width,p.height);
 for(const key of failures.keys())if(!wanted.has(key))failures.delete(key);
 // Size changes no longer abort in-flight work. Camera motion can change a plan
 // every frame; finishing a slightly stale decode is cheaper than restarting it.
 for(const [key,c]of pending)if(!wanted.has(key))c.abort();
 let forecast=memoryBytes;
 for(const [key,p]of admitted){if(memory.has(key))continue;const c=pending.get(key);forecast+=(c?c.width*c.height*4:imageBytes(p));}
 forecast=evictImages(forecast);
 // If retained high-resolution bitmaps still block required visible work, reduce
 // only those entries whose current bitmap is larger than the admitted plan.
 if(forecast>assetLimit){
  const downgrade=[];for(const [key,p]of admitted){const e=memory.get(key);if(e&&e.size>imageBytes(p))downgrade.push({key,p,e,saving:e.size-imageBytes(p)});}
  downgrade.sort((a,b)=>b.saving-a.saving);
  for(const d of downgrade){if(forecast<=assetLimit)break;forecast-=d.saving;releaseImage(d.key);}
 }
 // Upgrade undersized visible entries only when the budget can accommodate the
 // replacement; prefetch never forces an upgrade or visible eviction.
 for(const {key,plan}of visible){const e=memory.get(key);if(!e)continue;if(e.bitmap.width>=plan.width&&e.bitmap.height>=plan.height)continue;const target=imageBytes(plan);if(memoryBytes-e.size+target<=assetLimit)releaseImage(key);}
 trimImages();
 const ordered=[...admitted].sort((a,b)=>a[1].priority-b[1].priority);
 for(const [key,p]of ordered){if(activeLoads>=6)break;if(memory.has(key)||pending.has(key)||(failures.get(key)||0)>Date.now())continue;loadImage(key,p);}
}
function loadImage(key,plan){
 const controller=new AbortController();controller.width=plan.width;controller.height=plan.height;pending.set(key,controller);activeLoads++;
 (async()=>{
  let blob=await diskGet(key);if(controller.signal.aborted)return;
  if(blob)cacheHits++;else{const path=key.slice(credentials.session.length+1),scene=state?.scene?.id;if(!scene)throw new DOMException('Scene changed','AbortError');const query=new URLSearchParams({session:credentials.session,scene,activeTokenId:state?.activeTokenId||''});const res=await fetch(`/api/asset/${path}?${query}`,{headers:{Authorization:`Bearer ${credentials.key}`},signal:controller.signal,cache:diskEnabled?'no-store':'force-cache'});if(!res.ok)throw new Error(`Изображение: HTTP ${res.status}`);blob=await res.blob();tileBytes+=blob.size;diskPut(key,blob);}
  if(controller.signal.aborted)return;
  // Prepared resources are PNG: inspect IHDR before decode, including assets
  // newly revealed to a player whose snapshot did not yet contain metadata.
  const header=new DataView(await blob.slice(0,24).arrayBuffer());
  if(header.byteLength<24||header.getUint32(0)!==0x89504e47)throw new Error('Некорректный PNG');
  const width=header.getUint32(16),height=header.getUint32(20);nativeSizes.set(key,{width,height});
  const sizePlan=fitImageSize(width,height,Math.max(plan.width,plan.height)),decodeStart=performance.now();
  const bitmap=await createImageBitmap(blob,{resizeWidth:sizePlan.width,resizeHeight:sizePlan.height,resizeQuality:'high'});decodeCount++;decodeMs+=performance.now()-decodeStart;
  if(controller.signal.aborted||!wanted.has(key)){bitmap.close();return;}
  const size=bitmap.width*bitmap.height*4;if(memoryBytes+size>assetLimit){bitmap.close();return;}
  memory.set(key,{bitmap,size,used:performance.now()});memoryBytes+=size;failures.delete(key);
 })().catch(e=>{if(e.name!=='AbortError'){failures.set(key,Date.now()+5000);if(failures.size===1)toast('Не удалось загрузить изображения. Повтор через 5 секунд.');}}).finally(()=>{pending.delete(key);activeLoads--;dirty=true;});
}
function draw(now){const start=performance.now();dirty=false;imagePlans=new Map();const dpr=canvas.width/viewport.w;ctx.setTransform(dpr,0,0,dpr,0,0);ctx.fillStyle='#121c1d';ctx.fillRect(0,0,viewport.w,viewport.h);ctx.save();ctx.scale(camera.scale,camera.scale);ctx.translate(-camera.x,-camera.y);const right=camera.x+viewport.w/camera.scale,bottom=camera.y+viewport.h/camera.scale;ensureSceneRegion({left:camera.x,top:camera.y,right,bottom});let tokenAnimating=false;
 if(state){const bounds=state.scene.bounds,gm=state.you.role==='gm';ctx.fillStyle='#182322';ctx.fillRect(0,0,bounds.width,bounds.height);drawSceneStack({ctx,state,currentFloorId:currentFloorID,view:{left:camera.x,top:camera.y,right,bottom},camera,dpr,requestImage,selectedElement,editor:gm,drawTokenLayer:(floorId,alpha)=>{tokenAnimating=drawTokens({ctx,camera,viewport,dpr,tokens:state.tokens,index:tokenIndex,visuals,moving:movingTokens,drag,selected,queue:[...(outbox?.data.queue||[]),...(positionOutbox?.data.queue||[])],requestImage,artwork,delta:now-lastFrame,floorId,alpha,walkableBounds:walkableBounds(state,floorId),showOutOfBounds:gm&&floorId===currentFloorID})||tokenAnimating;}});}
 if(grid){const step=100;const screenStep=step*camera.scale;if(screenStep>=12){ctx.strokeStyle='#8ea18a16';ctx.lineWidth=1/camera.scale;ctx.beginPath();for(let x=Math.floor(camera.x/step)*step;x<right;x+=step){ctx.moveTo(x,camera.y);ctx.lineTo(x,bottom);}for(let y=Math.floor(camera.y/step)*step;y<bottom;y+=step){ctx.moveTo(camera.x,y);ctx.lineTo(right,y);}ctx.stroke();}}
 if(state?.you.role==='gm'){drawWalkableOverlay(ctx,state,currentFloorID,camera);drawTransitionOverlay(ctx,state,currentFloorID,camera,{selectedId:selectedTransition,draft:transitionDraft,cursor:transitionCursor});}dirty=tokenAnimating||dirty;
 ctx.restore();scheduleImages();$('zoom').textContent=`${Math.round(camera.scale*100)}%`;frameTime=performance.now()-start;rendered++;
}
function loop(now){if(!$('app').hidden&&(dirty||$('continuous').checked))draw(now);lastFrame=now;requestAnimationFrame(loop);}requestAnimationFrame(loop);
setInterval(()=>{fps=rendered;rendered=0;trimImages();if(failures.size)dirty=true;$('diagnostics').textContent=`${fps} кадров/с · ${frameTime.toFixed(1)} мс/кадр\nRAM изображений ${((memoryBytes+artwork.bytes)/MIB).toFixed(1)} / ${memoryLimit/MIB} МБ${imageMemorySetting==='auto'?' · Auto':''}\nДиск ${(diskBytes/1048576).toFixed(1)} / 128 МБ${diskEnabled?'':' · недоступен'} · R/W ${diskReads}/${diskWrites} · evict ${diskEvictions} · drop ${diskDrops}\nТайлы ↓ ${(tileBytes/1048576).toFixed(2)} МБ · кэш ${cacheHits}\nDecode ${decodeCount} · ${decodeMs.toFixed(0)} мс суммарно · edge ${decodeEdge}\nWS ↓ ${(bytesIn/1024).toFixed(1)} ↑ ${(bytesOut/1024).toFixed(1)} КБ\nЗагрузка ${activeLoads} · ошибки ${failures.size}`;},1000);
