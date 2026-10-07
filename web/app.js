import {SpatialIndex,ArtworkCache,LimitedMap,MetadataTouchTracker,decodedSize,fitImageSize,fitPlansToBudget,resolveImageMemoryBudgetMiB,planLRUEviction} from './rendering.js';
import {drawTokens} from './token-renderer.js';
import {Outbox, Drafts, CacheWriteBudget,blockedMoveCorrection,sameValue} from './reliability.js';
import {ScenesRuntime,needsActiveTokenRequest} from './scenes.js';
import {FloorRenderBoundsPathCache,FloorRenderBoundsResourceCache,SceneRenderIndex,abortUnwantedImageLoads,clipFloorRenderBounds,compositeFloors,drawPlayerWalkableOverlay,drawRenderBoundsEditor,drawSceneStack,drawTransitionOverlay,drawWalkableEditor,elementHandleAt,elementsAtPoint,floorCameraBounds,hitElement,inversePoint,orderedFloors,orderedLayers,pointInPlayableArea,pointInRenderBounds,renderBoundsAABB,renderBoundsHit,transitionHit,transformedFromDrag,validRenderBounds,validRenderBoundsChange,walkableComponentAt} from './scene-content.js';
import {SceneTreeRuntime} from './scene-tree.js';
import {constrainTokenMovement} from './movement-geometry.js';
import {CharacterCatalog,CharacterWatch,statInputValue,statValueFromInput} from './characters.js';
import {DefinitionsEditor} from './definitions.js';
import {createDicePanel,DiceAnimator,DiceRuntime} from './dice.js';
import {DiceRendererAdapter} from './dice-renderer.js';
const $ = id => document.getElementById(id);
const isGM=member=>!!(member?.gm||member?.role==='gm');
const isEditorView=()=>!!state&&isGM(state.you)&&!state.playerPreview&&!previewSwitchPending;
const usesPlayerInteraction=()=>!!state&&!isEditorView();
const characterPanel=document.createElement('section');characterPanel.id='characterPanel';characterPanel.hidden=true;characterPanel.innerHTML=`<div id="characterPending" class="muted" hidden>Загрузка персонажа…</div><div id="characterUnavailable" class="muted" hidden>Персонаж недоступен.</div><div id="characterSummary" hidden><div class="character-avatar"><img id="characterAvatar" alt="" hidden><span id="characterAvatarEmpty">◇</span></div><h2 id="characterName"></h2><p id="characterMeta" class="muted"></p><div id="characterAvatarControls"><label class="file-button">Загрузить avatar<input id="characterAvatarUpload" type="file" accept="image/png,image/jpeg" hidden></label><button id="characterAvatarReset" class="subtle wide" type="button">Сбросить avatar</button></div><h3>Характеристики</h3><div id="characterStats"></div><form id="characterStatAdd"><div class="character-add-stat"><input id="characterStatID" placeholder="ID характеристики" maxlength="128" required><select id="characterStatType"><option value="integer">Целое</option><option value="number">Число</option><option value="boolean">Да / нет</option><option value="string">Строка</option></select></div><input id="characterStatValue" placeholder="Значение"><label id="characterStatBooleanLabel" class="check" hidden><input id="characterStatBoolean" type="checkbox"> Значение</label><button class="subtle wide" type="submit">Добавить характеристику</button></form><h3>Действия</h3><div id="characterActions"></div><div id="characterActionManage" hidden><select id="characterActionAdd"></select><button id="characterActionAddButton" class="subtle wide" type="button">Добавить действие</button><button id="characterActionMore" class="subtle wide" type="button" hidden>Ещё действия</button></div></div><div id="characterEmpty" class="character-empty" hidden><span>◇</span><h2>Персонаж не назначен</h2><p class="muted">Выбор токена сам по себе не создаёт персонажа.</p></div><div id="characterGM" class="gm character-gm"><label>Создать из пресета<select id="characterPreset"></select></label><button id="characterCreatePreset" class="primary wide" type="button">Назначить новый instance</button><button id="characterPresetMore" class="subtle wide" type="button" hidden>Ещё пресеты</button><button id="characterCreateEmpty" class="subtle wide" type="button">Создать пустого персонажа</button><div class="divider"></div><label>Сохранённый персонаж<select id="characterRoster"></select></label><button id="characterLink" class="subtle wide" type="button">Назначить сохранённого</button><button id="characterRosterMore" class="subtle wide" type="button" hidden>Показать ещё</button><label id="characterPersistentLabel" class="check" hidden><input id="characterPersistent" type="checkbox"> Сохранить в кампании</label><button id="characterUnlink" class="danger wide" type="button" hidden>Отвязать от токена</button></div>`;document.querySelector('.inspector .panel-heading').after(characterPanel);
const CONTENT_COMMANDS=new Set(['move','final','properties','create','delete','floorCreate','floorUpdate','floorDelete','layerCreate','layerUpdate','layerDelete','elementCreate','elementUpdate','elementDelete','elementPreview','elementTransform','transitionCreate','transitionUpdate','transitionDelete','assetRetention','addWalkableRect','subtractWalkableRect','moveWalkableComponent','deleteWalkableComponent','setWalkableMode','setRenderBounds','clearRenderBounds']);
const UPLOAD_LIMIT_BYTES=256*1024*1024;
// Mutable only so the isolated profiler can compare Canvas lifecycles. Normal
// application code never replaces the surface in this iteration.
let canvas = $('board'), ctx = canvas.getContext('2d');
const saveStatus=document.createElement('span');saveStatus.id='saveStatus';saveStatus.className='status';$('connection').after(saveStatus);
const elementOffscreenNote=document.createElement('p');elementOffscreenNote.id='elementOffscreenNote';elementOffscreenNote.className='muted';elementOffscreenNote.textContent='Элемент вне загруженной области. Центрируйте его, чтобы менять положение, размер и поворот.';$('elementProperties').prepend(elementOffscreenNote);
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
    },updateCommandStatus,(cmd,error,errorCode,receipt)=>{
      dirty=true;
      dice.handleReceipt(cmd,receipt);
      definitionsEditor.handleCompleted(cmd,error);
      if(error){
        if(errorCode!=='movementBlocked'){toast(error);commitStatus(error);}
        if(cmd.type.startsWith('character')){if(cmd.statId&&sameValue(characterDrafts.get(cmd.characterId)[cmd.statId],cmd.statValue))pendingNewStats.delete(`${cmd.characterId}/${cmd.statId}`);renderCharacterPanel();}
        if(cmd.type==='transitionCreate'&&transitionDraft?.phase==='awaitingCreate'){transitionDraft={...transitionDraft,phase:'editingB'};delete transitionDraft.priorIds;fillTransitionProperties();}
        if(cmd.type==='transitionUpdate')send({type:'sync'});
      }else if(cmd.type==='properties'){drafts.confirm(cmd.token.id,cmd.properties);if(cmd.sceneId===state?.scene?.id)fillProperties();}
      else if(cmd.type==='elementUpdate'){elementDrafts.confirm(cmd.element.id,cmd.elementProperties);if(cmd.sceneId===state?.scene?.id)fillElementProperties();}
      else if(cmd.type==='elementTransform'){elementDrafts.confirm(cmd.element.id,cmd.element.transform);if(cmd.sceneId===state?.scene?.id)fillElementProperties();}
      else if(cmd.type==='characterStatSet'){
        characterDrafts.confirm(cmd.characterId,{[cmd.statId]:cmd.statValue});
        const key=`${cmd.characterId}/${cmd.statId}`;if(pendingNewStats.delete(key))toast(`Создана характеристика ${cmd.statId} в кампании`);
        renderCharacterPanel();
      }
      else if(cmd.type==='characterStatReset'){characterDrafts.confirm(cmd.characterId,{[cmd.statId]:undefined});renderCharacterPanel();}
      else if(cmd.type==='characterSetPersistent')refreshCharacterCatalog();
      if(position)attemptPreviewSwitch();
    });
    outbox=make(`atlas-outbox/${credentials.session}/${credentials.key}`,false);
    positionOutbox=make(`atlas-position-outbox/${credentials.session}/${credentials.key}`,true);
    updateCommandStatus();
  }catch(e){fatal('Не удалось прочитать очередь команд браузера');}
}
function queueCommand(type,payload){if(stopped||!outbox)return false;if(CONTENT_COMMANDS.has(type)){if(!state?.scene?.id)return false;payload={...payload,sceneId:state.scene.id};}try{outbox.enqueue(type,payload);return true;}catch(e){fatal('Не удалось сохранить команду в браузере. Изменение не отправлено');return false;}}
function queueTransform(type,payload){if(stopped||!positionOutbox||!state?.scene?.id)return false;try{positionOutbox.enqueue(type,{...payload,sceneId:state.scene.id});return true;}catch(e){fatal('Не удалось сохранить положение в браузере. Изменение не отправлено');return false;}}
function queuePosition(payload){return queueTransform('final',{...payload,previewRevision:state?.previewRevision||0});}
let credentials, state, campaign, desiredSceneID='', socket, reconnectTimer, selected, selectedElement, selectedTransition='', transitionDraft=null, transitionCursor=null, walkableTool='', selectedWalkableComponent='', renderBoundsTool='', renderBoundsDraft=null, selectedRenderBoundsVertex=-1, currentFloorID='', pendingActiveFocus='', confirmedActiveTokenID='', confirmedFloorID='', pendingActiveSwitch=null, drag, inviteCode, outbox, positionOutbox, commandTimer, positionCommandTimer, saveRetry, positionSaveRetry, outboxNeedsResume=true, preferredPlayerPreview=false, previewSwitchPending=null, previewSwitchTimer=null, previewSelection=''; const drafts=new Drafts();
const characterDrafts=new Drafts(),characterStatTimers=new Map(),pendingNewStats=new Set(),characterAvatarUploads=new Map();let characterWatchReason='',characterAvatarAsset='',characterAvatarURL='',characterAvatarDisplayController=null;
const characterWatch=new CharacterWatch(value=>send(value),onCharacterWatchChange);let characterWatchNeedsResume=true,characterCatalogSession='';
async function loadCharacterCatalogPage(kind,cursor){const query=new URLSearchParams({session:credentials.session,kind,limit:'50'});if(cursor)query.set('cursor',cursor);const response=await fetch(`/api/characters?${query}`,{headers:{Authorization:`Bearer ${credentials.key}`}});if(!response.ok)throw new Error(await response.text());return response.json();}
const characterCatalog=new CharacterCatalog(loadCharacterCatalogPage,()=>renderCharacterPanel());
const diceAnimator=new DiceAnimator({createRenderer:()=>new DiceRendererAdapter(),onError:error=>toast(`3D-анимация кубиков недоступна: ${error.message}`)});
const dice=new DiceRuntime(createDicePanel(),{queue:queueCommand,getCharacterId:()=>state?.tokens?.[selected]?.characterInstanceId||'',toast,animator:diceAnimator});
function refreshCharacterCatalog(){if(!credentials||!isGM(state?.you||campaign?.you))return Promise.resolve();return characterCatalog.reload().catch(error=>toast(error.message));}
async function loadDefinitions(){const query=new URLSearchParams({session:credentials.session});const response=await fetch(`/api/definitions?${query}`,{headers:{Authorization:`Bearer ${credentials.key}`}});if(!response.ok)throw new Error(await response.text());return response.json();}
async function definitionDocumentRequest(kind,operation,payload,preview=null,key='',options={}){const query=new URLSearchParams({session:credentials.session});if(preview){query.set('expectedRevision',String(preview.registryRevision));query.set('digest',preview.digest);query.set('key',key);}if(options.replaceFile)query.set('replaceFile',options.replaceFile);const headers={Authorization:`Bearer ${credentials.key}`};let body=payload;if(kind==='ruleset'){const files=Array.isArray(payload)?payload:[payload];body=JSON.stringify({files:await Promise.all(files.filter(Boolean).map(async file=>({name:file.name,content:await file.text()})))});headers['Content-Type']='application/json';}else headers['Content-Type']='application/toml';const response=await fetch(`/api/definitions/${kind}/${operation}?${query}`,{method:'POST',headers,body});if(!response.ok)throw new Error(await response.text());return response.json();}
function definitionOperationKey(kind,digest){const storageKey=`atlas-definition-operation:${credentials.session}:${kind}`;try{const saved=JSON.parse(localStorage.getItem(storageKey)||'null');if(saved?.digest===digest&&saved?.key)return saved.key;const key=`web-${kind}-${crypto.randomUUID()}`,value={digest,key};localStorage.setItem(storageKey,JSON.stringify(value));return key;}catch{return `web-${kind}-${crypto.randomUUID()}`;}}
function completeDefinitionOperation(kind,digest,key){const storageKey=`atlas-definition-operation:${credentials.session}:${kind}`;try{const saved=JSON.parse(localStorage.getItem(storageKey)||'null');if(saved?.digest===digest&&saved?.key===key)localStorage.removeItem(storageKey);}catch{}}
async function exportDefinitionDocument(kind){const query=new URLSearchParams({session:credentials.session}),response=await fetch(`/api/definitions/${kind}/export?${query}`,{headers:{Authorization:`Bearer ${credentials.key}`}});if(!response.ok)throw new Error(await response.text());const blob=await response.blob(),url=URL.createObjectURL(blob),link=document.createElement('a');link.href=url;link.download=kind==='ruleset'?'ruleset.toml':'campaign-extensions.toml';document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),0);}
async function uploadPresetAvatar(presetID,file){const query=new URLSearchParams({session:credentials.session,kind:'avatar',presetId:presetID});return api(`/api/upload?${query}`,file,true);}
async function loadDefinitionAvatar(assetID,signal){const response=await fetch(avatarPath(assetID),{headers:{Authorization:`Bearer ${credentials.key}`},signal});if(!response.ok)throw new Error(`Avatar: HTTP ${response.status}`);return response.blob();}
const definitionDocuments={preview:(kind,payload,options={})=>definitionDocumentRequest(kind,'preview',payload,null,'',options),apply:(kind,payload,preview,key,options={})=>definitionDocumentRequest(kind,'apply',payload,preview,key,options),operationKey:definitionOperationKey,complete:completeDefinitionOperation,export:exportDefinitionDocument};
const definitionsEditor=new DefinitionsEditor($('definitionsMount'),{load:loadDefinitions,queue:queueCommand,getCharacter:()=>characterWatch.snapshot,toast,documents:definitionDocuments,uploadPresetAvatar,loadAvatar:loadDefinitionAvatar});
$('definitionsOpen').onclick=()=>definitionsEditor.open();
const elementDrafts=new Drafts(),FORM_AUTOSAVE_DELAY=650;let tokenAutosaveTimer,elementAutosaveTimer;
function commandAlreadyQueued(box,type,id,field,value){return !!box?.data.queue.some(command=>command.type===type&&(command.token?.id===id||command.element?.id===id)&&Object.hasOwn(command.properties||command.elementProperties||command.element?.transform||{},field)&&sameValue((command.properties||command.elementProperties||command.element.transform)[field],value));}
function tokenFieldValue(id){return id==='tokenOwner'?[...$(id).selectedOptions].map(option=>option.value):id==='tokenHidden'?$(id).checked:id==='tokenSize'||id==='tokenOpacity'?Number($(id).value):$(id).value;}
function rememberTokenField(id,field){if(!selected)return;drafts.set(selected,field,tokenFieldValue(id));$('tokenPreview').style.background=$('tokenColor').value;$('tokenPreview').textContent=$('tokenName').value.slice(0,1);}
function commitTokenProperties(id=selected){clearTimeout(tokenAutosaveTimer);tokenAutosaveTimer=null;const source=state?.tokens[id];if(!source||!isEditorView()||!$('properties').checkValidity())return;const draft={...drafts.get(id)},properties={},unchanged={};for(const [field,value] of Object.entries(draft)){if(sameValue(source[field],value))unchanged[field]=value;else if(!commandAlreadyQueued(outbox,'properties',id,field,value))properties[field]=value;}if(Object.keys(unchanged).length)drafts.confirm(id,unchanged);if(Object.keys(properties).length)queueCommand('properties',{token:{id},properties});else if(id===selected)fillProperties();}
function scheduleTokenProperties(id=selected){clearTimeout(tokenAutosaveTimer);tokenAutosaveTimer=setTimeout(()=>commitTokenProperties(id),FORM_AUTOSAVE_DELAY);}
for(const [id,field] of [['tokenName','name'],['tokenSize','size'],['tokenColor','color'],['tokenOpacity','opacity'],['tokenOwner','ownerIds'],['tokenHidden','hidden'],['tokenFloor','floorId']]){
  const control=$(id);control.addEventListener('input',()=>{rememberTokenField(id,field);if(control.type==='text'||control.type==='number')scheduleTokenProperties();});control.addEventListener('change',()=>{rememberTokenField(id,field);commitTokenProperties();});control.addEventListener('blur',()=>commitTokenProperties());
}
const uploadControllers=new Set();
const rotationOverrides=new Set();
const scenes=new ScenesRuntime({homeAdd:$('homeSceneAdd'),back:$('toScenes'),home:$('campaignHome'),workspace:$('workspace'),footer:$('appFooter'),cards:$('sceneCards'),empty:$('emptyScenes'),campaignName:$('campaignName'),sceneTabs:$('sceneTabs')},{open:openScene,home:goCampaignHome,queue:queueCommand});
const sceneTree=new SceneTreeRuntime({tree:$('sceneTree'),addFloor:$('addFloor'),addLayer:$('addLayer'),addTransition:$('addTransition'),controls:[$('addFloor').parentElement,$('mapUpload').parentElement]},{queue:queueCommand,selectElement,selectTransition,beginTransition,setFloor,getFloor:()=>currentFloorID,focusElement,setWalkableTool,getWalkableTool:()=>walkableTool,setRenderBoundsTool,getRenderBoundsTool:()=>renderBoundsTool});
const sceneRenderIndex=new SceneRenderIndex();
const renderBoundsPaths=new FloorRenderBoundsPathCache();
const renderBoundsResources=new FloorRenderBoundsResourceCache();
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
const MIB=1024*1024,DISK_LIMIT=512*MIB,DISK_LOW_WATER=Math.floor(DISK_LIMIT*9/10),IMAGE_MEMORY_SETTING_KEY='atlas-image-memory-budget-v1',IMAGE_MEMORY_SETTINGS=new Set(['auto','64','128','256','512']);
const DISK_WRITE_QUEUE_LIMIT=32*1024*1024,DISK_WRITE_QUEUE_COUNT=64;
const ARTWORK_LIMIT=8*MIB;const tokenIndex=new SpatialIndex(),movingTokens=new Set(),artwork=new ArtworkCache(ARTWORK_LIMIT);
function readImageMemorySetting(){try{const value=localStorage.getItem(IMAGE_MEMORY_SETTING_KEY)||'auto';return IMAGE_MEMORY_SETTINGS.has(value)?value:'auto';}catch{return 'auto';}}
let imageMemorySetting=readImageMemorySetting(),memoryLimit=resolveImageMemoryBudgetMiB(navigator.deviceMemory,imageMemorySetting)*MIB,assetLimit=memoryLimit-ARTWORK_LIMIT;
const METADATA_CACHE_LIMIT=4096,tokenRows=new Map(),diskTouches=new MetadataTouchTracker(METADATA_CACHE_LIMIT);
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
  const now=Date.now(),touch=diskTouches.due(key,now);
  return new Promise(resolve => { const tx = db.transaction('assets',touch?'readwrite':'readonly'), store = tx.objectStore('assets'), req = store.get(key);
    let blob = null,touched=false;
    req.onsuccess = () => { if(req.result){blob=req.result.blob;if(touch){store.put({...req.result,used:now});touched=true;}} };
    tx.oncomplete = () => {if(touched)diskTouches.record(key,now);resolve(blob);}; tx.onerror = tx.onabort = () => resolve(null);
  });
}
function diskPut(key, blob) {
  const release=diskWriteBudget.acquire(blob.size);if(!release){diskDrops++;return;}
  cacheQueue = cacheQueue.then(async () => {
    const db = await dbPromise; if (!db || !diskEnabled) return;await diskReady;
    await new Promise(resolve => {const tx = db.transaction('assets','readwrite'), store = tx.objectStore('assets'), get=store.get(key);let total=diskBytes;
      get.onsuccess=()=>{const old=get.result;total+=blob.size-(old?.size||old?.blob?.size||0);store.put({key,blob,size:blob.size,used:Date.now()});diskWrites++;
        if(total<=DISK_LIMIT)return;
        const cursorReq=store.index('used').openCursor();cursorReq.onsuccess=()=>{const cursor=cursorReq.result;if(!cursor||total<=DISK_LOW_WATER)return;const row=cursor.value;if(row.key!==key){total-=row.size||row.blob?.size||0;cursor.delete();diskEvictions++;}cursor.continue();};
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
 if(value.type==='move'){value={...value,client:positionOutbox.data.client,after:positionOutbox.data.seq,previewRevision:state?.previewRevision||0};}
 const message=JSON.stringify(value);bytesOut+=message.length;socket.send(message);return true;
}
function renderInteractionMode(){
 const authorized=isGM(state?.you||campaign?.you),preview=!!state?.playerPreview,pending=!!previewSwitchPending;
 $('viewMode').hidden=!authorized||!state?.scene;$('gmView').classList.toggle('active',authorized&&!preview);$('playerView').classList.toggle('active',authorized&&preview);$('gmView').disabled=pending;$('playerView').disabled=pending;$('viewMode').classList.toggle('pending',pending);
 if(state||campaign)$('roleBadge').textContent=authorized?(preview?'ВЕДУЩИЙ · PLAYER VIEW':'ВЕДУЩИЙ'):'ИГРОК';
 document.querySelectorAll('.gm').forEach(el=>el.hidden=!authorized||!!state&&!isEditorView()&&!el.classList.contains('preview-always'));
}
function clearBuildInteraction(){
 if(drag?.type==='token'||drag?.type==='element')endDrag(true);else drag=null;
 clearTimeout(elementAutosaveTimer);elementAutosaveTimer=null;if(selectedElement)elementDrafts.delete(selectedElement);
 definitionsEditor.close();
 selectedElement=null;selectedTransition='';transitionDraft=null;transitionCursor=null;walkableTool='';selectedWalkableComponent='';renderBoundsTool='';renderBoundsDraft=null;selectedRenderBoundsVertex=-1;
}
function attemptPreviewSwitch(){
 clearTimeout(previewSwitchTimer);previewSwitchTimer=null;const target=previewSwitchPending;if(!target||target.sent||!state?.scene?.id)return;
 if(positionOutbox?.inflight||positionOutbox?.data.queue.length){previewSwitchTimer=setTimeout(attemptPreviewSwitch,50);return;}
 if(send({type:'playerPreview',sceneId:state.scene.id,enabled:target.enabled})){target.sent=true;renderInteractionMode();}else previewSwitchTimer=setTimeout(attemptPreviewSwitch,250);
}
function requestPlayerPreview(enabled){
 if(!state?.scene?.id||!isGM(state.you)||previewSwitchPending||!!state.playerPreview===enabled)return;
 previewSelection=selected||state.activeTokenId||'';clearBuildInteraction();preferredPlayerPreview=enabled;previewSwitchPending={enabled,sent:false};renderPanels();attemptPreviewSwitch();dirty=true;
}
$('gmView').onclick=()=>requestPlayerPreview(false);
$('playerView').onclick=()=>requestPlayerPreview(true);
function viewportStorageKey(sceneID){return `atlas-viewport-v1/${credentials.session}/${credentials.key}/${sceneID}`;}
function activeTokenStorageKey(sceneID){return `atlas-active-token-v1/${credentials.session}/${credentials.key}/${sceneID}`;}
function readSavedActiveToken(sceneID){try{return localStorage.getItem(activeTokenStorageKey(sceneID))||'';}catch{return '';}}
function rememberActiveToken(sceneID,tokenID){try{if(tokenID)localStorage.setItem(activeTokenStorageKey(sceneID),tokenID);else localStorage.removeItem(activeTokenStorageKey(sceneID));}catch{}}
function readSavedViewport(sceneID,mapID){try{const saved=JSON.parse(localStorage.getItem(viewportStorageKey(sceneID))||'null');if(!saved||saved.map!==(mapID||'')||![saved.cx,saved.cy,saved.scale].every(Number.isFinite)||saved.scale<.015||saved.scale>4)return null;return saved;}catch{return null;}}
function saveViewportNow(){clearTimeout(viewportSaveTimer);viewportSaveTimer=null;if(!viewportSaveDirty||!state?.scene?.id||!credentials)return;const scale=camera.scale,cx=camera.x+viewport.w/(2*scale),cy=camera.y+viewport.h/(2*scale);try{localStorage.setItem(viewportStorageKey(state.scene.id),JSON.stringify({cx,cy,scale,map:''}));viewportSaveDirty=false;viewportLastSave=Date.now();}catch{}}
function scheduleViewportSave(){if(!state?.scene?.id)return;viewportSaveDirty=true;if(viewportSaveTimer)return;const delay=Math.max(0,VIEWPORT_SAVE_INTERVAL-(Date.now()-viewportLastSave));viewportSaveTimer=setTimeout(()=>{viewportSaveTimer=null;saveViewportNow();},delay);}
function initializeSceneCamera(snapshot){
 const saved=readSavedViewport(snapshot.scene.id,'');let cx,cy,scale;
 if(saved){({cx,cy,scale}=saved);}else{const floorId=snapshot.currentFloorId||orderedFloors(snapshot)[0]?.id||'',bounds=floorCameraBounds(snapshot,floorId),entry=snapshot.entry,fitScale=bounds?Math.min(2,(viewport.w-70)/bounds.width,(viewport.h-70)/bounds.height):INITIAL_SCENE_SCALE,regionScale=Math.max(viewport.w,viewport.h)*(1+REGION_PREFETCH*2)/INITIAL_REGION_WORLD_SPAN;scale=Math.min(2,Math.max(INITIAL_SCENE_SCALE,regionScale,fitScale));cx=entry?.x??(bounds?(bounds.left+bounds.right)/2:0);cy=entry?.y??(bounds?(bounds.top+bounds.bottom)/2:0);}
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
function openScene(sceneID,preserveCharacterWatch=false){if(!sceneID||state?.scene?.id===sceneID)return;desiredSceneID=sceneID;clearSceneState(preserveCharacterWatch);$('connection').textContent='Открываем сцену…';send({type:'subscribe',sceneId:sceneID,activeTokenId:readSavedActiveToken(sceneID),enabled:isGM(campaign?.you)?preferredPlayerPreview:undefined});}
function goCampaignHome(){desiredSceneID='';clearSceneState();if(campaign)scenes.showHome();send({type:'subscribe',sceneId:''});}
function connect(){
 characterWatchNeedsResume=true;
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
  if(characterWatch.handle(msg))return;
  if(msg.type==='registryChanged'){if(isGM(state?.you||campaign?.you)){refreshCharacterCatalog();definitionsEditor.registryChanged();}return;}
  if(msg.type==='presence'&&state){
   if(msg.member)state.members[msg.member.id]=msg.member;
   if(msg.member)state.online[msg.member.id]=!!msg.online;
   renderMembers();if(selected)fillProperties();return;
  }
  if(msg.type==='campaignSnapshot'){
   campaign=msg;scenes.update(campaign);
   retry=0;$('connection').textContent='● В сети';$('connection').className='status connected';$('sessionTitle').textContent=msg.name;renderInteractionMode();
   definitionsEditor.setAuthorized(isGM(msg.you));if(isGM(msg.you)&&characterCatalogSession!==credentials.session){characterCatalogSession=credentials.session;refreshCharacterCatalog();}else if(!isGM(msg.you)){characterCatalogSession='';characterCatalog.clear();}
   if(outboxNeedsResume){outboxNeedsResume=false;outbox.reconnect();positionOutbox.reconnect();}
   const resumeCharacterWatch=characterWatchNeedsResume&&!!characterWatch.characterId;if(characterWatchNeedsResume){characterWatchNeedsResume=false;characterWatch.reconnect();}
   const active=state?.scene?.id,summary=msg.scenes.find(scene=>scene.id===active),accessible=id=>msg.scenes.some(scene=>scene.id===id);
   if(active&&summary){state.scene={...state.scene,name:summary.name,published:summary.published};$('sceneLabel').textContent=summary.name;scenes.showScene();return;}
   if(active&&!accessible(active))desiredSceneID='';
   if(!state&&desiredSceneID&&accessible(desiredSceneID)){const target=desiredSceneID;desiredSceneID='';openScene(target,resumeCharacterWatch);return;}
   desiredSceneID='';clearSceneState();scenes.showHome();
   return;
  }
  if(msg.type==='tokenLocatorUpsert'||msg.type==='tokenLocatorMove'||msg.type==='tokenLocatorDelete'){
   if(!state||msg.sceneId!==state.scene.id||isEditorView())return;
   const prior=state.ownedTokens?.[msg.id];
   if(msg.type==='tokenLocatorDelete'){
    delete state.ownedTokens[msg.id];removeTokenRow(msg.id);
    if(drag?.type==='token'&&drag.id===msg.id)drag=null;
    if(selected===msg.id)select(null);
   }else if(msg.type==='tokenLocatorMove'){
    if(!prior){send({type:'sync'});return;}
    const locator={...prior,x:msg.x,y:msg.y};state.ownedTokens[msg.id]=locator;updateTokenRow(locator);
   }else if(msg.locator){
    state.ownedTokens[msg.id]=msg.locator;updateTokenRow(msg.locator);
   }
   if(Number.isSafeInteger(msg.revision))state.revision=msg.revision;
   $('revision').textContent=`Ревизия ${state.revision}`;dirty=true;return;
  }
  if(msg.type==='snapshot'){
   if(!desiredSceneID||msg.scene.id!==desiredSceneID)return;
	  const draggedFloor=drag?.type==='token'?state?.tokens?.[drag.id]?.floorId:'',priorCatalog=state?.elementCatalog,priorCatalogVersion=state?._catalogVersion||0,wasPreview=!!state?.playerPreview;const initialRegion=msg.region==null;state=msg;if(isGM(state.you)&&!state.playerPreview){if(state.elementCatalog)state._catalogVersion=priorCatalogVersion+1;else{state.elementCatalog=priorCatalog||{};state._catalogVersion=priorCatalogVersion;}}else{state.elementCatalog={};state._catalogVersion=0;}if(!initialRegion)regionInFlight=false;desiredSceneID=msg.scene.id;scenes.update(campaign);scenes.showScene();retry=0;
   preferredPlayerPreview=!!state.playerPreview;if(previewSwitchPending&&previewSwitchPending.enabled===preferredPlayerPreview)previewSwitchPending=null;clearTimeout(previewSwitchTimer);previewSwitchTimer=null;$('connection').textContent='● В сети';$('connection').className='status connected';$('sessionTitle').textContent=msg.name;dice.setScene(msg.scene.id,msg.you.id);renderInteractionMode();
   if(!sceneCameraInitialized){initializeSceneCamera(msg);sceneCameraInitialized=true;}
	  state.ownedTokens=state.ownedTokens||{};currentFloorID=msg.currentFloorId||orderedFloors(state)[0]?.id||'';confirmedActiveTokenID=state.activeTokenId||'';confirmedFloorID=currentFloorID;pendingActiveSwitch=null;sceneRenderIndex.reset();renderBoundsPaths.syncState(state);renderBoundsResources.syncState(state);if(usesPlayerInteraction())rememberActiveToken(state.scene.id,state.activeTokenId||'');resolveCreatedTransition();if(selectedTransition&&!state.transitions?.[selectedTransition])selectedTransition='';if(selectedWalkableComponent&&!state.floors?.[currentFloorID]?.walkableComponents?.some(component=>component.id===selectedWalkableComponent))selectedWalkableComponent='';if(drag?.type==='token'&&draggedFloor&&state.tokens?.[drag.id]?.floorId!==draggedFloor)drag=null;if(drag?.type?.startsWith('walkable')&&drag.geometryRevision!==state.floors?.[drag.floorId]?.geometryRevision)drag=null;if(drag?.type?.startsWith('renderBounds')&&drag.geometryRevision!==state.floors?.[drag.floorId]?.geometryRevision){drag=null;selectedRenderBoundsVertex=-1;}if(renderBoundsDraft&&renderBoundsDraft.geometryRevision!==state.floors?.[renderBoundsDraft.floorId]?.geometryRevision)renderBoundsDraft=null;
   if(pendingActiveFocus&&pendingActiveFocus===state.activeTokenId){const token=navigationToken(pendingActiveFocus);if(token){camera.x=token.x-viewport.w/(2*camera.scale);camera.y=token.y-viewport.h/(2*camera.scale);scheduleViewportSave();}pendingActiveFocus='';}
   for(const id of visuals.keys())if(!state.tokens[id])visuals.delete(id);
	  rebuildTokenIndex();if(isEditorView()&&previewSelection&&state.tokens[previewSelection]){select(previewSelection);previewSelection='';}if(selected&&(!state.tokens[selected]||(usesPlayerInteraction()&&!state.ownedTokens[selected])))select(null);else if(selected)syncCharacterWatch();if(selectedElement&&!state.elementCatalog?.[selectedElement])selectElement(null);renderPanels();if(!wasPreview&&state.playerPreview&&previewSelection&&state.ownedTokens[previewSelection]&&state.activeTokenId!==previewSelection)activatePlayerToken(state.ownedTokens[previewSelection],false);dirty=true;ensureSceneRegion(currentViewRegion());if(!outbox.inflight)outbox.flush();if(!positionOutbox.inflight)positionOutbox.flush();return;
  }
  if(msg.type==='error'){const correction=blockedMoveCorrection(msg,drag,state?.tokens?.[msg.id],state?.scene?.id);if(correction){drag=correction.drag;if(correction.token){state.tokens[msg.id]=correction.token;tokenIndex.set(correction.token);}dirty=true;return;}regionInFlight=false;pendingActiveFocus='';if(msg.operation==='playerPreview'){preferredPlayerPreview=!!state?.playerPreview;previewSwitchPending=null;clearTimeout(previewSwitchTimer);previewSwitchTimer=null;renderInteractionMode();}if(msg.operation==='activeToken'&&pendingActiveSwitch&&state){state.activeTokenId=pendingActiveSwitch.activeTokenId;state.currentFloorId=pendingActiveSwitch.floorId;currentFloorID=pendingActiveSwitch.floorId;camera={...pendingActiveSwitch.camera};rememberActiveToken(state.scene.id,state.activeTokenId||'');pendingActiveSwitch=null;renderPanels();send({type:'sync'});dirty=true;}toast(msg.message);drag=null;if(!state&&desiredSceneID){desiredSceneID='';scenes.showHome();}return;}
  if(dice.handle(msg))return;
  if(!state)return;
  if(msg.sceneId!==state.scene.id)return;
  if(!Number.isSafeInteger(msg.delivery)){send({type:'sync'});return;}
   if(msg.delivery<=state.delivery)return;
   if(msg.delivery!==state.delivery+1){send({type:'sync'});return;}
   if(msg.type.startsWith('element')){
    const old=state.elements?.[msg.id];let next=msg.element;if(msg.asset?.id)state.assets[msg.asset.id]=msg.asset;if(msg.type==='elementTransform'){if(!old){send({type:'sync'});return;}next={...old,transform:msg.transform};}
    state.revision=msg.revision;state.delivery=msg.delivery;
    if(msg.catalogElement){state.elementCatalog[msg.catalogElement.id]=msg.catalogElement;state._catalogVersion=(state._catalogVersion||0)+1;}else if(msg.catalogDeleted){delete state.elementCatalog[msg.id];state._catalogVersion=(state._catalogVersion||0)+1;if(selectedElement===msg.id)selectElement(null);}
    if(msg.type==='elementDelete'){delete state.elements[msg.id];sceneRenderIndex.elementChanged(old,null);if(!state.elementCatalog?.[msg.id]&&selectedElement===msg.id)selectElement(null);if(old?.assetId)pruneAssetMetadata(old.assetId);}else if(msg.type==='elementUpsert'&&next){state.elements[next.id]=next;sceneRenderIndex.elementChanged(old,next);}else if(msg.type==='elementTransform'&&next){state.elements[next.id]=next;sceneRenderIndex.elementChanged(old,next);}
    if(msg.type!=='elementTransform'){renderPanels();}else if(selectedElement===msg.id)fillElementProperties();dirty=true;return;
   }
   const old=state.tokens[msg.id];let next=msg.token;if(msg.asset?.id)state.assets[msg.asset.id]=msg.asset;
  if(msg.type==='move'){
   if(!old){send({type:'sync'});return;}
   next={...old,x:msg.x,y:msg.y};
  }
   const propertiesChanged=!old||!next||old.name!==next.name||old.size!==next.size||old.rotation!==next.rotation||old.floorId!==next.floorId||old.color!==next.color||old.opacity!==next.opacity||!sameValue(old.ownerIds,next.ownerIds)||old.hidden!==next.hidden||old.asset!==next.asset,characterLinkChanged=old?.characterInstanceId!==next?.characterInstanceId;
  state.revision=msg.revision;
  state.delivery=msg.delivery;
	  if(drag?.type==='token'&&msg.id===drag.id&&msg.type==='upsert'&&old&&(next?.floorId!==old.floorId||next?.x!==drag.x||next?.y!==drag.y))drag=null;
	  if(msg.type==='delete'){
   delete state.tokens[msg.id];visuals.delete(msg.id);tokenIndex.remove(msg.id);movingTokens.delete(msg.id);const listed=state.ownedTokens?.[msg.id];if(usesPlayerInteraction()&&listed)updateTokenRow(listed);else removeTokenRow(msg.id);
   }else if(next){
    state.tokens[msg.id]=next;tokenIndex.set(next);if(!old||old.x!==next.x||old.y!==next.y)movingTokens.add(msg.id);if(usesPlayerInteraction()){const listed=state.ownedTokens[msg.id];if(listed)updateTokenRow(listed);else removeTokenRow(msg.id);}else if(next.floorId===currentFloorID&&propertiesChanged)updateTokenRow(next);else if(next.floorId!==currentFloorID)removeTokenRow(next.id);
  }
  $('revision').textContent=`Ревизия ${state.revision}`;
  if(propertiesChanged&&selected===msg.id)fillProperties();
  if(characterLinkChanged&&selected===msg.id){syncCharacterWatch();renderCharacterPanel();}
  if(old?.asset&&old.asset!==next?.asset)pruneAssetMetadata(old.asset);
  if(selected&&!state.tokens[selected]&&(isEditorView()||!state.ownedTokens?.[selected]))select(null);
  dirty=true;
 };
 socket.onclose=()=>{clearTimeout(commandTimer);clearTimeout(positionCommandTimer);abortUploads();characterWatch.disconnect();dice.disconnect();if(stopped)return;outbox.inflight=false;positionOutbox.inflight=false;if(drag?.type==='token'||drag?.type==='element')endDrag();drag=null;dirty=true;$('connection').textContent='● Нет связи · повтор…';$('connection').className='status';reconnectTimer=setTimeout(connect,Math.min(1000*2**retry++,10000));};
 socket.onerror=()=>socket.close();
}
$('leaveCampaign').onclick=()=>{saveViewportNow();stopped=true;socket?.close();location.href='/';};
$('invite').onclick=async()=>{const url=`${location.origin}/#session=${credentials.session}&invite=${inviteCode}`;if(!inviteCode){toast('Приглашение доступно в исходной вкладке ведущего.');return;}try{await navigator.clipboard.writeText(url);toast('Ссылка для игроков скопирована');}catch{prompt('Скопируйте ссылку для игроков',url);}};
function canMove(t){return !!state&&(isEditorView()||(isGM(state.you)&&state.playerPreview?true:t.ownerIds?.includes(state.you.id)&&!t.hidden));}
function navigationToken(id){return state?.tokens?.[id]||state?.ownedTokens?.[id];}
function activatePlayerToken(token,focus){
 if(!token||!usesPlayerInteraction()||(!isGM(state.you)&&(token.hidden||!token.ownerIds?.includes(state.you.id))))return false;
 if(!needsActiveTokenRequest(token,focus,confirmedActiveTokenID,confirmedFloorID,pendingActiveSwitch)){select(token.id);dirty=true;return true;}
 const region=boundedRegion(currentViewRegion()),previous={activeTokenId:confirmedActiveTokenID,floorId:confirmedFloorID||currentFloorID,camera:{...camera}};
 if(!send({type:'activeToken',sceneId:state.scene.id,activeTokenId:token.id,focus,region,previewRevision:state.previewRevision||0}))return false;
 regionInFlight=true;pendingActiveSwitch=previous;
 if(focus){pendingActiveFocus=token.id;camera.x=token.x-viewport.w/(2*camera.scale);camera.y=token.y-viewport.h/(2*camera.scale);scheduleViewportSave();}
 state.activeTokenId=token.id;state.currentFloorId=token.floorId;currentFloorID=token.floorId;rememberActiveToken(state.scene.id,token.id);select(token.id);
 renderPanels();dirty=true;return true;
}
function abortUploads(){for(const controller of uploadControllers)controller.abort();uploadControllers.clear();}
function clearSceneState(preserveCharacterWatch=false){saveViewportNow();clearTimeout(viewportSaveTimer);clearTimeout(tokenAutosaveTimer);clearTimeout(elementAutosaveTimer);clearTimeout(previewSwitchTimer);viewportSaveTimer=null;tokenAutosaveTimer=null;elementAutosaveTimer=null;previewSwitchTimer=null;previewSwitchPending=null;viewportSaveDirty=false;abortUploads();dice.setScene('');if(preserveCharacterWatch){const previous=selected;selected=null;if(previous&&tokenRows.has(previous))tokenRows.get(previous).classList.remove('selected');fillProperties();}else select(null);selectElement(null);selectedTransition='';transitionDraft=null;transitionCursor=null;walkableTool='';selectedWalkableComponent='';renderBoundsTool='';renderBoundsDraft=null;selectedRenderBoundsVertex=-1;drag=null;state=null;currentFloorID='';pendingActiveFocus='';confirmedActiveTokenID='';confirmedFloorID='';pendingActiveSwitch=null;regionInFlight=false;sceneCameraInitialized=false;rotationOverrides.clear();tokenIndex.clear();sceneRenderIndex.clear();renderBoundsPaths.clear();renderBoundsResources.clear();visuals.clear();movingTokens.clear();for(const id of [...tokenRows.keys()])removeTokenRow(id);wanted.clear();imagePlans.clear();for(const controller of pending.values())controller.abort();$('emptyMap').hidden=false;renderInteractionMode();dirty=true;}
function rebuildTokenIndex(){tokenIndex.clear();movingTokens.clear();for(const t of Object.values(state.tokens)){tokenIndex.set(t);const v=visuals.get(t.id);if(v&&(v.x!==t.x||v.y!==t.y))movingTokens.add(t.id);}}
function renderMembers(){
 $('members').replaceChildren();let online=0;
 const gmCount=Object.values(state.members).filter(isGM).length;
  for(const m of Object.values(state.members)){const row=document.createElement('div');row.className='member';const dot=document.createElement('span');dot.className='live-dot'+(state.online[m.id]?'':' offline');const name=document.createElement('span');name.textContent=m.name;const role=document.createElement('small');role.textContent=isGM(m)?'ведущий':'игрок';row.append(dot,name,role);if(isEditorView()){const toggle=document.createElement('button');toggle.type='button';toggle.className='member-role-toggle';toggle.textContent=isGM(m)?'Снять GM':'Сделать GM';toggle.title=isGM(m)?'Убрать права ведущего':'Дать права ведущего';toggle.disabled=isGM(m)&&gmCount<=1;toggle.onclick=()=>queueCommand('memberUpdate',{memberId:m.id,gm:!isGM(m)});row.append(toggle);}$('members').append(row);if(state.online[m.id])online++;}
 $('memberCount').textContent=online;
}
function renderPanels(){
 const gm=isEditorView();$('mapTab').hidden=!gm;if(!gm&&!sidebarSections.map.panel.hidden)showSidebarSection('objects');renderInteractionMode();
 $('noSelection').querySelector('p').textContent=gm?'Выберите элемент сцены или переход':'Здесь появится лист персонажа';
 $('emptyMap').hidden=!!Object.keys(gm?state.elementCatalog||{}:state.elements||{}).length;$('sceneLabel').textContent=state.floors?.[currentFloorID]?`${state.scene.name} · ${state.floors[currentFloorID].name}`:state.scene.name;
	 renderMembers();renderTokenList();fillProperties();fillElementProperties();fillTransitionProperties();sceneTree.update(state,selectedElement,selectedTransition,gm);
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
 if(!state)return;$('revision').textContent=`Ревизия ${state.revision}`;const listed=usesPlayerInteraction()?Object.values(state.ownedTokens||{}):Object.values(state.tokens).filter(t=>t.floorId===currentFloorID),ids=new Set(listed.map(t=>t.id));
 for(const id of tokenRows.keys())if(!ids.has(id))removeTokenRow(id);
 for(const t of listed)updateTokenRow(t);
 $('emptyTokens').hidden=ids.size>0;
}
function fillCatalogSelect(control,items,emptyText){const selectedValue=control.value;control.replaceChildren();const empty=document.createElement('option');empty.value='';empty.textContent=emptyText;control.append(empty);for(const item of items){const option=document.createElement('option');option.value=item.id;option.textContent=item.name||item.id;control.append(option);}if(items.some(item=>item.id===selectedValue))control.value=selectedValue;}
function cancelCharacterStatTimers(characterID='',discard=false){for(const [key,timer]of characterStatTimers)if(!characterID||key.startsWith(`${characterID}/`)){clearTimeout(timer);characterStatTimers.delete(key);}if(discard&&characterID)characterDrafts.delete(characterID);}
function onCharacterWatchChange(snapshot,reason,characterID){characterWatchReason=snapshot?'':reason;if(!snapshot){const revoked=reason==='accessRevoked'||reason==='unavailable'||reason==='closed';cancelCharacterStatTimers(characterID,revoked);if(revoked)for(const key of [...pendingNewStats])if(key.startsWith(`${characterID}/`))pendingNewStats.delete(key);if(characterAvatarDisplayController)characterAvatarDisplayController.abort();characterAvatarUploads.get(characterID)?.abort();}renderCharacterPanel();}
function avatarPath(assetID){const query=new URLSearchParams({session:credentials.session});return `/api/asset/${encodeURIComponent(assetID)}/avatar.png?${query}`;}
async function showCharacterAvatar(assetID,characterID){
 if(assetID===characterAvatarAsset)return;if(characterAvatarDisplayController)characterAvatarDisplayController.abort();characterAvatarDisplayController=null;if(characterAvatarURL)URL.revokeObjectURL(characterAvatarURL);characterAvatarURL='';characterAvatarAsset=assetID||'';$('characterAvatar').hidden=true;$('characterAvatarEmpty').hidden=false;if(!assetID)return;
 const controller=new AbortController();characterAvatarDisplayController=controller;try{const response=await fetch(avatarPath(assetID),{headers:{Authorization:`Bearer ${credentials.key}`},signal:controller.signal});if(!response.ok)throw new Error(`Avatar: HTTP ${response.status}`);const blob=await response.blob();if(controller.signal.aborted||characterWatch.snapshot?.characterId!==characterID||characterWatch.snapshot?.effective?.avatarAssetId!==assetID)return;characterAvatarURL=URL.createObjectURL(blob);$('characterAvatar').src=characterAvatarURL;$('characterAvatar').hidden=false;$('characterAvatarEmpty').hidden=true;}catch(error){if(error.name!=='AbortError')toast(error.message);}finally{if(characterAvatarDisplayController===controller)characterAvatarDisplayController=null;}
}
function queueCharacterStat(characterID,statID,value,isNew=false){if(!characterWatch.snapshot?.permissions?.edit||characterWatch.characterId!==characterID)return false;if(outbox?.data.queue.some(command=>command.type==='characterStatSet'&&command.characterId===characterID&&command.statId===statID&&sameValue(command.statValue,value)))return true;if(isNew)pendingNewStats.add(`${characterID}/${statID}`);return queueCommand('characterStatSet',{characterId:characterID,statId:statID,statValue:value});}
function scheduleCharacterStat(characterID,statID,value){characterDrafts.set(characterID,statID,value);const key=`${characterID}/${statID}`;clearTimeout(characterStatTimers.get(key));characterStatTimers.set(key,setTimeout(()=>{characterStatTimers.delete(key);if(characterWatch.characterId===characterID&&characterWatch.snapshot?.permissions?.edit)queueCharacterStat(characterID,statID,value);},FORM_AUTOSAVE_DELAY));}
function renderCharacterStats(snapshot){
 const container=$('characterStats');container.replaceChildren();const effective=snapshot.effective?.stats||{},drafts=characterDrafts.get(snapshot.characterId),ids=Object.keys(effective).sort((a,b)=>(snapshot.statDefinitions?.[a]?.name||a).localeCompare(snapshot.statDefinitions?.[b]?.name||b));
 for(const statID of ids){const definition=snapshot.statDefinitions?.[statID]||{id:statID,name:statID,type:effective[statID].type},server=effective[statID],value=Object.hasOwn(drafts,statID)?drafts[statID]:server,override=Object.hasOwn(snapshot.instance?.statOverrides||{},statID),row=document.createElement('div');row.className='character-stat';const heading=document.createElement('div');heading.className='character-stat-heading';const name=document.createElement('strong');name.textContent=definition.name||statID;const source=document.createElement('small');source.textContent=override?'Переопределено':'Наследуется';heading.append(name,source);const input=document.createElement('input');input.dataset.statId=statID;input.disabled=!snapshot.permissions?.edit;if(definition.type==='boolean'){input.type='checkbox';input.checked=!!value.value;}else{input.type=definition.type==='string'?'text':'number';input.value=statInputValue(value);if(definition.type==='integer')input.step='1';else if(definition.type==='number')input.step='any';}
  const remember=()=>{try{const next=statValueFromInput(definition.type,input.value,input.checked);scheduleCharacterStat(snapshot.characterId,statID,next);}catch(error){input.setCustomValidity(error.message);return false;}input.setCustomValidity('');return true;};input.addEventListener('input',()=>{remember();});input.addEventListener('change',()=>{if(remember()){const key=`${snapshot.characterId}/${statID}`;clearTimeout(characterStatTimers.get(key));characterStatTimers.delete(key);queueCharacterStat(snapshot.characterId,statID,characterDrafts.get(snapshot.characterId)[statID]);}});input.addEventListener('blur',()=>{if(input.checkValidity()&&Object.hasOwn(characterDrafts.get(snapshot.characterId),statID))queueCharacterStat(snapshot.characterId,statID,characterDrafts.get(snapshot.characterId)[statID]);});row.append(heading,input);
  if(override){const reset=document.createElement('button');reset.type='button';reset.className='subtle character-stat-reset';reset.textContent='Сбросить к пресету';reset.disabled=!snapshot.permissions?.edit;reset.onclick=()=>{const key=`${snapshot.characterId}/${statID}`;clearTimeout(characterStatTimers.get(key));characterStatTimers.delete(key);characterDrafts.confirm(snapshot.characterId,{[statID]:characterDrafts.get(snapshot.characterId)[statID]});queueCommand('characterStatReset',{characterId:snapshot.characterId,statId:statID});};row.append(reset);}container.append(row);
 }
 $('characterStatAdd').hidden=!snapshot.permissions?.edit;
}
function formatRoll(roll){const modifier=[roll.modifierStat,roll.modifierFixed?String(roll.modifierFixed):''].filter(Boolean).join(' + ');return `${roll.count}d${roll.sides}${modifier?` + ${modifier}`:''}`;}
function renderCharacterActions(snapshot){const container=$('characterActions');container.replaceChildren();for(const actionID of snapshot.effective?.actionIds||[]){const action=snapshot.actions?.[actionID],row=document.createElement('div');row.className='character-action';const title=document.createElement('strong');title.textContent=action?.name||actionID;row.append(title);if(action?.description){const description=document.createElement('p');description.textContent=action.description;row.append(description);}for(const roll of action?.rolls||[]){const button=document.createElement('button');button.type='button';button.className='subtle character-roll';button.textContent=`${roll.name||roll.id}: ${formatRoll(roll)}`;button.onclick=()=>dice.rollAction(snapshot.characterId,actionID,roll.id);row.append(button);}if(snapshot.permissions?.manage&&isEditorView()){const remove=document.createElement('button');remove.type='button';remove.className='danger';remove.textContent='Убрать';remove.onclick=()=>queueCommand('characterActionRemove',{characterId:snapshot.characterId,actionId:actionID});row.append(remove);}container.append(row);}if(!container.children.length)container.textContent='Нет действий.';
 const effective=new Set(snapshot.effective?.actionIds||[]),available=characterCatalog.actions.filter(action=>!effective.has(action.id));fillCatalogSelect($('characterActionAdd'),available,characterCatalog.loading.has('actions')?'Загрузка…':'Выберите действие');const manage=snapshot.permissions?.manage&&isEditorView();$('characterActionManage').hidden=!manage;$('characterActionAddButton').disabled=!$('characterActionAdd').value;$('characterActionMore').hidden=!manage||!characterCatalog.next.actions;$('characterActionMore').disabled=characterCatalog.loading.has('actions');
}
function renderCharacterPanel(){
 const token=state?.tokens?.[selected],snapshot=characterWatch.snapshot,linkedID=token?.characterInstanceId||'',gm=isEditorView();characterPanel.hidden=!token;characterPanel.dataset.characterId=linkedID;
 if(!token){showCharacterAvatar('','');return;}
 const ready=!!linkedID&&snapshot?.characterId===linkedID,instance=ready?snapshot.instance:null;
 const unavailable=!!linkedID&&!ready&&['accessRevoked','unavailable','closed'].includes(characterWatchReason);$('characterEmpty').hidden=!!linkedID;$('characterUnavailable').hidden=!unavailable;$('characterPending').hidden=!linkedID||ready||unavailable;$('characterSummary').hidden=!ready;
 if(ready){$('characterName').textContent=snapshot.effective?.name||instance.name||'Без имени';const preset=snapshot.preset?.name||instance.presetId,avatarOverride=Object.hasOwn(instance,'avatarAssetId');$('characterMeta').textContent=[preset?`Пресет: ${preset}`:'Без пресета',avatarOverride?'avatar переопределён':'avatar наследуется'].join(' · ');renderCharacterStats(snapshot);renderCharacterActions(snapshot);$('characterAvatarControls').hidden=!snapshot.permissions?.edit;$('characterAvatarReset').disabled=!snapshot.permissions?.edit||!avatarOverride;showCharacterAvatar(snapshot.effective?.hasAvatar?snapshot.effective.avatarAssetId:'',snapshot.characterId);}else showCharacterAvatar('','');
 fillCatalogSelect($('characterPreset'),characterCatalog.presets,characterCatalog.loading.has('presets')?'Загрузка…':'Выберите пресет');
 fillCatalogSelect($('characterRoster'),characterCatalog.roster,characterCatalog.loading.has('roster')?'Загрузка…':'Выберите персонажа');
 $('characterCreatePreset').disabled=!gm||!$('characterPreset').value;$('characterCreateEmpty').disabled=!gm;$('characterLink').disabled=!gm||!$('characterRoster').value;$('characterLink').textContent=linkedID?'Перепривязать к сохранённому':'Назначить сохранённого';
 $('characterPresetMore').hidden=!gm||!characterCatalog.next.presets;$('characterPresetMore').disabled=characterCatalog.loading.has('presets');
 $('characterRosterMore').hidden=!gm||!characterCatalog.next.roster;$('characterRosterMore').disabled=characterCatalog.loading.has('roster');
 $('characterPersistentLabel').hidden=!gm||!ready;$('characterPersistent').checked=!!instance?.persistent;$('characterUnlink').hidden=!gm||!linkedID;
}
function hasTransitionSelection(){return isEditorView()&&(!!transitionDraft||!!state?.transitions?.[selectedTransition]);}
function updateInspectorState(){const visible=!!state?.tokens?.[selected]||!!state?.elements?.[selectedElement]||isEditorView()&&!!state?.elementCatalog?.[selectedElement]||hasTransitionSelection();$('noSelection').hidden=visible;document.querySelector('.inspector').classList.toggle('has-selection',visible);renderCharacterPanel();}
function elementRotationAllowed(element){if(!element)return false;const lowest=orderedLayers(state,element.floorId).find(layer=>layer.kind==='visual');return lowest?.id!==element.layerId||rotationOverrides.has(element.id);}
function cancelTransitionDraft(){transitionDraft=null;transitionCursor=null;if(drag?.type?.startsWith('transition'))drag=null;fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition,isEditorView());dirty=true;}
function beginTransition(){if(!isEditorView())return;select(null);selectElement(null);selectedTransition='';transitionDraft={phase:'placingA',endpointA:null,endpointB:null,direction:'bidirectional'};transitionCursor=null;fillTransitionProperties();sceneTree.update(state,selectedElement,selectedTransition,true);canvas.focus();toast('Поставьте точку A перехода');dirty=true;}
function setWalkableTool(tool){if(!isEditorView())return;walkableTool=tool;selectedWalkableComponent='';renderBoundsTool='';renderBoundsDraft=null;selectedRenderBoundsVertex=-1;if(drag?.type?.startsWith('walkable')||drag?.type?.startsWith('renderBounds'))drag=null;select(null);selectElement(null);selectedTransition='';transitionDraft=null;sceneTree.update(state,selectedElement,selectedTransition,true);canvas.focus();dirty=true;}
function setRenderBoundsTool(tool){if(!isEditorView())return;renderBoundsTool=tool;renderBoundsDraft=null;selectedRenderBoundsVertex=-1;walkableTool='';selectedWalkableComponent='';if(drag?.type?.startsWith('walkable')||drag?.type?.startsWith('renderBounds'))drag=null;select(null);selectElement(null);selectedTransition='';transitionDraft=null;sceneTree.update(state,selectedElement,selectedTransition,true);canvas.focus();dirty=true;}
function renderBoundsPolygon(points){return {outer:points.map(point=>({x:point.x,y:point.y})),holes:[]};}
function cloneRenderBounds(polygon){return renderBoundsPolygon(polygon?.outer||[]);}
function finishRenderBoundsDraft(){if(!renderBoundsDraft)return;const polygon=renderBoundsPolygon(renderBoundsDraft.points);if(!validRenderBounds(polygon)){renderBoundsDraft.invalid=true;toast('Граница должна быть простым полигоном ненулевой площади');dirty=true;return;}if(queueCommand('setRenderBounds',{floorId:renderBoundsDraft.floorId,expectedGeometryRevision:renderBoundsDraft.geometryRevision,renderBounds:polygon}))renderBoundsDraft=null;dirty=true;}
function deleteSelectedRenderBoundsVertex(){const floor=state?.floors?.[currentFloorID],polygon=floor?.renderBounds;if(renderBoundsTool!=='edit'||!polygon||selectedRenderBoundsVertex<0)return false;if(polygon.outer.length<=3){toast('У границы должно остаться не меньше трёх вершин');return true;}const next=cloneRenderBounds(polygon),changedEdge=selectedRenderBoundsVertex-1;next.outer.splice(selectedRenderBoundsVertex,1);if(!validRenderBoundsChange(next,[changedEdge])){toast('Удаление создаёт недопустимую границу');return true;}if(queueCommand('setRenderBounds',{floorId:floor.id,expectedGeometryRevision:floor.geometryRevision,renderBounds:next}))selectedRenderBoundsVertex=-1;dirty=true;return true;}
function syncCharacterWatch(){const token=state?.tokens?.[selected];if(token?.characterInstanceId)characterWatch.watch(token.characterInstanceId);else characterWatch.unwatch();}
function select(id){const previous=selected;selected=id;if(id){selectedElement=null;selectedTransition='';transitionDraft=null;}if(previous&&tokenRows.has(previous))tokenRows.get(previous).classList.remove('selected');if(id&&tokenRows.has(id))tokenRows.get(id).classList.add('selected');syncCharacterWatch();fillProperties();fillElementProperties();fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition,isEditorView());dirty=true;}
function selectElement(id){if(id&&!isEditorView())return;if(id){select(null);selectedTransition='';transitionDraft=null;}selectedElement=id;fillProperties();fillElementProperties();fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition,isEditorView());dirty=true;}
function selectTransition(id){if(id&&!isEditorView())return;select(null);selectedElement=null;transitionDraft=null;selectedTransition=id||'';fillProperties();fillElementProperties();fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition,isEditorView());dirty=true;}
function setFloor(id,preserveTransition=false){if(!state?.floors?.[id]||!isEditorView()||id===currentFloorID)return;if(!preserveTransition){select(null);selectElement(null);selectedTransition='';transitionDraft=null;}renderBoundsDraft=null;selectedRenderBoundsVertex=-1;if(drag?.type?.startsWith('renderBounds'))drag=null;const region=boundedRegion(currentViewRegion());if(send({type:'view',sceneId:state.scene.id,viewFloorId:id,region}))regionInFlight=true;}
function focusElement(element){if(!element||!state?.elementCatalog?.[element.id])return;selectElement(element.id);const t=element.transform,angle=t.rotation*Math.PI/180,width=Math.abs(t.width*Math.cos(angle))+Math.abs(t.height*Math.sin(angle)),height=Math.abs(t.width*Math.sin(angle))+Math.abs(t.height*Math.cos(angle)),fitScale=Math.max(.015,Math.min(4,(viewport.w-100)/Math.max(1,width),(viewport.h-100)/Math.max(1,height)));camera.scale=Math.min(camera.scale,fitScale);camera.x=t.x+t.width/2-viewport.w/(2*camera.scale);camera.y=t.y+t.height/2-viewport.h/(2*camera.scale);scheduleViewportSave();if(element.floorId!==currentFloorID)setFloor(element.floorId,true);else ensureSceneRegion(currentViewRegion());dirty=true;}
function fillProperties(){const source=state?.tokens[selected];const t=source?{...source,...drafts.get(selected)}:null;$('properties').hidden=!t;updateInspectorState();if(!t)return;$('tokenName').value=t.name;$('tokenSize').value=t.size;$('tokenColor').value=/^#[0-9a-f]{6}$/i.test(t.color)?t.color:'#c2d89b';$('tokenOpacity').value=t.opacity??1;$('tokenHidden').checked=t.hidden;$('tokenPreview').style.background=t.color;$('tokenPreview').textContent=t.name.slice(0,1);$('tokenOwner').replaceChildren();const owners=new Set(t.ownerIds||[]),add=(id,name)=>{const option=document.createElement('option');option.value=id;option.textContent=name;option.selected=owners.has(id);$('tokenOwner').append(option);};for(const m of Object.values(state.members))if(!isGM(m))add(m.id,m.name);$('tokenFloor').replaceChildren();for(const floor of orderedFloors(state)){const option=document.createElement('option');option.value=floor.id;option.textContent=floor.name;$('tokenFloor').append(option);}$('tokenFloor').value=t.floorId;for(const el of $('properties').querySelectorAll('input,select'))el.disabled=!isEditorView();}
function fillElementProperties(){const loaded=state?.elements?.[selectedElement],source=isEditorView()?loaded||state?.elementCatalog?.[selectedElement]:null;$('elementProperties').hidden=!source;updateInspectorState();if(!source)return;const draft=elementDrafts.get(selectedElement),element={...source,...draft},t={...source.transform,...draft};$('elementName').value=element.name;$('elementX').value=t.x;$('elementY').value=t.y;$('elementWidth').value=t.width;$('elementHeight').value=t.height;$('elementRotation').value=t.rotation;$('elementOpacity').value=element.opacity;$('elementVisible').checked=element.visible;$('elementLocked').checked=element.locked;$('elementLayer').replaceChildren();for(const layer of orderedLayers(state,element.floorId).filter(layer=>layer.kind==='visual')){const option=document.createElement('option');option.value=layer.id;option.textContent=layer.name;$('elementLayer').append(option);}$('elementLayer').value=element.layerId;elementOffscreenNote.hidden=!!loaded;for(const id of ['elementX','elementY','elementWidth','elementHeight','elementRotation'])$(id).disabled=!loaded;for(const id of ['elementName','elementOpacity','elementLayer','elementVisible','elementLocked','deleteElement'])$(id).disabled=false;$('duplicateElement').disabled=!loaded;const rotationAllowed=loaded&&elementRotationAllowed(loaded);$('allowElementRotation').hidden=!!loaded&&rotationAllowed;$('allowElementRotation').disabled=!loaded;}
function transitionForInspector(){return transitionDraft||state?.transitions?.[selectedTransition]||null;}
function fillTransitionProperties(){const transition=isEditorView()?transitionForInspector():null;$('transitionProperties').hidden=!transition;updateInspectorState();if(!transition)return;const draft=!!transitionDraft,waiting=draft&&transition.phase==='awaitingCreate',a=transition.endpointA,b=transition.endpointB;$('transitionDirection').value=transition.direction||'bidirectional';for(const [select,endpoint]of [[$('transitionFloorA'),a],[$('transitionFloorB'),b]]){select.replaceChildren();for(const floor of orderedFloors(state)){const option=document.createElement('option');option.value=floor.id;option.textContent=floor.name;select.append(option);}select.value=endpoint?.floorId||currentFloorID;select.disabled=!endpoint||waiting;}$('transitionRadiusA').value=a?.radius??'';$('transitionRadiusB').value=b?.radius??'';$('transitionRadiusA').disabled=!a||waiting;$('transitionRadiusB').disabled=!b||waiting;$('transitionGoA').disabled=!a;$('transitionGoB').disabled=!b;$('transitionDirection').disabled=draft;$('transitionConfirm').hidden=!(draft&&(transition.phase==='editingA'||transition.phase==='editingB'));$('transitionCancel').hidden=!draft||waiting;$('transitionSave').hidden=draft;$('deleteTransition').hidden=draft;}
function currentTransitionEndpoint(key){return transitionForInspector()?.[key]||null;}
function cloneTransitionEndpoint(endpoint){return endpoint?{...endpoint,position:{...endpoint.position}}:endpoint;}
function transitionPosition(_floorId,position){return {...position};}
function navigateTransitionEndpoint(key){const endpoint=currentTransitionEndpoint(key);if(!endpoint)return;camera.x=endpoint.position.x-viewport.w/(2*camera.scale);camera.y=endpoint.position.y-viewport.h/(2*camera.scale);scheduleViewportSave();if(endpoint.floorId!==currentFloorID)setFloor(endpoint.floorId,true);dirty=true;}
function sameEndpoint(a,b){return a&&b&&a.floorId===b.floorId&&a.radius===b.radius&&a.position.x===b.position.x&&a.position.y===b.position.y;}
function resolveCreatedTransition(){if(!transitionDraft||transitionDraft.phase!=='awaitingCreate')return;const prior=new Set(transitionDraft.priorIds||[]),found=Object.values(state?.transitions||{}).find(item=>!prior.has(item.id)&&sameEndpoint(item.endpointA,transitionDraft.endpointA)&&sameEndpoint(item.endpointB,transitionDraft.endpointB));if(found){selectedTransition=found.id;transitionDraft=null;transitionCursor=null;}}
function confirmTransitionEndpoint(){if(!transitionDraft)return;if(transitionDraft.phase==='editingA'){transitionDraft.phase='placingB';transitionCursor={x:transitionDraft.endpointA.position.x,y:transitionDraft.endpointA.position.y};toast('Поставьте точку B перехода');}else if(transitionDraft.phase==='editingB'){const payload={name:'Переход',endpointA:cloneTransitionEndpoint(transitionDraft.endpointA),endpointB:cloneTransitionEndpoint(transitionDraft.endpointB),direction:'bidirectional'};if(queueCommand('transitionCreate',{transition:payload}))transitionDraft={...transitionDraft,phase:'awaitingCreate',priorIds:Object.keys(state?.transitions||{})};}fillTransitionProperties();dirty=true;}
$('transitionConfirm').onclick=confirmTransitionEndpoint;
$('transitionCancel').onclick=cancelTransitionDraft;
$('transitionGoA').onclick=()=>navigateTransitionEndpoint('endpointA');
$('transitionGoB').onclick=()=>navigateTransitionEndpoint('endpointB');
$('transitionFloorA').onchange=()=>{if(transitionDraft?.endpointA){const floorId=$('transitionFloorA').value;transitionDraft.endpointA={...transitionDraft.endpointA,floorId,position:transitionPosition(floorId,transitionDraft.endpointA.position)};dirty=true;}};
$('transitionFloorB').onchange=()=>{if(transitionDraft?.endpointB){const floorId=$('transitionFloorB').value;transitionDraft.endpointB={...transitionDraft.endpointB,floorId,position:transitionPosition(floorId,transitionDraft.endpointB.position)};dirty=true;}};
$('transitionRadiusA').oninput=()=>{if(transitionDraft?.endpointA){const radius=Math.max(8,Number($('transitionRadiusA').value)||8);transitionDraft.endpointA={...transitionDraft.endpointA,radius};dirty=true;}};
$('transitionRadiusB').oninput=()=>{if(transitionDraft?.endpointB){const radius=Math.max(8,Number($('transitionRadiusB').value)||8);transitionDraft.endpointB={...transitionDraft.endpointB,radius};dirty=true;}};
$('transitionProperties').onsubmit=e=>{e.preventDefault();const transition=state?.transitions?.[selectedTransition];if(!transition)return;const floorA=$('transitionFloorA').value,floorB=$('transitionFloorB').value,next={...transition,direction:$('transitionDirection').value,endpointA:{...transition.endpointA,floorId:floorA,position:transitionPosition(floorA,transition.endpointA.position),radius:Math.max(8,Number($('transitionRadiusA').value)||8)},endpointB:{...transition.endpointB,floorId:floorB,position:transitionPosition(floorB,transition.endpointB.position),radius:Math.max(8,Number($('transitionRadiusB').value)||8)}};queueCommand('transitionUpdate',{transition:next});};
$('deleteTransition').onclick=()=>{const transition=state?.transitions?.[selectedTransition];if(transition&&confirm('Удалить переход?')){queueCommand('transitionDelete',{transition:{id:transition.id}});selectTransition(null);}};
$('properties').onsubmit=e=>{e.preventDefault();commitTokenProperties();};
$('delete').onclick=()=>{if(selected)queueCommand('delete',{token:{id:selected}});select(null);};
$('characterPreset').onchange=renderCharacterPanel;
$('characterRoster').onchange=renderCharacterPanel;
$('characterPresetMore').onclick=()=>characterCatalog.more('presets').catch(error=>toast(error.message));
$('characterCreatePreset').onclick=()=>{const token=state?.tokens?.[selected],presetId=$('characterPreset').value;if(token&&presetId&&isEditorView())queueCommand('characterCreate',{sceneId:state.scene.id,tokenId:token.id,presetId});};
$('characterCreateEmpty').onclick=()=>{const token=state?.tokens?.[selected];if(!token||!isEditorView())return;const name=prompt('Имя персонажа',token.name||'Новый персонаж');if(name!==null)queueCommand('characterCreate',{sceneId:state.scene.id,tokenId:token.id,characterName:name.trim()});};
$('characterLink').onclick=()=>{const token=state?.tokens?.[selected],characterId=$('characterRoster').value;if(token&&characterId&&isEditorView())queueCommand('characterLink',{sceneId:state.scene.id,tokenId:token.id,characterId});};
$('characterUnlink').onclick=()=>{const token=state?.tokens?.[selected];if(token?.characterInstanceId&&isEditorView())queueCommand('characterUnlink',{sceneId:state.scene.id,tokenId:token.id});};
$('characterPersistent').onchange=()=>{const token=state?.tokens?.[selected];if(token?.characterInstanceId&&isEditorView())queueCommand('characterSetPersistent',{characterId:token.characterInstanceId,persistent:$('characterPersistent').checked});};
$('characterRosterMore').onclick=()=>characterCatalog.more('roster').catch(error=>toast(error.message));
$('characterActionAdd').onchange=()=>{$('characterActionAddButton').disabled=!$('characterActionAdd').value;};
$('characterActionAddButton').onclick=()=>{const snapshot=characterWatch.snapshot,actionId=$('characterActionAdd').value;if(snapshot?.permissions?.manage&&actionId)queueCommand('characterActionAdd',{characterId:snapshot.characterId,actionId});};
$('characterActionMore').onclick=()=>characterCatalog.more('actions').catch(error=>toast(error.message));
function updateNewStatInput(){const type=$('characterStatType').value,boolean=type==='boolean';$('characterStatValue').hidden=boolean;$('characterStatValue').required=type==='integer'||type==='number';$('characterStatBooleanLabel').hidden=!boolean;}
$('characterStatType').onchange=updateNewStatInput;updateNewStatInput();
$('characterStatAdd').onsubmit=e=>{e.preventDefault();const snapshot=characterWatch.snapshot;if(!snapshot?.permissions?.edit)return;const statId=$('characterStatID').value.trim();try{const type=$('characterStatType').value,value=statValueFromInput(type,$('characterStatValue').value,$('characterStatBoolean').checked),isNew=!Object.hasOwn(snapshot.statDefinitions||{},statId);characterDrafts.set(snapshot.characterId,statId,value);if(queueCharacterStat(snapshot.characterId,statId,value,isNew)){$('characterStatID').value='';$('characterStatValue').value='';$('characterStatBoolean').checked=false;renderCharacterPanel();}}catch(error){toast(error.message);}};
$('characterAvatarUpload').onchange=async e=>{const control=e.target,file=control.files[0],snapshot=characterWatch.snapshot;if(!file||!snapshot?.permissions?.edit)return;if(file.size>10*1024*1024){toast('Avatar не должен превышать 10 МиБ');control.value='';return;}const characterID=snapshot.characterId,controller=new AbortController();characterAvatarUploads.get(characterID)?.abort();characterAvatarUploads.set(characterID,controller);uploadControllers.add(controller);control.disabled=true;try{const query=new URLSearchParams({session:credentials.session,kind:'avatar',characterId:characterID});await api(`/api/upload?${query}`,file,true,controller.signal);if(!controller.signal.aborted&&characterWatch.characterId===characterID)toast('Avatar обновлён');}catch(error){if(error.name!=='AbortError')toast(error.message);}finally{uploadControllers.delete(controller);if(characterAvatarUploads.get(characterID)===controller)characterAvatarUploads.delete(characterID);control.disabled=false;control.value='';}};
$('characterAvatarReset').onclick=()=>{const snapshot=characterWatch.snapshot;if(snapshot?.permissions?.edit)queueCommand('characterAvatarReset',{characterId:snapshot.characterId});};
function rememberElementField(id,field){const element=state?.elements?.[selectedElement]||state?.elementCatalog?.[selectedElement];if(!element)return;const control=$(id);let value=control.type==='checkbox'?control.checked:control.type==='number'||control.type==='range'?Number(control.value):control.value;if(field==='layerId'){const layer=state.layers[value];if(!layer)return;elementDrafts.set(element.id,'floorId',layer.floorId);}elementDrafts.set(element.id,field,value);}
function commitElementProperties(id=selectedElement){clearTimeout(elementAutosaveTimer);elementAutosaveTimer=null;const loaded=state?.elements?.[id],element=loaded||state?.elementCatalog?.[id];if(!element||!isEditorView()||!$('elementProperties').checkValidity())return;const draft={...elementDrafts.get(id)},properties={},transform={},unchanged={},unavailable={};for(const [field,raw] of Object.entries(draft)){const transformField=field in element.transform;if(transformField&&!loaded){unavailable[field]=raw;continue;}const value=field==='name'?String(raw).trim():raw,base=transformField?element.transform[field]:element[field],type=transformField?'elementTransform':'elementUpdate',box=transformField?positionOutbox:outbox;if(value!==raw)elementDrafts.set(id,field,value);if(base===value)unchanged[field]=value;else if(!commandAlreadyQueued(box,type,id,field,value))(transformField?transform:properties)[field]=value;}if(Object.keys(unavailable).length)elementDrafts.confirm(id,unavailable);if(Object.keys(unchanged).length)elementDrafts.confirm(id,unchanged);if(Object.keys(properties).length)queueCommand('elementUpdate',{element:{id},elementProperties:properties});if(Object.keys(transform).length)queueTransform('elementTransform',{element:{id,transform:{...element.transform,...transform}}});if(!Object.keys(properties).length&&!Object.keys(transform).length&&id===selectedElement)fillElementProperties();}
function scheduleElementProperties(id=selectedElement){clearTimeout(elementAutosaveTimer);elementAutosaveTimer=setTimeout(()=>commitElementProperties(id),FORM_AUTOSAVE_DELAY);}
for(const [id,field] of [['elementName','name'],['elementX','x'],['elementY','y'],['elementWidth','width'],['elementHeight','height'],['elementRotation','rotation'],['elementOpacity','opacity'],['elementLayer','layerId'],['elementVisible','visible'],['elementLocked','locked']]){const control=$(id);control.addEventListener('input',()=>{rememberElementField(id,field);scheduleElementProperties();dirty=true;});control.addEventListener('change',()=>{rememberElementField(id,field);commitElementProperties();dirty=true;});control.addEventListener('blur',()=>commitElementProperties());}
$('elementProperties').onsubmit=e=>{e.preventDefault();commitElementProperties();};
$('allowElementRotation').onclick=()=>{if(selectedElement){rotationOverrides.add(selectedElement);fillElementProperties();dirty=true;}};
$('deleteElement').onclick=()=>{if(selectedElement&&confirm('Удалить элемент сцены?'))queueCommand('elementDelete',{element:{id:selectedElement}});selectElement(null);};
$('duplicateElement').onclick=()=>{const element=state?.elements?.[selectedElement];if(!element)return;queueCommand('elementCreate',{element:{...element,id:'',name:`${element.name} копия`,transform:{...element.transform,x:element.transform.x+30,y:element.transform.y+30}}});};
function addToken(asset='',x=camera.x+viewport.w/(2*camera.scale),y=camera.y+viewport.h/(2*camera.scale)){if(!queueCommand('create',{token:{name:asset?'Новый персонаж':'Искатель',floorId:currentFloorID,x,y,size:80,rotation:0,color:'#c2d89b',opacity:1,ownerIds:[],hidden:false,asset}}))toast('Дождитесь подключения к серверу');}
$('add').onclick=()=>addToken();
async function upload(file,kind){if(!file)return;if(!state?.scene?.id)throw new Error('Сначала откройте сцену');if(file.size>UPLOAD_LIMIT_BYTES)throw new Error('Размер файла не должен превышать 256 МиБ');const sceneID=state.scene.id,controller=new AbortController(),activeElement=state.elements?.[selectedElement],layer=activeElement?.layerId||orderedLayers(state,currentFloorID).find(item=>item.kind==='visual')?.id||'';uploadControllers.add(controller);toast('Подготовка изображения на сервере…');try{const query=new URLSearchParams({session:credentials.session,scene:sceneID,kind,name:file.name,floor:currentFloorID,layer});const a=await api(`/api/upload?${query}`,file,true,controller.signal);if(controller.signal.aborted||state?.scene?.id!==sceneID)throw new DOMException('Scene changed','AbortError');toast(kind==='scene'?(a.renderMode==='tiled'?'Изображение добавлено. Загружаются видимые тайлы.':'Изображение добавлено.'):'Изображение токена загружено');if(kind==='token')addToken(a.id);return a;}finally{uploadControllers.delete(controller);}}
for(const [input,kind] of [['map','scene'],['token','token']])$(`${input}Upload`).onchange=async e=>{const control=e.target;control.disabled=true;try{await upload(control.files[0],kind);}catch(e){if(e.name!=='AbortError')toast(e.message);}finally{control.disabled=false;control.value='';}};
function resize(){const rect=$('stage').getBoundingClientRect();viewport={w:Math.max(1,rect.width),h:Math.max(1,rect.height)};const dpr=Math.min(devicePixelRatio||1,2);canvas.width=Math.round(viewport.w*dpr);canvas.height=Math.round(viewport.h*dpr);dirty=true;}
new ResizeObserver(resize).observe($('stage'));
function fit(){const bounds=floorCameraBounds(state,currentFloorID),entry=state?.entry;if(bounds){camera.scale=Math.max(.015,Math.min(2,(viewport.w-70)/bounds.width,(viewport.h-70)/bounds.height));camera.x=(bounds.left+bounds.right)/2-viewport.w/2/camera.scale;camera.y=(bounds.top+bounds.bottom)/2-viewport.h/2/camera.scale;}else{const scale=.75,cx=entry?.x??0,cy=entry?.y??0;camera={x:cx-viewport.w/(2*scale),y:cy-viewport.h/(2*scale),scale};}scheduleViewportSave();dirty=true;}
function zoom(factor,x=viewport.w/2,y=viewport.h/2){const wx=camera.x+x/camera.scale,wy=camera.y+y/camera.scale;camera.scale=Math.max(.015,Math.min(4,camera.scale*factor));camera.x=wx-x/camera.scale;camera.y=wy-y/camera.scale;scheduleViewportSave();dirty=true;}
$('fit').onclick=fit;$('plus').onclick=()=>zoom(1.25);$('minus').onclick=()=>zoom(.8);$('grid').onclick=()=>{grid=!grid;$('grid').classList.toggle('active',grid);dirty=true;};
const point=e=>{const r=canvas.getBoundingClientRect();return{x:e.clientX-r.left,y:e.clientY-r.top};};
const alphaCanvas=typeof OffscreenCanvas!=='undefined'?new OffscreenCanvas(1,1):document.createElement('canvas');alphaCanvas.width=1;alphaCanvas.height=1;const alphaCtx=alphaCanvas.getContext('2d',{willReadFrequently:true});
function sampleBitmapAlpha(bitmap,x,y){if(!bitmap||x<0||y<0||x>=bitmap.width||y>=bitmap.height)return 0;try{alphaCtx.clearRect(0,0,1,1);alphaCtx.drawImage(bitmap,Math.max(0,Math.min(bitmap.width-1,x)),Math.max(0,Math.min(bitmap.height-1,y)),1,1,0,0,1,1);return alphaCtx.getImageData(0,0,1,1).data[3];}catch{return null;}}
function elementAlphaAt(element,x,y){const asset=state?.assets?.[element.assetId];if(!asset)return null;const local=inversePoint(element.transform,x,y),u=local.x/element.transform.width*asset.width,v=local.y/element.transform.height*asset.height;if(u<0||v<0||u>=asset.width||v>=asset.height)return 0;if(asset.renderMode==='tiled'){for(let z=0;z<asset.levels;z++){const unit=512*2**z,tx=Math.floor(u/unit),ty=Math.floor(v/unit),entry=memory.get(cacheKey(`${asset.id}/${z}_${tx}_${ty}.png`));if(!entry)continue;const spanX=Math.min(unit,asset.width-tx*unit),spanY=Math.min(unit,asset.height-ty*unit),px=(u-tx*unit)/spanX*entry.bitmap.width,py=(v-ty*unit)/spanY*entry.bitmap.height;return sampleBitmapAlpha(entry.bitmap,px,py);}return null;}const entry=memory.get(cacheKey(`${asset.id}/image.png`));if(!entry)return null;return sampleBitmapAlpha(entry.bitmap,u/asset.width*entry.bitmap.width,v/asset.height*entry.bitmap.height);}
function pickTransitionFloor(x,y){const floors=[...compositeFloors(state,currentFloorID)].reverse();for(const {floor}of floors)for(const element of elementsAtPoint(state,floor.id,x,y,sceneRenderIndex)){const alpha=elementAlphaAt(element,x,y);if(alpha!==null&&alpha>0)return floor.id;}return currentFloorID||orderedFloors(state)[0]?.id||'';}
function transitionDefaultRadius(){return Math.max(24,Math.min(250,50/camera.scale));}
function draftTransitionHit(x,y){if(!transitionDraft)return null;const key=transitionDraft.phase==='editingA'?'endpointA':transitionDraft.phase==='editingB'?'endpointB':'';const endpoint=transitionDraft[key];if(!endpoint||endpoint.floorId!==currentFloorID)return null;const hit=10/camera.scale,radiusPoint={x:endpoint.position.x+endpoint.radius,y:endpoint.position.y};if(Math.hypot(x-radiusPoint.x,y-radiusPoint.y)<=hit)return {key,part:'radius'};if(Math.hypot(x-endpoint.position.x,y-endpoint.position.y)<=hit)return {key,part:'center'};const offset=endpoint.radius+18/camera.scale,size=14/camera.scale,confirmX=endpoint.position.x+offset,cancelX=confirmX+21/camera.scale,controlY=endpoint.position.y-14/camera.scale;if(Math.abs(x-confirmX)<=size/2&&Math.abs(y-controlY)<=size/2)return {key,part:'confirm'};if(Math.abs(x-cancelX)<=size/2&&Math.abs(y-controlY)<=size/2)return {key,part:'cancel'};return null;}
canvas.onwheel=e=>{e.preventDefault();const p=point(e);zoom(Math.exp(-e.deltaY*.001),p.x,p.y);};
canvas.onpointerdown=e=>{if(!state||previewSwitchPending)return;canvas.focus();try{canvas.setPointerCapture(e.pointerId);}catch{}const p=point(e),wx=camera.x+p.x/camera.scale,wy=camera.y+p.y/camera.scale,gm=isEditorView();
 if(gm&&renderBoundsTool&&e.button===0&&!e.altKey){const floor=state.floors?.[currentFloorID];if(!floor)return;if(renderBoundsTool==='rect')drag={type:'renderBoundsRect',floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},current:{x:wx,y:wy},moved:false};else if(renderBoundsTool==='polygon'){const closeRadius=10/camera.scale;if(!renderBoundsDraft)renderBoundsDraft={floorId:currentFloorID,geometryRevision:floor.geometryRevision,points:[{x:wx,y:wy}],cursor:{x:wx,y:wy},invalid:false};else if(renderBoundsDraft.points.length>=3&&Math.hypot(wx-renderBoundsDraft.points[0].x,wy-renderBoundsDraft.points[0].y)<=closeRadius)finishRenderBoundsDraft();else{const last=renderBoundsDraft.points.at(-1);if(Math.hypot(wx-last.x,wy-last.y)>.0005)renderBoundsDraft.points.push({x:wx,y:wy});const polygon=renderBoundsPolygon(renderBoundsDraft.points),n=polygon.outer.length;renderBoundsDraft.invalid=n>=3&&!validRenderBoundsChange(polygon,[n-2,n-1]);}}else if(renderBoundsTool==='edit'&&floor.renderBounds){const hit=renderBoundsHit(floor.renderBounds,wx,wy,camera.scale),original=cloneRenderBounds(floor.renderBounds);if(hit?.part==='vertex'){selectedRenderBoundsVertex=hit.index;drag={type:'renderBoundsEdit',mode:'vertex',vertexIndex:hit.index,floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},original,preview:cloneRenderBounds(original),invalid:false,moved:false};}else if(hit?.part==='edge'){selectedRenderBoundsVertex=hit.index+1;original.outer.splice(selectedRenderBoundsVertex,0,{x:wx,y:wy});drag={type:'renderBoundsEdit',mode:'insert',vertexIndex:selectedRenderBoundsVertex,floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},original,preview:cloneRenderBounds(original),invalid:false,moved:true};}else if(hit?.part==='polygon'){selectedRenderBoundsVertex=-1;drag={type:'renderBoundsEdit',mode:'move',vertexIndex:-1,floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},original,preview:cloneRenderBounds(original),invalid:false,moved:false};}else selectedRenderBoundsVertex=-1;}dirty=true;return;}
 if(gm&&walkableTool&&e.button===0&&!e.altKey){const floor=state.floors?.[currentFloorID];if(!floor)return;if(walkableTool==='add'||walkableTool==='subtract')drag={type:'walkableRect',operation:walkableTool==='add'?'addWalkableRect':'subtractWalkableRect',floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},current:{x:wx,y:wy},moved:false};else{const component=walkableComponentAt(state,currentFloorID,wx,wy);selectedWalkableComponent=component?.id||'';drag=component?{type:'walkableComponent',componentId:component.id,floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},dx:0,dy:0,moved:false}:null;}dirty=true;return;}
 if(gm&&transitionDraft&&transitionDraft.phase!=='awaitingCreate'&&e.button===0){if(transitionDraft.phase==='placingA'||transitionDraft.phase==='placingB'){const key=transitionDraft.phase==='placingA'?'endpointA':'endpointB',floorId=pickTransitionFloor(wx,wy),endpoint={floorId,position:transitionPosition(floorId,{x:wx,y:wy}),radius:transitionDefaultRadius()};transitionDraft={...transitionDraft,[key]:endpoint,phase:key==='endpointA'?'editingA':'editingB'};drag={type:'transitionDraftRadius',key,start:{x:wx,y:wy},moved:false};fillTransitionProperties();dirty=true;return;}const hit=draftTransitionHit(wx,wy);if(hit?.part==='confirm'){confirmTransitionEndpoint();return;}if(hit?.part==='cancel'){cancelTransitionDraft();return;}if(hit){drag={type:hit.part==='radius'?'transitionDraftRadius':'transitionDraftCenter',key:hit.key,startEndpoint:cloneTransitionEndpoint(transitionDraft[hit.key]),start:{x:wx,y:wy},moved:false};return;}dirty=true;return;}const transitionHitResult=gm?transitionHit(state,currentFloorID,wx,wy,camera.scale,selectedTransition):null;if(transitionHitResult&&e.button===0&&!e.altKey){if(transitionHitResult.transition.id!==selectedTransition){selectTransition(transitionHitResult.transition.id);}else if(transitionHitResult.part==='center'||transitionHitResult.part==='radius'){drag={type:transitionHitResult.part==='center'?'transitionCenter':'transitionRadius',id:selectedTransition,key:transitionHitResult.key,startEndpoint:cloneTransitionEndpoint(transitionHitResult.transition[transitionHitResult.key]),start:{x:wx,y:wy},moved:false};}dirty=true;return;}const playerPointVisible=gm||pointInRenderBounds(state,currentFloorID,wx,wy),t=playerPointVisible?tokenIndex.query(wx,wy,wx,wy).reverse().find(t=>(gm?t.floorId===currentFloorID:canMove(t))&&Math.hypot(wx-t.x,wy-t.y)<=t.size/2):null,chosen=state.elements?.[selectedElement],handle=gm?elementHandleAt(chosen,wx,wy,camera.scale,elementRotationAllowed(chosen)):null,element=gm?(handle?chosen:hitElement(state,currentFloorID,wx,wy,sceneRenderIndex)):null;if(t&&e.button===0&&!e.altKey){if(!gm)activatePlayerToken(t,false);else select(t.id);if(canMove(t))drag={type:'token',id:t.id,floorId:t.floorId,dx:wx-t.x,dy:wy-t.y,x:t.x,y:t.y,networkX:t.x,networkY:t.y,pointerX:wx-t.x,pointerY:wy-t.y,pointerDirty:false,moved:false};}else if(element&&e.button===0&&!e.altKey){selectElement(element.id);const layer=state.layers[element.layerId];if(!element.locked&&!layer.locked)drag={type:'element',id:element.id,mode:handle||'move',start:{...element.transform,pointerX:wx,pointerY:wy},pointerX:wx,pointerY:wy,transform:{...element.transform},moved:false};}else{select(null);selectElement(null);selectedTransition='';fillTransitionProperties();drag={type:'pan',px:p.x,py:p.y,x:camera.x,y:camera.y};}dirty=true;};
function processTokenDrag(now=performance.now()){
 if(drag?.type!=='token'||!drag.pointerDirty)return;drag.pointerDirty=false;const target={x:drag.pointerX,y:drag.pointerY},gm=isEditorView(),position=gm?target:constrainTokenMovement(state,drag.floorId,{x:drag.networkX,y:drag.networkY},target);if(!position)return;const {x,y}=position;
 if(x!==drag.x||y!==drag.y){drag.x=x;drag.y=y;drag.moved=true;dirty=true;}
 if(drag.moved&&(x!==drag.networkX||y!==drag.networkY)&&now-lastMove>50&&!positionOutbox?.hasQueuedBehindInflight()&&send({type:'move',token:{id:drag.id,floorId:drag.floorId,x,y}})){drag.networkX=x;drag.networkY=y;lastMove=now;if(x!==target.x||y!==target.y)drag.pointerDirty=true;}
}
 canvas.onpointermove=e=>{const p=point(e),wx=camera.x+p.x/camera.scale,wy=camera.y+p.y/camera.scale;if(transitionDraft?.phase==='placingB'){transitionCursor={x:wx,y:wy};dirty=true;}if(renderBoundsDraft){renderBoundsDraft.cursor={x:wx,y:wy};dirty=true;}if(!drag)return;if(drag.type==='renderBoundsRect'){drag.current={x:wx,y:wy};drag.moved=Math.abs(wx-drag.start.x)>=.001&&Math.abs(wy-drag.start.y)>=.001;}else if(drag.type==='renderBoundsEdit'){const dx=wx-drag.start.x,dy=wy-drag.start.y;drag.preview=cloneRenderBounds(drag.original);if(drag.mode==='move')for(const point of drag.preview.outer){point.x+=dx;point.y+=dy;}else drag.preview.outer[drag.vertexIndex]={x:wx,y:wy};drag.invalid=!validRenderBoundsChange(drag.preview,drag.mode==='move'?[]:[drag.vertexIndex-1,drag.vertexIndex]);drag.moved=drag.mode==='insert'||Math.abs(dx)>=.001||Math.abs(dy)>=.001;}else if(drag.type==='walkableRect'){drag.current={x:wx,y:wy};drag.moved=Math.abs(wx-drag.start.x)>=.001&&Math.abs(wy-drag.start.y)>=.001;}else if(drag.type==='walkableComponent'){drag.dx=wx-drag.start.x;drag.dy=wy-drag.start.y;drag.moved=Math.abs(drag.dx)>=.001||Math.abs(drag.dy)>=.001;}else if(drag.type==='pan'){camera.x=drag.x-(p.x-drag.px)/camera.scale;camera.y=drag.y-(p.y-drag.py)/camera.scale;scheduleViewportSave();}else if(drag.type==='token'){drag.pointerX=wx-drag.dx;drag.pointerY=wy-drag.dy;drag.pointerDirty=true;}else if(drag.type==='element'){const transform=transformedFromDrag(drag.start,drag.mode,wx-drag.pointerX,wy-drag.pointerY,e.shiftKey);drag.transform=transform;drag.moved=true;state.elements[drag.id]={...state.elements[drag.id],transform};if(performance.now()-lastMove>50&&!positionOutbox?.hasQueuedBehindInflight()){send({type:'elementPreview',element:{id:drag.id,transform}});lastMove=performance.now();}}else if(drag.type==='transitionDraftRadius'){const endpoint=transitionDraft?.[drag.key];if(endpoint){const radius=Math.max(8,Math.hypot(wx-endpoint.position.x,wy-endpoint.position.y));if(Math.abs(radius-endpoint.radius)>.01)drag.moved=true;transitionDraft[drag.key]={...endpoint,radius};}}else if(drag.type==='transitionDraftCenter'){const start=drag.startEndpoint,dx=wx-drag.start.x,dy=wy-drag.start.y;transitionDraft[drag.key]={...start,position:transitionPosition(start.floorId,{x:start.position.x+dx,y:start.position.y+dy})};drag.moved=true;}else if(drag.type==='transitionRadius'||drag.type==='transitionCenter'){const transition=state.transitions?.[drag.id],start=drag.startEndpoint;if(transition){const endpoint=drag.type==='transitionRadius'?{...start,radius:Math.max(8,Math.hypot(wx-start.position.x,wy-start.position.y))}:{...start,position:transitionPosition(start.floorId,{x:start.position.x+wx-drag.start.x,y:start.position.y+wy-drag.start.y})};state.transitions[drag.id]={...transition,[drag.key]:endpoint};drag.moved=true;}}dirty=true;};
function endDrag(commit=true){if(commit&&drag?.type==='token')processTokenDrag();const completed=drag;drag=null;if(commit&&completed?.type==='renderBoundsRect'&&completed.moved){const left=Math.min(completed.start.x,completed.current.x),top=Math.min(completed.start.y,completed.current.y),right=Math.max(completed.start.x,completed.current.x),bottom=Math.max(completed.start.y,completed.current.y),polygon=renderBoundsPolygon([{x:left,y:top},{x:right,y:top},{x:right,y:bottom},{x:left,y:bottom}]);if(validRenderBounds(polygon))queueCommand('setRenderBounds',{floorId:completed.floorId,expectedGeometryRevision:completed.geometryRevision,renderBounds:polygon});}else if(commit&&completed?.type==='renderBoundsEdit'&&completed.moved){if(!completed.invalid){if(queueCommand('setRenderBounds',{floorId:completed.floorId,expectedGeometryRevision:completed.geometryRevision,renderBounds:completed.preview}))selectedRenderBoundsVertex=-1;}else toast('Недопустимое изменение границы отменено');}else if(commit&&completed?.type==='walkableRect'&&completed.moved){const x=Math.min(completed.start.x,completed.current.x),y=Math.min(completed.start.y,completed.current.y),width=Math.abs(completed.current.x-completed.start.x),height=Math.abs(completed.current.y-completed.start.y);queueCommand(completed.operation,{floorId:completed.floorId,expectedGeometryRevision:completed.geometryRevision,walkableBounds:{x,y,width,height}});}else if(commit&&completed?.type==='walkableComponent'&&completed.moved)queueCommand('moveWalkableComponent',{floorId:completed.floorId,componentId:completed.componentId,expectedGeometryRevision:completed.geometryRevision,deltaX:completed.dx,deltaY:completed.dy});else if(completed?.type==='token'&&completed.moved)queuePosition({token:{id:completed.id,floorId:completed.floorId,x:completed.x,y:completed.y}});else if(completed?.type==='element'&&completed.moved)queueTransform('elementTransform',{element:{id:completed.id,transform:completed.transform}});else if((completed?.type==='transitionRadius'||completed?.type==='transitionCenter')&&completed.moved){const transition=state?.transitions?.[completed.id];if(transition)queueCommand('transitionUpdate',{transition});}fillElementProperties();fillTransitionProperties();dirty=true;}
canvas.onpointerup=()=>endDrag(true);canvas.onpointercancel=()=>endDrag(false);canvas.onlostpointercapture=()=>endDrag(false);canvas.oncontextmenu=e=>e.preventDefault();
canvas.onkeydown=e=>{if(e.key==='Escape'){if(drag?.type?.startsWith('renderBounds')){drag=null;dirty=true;}else if(renderBoundsDraft){renderBoundsDraft=null;dirty=true;}else if(renderBoundsTool)setRenderBoundsTool('');else if(drag?.type?.startsWith('walkable')){drag=null;dirty=true;}else if(walkableTool)setWalkableTool('');else if(transitionDraft)cancelTransitionDraft();else{select(null);selectElement(null);selectedTransition='';fillTransitionProperties();}}if(e.key==='Enter'&&renderBoundsDraft)finishRenderBoundsDraft();else if(e.key==='Enter'&&transitionDraft&&(transitionDraft.phase==='editingA'||transitionDraft.phase==='editingB'))confirmTransitionEndpoint();if(e.key==='Delete'&&isEditorView()&&!deleteSelectedRenderBoundsVertex()){if(selected)$('delete').click();else if(selectedElement)$('deleteElement').click();else if(selectedTransition)$('deleteTransition').click();}if(e.key==='f')fit();};
$('continuous').onchange=()=>{dirty=true;};$('reconnect').onclick=()=>socket?.close();
$('stress').onclick=()=>{if(!state)return;for(let i=0;i<100;i++)addToken('',camera.x+(i%10)*100,camera.y+Math.floor(i/10)*100);toast('Добавлено 100 тестовых токенов');};
$('clearCache').onclick=async()=>{await cacheQueue;const db=await dbPromise;if(db){await new Promise(resolve=>{const tx=db.transaction('assets','readwrite');tx.objectStore('assets').clear();tx.oncomplete=tx.onerror=tx.onabort=resolve;});}diskBytes=0;diskEnabled=!!db;diskTouches.clear();nativeSizes.clear();for(const entry of memory.values()){entry.bitmap.close();entry.fallback?.bitmap.close();}memory.clear();memoryBytes=0;artwork.clear();dirty=true;toast('Кэш очищен. Сцена сохранена.');};

let decodeEdge=512, imagePlans=new Map(),fallbackWanted=new Set(),lodFallbacks=0,imageDegradations=0;const nativeSizes=new LimitedMap(METADATA_CACHE_LIMIT);
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
function peekImage(path){const key=cacheKey(path),entry=memory.get(key);if(!entry)return null;entry.used=performance.now();memory.delete(key);memory.set(key,entry);fallbackWanted.add(key);lodFallbacks++;return entry.bitmap;}
function releaseImage(key){const e=memory.get(key);if(e){e.bitmap.close();e.fallback?.bitmap.close();memoryBytes-=e.size;memory.delete(key);}}
function degradeImage(key){const e=memory.get(key);if(!e?.fallback)return;const fallback=e.fallback;e.bitmap.close();memoryBytes-=e.size-fallback.size;memory.set(key,{bitmap:fallback.bitmap,bitmapSize:fallback.size,size:fallback.size,used:e.used});imageDegradations++;}
function dropImageFallback(key){const e=memory.get(key);if(!e?.fallback)return;e.fallback.bitmap.close();memoryBytes-=e.fallback.size;e.size=e.bitmapSize;delete e.fallback;}
function evictImages(projectedBytes=memoryBytes){const plan=planLRUEviction(memory,wanted,projectedBytes,assetLimit);for(const key of plan.degradeKeys)degradeImage(key);for(const key of plan.keys)releaseImage(key);return plan.remaining;}
function trimImages(){evictImages();}
const imageBytes=p=>p.width*p.height*4;
function imageNeedsDecode(entry,plan){return !entry||entry.bitmap.width!==plan.width||entry.bitmap.height!==plan.height;}
function scheduleImages(){
 const visible=[],prefetch=[];for(const [key,plan]of imagePlans)(plan.priority===0?visible:prefetch).push({key,plan});
 let fallbackBytes=0;for(const key of fallbackWanted)fallbackBytes+=memory.get(key)?.size||0;
 const visibleBytes=fitPlansToBudget(visible,Math.max(0,assetLimit-fallbackBytes)),admitted=new Map();for(const item of visible)admitted.set(item.key,item.plan);
 let prefetchBudget=Math.min(Math.max(0,assetLimit-fallbackBytes-visibleBytes),Math.floor(assetLimit*.20));
 // Reuse already-decoded neighbours first, then admit cheap new prefetches.
 prefetch.sort((a,b)=>(memory.has(b.key)?1:0)-(memory.has(a.key)?1:0)||imageBytes(a.plan)-imageBytes(b.plan));
 for(const {key,plan}of prefetch){const bytes=imageBytes(plan);if(bytes<=prefetchBudget){admitted.set(key,plan);prefetchBudget-=bytes;}}
 wanted=new Set([...admitted.keys(),...fallbackWanted]);decodeEdge=0;for(const p of admitted.values())decodeEdge=Math.max(decodeEdge,p.width,p.height);
 for(const key of failures.keys())if(!wanted.has(key))failures.delete(key);
 // Size changes no longer abort in-flight work. Camera motion can change a plan
 // every frame; finishing a slightly stale decode is cheaper than restarting it.
 abortUnwantedImageLoads(pending,wanted);
 let forecast=memoryBytes;
 for(const [key,p]of admitted){const e=memory.get(key),c=pending.get(key);if(!imageNeedsDecode(e,p))continue;const target=c?c.width*c.height*4:imageBytes(p);forecast+=Math.max(0,target-(e?.bitmapSize||0));}
 if(forecast>assetLimit)for(const key of admitted.keys()){const fallbackSize=memory.get(key)?.fallback?.size||0;if(!fallbackSize)continue;dropImageFallback(key);forecast-=fallbackSize;if(forecast<=assetLimit)break;}
 evictImages(forecast);
 trimImages();
 const ordered=[...admitted].sort((a,b)=>a[1].priority-b[1].priority);
 for(const [key,p]of ordered){if(activeLoads>=6)break;const entry=memory.get(key);if((p.priority>0&&entry)||!imageNeedsDecode(entry,p)||pending.has(key)||(failures.get(key)||0)>Date.now())continue;loadImage(key,p);}
}
function installImage(key,bitmap){
 const size=bitmap.width*bitmap.height*4,old=memory.get(key);
 evictImages(memoryBytes+Math.max(0,size-(old?.bitmapSize||0)));const baseBytes=memoryBytes-(old?.size||0)+size;if(baseBytes>assetLimit&&(!old||baseBytes>=memoryBytes)){bitmap.close();return false;}
 const candidates=[];if(old?.bitmapSize<size)candidates.push({bitmap:old.bitmap,size:old.bitmapSize});if(old?.fallback?.size<size)candidates.push(old.fallback);candidates.sort((a,b)=>b.size-a.size);
 const fallback=candidates.find(candidate=>memoryBytes-(old?.size||0)+size+candidate.size<=Math.floor(assetLimit*.85))||null;
 if(old){if(old.bitmap!==fallback?.bitmap)old.bitmap.close();if(old.fallback?.bitmap!==fallback?.bitmap)old.fallback?.bitmap.close();memoryBytes-=old.size;}
 const entry={bitmap,bitmapSize:size,size:size+(fallback?.size||0),used:performance.now()};if(fallback)entry.fallback=fallback;memory.set(key,entry);memoryBytes+=entry.size;return true;
}
function loadImage(key,plan){
 const controller=new AbortController();controller.width=plan.width;controller.height=plan.height;pending.set(key,controller);activeLoads++;
 (async()=>{
  let blob=await diskGet(key);if(controller.signal.aborted)return;
  if(blob)cacheHits++;else{const path=key.slice(credentials.session.length+1),scene=state?.scene?.id;if(!scene)throw new DOMException('Scene changed','AbortError');const query=new URLSearchParams({session:credentials.session,scene,activeTokenId:state?.activeTokenId||'',previewKey:state?.playerPreview?state.previewAssetKey||'':''});const res=await fetch(`/api/asset/${path}?${query}`,{headers:{Authorization:`Bearer ${credentials.key}`},signal:controller.signal,cache:diskEnabled?'no-store':'force-cache'});if(!res.ok)throw new Error(`Изображение: HTTP ${res.status}`);blob=await res.blob();tileBytes+=blob.size;diskPut(key,blob);}
  if(controller.signal.aborted)return;
  // Prepared resources are PNG: inspect IHDR before decode, including assets
  // newly revealed to a player whose snapshot did not yet contain metadata.
  const header=new DataView(await blob.slice(0,24).arrayBuffer());
  if(header.byteLength<24||header.getUint32(0)!==0x89504e47)throw new Error('Некорректный PNG');
  const width=header.getUint32(16),height=header.getUint32(20);nativeSizes.set(key,{width,height});
  const sizePlan=fitImageSize(width,height,Math.max(plan.width,plan.height)),decodeStart=performance.now();
  const bitmap=await createImageBitmap(blob,{resizeWidth:sizePlan.width,resizeHeight:sizePlan.height,resizeQuality:'high'});decodeCount++;decodeMs+=performance.now()-decodeStart;
  if(controller.signal.aborted||!wanted.has(key)){bitmap.close();return;}
  if(installImage(key,bitmap))failures.delete(key);
 })().catch(e=>{if(e.name!=='AbortError'){failures.set(key,Date.now()+5000);if(failures.size===1)toast('Не удалось загрузить изображения. Повтор через 5 секунд.');}}).finally(()=>{pending.delete(key);activeLoads--;dirty=true;});
}
function draw(now){const start=performance.now();dirty=false;imagePlans=new Map();fallbackWanted=new Set();const dpr=canvas.width/viewport.w;ctx.setTransform(dpr,0,0,dpr,0,0);ctx.fillStyle='#121c1d';ctx.fillRect(0,0,viewport.w,viewport.h);ctx.save();ctx.scale(camera.scale,camera.scale);ctx.translate(-camera.x,-camera.y);const right=camera.x+viewport.w/camera.scale,bottom=camera.y+viewport.h/camera.scale;ensureSceneRegion({left:camera.x,top:camera.y,right,bottom});let tokenAnimating=false,gm=false;
 if(state){const floor=state.floors?.[currentFloorID],bounds=renderBoundsAABB(floor?.renderBounds),selectedSceneElement=state.elements?.[selectedElement];gm=isEditorView();ctx.save();ctx.fillStyle='#182322';if(bounds){clipFloorRenderBounds(ctx,state,floor,renderBoundsPaths);ctx.fillRect(bounds.left,bounds.top,bounds.width,bounds.height);}else ctx.fillRect(camera.x,camera.y,right-camera.x,bottom-camera.y);ctx.restore();drawSceneStack({ctx,state,currentFloorId:currentFloorID,view:{left:camera.x,top:camera.y,right,bottom},camera,dpr,requestImage,peekImage,selectedElement,editor:gm,renderIndex:sceneRenderIndex,renderBoundsPaths,resourceBounds:renderBoundsResources,showRotationHandle:elementRotationAllowed(selectedSceneElement),drawTokenLayer:(floorId,alpha)=>{const tokenFloor=state.floors?.[floorId],resourceVisible=!gm&&tokenFloor?.renderBounds?(x,y,radius)=>renderBoundsResources.intersectsCircle(state,tokenFloor,x,y,radius):null;const outOfBounds=gm?(x,y)=>!pointInPlayableArea(state,floorId,x,y):null;tokenAnimating=drawTokens({ctx,camera,viewport,dpr,tokens:state.tokens,index:tokenIndex,visuals,moving:movingTokens,drag,selected,queue:[...(outbox?.data.queue||[]),...(positionOutbox?.data.queue||[])],requestImage,artwork,delta:now-lastFrame,floorId,alpha,outOfBounds,resourceVisible})||tokenAnimating;}});}
 if(grid){const step=100;const screenStep=step*camera.scale;if(screenStep>=12){ctx.save();const floor=state?.floors?.[currentFloorID];if(!gm&&floor?.renderBounds)clipFloorRenderBounds(ctx,state,floor,renderBoundsPaths);ctx.strokeStyle='#8ea18a16';ctx.lineWidth=1/camera.scale;ctx.beginPath();for(let x=Math.floor(camera.x/step)*step;x<right;x+=step){ctx.moveTo(x,camera.y);ctx.lineTo(x,bottom);}for(let y=Math.floor(camera.y/step)*step;y<bottom;y+=step){ctx.moveTo(camera.x,y);ctx.lineTo(right,y);}ctx.stroke();ctx.restore();}}
 if(gm){drawWalkableEditor(ctx,state,currentFloorID,camera,{drag,selectedId:selectedWalkableComponent});drawRenderBoundsEditor(ctx,state,currentFloorID,camera,{tool:renderBoundsTool,draft:renderBoundsDraft,drag,selectedVertex:selectedRenderBoundsVertex});drawTransitionOverlay(ctx,state,currentFloorID,camera,{selectedId:selectedTransition,draft:transitionDraft,cursor:transitionCursor});}else drawPlayerWalkableOverlay(ctx,state,currentFloorID,camera);dirty=tokenAnimating||dirty;
 ctx.restore();scheduleImages();$('zoom').textContent=`${Math.round(camera.scale*100)}%`;frameTime=performance.now()-start;rendered++;
}
function loop(now){processTokenDrag(now);if(!$('app').hidden&&(dirty||$('continuous').checked))draw(now);lastFrame=now;requestAnimationFrame(loop);}requestAnimationFrame(loop);
setInterval(()=>{fps=rendered;rendered=0;trimImages();if(failures.size)dirty=true;$('diagnostics').textContent=`${fps} кадров/с · ${frameTime.toFixed(1)} мс/кадр\nRAM изображений ${((memoryBytes+artwork.bytes)/MIB).toFixed(1)} / ${memoryLimit/MIB} МБ${imageMemorySetting==='auto'?' · Auto':''}\nДиск ${(diskBytes/MIB).toFixed(1)} / ${DISK_LIMIT/MIB} МБ${diskEnabled?'':' · недоступен'} · R/W ${diskReads}/${diskWrites} · evict ${diskEvictions} · drop ${diskDrops}\nТайлы ↓ ${(tileBytes/MIB).toFixed(2)} МБ · кэш ${cacheHits} · LOD fallback ${lodFallbacks}\nDecode ${decodeCount} · ${decodeMs.toFixed(0)} мс суммарно · edge ${decodeEdge} · degrade ${imageDegradations}\nWS ↓ ${(bytesIn/1024).toFixed(1)} ↑ ${(bytesOut/1024).toFixed(1)} КБ\nЗагрузка ${activeLoads} · ошибки ${failures.size}`;},1000);
