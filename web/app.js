import {loadLayout,saveLayout,buildSheet,moveEntry,normalizeLayout} from './character-sheet-layout.js';
import {SpatialIndex,ArtworkCache,LimitedMap,MetadataTouchTracker,decodedSize,fitImageSize,fitPlansToBudget,resolveImageMemoryBudgetMiB,planLRUEviction} from './rendering.js';
import {drawTokens,shortCharacterID} from './token-renderer.js';
import {Outbox, Drafts, CacheWriteBudget,blockedMoveCorrection,sameValue} from './reliability.js';
import {ScenesRuntime,needsActiveTokenRequest} from './scenes.js';
import {FloorRenderBoundsPathCache,FloorRenderBoundsResourceCache,SceneRenderIndex,abortUnwantedImageLoads,clipFloorRenderBounds,compositeFloors,drawPlayerWalkableOverlay,drawPlayerTransitionMarkers,drawRenderBoundsEditor,drawSceneStack,drawTransitionOverlay,drawWalkableEditor,elementHandleAt,elementsAtPoint,floorCameraBounds,hitElement,inversePoint,orderedFloors,orderedLayers,pointInPlayableArea,pointInRenderBounds,renderBoundsAABB,renderBoundsHit,transitionHit,transformedFromDrag,validRenderBounds,validRenderBoundsChange,walkableComponentAt} from './scene-content.js';
import {SceneTreeRuntime} from './scene-tree.js';
import {constrainTokenMovement} from './movement-geometry.js';
import {CharacterCatalog,CharacterWatch,statInputValue,statValueFromInput,actionDependencies} from './characters.js';
import {DefinitionsEditor} from './definitions.js';
import {ResourceOverlayEditor} from './resource-overlay-editor.js';
import {PresetEditor} from './preset-editor.js';
import {createDicePanel,DiceAnimator,DiceRuntime} from './dice.js';
import {DiceRendererAdapter} from './dice-renderer.js';
import {LibraryAssetPicker} from './library-explorer.js';
const $ = id => document.getElementById(id);
const isGM=member=>!!(member?.gm||member?.role==='gm');
const isEditorView=()=>!!state&&isGM(state.you)&&!state.playerPreview&&!previewSwitchPending;
const usesPlayerInteraction=()=>!!state&&!isEditorView();
const characterPanel=document.createElement('section');
characterPanel.id='characterPanel';
characterPanel.hidden=true;
characterPanel.innerHTML=`
  <div id="characterPending" class="muted" hidden>Загрузка персонажа…</div>
  <div id="characterUnavailable" class="muted" hidden>Персонаж недоступен.</div>
  <div id="characterSummary" hidden>
    <div class="character-head">
      <div class="character-avatar"><img id="characterAvatar" alt="" hidden><span id="characterAvatarEmpty">◇</span></div>
      <div class="character-head-text"><h2 id="characterName"></h2><p id="characterSubtitle" class="muted"></p></div>
      <div class="character-head-tools">
        <button id="characterLayoutToggle" type="button" class="subtle character-tool" title="Настроить раскладку" aria-label="Настроить раскладку">☷</button>
        <button id="characterMechanicsToggle" type="button" class="subtle character-tool" title="Редактировать персонажа" aria-label="Редактировать персонажа" aria-expanded="false">✎</button>
        <button id="characterSettingsToggle" type="button" class="subtle character-tool" title="Настройки персонажа" aria-label="Настройки персонажа" aria-expanded="false">⚙</button>
      </div>
    </div>
    <div id="characterEditHeader" class="character-edit-header" hidden>
      <div id="characterEditIdentity" class="character-edit-identity"></div>
      <div class="character-edit-footer"><span id="characterEditFeedback" role="status" class="muted"></span><button type="button" id="characterEditCancel" class="subtle">Отмена</button><button type="button" id="characterEditSave" class="primary">Сохранить</button></div>
    </div>
    <div id="characterAddToolbar" class="character-add-toolbar" hidden><button id="characterAddToggle" type="button" class="subtle">＋ Добавить</button><span class="muted">В текущую вкладку</span></div>
    <section id="characterAddPopover" class="character-add-popover" hidden><div id="characterAddActionRow"></div></section>
    <div id="characterLayoutTabs" class="character-tabs character-layout-tabs" role="tablist"></div>
    <div id="characterLayoutContent"></div>
    <div class="character-tabs" id="characterLegacyTabs" hidden role="tablist" aria-label="Разделы персонажа">
      <button id="characterTabStats" type="button" role="tab" aria-controls="characterStatsTab">Статы</button>
      <button id="characterTabActions" type="button" role="tab" aria-controls="characterActionsTab">Действия</button>
    </div>
    <div id="characterStatsTab" role="tabpanel" aria-labelledby="characterTabStats">
      <section class="character-section"><h3>Характеристики</h3><div id="characterStats" class="character-stat-grid"></div></section>
      <section class="character-section"><h3>Характеристики с текущим значением</h3><div id="characterResources" class="character-resource-list"></div></section>
    </div>
    <div id="characterActionsTab" role="tabpanel" aria-labelledby="characterTabActions" hidden>
      <div id="characterActions" class="character-action-grid"></div>
    </div>
    <div id="characterSettingsPanel" class="character-settings" hidden>
      <h3>Настройки персонажа</h3>
      <details id="characterIdentitySection" class="character-settings-section"><summary>Персонаж</summary>
        <label id="characterRenameRow">Имя персонажа<input id="characterRename" maxlength="200"></label>
        <div id="characterAvatarControls"><label class="file-button">Загрузить изображение<input id="characterAvatarUpload" type="file" accept="image/png,image/jpeg,image/webp" hidden></label><button id="characterMediaPick" class="subtle gm" type="button">Выбрать из галереи</button><button id="characterAvatarReset" class="subtle" type="button">Наследовать</button><button id="characterAvatarPlaceholder" class="subtle" type="button">Без изображения</button></div>
      </details>
      <details id="characterMechanicsSection" class="character-settings-section" hidden><summary>Редактирование механики</summary>
        <h4>Параметры персонажа</h4>
        <div class="character-parameter-current"><strong>Характеристики</strong><div id="characterStatEditor"></div><strong>Отслеживаемые характеристики · исходные значения</strong><div id="characterResourceInitialEditor"></div></div>
        <form id="characterParameterAdd" class="character-resource-add character-parameter-add">
          <strong>Добавить характеристику или действие</strong>
          <div class="character-add-stat"><select id="characterParameterKind"><option value="all">Все</option><option value="stat">Характеристики</option><option value="resource">Ресурсы</option></select><input id="characterParameterSearch" placeholder="Поиск параметра…" aria-label="Поиск параметра"></div>
          <label>Из существующих<select id="characterParameterSelect" required></select></label>
          <label id="characterParameterValueLabel">Значение<input id="characterParameterValue" type="number" step="1" value="0"></label>
          <label id="characterParameterBooleanRow" class="check" hidden><input id="characterParameterBoolean" type="checkbox"> Включено</label>
          <button type="submit" class="primary wide">Добавить персонажу</button>
          <details id="characterParameterNew" class="character-parameter-new"><summary>Создать новое… (GM)</summary>
            <p class="muted">Новое определение будет доступно в кампании, но назначится только этому персонажу.</p>
            <label>Название<input id="characterParameterName" maxlength="200" placeholder="Например, Удача"></label>
            <label>Вид<select id="characterParameterNewKind"><option value="stat">Характеристика</option><option value="resource">Ресурс</option></select></label>
            <label>Тип<select id="characterParameterNewType"><option value="integer">Число</option><option value="boolean">Да / нет</option><option value="string">Строка</option></select></label>
            <label>Технический ID (можно изменить)<input id="characterParameterNewID" maxlength="128" placeholder="Создаётся автоматически"></label>
            <label id="characterParameterNewValueLabel">Значение<input id="characterParameterNewValue" type="number" step="1" value="0"></label>
            <label id="characterParameterNewBooleanRow" class="check" hidden><input id="characterParameterNewBoolean" type="checkbox"> Включено</label>
            <button id="characterParameterCreate" type="button" class="subtle wide">Создать и добавить</button>
          </details>
        </form>
        <h4>Действия</h4><div id="characterActionRemoveList"></div>
        <div id="characterActionManage" hidden><select id="characterActionAdd"></select><button id="characterActionAddButton" class="subtle wide" type="button">Добавить действие</button><button id="characterActionMore" class="subtle wide" type="button" hidden>Ещё действия</button></div>
        <button id="editCharacterPreset" class="subtle wide" type="button" hidden>Редактировать шаблон</button>
      </details>
      <details id="characterLinkSection" class="character-settings-section gm" hidden><summary>Связь токена с персонажем</summary>
        <div id="characterGM" class="character-gm"><label>Создать из пресета<select id="characterPreset"></select></label><button id="characterCreatePreset" class="primary wide" type="button">Сменить персонажа: новый из шаблона</button><button id="characterPresetMore" class="subtle wide" type="button" hidden>Ещё пресеты</button><div class="divider"></div><label>Сохранённый персонаж<select id="characterRoster"></select></label><button id="characterLink" class="subtle wide" type="button">Сменить на существующего персонажа</button><button id="characterRosterMore" class="subtle wide" type="button" hidden>Показать ещё</button></div>
        <button id="characterPersistentToggle" class="subtle gm character-persistent-toggle" type="button" hidden></button>
      </details>
      <details id="characterTechnicalSection" class="character-settings-section gm" hidden><summary>Техническая информация</summary><dl class="character-technical"><dt>Character ID</dt><dd id="characterTechnicalID"></dd><dt>Пресет</dt><dd id="characterTechnicalPreset"></dd><dt>Механика</dt><dd id="characterTechnicalMechanics"></dd><dt>Artwork</dt><dd id="characterTechnicalArtwork"></dd></dl></details>
    </div>
  </div>`;
document.querySelector('.inspector .panel-heading').after(characterPanel);
// Settings remain reachable if a linked character becomes unavailable.
$('characterSummary').after($('characterSettingsPanel'));
$('characterSettingsPanel').prepend($('properties'));
$('characterEditIdentity').append($('characterRenameRow'),$('characterAvatarControls'));
const characterAvatarFrame=$('characterAvatar').closest('.character-avatar');
characterAvatarFrame.tabIndex=0;characterAvatarFrame.title='В режиме редактирования нажмите для смены аватара';
characterAvatarFrame.onclick=()=>{if(characterEditSession)$('characterAvatarUpload').click();};
characterAvatarFrame.onkeydown=e=>{if(characterEditSession&&(e.key==='Enter'||e.key===' ')){e.preventDefault();$('characterAvatarUpload').click();}};
$('characterAddPopover').append($('characterParameterAdd'));
$('characterAddActionRow').append($('characterActionManage'));
$('characterParameterKind').innerHTML='<option value="all">Все</option><option value="stat">Характеристики</option><option value="action">Действия</option>';
$('characterParameterNewKind').innerHTML='<option value="stat">Характеристика</option><option value="action">Действие</option>';
const trackedLabel=document.createElement('label');trackedLabel.className='check';trackedLabel.id='characterParameterTrackedLabel';trackedLabel.innerHTML='<input type="checkbox" id="characterParameterTracked"> Отслеживать текущее значение';
$('characterParameterNewKind').closest('label').after(trackedLabel);
const descLabel=document.createElement('label');descLabel.id='characterNewActionDescription';descLabel.hidden=true;descLabel.innerHTML='Описание действия<textarea id="characterActionDescription" maxlength="2000"></textarea>';
$('characterParameterNewType').closest('label').after(descLabel);
const actionSelectLabel=document.createElement('label');actionSelectLabel.id='characterExistingActionLabel';actionSelectLabel.hidden=true;actionSelectLabel.innerHTML='Действие<select id="characterExistingAction"></select>';
$('characterParameterSelect').closest('label').after(actionSelectLabel);

const characterRecoveryToggle=document.createElement('button');characterRecoveryToggle.id='characterRecoveryToggle';characterRecoveryToggle.type='button';characterRecoveryToggle.className='subtle wide';characterRecoveryToggle.textContent='Сменить персонажа';characterRecoveryToggle.hidden=true;$('characterUnavailable').after(characterRecoveryToggle);characterRecoveryToggle.onclick=()=>{characterSettingsOpen=!characterSettingsOpen;$('characterSettingsPanel').hidden=!characterSettingsOpen;};
const tokenCreateControls=document.createElement('div');
tokenCreateControls.id='tokenCreateControls';tokenCreateControls.className='token-create-panel';tokenCreateControls.hidden=true;
tokenCreateControls.innerHTML=`
  <div class="token-create-title">Добавить на сцену</div>
  <button id="addEmptyToken" type="button" class="token-create-choice"><span class="token-create-choice-icon" aria-hidden="true">◇</span><span><strong>Пустой токен</strong><small>Без изображения и шаблона</small></span></button>
  <button type="button" class="token-create-choice" data-token-catalog="presets"><span class="token-create-choice-icon" aria-hidden="true">▦</span><span><strong>Из шаблона</strong><small>Создать нового персонажа из ruleset</small></span></button>
  <button type="button" class="token-create-choice" data-token-catalog="roster"><span class="token-create-choice-icon" aria-hidden="true">♙</span><span><strong>Существующий персонаж</strong><small>Разместить сохранённого в кампании</small></span></button>
  <button id="addImageToken" type="button" class="token-create-choice"><span class="token-create-choice-icon" aria-hidden="true">▧</span><span><strong>Загрузить изображение</strong><small>Создать персонажа с аватаром</small></span></button>
  <section id="tokenCatalogPicker" class="token-catalog-picker" hidden aria-label="Выбор персонажа или шаблона">
    <div class="token-catalog-tabs" role="tablist"><button id="tokenCatalogRosterTab" type="button" role="tab">♟ Персонажи</button><button id="tokenCatalogPresetsTab" type="button" role="tab">▦ Шаблоны</button></div>
    <label class="token-catalog-search"><span aria-hidden="true">⌕</span><input id="tokenCatalogSearch" type="search" placeholder="Поиск…" autocomplete="off" aria-label="Поиск в каталоге"></label>
    <div id="tokenCatalogList" class="token-catalog-list" role="listbox"></div>
    <p id="tokenCatalogEmpty" class="muted" hidden>Ничего не найдено</p>
    <div class="token-catalog-actions"><button id="tokenCatalogMore" type="button" class="subtle">Показать ещё</button><button id="tokenCatalogCancel" type="button" class="subtle">Отмена</button><button id="tokenCatalogPlace" type="button" class="primary">Разместить</button></div>
  </section>`;
$('tokens').before(tokenCreateControls);
const characterIDToggle=document.createElement('button');characterIDToggle.id='characterIDToggle';characterIDToggle.className='subtle gm';characterIDToggle.type='button';characterIDToggle.title='Показывать короткие Character ID';characterIDToggle.textContent='ID';$('viewMode').after(characterIDToggle);
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
        if(cmd.type==='characterEdit'&&characterEditSession?.characterId===cmd.characterId){characterEditSession.saving=false;characterEditSession.conflict=/changed|revision|conflict/i.test(String(error));updateEditButtons();}if(cmd.type.startsWith('character')){if(cmd.statId&&sameValue(characterDrafts.get(cmd.characterId)[cmd.statId],cmd.statValue))pendingNewStats.delete(`${cmd.characterId}/${cmd.statId}`);renderCharacterPanel();}
        if(cmd.type==='transitionCreate'&&transitionDraft?.phase==='awaitingCreate'){transitionDraft={...transitionDraft,phase:'editingB'};delete transitionDraft.priorIds;fillTransitionProperties();}
        if(cmd.type==='transitionUpdate')send({type:'sync'});
      }else if(cmd.type==='properties'){drafts.confirm(cmd.token.id,cmd.properties);if(cmd.sceneId===state?.scene?.id)fillProperties();}
      else if(cmd.type==='elementUpdate'){elementDrafts.confirm(cmd.element.id,cmd.elementProperties);if(cmd.sceneId===state?.scene?.id)fillElementProperties();}
      else if(cmd.type==='elementTransform'){elementDrafts.confirm(cmd.element.id,cmd.element.transform);if(cmd.sceneId===state?.scene?.id)fillElementProperties();}
      else if(cmd.type==='characterEdit'){const d=characterEditSession;if(d?.characterId===cmd.characterId){releaseEditSession();applyEditPlacements(d);renderCharacterPanel();}}
      else if(cmd.type==='characterStatSet'){
        characterDrafts.confirm(cmd.characterId,{[cmd.statId]:cmd.statValue});
        const key=`${cmd.characterId}/${cmd.statId}`;if(pendingNewStats.delete(key))toast(`Создана характеристика ${cmd.statId} в кампании`);
        renderCharacterPanel();
      }
      else if(cmd.type==='characterStatReset'){characterDrafts.confirm(cmd.characterId,{[cmd.statId]:undefined});renderCharacterPanel();}
      else if(cmd.type==='characterParameterAdd')clearCharacterParameterDraft(!!cmd.statDefinition);
      else if(cmd.type==='characterSetPersistent')refreshCharacterCatalog();
      if(cmd.type==='resourceOverlayConfigSet')resourceOverlayEditor.onSaved(error,resourceOverlayState().resourceOverlays);
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
let credentials, state, campaign, desiredSceneID='', socket, reconnectTimer, selected, selectedElement, selectedTransition='', transitionDraft=null, transitionCursor=null, walkableTool='', selectedWalkableComponent='', renderBoundsTool='', renderBoundsDraft=null, selectedRenderBoundsVertex=-1, currentFloorID='', pendingActiveFocus='', confirmedActiveTokenID='', confirmedFloorID='', pendingActiveSwitch=null, drag, inviteCode, outbox, positionOutbox, commandTimer, positionCommandTimer, saveRetry, positionSaveRetry, outboxNeedsResume=true, preferredPlayerPreview=false, previewSwitchPending=null, previewSwitchTimer=null, previewSelection='',showCharacterIDs=false; const drafts=new Drafts();
let resourceDefinitionRegistry=null,actionDefinitionRegistry=null;const characterDrafts=new Drafts(),characterStatTimers=new Map(),pendingNewStats=new Set(),characterAvatarUploads=new Map(),confirmedMechanicalEdits=new Set();let characterWatchReason='',characterAvatarAsset='',characterAvatarURL='',characterAvatarDisplayController=null;
const characterWatch=new CharacterWatch(value=>send(value),onCharacterWatchChange);let characterWatchNeedsResume=true,characterCatalogSession='';
async function loadCharacterCatalogPage(kind,cursor){const query=new URLSearchParams({session:credentials.session,kind,limit:'50'});if(cursor)query.set('cursor',cursor);const response=await fetch(`/api/characters?${query}`,{headers:{Authorization:`Bearer ${credentials.key}`}});if(!response.ok)throw new Error(await response.text());return response.json();}
const characterCatalog=new CharacterCatalog(loadCharacterCatalogPage,()=>{renderTokenCreateOptions();renderCharacterPanel();});
const diceAnimator=new DiceAnimator({createRenderer:()=>new DiceRendererAdapter(),onError:error=>toast(`3D-анимация кубиков недоступна: ${error.message}`),onFinished:event=>dice.finishAnimation(event)});
const dice=new DiceRuntime(createDicePanel(),{queue:queueCommand,getCharacterId:()=>navigationToken(selected)?.characterInstanceId||'',toast,animator:diceAnimator});
function refreshCharacterCatalog(){if(!credentials||!isGM(state?.you||campaign?.you))return Promise.resolve();return characterCatalog.reload().catch(error=>toast(error.message));}
async function loadDefinitions(){const query=new URLSearchParams({session:credentials.session});const response=await fetch(`/api/definitions?${query}`,{headers:{Authorization:`Bearer ${credentials.key}`}});if(!response.ok)throw new Error(await response.text());return response.json();}
async function definitionDocumentRequest(kind,operation,payload,preview=null,key=''){const query=new URLSearchParams({session:credentials.session});if(preview){query.set('expectedRevision',String(preview.registryRevision));query.set('digest',preview.digest);query.set('key',key);}const headers={Authorization:`Bearer ${credentials.key}`};let body=payload;if(kind==='ruleset'){const files=payload?.files|| (Array.isArray(payload)?payload:[payload]);body=JSON.stringify({files:await Promise.all(files.filter(Boolean).map(async file=>({name:file.name,content:Object.hasOwn(file,'content')?file.content:await file.text()}))),...(Object.hasOwn(payload||{},'media')?{media:payload.media}:{})});headers['Content-Type']='application/json';}else headers['Content-Type']='application/toml';const response=await fetch(`/api/definitions/${kind}/${operation}?${query}`,{method:'POST',headers,body});if(!response.ok){const error=new Error(await response.text());error.status=response.status;throw error;}return response.json();}
function definitionOperationKey(kind,digest){const storageKey=`atlas-definition-operation:${credentials.session}:${kind}`;try{const saved=JSON.parse(localStorage.getItem(storageKey)||'null');if(saved?.digest===digest&&saved?.key)return saved.key;const key=`web-${kind}-${crypto.randomUUID()}`,value={digest,key};localStorage.setItem(storageKey,JSON.stringify(value));return key;}catch{return `web-${kind}-${crypto.randomUUID()}`;}}
function completeDefinitionOperation(kind,digest,key){const storageKey=`atlas-definition-operation:${credentials.session}:${kind}`;try{const saved=JSON.parse(localStorage.getItem(storageKey)||'null');if(saved?.digest===digest&&saved?.key===key)localStorage.removeItem(storageKey);}catch{}}
async function exportDefinitionDocument(kind){const query=new URLSearchParams({session:credentials.session}),response=await fetch(`/api/definitions/${kind}/export?${query}`,{headers:{Authorization:`Bearer ${credentials.key}`}});if(!response.ok)throw new Error(await response.text());const blob=await response.blob(),url=URL.createObjectURL(blob),link=document.createElement('a');link.href=url;link.download=kind==='ruleset'?'ruleset.toml':'campaign-extensions.toml';document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),0);}
async function downloadDefinitionsFile(endpoint, filename, params={}){
 const query=new URLSearchParams({session:credentials.session,...params});const response=await fetch(`${endpoint}?${query}`,{headers:{Authorization:`Bearer ${credentials.key}`}});
 if(!response.ok)throw new Error(await response.text());const blob=await response.blob(),url=URL.createObjectURL(blob),link=document.createElement('a');
 link.href=url;link.download=filename;document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),0);
}
async function uploadPresetAvatar(presetID,file){const query=new URLSearchParams({session:credentials.session,kind:'avatar',presetId:presetID});return api(`/api/upload?${query}`,file,true);}
async function loadDefinitionAvatar(assetID,signal){const response=await fetch(avatarPath(assetID),{headers:{Authorization:`Bearer ${credentials.key}`},signal});if(!response.ok)throw new Error(`Avatar: HTTP ${response.status}`);return response.blob();}
async function presetAPI(path,body){
 const query=new URLSearchParams({session:credentials.session}),join=path.includes('?')?'&':'?',response=await fetch(`${path}${join}${query}`,{method:body?'POST':'GET',headers:{Authorization:`Bearer ${credentials.key}`,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});
 if(!response.ok)throw new Error(await response.text());return response.json();
}
async function uploadSharedMedia(file){const query=new URLSearchParams({session:credentials.session,filename:file.name});return api(`/api/definitions/ruleset/media/upload?${query}`,file,true);}
const mediaTransport={upload:uploadSharedMedia,list:()=>presetAPI('/api/definitions/ruleset/media/assets'),path:async(filename,used)=>{const query=new URLSearchParams({filename});for(const path of used)query.append('used',path);return(await presetAPI(`/api/definitions/ruleset/media/path?${query}`)).path;}};
async function requestPortableImport(operation,file,options,preview=null){
 const query=new URLSearchParams({session:credentials.session});const body=new FormData();
 body.set('file',file);body.set('options',JSON.stringify(options));
 if(preview){body.set('digest',preview.digest);body.set('registryRevision',String(preview.registryRevision));body.set('characterRevision',String(preview.characterRevision));body.set('campaignRevision',String(preview.campaignRevision));body.set('libraryRevision',String(preview.libraryRevision));body.set('optionsDigest',preview.optionsDigest);}
 const response=await fetch(`/api/definitions/package/${operation}?${query}`,{method:'POST',headers:{Authorization:`Bearer ${credentials.key}`},body});
 if(!response.ok)throw new Error(await response.text());return response.json();
}
const definitionDocuments={previewImport:(file,options)=>requestPortableImport('preview',file,options),applyImport:(file,options,preview)=>requestPortableImport('apply',file,options,preview),preview:(kind,payload)=>definitionDocumentRequest(kind,'preview',payload),apply:(kind,payload,preview,key)=>definitionDocumentRequest(kind,'apply',payload,preview,key),operationKey:definitionOperationKey,complete:completeDefinitionOperation,export:exportDefinitionDocument,exportPackage:includeMedia=>downloadDefinitionsFile('/api/definitions/package/export','atlas-export.zip',{includeMedia:String(includeMedia)}),downloadFile:name=>downloadDefinitionsFile('/api/definitions/file/download',name.startsWith('campaign/')?name.slice(9):name,{name}),currentFile:async name=>{const query=new URLSearchParams({session:credentials.session,name});const response=await fetch(`/api/definitions/file?${query}`,{headers:{Authorization:`Bearer ${credentials.key}`}});if(!response.ok)throw new Error(await response.text());return response.text();}};
const libraryTransport={
  list:()=>presetAPI('/api/library'),
  inspect:path=>presetAPI('/api/library/inspect?path='+encodeURIComponent(path)),
  operate:async command=>{const query=new URLSearchParams({session:credentials.session});const response=await fetch(`/api/library/operation?${query}`,{method:'POST',headers:{Authorization:`Bearer ${credentials.key}`,'Content-Type':'application/json'},body:JSON.stringify(command)});if(!response.ok){const error=new Error(await response.text());error.status=response.status;throw error;}return response.json();},
  download:path=>downloadDefinitionsFile('/api/library/file/download',path.split('/').at(-1),{path}),
  thumbnail:(id,signal)=>loadRulesetMediaThumbnail(id,signal),
  exportPackage:definitionDocuments.exportPackage,
  previewImport:definitionDocuments.previewImport,
  inventoryImport:file=>requestPortableImport('inventory',file,{mode:'selected'}),
  applyImport:definitionDocuments.applyImport,
  openCharacter:id=>{const tokenID=Object.keys(state?.tokenCatalog||state?.tokens||{}).find(key=>navigationToken(key)?.characterInstanceId===id);if(tokenID){definitionsEditor.close(true);select(tokenID);}else toast('Для открытия чарлиста персонаж должен находиться на текущей сцене');},
};
const characterAssetPicker=new LibraryAssetPicker({list:libraryTransport.list,thumbnail:libraryTransport.thumbnail});
const definitionsEditor=new DefinitionsEditor($('definitionsMount'),{load:loadDefinitions,queue:queueCommand,getCharacter:()=>characterWatch.snapshot,toast,documents:definitionDocuments,uploadPresetAvatar,loadAvatar:loadDefinitionAvatar,mediaTransport,libraryTransport});
async function loadRulesetMediaThumbnail(assetID,signal){const query=new URLSearchParams({session:credentials.session}),response=await fetch(`/api/definitions/ruleset/media/asset/${encodeURIComponent(assetID)}?${query}`,{headers:{Authorization:`Bearer ${credentials.key}`},signal});if(!response.ok)throw new Error('Изображение недоступно');return response.blob();}
const presetEditor=new PresetEditor({request:presetAPI,upload:uploadSharedMedia,thumbnail:loadRulesetMediaThumbnail,toast,onSaved:()=>{definitionsEditor.registryChanged();refreshCharacterCatalog();}});
$('characterLinkSection').append($('editCharacterPreset'));
$('editCharacterPreset').onclick=()=>{const snapshot=characterWatch.snapshot;if(isGM(state?.you||campaign?.you)&&snapshot?.instance?.presetId)presetEditor.open(snapshot.instance.presetId);};
// Reuse the existing ruleset editor dialog as the campaign settings window.
const settingsPanel=definitionsEditor.backdrop.querySelector('.definitions-panel');
const settingsHeader=settingsPanel.querySelector('header');
settingsHeader.querySelector('small').textContent='КАМПАНИЯ';
settingsHeader.querySelector('h2').textContent='Настройки кампании';
const settingsTabs=document.createElement('nav');settingsTabs.className='campaign-settings-tabs';settingsTabs.setAttribute('aria-label','Раздел настроек кампании');settingsTabs.setAttribute('role','tablist');
const resourceTab=document.createElement('button'),rulesetTab=document.createElement('button');
for(const [tab,label] of [[rulesetTab,'Рулсеты и файлы'],[resourceTab,'Индикаторы характеристик']]){tab.type='button';tab.textContent=label;tab.setAttribute('role','tab');settingsTabs.append(tab);}
const rulesetView=document.createElement('section');rulesetView.className='campaign-settings-ruleset';
for(const node of [...settingsPanel.children].filter(node=>node!==settingsHeader))rulesetView.append(node);
const resourceView=document.createElement('section');resourceView.className='campaign-settings-resources';
settingsPanel.append(settingsTabs,resourceView,rulesetView);
const resourceOverlayEditor=new ResourceOverlayEditor(resourceView,{save:resourceOverlays=>queueCommand('resourceOverlayConfigSet',{resourceOverlays}),toast});
definitionsEditor.onBeforeClose=()=>resourceOverlayEditor.saving?false:!resourceOverlayEditor.isDirty()||confirm('Несохранённые настройки индикаторов будут потеряны. Закрыть?');
function showCampaignSettingsTab(tab){
 const resources=tab==='resources';resourceView.hidden=!resources;rulesetView.hidden=resources;
 resourceTab.classList.toggle('active',resources);rulesetTab.classList.toggle('active',!resources);
 resourceTab.setAttribute('aria-selected',String(resources));rulesetTab.setAttribute('aria-selected',String(!resources));
}
resourceTab.onclick=()=>showCampaignSettingsTab('resources');rulesetTab.onclick=()=>showCampaignSettingsTab('rulesets');
$('definitionsOpen').onclick=async()=>{
 if(!isGM(state?.you||campaign?.you)||state?.playerPreview)return;
 const source=resourceOverlayState();
 resourceOverlayEditor.open(source.resourceOverlays||[],source.resourceOverlayDefinitions||{});
 showCampaignSettingsTab('rulesets');await definitionsEditor.open();
 if(definitionsEditor.backdrop.hidden)return;
 const stats=definitionsEditor.registry?.effective?.stats;
 if(stats)resourceOverlayEditor.setDefinitions(stats);
};
const elementDrafts=new Drafts(),FORM_AUTOSAVE_DELAY=650;let tokenAutosaveTimer,elementAutosaveTimer;
function commandAlreadyQueued(box,type,id,field,value){return !!box?.data.queue.some(command=>command.type===type&&(command.token?.id===id||command.element?.id===id)&&Object.hasOwn(command.properties||command.elementProperties||command.element?.transform||{},field)&&sameValue((command.properties||command.elementProperties||command.element.transform)[field],value));}
function tokenFieldValue(id){return id==='tokenHidden'?$(id).checked:Number($(id).value);}
function rememberTokenField(id,field){if(!selected)return;drafts.set(selected,field,tokenFieldValue(id));}
function commitTokenProperties(id=selected){clearTimeout(tokenAutosaveTimer);tokenAutosaveTimer=null;const source=navigationToken(id);if(!source||!isEditorView()||!$('properties').checkValidity())return;const draft={...drafts.get(id)},properties={},unchanged={};for(const [field,value] of Object.entries(draft)){if(sameValue(source[field],value))unchanged[field]=value;else if(!commandAlreadyQueued(outbox,'properties',id,field,value))properties[field]=value;}if(Object.keys(unchanged).length)drafts.confirm(id,unchanged);if(Object.keys(properties).length)queueCommand('properties',{token:{id},properties});else if(id===selected)fillProperties();}
function scheduleTokenProperties(id=selected){clearTimeout(tokenAutosaveTimer);tokenAutosaveTimer=setTimeout(()=>commitTokenProperties(id),FORM_AUTOSAVE_DELAY);}
for(const [id,field] of [['tokenSize','size'],['tokenOpacity','opacity'],['tokenHidden','hidden']]){
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

// Keep the inspector frame outside its scrollable contents so the drag handle never scrolls away.
const inspector=document.querySelector('.inspector'),inspectorFrame=document.createElement('div'),inspectorResize=document.createElement('div');
inspectorFrame.className='inspector-frame';inspectorResize.className='inspector-resize';
inspectorResize.tabIndex=0;inspectorResize.setAttribute('role','separator');inspectorResize.setAttribute('aria-orientation','vertical');inspectorResize.setAttribute('aria-label','Изменить ширину правой панели');
inspector.before(inspectorFrame);inspectorFrame.append(inspector,inspectorResize);
const INSPECTOR_WIDTH_KEY='atlas-inspector-width-v1';let inspectorWidth=360,inspectorDrag=null;
function inspectorLimits(){const sidebar=sidebarFrame.classList.contains('collapsed')?0:sidebarFrame.getBoundingClientRect().width;return {min:280,max:Math.max(280,Math.min(600,window.innerWidth-sidebar-260))};}
function setInspectorWidth(value,persist=true){const {min,max}=inspectorLimits();inspectorWidth=Math.min(max,Math.max(min,Number(value)||360));inspectorFrame.style.setProperty('--inspector-width',`${inspectorWidth}px`);inspectorResize.setAttribute('aria-valuenow',String(Math.round(inspectorWidth)));inspectorResize.setAttribute('aria-valuemin',String(min));inspectorResize.setAttribute('aria-valuemax',String(max));if(persist)storeSidebarSetting(INSPECTOR_WIDTH_KEY,String(Math.round(inspectorWidth)));}
try{const stored=Number(localStorage.getItem(INSPECTOR_WIDTH_KEY));if(Number.isFinite(stored)&&stored>0)inspectorWidth=stored;}catch{}
setInspectorWidth(inspectorWidth,false);
inspectorResize.onpointerdown=e=>{if(e.button!==0||window.innerWidth<=800)return;inspectorDrag={pointerId:e.pointerId,startX:e.clientX,startWidth:inspectorFrame.getBoundingClientRect().width};inspectorResize.setPointerCapture(e.pointerId);document.body.classList.add('inspector-resizing');e.preventDefault();};
inspectorResize.onpointermove=e=>{if(!inspectorDrag||inspectorDrag.pointerId!==e.pointerId)return;setInspectorWidth(inspectorDrag.startWidth+inspectorDrag.startX-e.clientX,false);};
function finishInspectorDrag(e){if(!inspectorDrag||inspectorDrag.pointerId!==e.pointerId)return;inspectorDrag=null;document.body.classList.remove('inspector-resizing');setInspectorWidth(inspectorWidth);}
inspectorResize.onpointerup=finishInspectorDrag;inspectorResize.onpointercancel=e=>{if(!inspectorDrag||inspectorDrag.pointerId!==e.pointerId)return;const previous=inspectorDrag.startWidth;inspectorDrag=null;document.body.classList.remove('inspector-resizing');setInspectorWidth(previous,false);};
inspectorResize.onkeydown=e=>{if(e.key==='ArrowLeft'||e.key==='ArrowRight'){setInspectorWidth(inspectorWidth+(e.key==='ArrowLeft'?16:-16));e.preventDefault();}};
window.addEventListener('resize',()=>setInspectorWidth(inspectorWidth,false));

let bytesIn = 0, bytesOut = 0, tileBytes = 0, stopped = false, toastTimer, retry = 0;
let wanted = new Set(), activeLoads = 0, cacheHits = 0;
const visuals = new Map(), memory = new Map(), failures = new Map(), pending = new Map();
const MIB=1024*1024,DISK_LIMIT=512*MIB,DISK_LOW_WATER=Math.floor(DISK_LIMIT*9/10),IMAGE_MEMORY_SETTING_KEY='atlas-image-memory-budget-v1',IMAGE_MEMORY_SETTINGS=new Set(['auto','64','128','256','512']);
const DISK_WRITE_QUEUE_LIMIT=32*1024*1024,DISK_WRITE_QUEUE_COUNT=64;
const ARTWORK_LIMIT=8*MIB;const tokenIndex=new SpatialIndex(),movingTokens=new Set(),artwork=new ArtworkCache(ARTWORK_LIMIT);
function readImageMemorySetting(){try{const value=localStorage.getItem(IMAGE_MEMORY_SETTING_KEY)||'auto';return IMAGE_MEMORY_SETTINGS.has(value)?value:'auto';}catch{return 'auto';}}
let imageMemorySetting=readImageMemorySetting(),memoryLimit=resolveImageMemoryBudgetMiB(navigator.deviceMemory,imageMemorySetting)*MIB,assetLimit=memoryLimit-ARTWORK_LIMIT;
const METADATA_CACHE_LIMIT=4096,tokenRows=new Map(),tokenRowAvatars=new Map(),diskTouches=new MetadataTouchTracker(METADATA_CACHE_LIMIT);
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
function enter(c){resourceDefinitionRegistry=null;credentials=c;try{showCharacterIDs=localStorage.getItem(characterIDStorageKey())==='1';}catch{showCharacterIDs=false;}stopped=false;initOutbox();if(stopped)return;inviteCode=c.invite;$('lobby').hidden=true;$('app').hidden=false;resize();connect();}
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
 if(state&&!isEditorView())closeTokenCreatePanel();
 characterIDToggle.classList.toggle('active',characterIDsEnabled());characterIDToggle.setAttribute('aria-pressed',String(characterIDsEnabled()));
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
characterIDToggle.onclick=()=>{if(!isGM(state?.you||campaign?.you))return;showCharacterIDs=!showCharacterIDs;try{localStorage.setItem(characterIDStorageKey(),showCharacterIDs?'1':'0');}catch{}renderInteractionMode();renderCharacterPanel();dirty=true;};
function viewportStorageKey(sceneID){return `atlas-viewport-v1/${credentials.session}/${credentials.key}/${sceneID}`;}
function activeTokenStorageKey(sceneID){return `atlas-active-token-v1/${credentials.session}/${credentials.key}/${sceneID}`;}
function characterIDStorageKey(){return `atlas-character-ids-v1/${credentials.session}/${credentials.key}`;}
function characterIDsEnabled(){return showCharacterIDs&&isGM(state?.you||campaign?.you);}
function characterDisplayTitle(name,characterID){return characterIDsEnabled()&&characterID?`[${shortCharacterID(characterID)}] ${name}`:name;}
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
function openScene(sceneID,preserveCharacterWatch=false){if(!sceneID||state?.scene?.id===sceneID)return;if(characterEditSession&&!confirmLeaveEdit())return;desiredSceneID=sceneID;clearSceneState(preserveCharacterWatch);$('connection').textContent='Открываем сцену…';send({type:'subscribe',sceneId:sceneID,activeTokenId:readSavedActiveToken(sceneID),enabled:isGM(campaign?.you)?preferredPlayerPreview:undefined});}
function goCampaignHome(){if(characterEditSession&&!confirmLeaveEdit())return;desiredSceneID='';clearSceneState();if(campaign)scenes.showHome();send({type:'subscribe',sceneId:''});}
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
  if(msg.type==='registryChanged'){resourceDefinitionRegistry=null;actionDefinitionRegistry=null;if(isGM(state?.you||campaign?.you)){if(characterMechanicsOpen)refreshResourceDefinitions();refreshCharacterCatalog();definitionsEditor.registryChanged().then(()=>{if(!definitionsEditor.backdrop.hidden)resourceOverlayEditor.setDefinitions(definitionsEditor.registry?.effective?.stats||{});});}return;}
  if(msg.type==='resourceOverlayConfigChanged'){if(campaign){campaign.resourceOverlays=msg.resourceOverlays||[];campaign.resourceOverlayDefinitions=msg.resourceOverlayDefinitions||{};}if(state){state.resourceOverlays=msg.resourceOverlays||[];state.resourceOverlayDefinitions=msg.resourceOverlayDefinitions||{};state.characterDisplays=msg.characterDisplays||{};dirty=true;}resourceOverlayEditor.onRemote(msg.resourceOverlays||[],msg.resourceOverlayDefinitions||{});return;}
  if(msg.type==='characterDisplayChanged'){if(state&&msg.sceneId===state.scene.id){state.characterDisplays=state.characterDisplays||{};state.characterDisplays[msg.characterId]=msg.display;dirty=true;}return;}
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
  if(msg.type==='tokenCatalogUpsert'||msg.type==='tokenCatalogMove'||msg.type==='tokenCatalogDelete'){
   if(!state||msg.sceneId!==state.scene.id||!isEditorView())return;
   state.tokenCatalog=state.tokenCatalog||{};
   if(msg.type==='tokenCatalogDelete'){
    delete state.tokenCatalog[msg.id];removeTokenRow(msg.id);
    if(selected===msg.id)select(null);
    }else if(msg.type==='tokenCatalogMove'){
    const prior=state.tokenCatalog[msg.id];if(prior){const moved={...prior,x:msg.x,y:msg.y};state.tokenCatalog[msg.id]=moved;updateTokenRow(moved);}else send({type:'sync'});
    }else if(msg.locator){const prior=state.tokenCatalog[msg.id];state.tokenCatalog[msg.id]=msg.locator;renderTokenList();if(selected===msg.id){fillProperties();if(prior?.characterInstanceId!==msg.locator.characterInstanceId){syncCharacterWatch();renderCharacterPanel();}}}
   if(Number.isSafeInteger(msg.revision))state.revision=msg.revision;
   dirty=true;return;
  }
  if(msg.type==='tokenLocatorUpsert'||msg.type==='tokenLocatorMove'||msg.type==='tokenLocatorDelete'){
   if(!state||msg.sceneId!==state.scene.id||isEditorView())return;
   const prior=state.ownedTokens?.[msg.id];
   if(msg.type==='tokenLocatorDelete'){
    delete state.ownedTokens[msg.id];delete state.tokenListAvatars?.[msg.id];removeTokenRow(msg.id);
    if(drag?.type==='token'&&drag.id===msg.id)drag=null;
    if(selected===msg.id)select(null);
   }else if(msg.type==='tokenLocatorMove'){
    if(!prior){send({type:'sync'});return;}
    const locator={...prior,x:msg.x,y:msg.y};state.ownedTokens[msg.id]=locator;updateTokenRow(locator);
   }else if(msg.locator){
    state.tokenListAvatars=state.tokenListAvatars||{};if(msg.avatarAssetId)state.tokenListAvatars[msg.id]=msg.avatarAssetId;else delete state.tokenListAvatars[msg.id];
    state.ownedTokens[msg.id]=msg.locator;updateTokenRow(msg.locator);
   }
   if(Number.isSafeInteger(msg.revision))state.revision=msg.revision;
   $('revision').textContent=`Ревизия ${state.revision}`;dirty=true;return;
  }
  if(msg.type==='snapshot'){
   if(!desiredSceneID||msg.scene.id!==desiredSceneID)return;
	  const draggedFloor=drag?.type==='token'?state?.tokens?.[drag.id]?.floorId:'',priorCatalog=state?.elementCatalog,priorTokenCatalog=state?.tokenCatalog,priorCatalogVersion=state?._catalogVersion||0,wasPreview=!!state?.playerPreview;const initialRegion=msg.region==null;state=msg;if(isEditorView()&&!state.tokenCatalog)state.tokenCatalog=priorTokenCatalog||{};if(isGM(state.you)&&!state.playerPreview){if(state.elementCatalog)state._catalogVersion=priorCatalogVersion+1;else{state.elementCatalog=priorCatalog||{};state._catalogVersion=priorCatalogVersion;}}else{state.elementCatalog={};state._catalogVersion=0;}if(!initialRegion)regionInFlight=false;desiredSceneID=msg.scene.id;scenes.update(campaign);scenes.showScene();retry=0;
   preferredPlayerPreview=!!state.playerPreview;if(previewSwitchPending&&previewSwitchPending.enabled===preferredPlayerPreview)previewSwitchPending=null;clearTimeout(previewSwitchTimer);previewSwitchTimer=null;$('connection').textContent='● В сети';$('connection').className='status connected';$('sessionTitle').textContent=msg.name;dice.setScene(msg.scene.id,msg.you.id);renderInteractionMode();
   if(!sceneCameraInitialized){initializeSceneCamera(msg);sceneCameraInitialized=true;}
	  state.ownedTokens=state.ownedTokens||{};state.tokenListAvatars=state.tokenListAvatars||{};currentFloorID=msg.currentFloorId||orderedFloors(state)[0]?.id||'';confirmedActiveTokenID=state.activeTokenId||'';confirmedFloorID=currentFloorID;pendingActiveSwitch=null;sceneRenderIndex.reset();renderBoundsPaths.syncState(state);renderBoundsResources.syncState(state);if(usesPlayerInteraction())rememberActiveToken(state.scene.id,state.activeTokenId||'');resolveCreatedTransition();if(selectedTransition&&!state.transitions?.[selectedTransition])selectedTransition='';if(selectedWalkableComponent&&!state.floors?.[currentFloorID]?.walkableComponents?.some(component=>component.id===selectedWalkableComponent))selectedWalkableComponent='';if(drag?.type==='token'&&draggedFloor&&state.tokens?.[drag.id]?.floorId!==draggedFloor)drag=null;if(drag?.type?.startsWith('walkable')&&drag.geometryRevision!==state.floors?.[drag.floorId]?.geometryRevision)drag=null;if(drag?.type?.startsWith('renderBounds')&&drag.geometryRevision!==state.floors?.[drag.floorId]?.geometryRevision){drag=null;selectedRenderBoundsVertex=-1;}if(renderBoundsDraft&&renderBoundsDraft.geometryRevision!==state.floors?.[renderBoundsDraft.floorId]?.geometryRevision)renderBoundsDraft=null;
   if(pendingActiveFocus&&pendingActiveFocus===state.activeTokenId){const token=navigationToken(pendingActiveFocus);if(token){camera.x=token.x-viewport.w/(2*camera.scale);camera.y=token.y-viewport.h/(2*camera.scale);scheduleViewportSave();}pendingActiveFocus='';}
   for(const id of visuals.keys())if(!state.tokens[id])visuals.delete(id);
	  rebuildTokenIndex();if(isEditorView()&&previewSelection&&state.tokens[previewSelection]){select(previewSelection);previewSelection='';}if(selected&&!navigationToken(selected))select(null);else if(selected)syncCharacterWatch();if(selectedElement&&!state.elementCatalog?.[selectedElement])selectElement(null);renderPanels();if(!wasPreview&&state.playerPreview&&previewSelection&&state.ownedTokens[previewSelection]&&state.activeTokenId!==previewSelection)activatePlayerToken(state.ownedTokens[previewSelection],false);dirty=true;ensureSceneRegion(currentViewRegion());if(!outbox.inflight)outbox.flush();if(!positionOutbox.inflight)positionOutbox.flush();return;
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
   const old=state.tokens[msg.id];let next=msg.token;if(msg.asset?.id)state.assets[msg.asset.id]=msg.asset;if(msg.characterDisplay?.id){state.characterDisplays=state.characterDisplays||{};state.characterDisplays[msg.characterDisplay.id]=msg.characterDisplay;}
  if(msg.type==='move'){
   if(!old){send({type:'sync'});return;}
   next={...old,x:msg.x,y:msg.y};
  }
   const propertiesChanged=!old||!next||old.name!==next.name||old.size!==next.size||old.rotation!==next.rotation||old.floorId!==next.floorId||old.color!==next.color||old.opacity!==next.opacity||!sameValue(old.ownerIds,next.ownerIds)||old.hidden!==next.hidden||old.asset!==next.asset,characterLinkChanged=old?.characterInstanceId!==next?.characterInstanceId;
  state.revision=msg.revision;
  state.delivery=msg.delivery;
	  if(drag?.type==='token'&&msg.id===drag.id&&msg.type==='upsert'&&old&&(next?.floorId!==old.floorId||next?.x!==drag.x||next?.y!==drag.y))drag=null;
	  if(msg.type==='delete'){
   delete state.tokens[msg.id];visuals.delete(msg.id);tokenIndex.remove(msg.id);movingTokens.delete(msg.id);const listed=state.ownedTokens?.[msg.id]||state.tokenCatalog?.[msg.id];if(listed)updateTokenRow(listed);else removeTokenRow(msg.id);
	if(old?.characterInstanceId&&!Object.values(state.tokens).some(token=>token.characterInstanceId===old.characterInstanceId))delete state.characterDisplays?.[old.characterInstanceId];
   }else if(next){
    state.tokens[msg.id]=next;tokenIndex.set(next);if(!old||old.x!==next.x||old.y!==next.y)movingTokens.add(msg.id);if(usesPlayerInteraction()){const listed=state.ownedTokens[msg.id];if(listed)updateTokenRow(listed);else removeTokenRow(msg.id);}else{state.tokenCatalog=state.tokenCatalog||{};state.tokenCatalog[msg.id]=next;if(propertiesChanged)renderTokenList();else updateTokenRow(next);}
  }
  $('revision').textContent=`Ревизия ${state.revision}`;
  if(propertiesChanged&&selected===msg.id)fillProperties();
  if(characterLinkChanged&&selected===msg.id){syncCharacterWatch();renderCharacterPanel();}
  if(old?.asset&&old.asset!==next?.asset)pruneAssetMetadata(old.asset);
  if(selected&&!navigationToken(selected))select(null);
  dirty=true;
 };
 socket.onclose=()=>{clearTimeout(commandTimer);clearTimeout(positionCommandTimer);abortUploads();characterWatch.disconnect();dice.disconnect();if(stopped)return;outbox.inflight=false;positionOutbox.inflight=false;if(drag?.type==='token'||drag?.type==='element')endDrag();drag=null;dirty=true;$('connection').textContent='● Нет связи · повтор…';$('connection').className='status';reconnectTimer=setTimeout(connect,Math.min(1000*2**retry++,10000));};
 socket.onerror=()=>socket.close();
}
$('leaveCampaign').onclick=()=>{saveViewportNow();stopped=true;socket?.close();location.href='/';};
$('invite').onclick=async()=>{const url=`${location.origin}/#session=${credentials.session}&invite=${inviteCode}`;if(!inviteCode){toast('Приглашение доступно в исходной вкладке ведущего.');return;}try{await navigator.clipboard.writeText(url);toast('Ссылка для игроков скопирована');}catch{prompt('Скопируйте ссылку для игроков',url);}};
function canMove(t){return !!state&&(isEditorView()||(isGM(state.you)&&state.playerPreview?true:t.ownerIds?.includes(state.you.id)&&!t.hidden));}
function navigationToken(id){const token=state?.tokens?.[id]||state?.tokenCatalog?.[id]||state?.ownedTokens?.[id];return token&&id===state?.activeTokenId&&state?.activeCharacterId&&!token.characterInstanceId?{...token,characterInstanceId:state.activeCharacterId}:token;}
function activatePlayerToken(token,focus){
 if(!token||!usesPlayerInteraction()||(!isGM(state.you)&&(token.hidden||!token.ownerIds?.includes(state.you.id))))return false;
 if(!needsActiveTokenRequest(token,focus,confirmedActiveTokenID,confirmedFloorID,pendingActiveSwitch)){select(token.id);dirty=true;return true;}
 const targetView=focus?{left:token.x-viewport.w/(2*camera.scale),top:token.y-viewport.h/(2*camera.scale),right:token.x+viewport.w/(2*camera.scale),bottom:token.y+viewport.h/(2*camera.scale)}:currentViewRegion();const region=boundedRegion(targetView),previous={activeTokenId:confirmedActiveTokenID,floorId:confirmedFloorID||currentFloorID,camera:{...camera}};
 if(!send({type:'activeToken',sceneId:state.scene.id,activeTokenId:token.id,focus,region,previewRevision:state.previewRevision||0}))return false;
 regionInFlight=true;pendingActiveSwitch=previous;
 if(focus){pendingActiveFocus=token.id;camera.x=token.x-viewport.w/(2*camera.scale);camera.y=token.y-viewport.h/(2*camera.scale);scheduleViewportSave();}
 state.activeTokenId=token.id;state.currentFloorId=token.floorId;currentFloorID=token.floorId;rememberActiveToken(state.scene.id,token.id);select(token.id);
 renderPanels();dirty=true;return true;
}
function abortUploads(){for(const controller of uploadControllers)controller.abort();uploadControllers.clear();}
function clearSceneState(preserveCharacterWatch=false){closeTokenContextMenu();closeTokenCreatePanel();saveViewportNow();clearTimeout(viewportSaveTimer);clearTimeout(tokenAutosaveTimer);clearTimeout(elementAutosaveTimer);clearTimeout(previewSwitchTimer);viewportSaveTimer=null;tokenAutosaveTimer=null;elementAutosaveTimer=null;previewSwitchTimer=null;previewSwitchPending=null;viewportSaveDirty=false;abortUploads();dice.setScene('');if(preserveCharacterWatch){const previous=selected;selected=null;if(previous&&tokenRows.has(previous))tokenRows.get(previous).classList.remove('selected');fillProperties();}else{select(null);discardCharacterPanelDisplay();}selectElement(null);selectedTransition='';transitionDraft=null;transitionCursor=null;walkableTool='';selectedWalkableComponent='';renderBoundsTool='';renderBoundsDraft=null;selectedRenderBoundsVertex=-1;drag=null;state=null;currentFloorID='';pendingActiveFocus='';confirmedActiveTokenID='';confirmedFloorID='';pendingActiveSwitch=null;regionInFlight=false;sceneCameraInitialized=false;rotationOverrides.clear();tokenIndex.clear();sceneRenderIndex.clear();renderBoundsPaths.clear();renderBoundsResources.clear();visuals.clear();movingTokens.clear();for(const id of [...tokenRows.keys()])removeTokenRow(id);wanted.clear();imagePlans.clear();for(const controller of pending.values())controller.abort();$('emptyMap').hidden=false;renderInteractionMode();dirty=true;}
function rebuildTokenIndex(){tokenIndex.clear();movingTokens.clear();for(const t of Object.values(state.tokens)){tokenIndex.set(t);const v=visuals.get(t.id);if(v&&(v.x!==t.x||v.y!==t.y))movingTokens.add(t.id);}}
function renderMembers(){
 $('members').replaceChildren();let online=0;
 const gmCount=Object.values(state.members).filter(isGM).length;
  for(const m of Object.values(state.members)){const row=document.createElement('div');row.className='member';const dot=document.createElement('span');dot.className='live-dot'+(state.online[m.id]?'':' offline');const name=document.createElement('span');name.textContent=m.name;const role=document.createElement('small');role.textContent=isGM(m)?'ведущий':'игрок';row.append(dot,name,role);if(isEditorView()){const toggle=document.createElement('button');toggle.type='button';toggle.className='member-role-toggle';toggle.textContent=isGM(m)?'Снять GM':'Сделать GM';toggle.title=isGM(m)?'Убрать права ведущего':'Дать права ведущего';toggle.disabled=isGM(m)&&gmCount<=1;toggle.onclick=()=>queueCommand('memberUpdate',{memberId:m.id,gm:!isGM(m)});row.append(toggle);}$('members').append(row);if(state.online[m.id])online++;}
 $('memberCount').textContent=online;
}
function resourceOverlayState(){return state||campaign||{};}
function renderPanels(){
 const gm=isEditorView();$('mapTab').hidden=!gm;if(!gm&&!sidebarSections.map.panel.hidden)showSidebarSection('objects');renderInteractionMode();
 $('noSelection').querySelector('p').textContent=gm?'Выберите элемент сцены или переход':'Здесь появится лист персонажа';
 $('emptyMap').hidden=!!Object.keys(gm?state.elementCatalog||{}:state.elements||{}).length;$('sceneLabel').textContent=state.floors?.[currentFloorID]?`${state.scene.name} · ${state.floors[currentFloorID].name}`:state.scene.name;
	 renderMembers();renderTokenList();fillProperties();fillElementProperties();fillTransitionProperties();sceneTree.update(state,selectedElement,selectedTransition,gm);
}
function chooseListedToken(id){
 const token=navigationToken(id);if(!token)return;
 if(usesPlayerInteraction())activatePlayerToken(token,false);
 else select(id);
}
function focusListedToken(id){
 const token=navigationToken(id);if(!token)return;
 if(usesPlayerInteraction()){activatePlayerToken(token,true);return;}
 if(id!==selected){select(id);if(selected!==id)return;}
 camera.x=token.x-viewport.w/(2*camera.scale);
 camera.y=token.y-viewport.h/(2*camera.scale);
 scheduleViewportSave();
 if(token.floorId!==currentFloorID)setFloor(token.floorId,true);
 else ensureSceneRegion(currentViewRegion());
 dirty=true;
}
const tokenContextMenu=document.createElement('div');tokenContextMenu.id='tokenContextMenu';tokenContextMenu.className='token-context-menu';tokenContextMenu.setAttribute('role','menu');tokenContextMenu.hidden=true;document.body.append(tokenContextMenu);
let tokenContextID='';
function closeTokenContextMenu(){tokenContextMenu.hidden=true;tokenContextMenu.replaceChildren();tokenContextID='';}
function openTokenContextMenu(id,x,y){
 const token=navigationToken(id);if(!token)return;
 closeTokenContextMenu();tokenContextID=id;
 const option=(title,action,danger=false,stayOpen=false)=>{const button=document.createElement('button');button.type='button';button.setAttribute('role','menuitem');button.textContent=title;button.className=danger?'danger':'';button.onclick=()=>{if(!stayOpen)closeTokenContextMenu();action();};tokenContextMenu.append(button);};
 option('Открыть лист персонажа',()=>chooseListedToken(id));
 option('Центрировать на карте',()=>focusListedToken(id));
 if(isEditorView()){
  const divider=document.createElement('div');divider.className='token-context-divider';tokenContextMenu.append(divider);
  option('Сменить этаж  ›',()=>showTokenContextFloors(id),false,true);
  option('Настроить управление  ›',()=>showTokenContextOwners(id),false,true);
  option('Убрать с карты',()=>{const current=navigationToken(id);if(current&&confirm(`Убрать «${current.name}» с карты?`)){queueCommand('delete',{token:{id}});if(selected===id)select(null);}},true);
 }
 tokenContextMenu.hidden=false;
 const width=tokenContextMenu.offsetWidth,height=tokenContextMenu.offsetHeight;
 tokenContextMenu.style.left=`${Math.max(8,Math.min(x,window.innerWidth-width-8))}px`;
 tokenContextMenu.style.top=`${Math.max(8,Math.min(y,window.innerHeight-height-8))}px`;
 tokenContextMenu.querySelector('button')?.focus();
}
function showTokenContextFloors(id){
 const token=navigationToken(id);if(!token)return;tokenContextMenu.replaceChildren();
 const back=document.createElement('button');back.type='button';back.textContent='‹ Назад';back.onclick=()=>reopenTokenContextMenu(id);tokenContextMenu.append(back);
 for(const floor of orderedFloors(state)){const button=document.createElement('button');button.type='button';button.textContent=`${floor.id===token.floorId?'✓  ':'    '}${floor.name}`;button.disabled=floor.id===token.floorId;button.onclick=()=>{queueCommand('properties',{token:{id},properties:{floorId:floor.id}});closeTokenContextMenu();};tokenContextMenu.append(button);}
}
function showTokenContextOwners(id){
 const token=navigationToken(id);if(!token)return;tokenContextMenu.replaceChildren();
 const back=document.createElement('button');back.type='button';back.textContent='‹ Назад';back.onclick=()=>reopenTokenContextMenu(id);tokenContextMenu.append(back);
 const owners=new Set(token.ownerIds||[]),players=Object.values(state.members).filter(member=>!isGM(member));
 if(!players.length){const empty=document.createElement('div');empty.className='token-context-empty';empty.textContent='В сцене пока нет игроков';tokenContextMenu.append(empty);return;}
 for(const member of players){const button=document.createElement('button');button.type='button';button.textContent=`${owners.has(member.id)?'✓  ':'    '}${member.name}`;button.onclick=()=>{const current=new Set(navigationToken(id)?.ownerIds||[]);if(current.has(member.id))current.delete(member.id);else current.add(member.id);queueCommand('properties',{token:{id},properties:{ownerIds:[...current]}});closeTokenContextMenu();};tokenContextMenu.append(button);}
 const settings=document.createElement('button');settings.type='button';settings.textContent='Все настройки владения…';settings.onclick=()=>{closeTokenContextMenu();openTokenSettings(id);};tokenContextMenu.append(settings);
}
function reopenTokenContextMenu(id){const rect=tokenContextMenu.getBoundingClientRect();openTokenContextMenu(id,rect.left,rect.top);}
function openTokenSettings(id){select(id);if(selected!==id)return;characterSettingsOpen=true;updateCharacterPanelMode();$('properties').scrollIntoView({block:'nearest'});}
document.addEventListener('pointerdown',event=>{if(!tokenContextMenu.hidden&&!event.target.closest('#tokenContextMenu,.token-row-menu'))closeTokenContextMenu();if(!tokenCreateControls.hidden&&!event.target.closest('#tokenCreateControls,#add'))closeTokenCreatePanel();});
document.addEventListener('keydown',event=>{if(event.key==='Escape'){if(!tokenContextMenu.hidden){closeTokenContextMenu();event.stopPropagation();}if(!tokenCreateControls.hidden){closeTokenCreatePanel();event.stopPropagation();}}},true);
document.addEventListener('scroll',()=>closeTokenContextMenu(),true);
// Only list entries near the scroll viewport retain decoded avatar URLs.
const visibleTokenAvatars=new Set();
const tokenAvatarObserver=typeof IntersectionObserver==='undefined'?null:new IntersectionObserver(entries=>{
 for(const entry of entries){
  const row=entry.target.closest('.token-row'),id=row?.dataset.tokenId;if(!id||!tokenRows.has(id))continue;
  if(entry.isIntersecting){visibleTokenAvatars.add(id);updateTokenRowAvatar(id,entry.target.dataset.avatarAsset||'',entry.target);}
  else{visibleTokenAvatars.delete(id);updateTokenRowAvatar(id,'',entry.target);}
 }
},{root:$('tokens'),rootMargin:'72px'});
function createTokenRow(id){
 const row=document.createElement('div');row.className='token-row';row.dataset.tokenId=id;
 const main=document.createElement('button');main.type='button';main.className='token-row-main';main.title='Открыть лист персонажа (двойной клик — центрировать)';
 const swatch=document.createElement('span');swatch.className='swatch';const image=document.createElement('img');image.alt='';image.hidden=true;const fallback=document.createElement('span');swatch.append(image,fallback);
 const info=document.createElement('span');info.className='token-row-info';const name=document.createElement('span');name.className='row-name';const locator=document.createElement('small');locator.className='token-row-location';info.append(name,locator);main.append(swatch,info);
 const focus=document.createElement('button');focus.type='button';focus.className='token-row-control token-row-focus';focus.title='Центрировать на карте';focus.setAttribute('aria-label','Центрировать на карте');focus.innerHTML='<svg viewBox="0 0 24 24" width="19" height="19" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="5"/><path d="M12 2v5m0 10v5M2 12h5m10 0h5"/></svg>';
 const menu=document.createElement('button');menu.type='button';menu.className='token-row-control token-row-menu';menu.title='Действия с персонажем';menu.setAttribute('aria-label','Действия с персонажем');menu.textContent='⋯';
 row.append(main,focus,menu);
 row.onclick=e=>{if(e.target.closest('.token-row-control'))return;chooseListedToken(id);};
 row.ondblclick=e=>{if(e.target.closest('.token-row-control'))return;e.preventDefault();focusListedToken(id);};
 focus.onclick=e=>{e.stopPropagation();focusListedToken(id);};
 menu.onclick=e=>{e.stopPropagation();if(tokenContextID===id&&!tokenContextMenu.hidden){closeTokenContextMenu();return;}const rect=menu.getBoundingClientRect();openTokenContextMenu(id,rect.right-195,rect.bottom+5);};
 row.oncontextmenu=e=>{e.preventDefault();openTokenContextMenu(id,e.clientX,e.clientY);};
 row.ondragstart=e=>{if(!isEditorView()){e.preventDefault();return;}e.dataTransfer.effectAllowed='move';e.dataTransfer.setData('text/plain',id);row.classList.add('dragging');};
 row.ondragend=()=>{row.classList.remove('dragging');for(const group of $('tokens').querySelectorAll('.token-floor-group'))group.classList.remove('drag-over');};
 tokenRows.set(id,row);$('tokens').append(row);tokenAvatarObserver?.observe(swatch);return row;
}
async function updateTokenRowAvatar(id,assetID,swatch){
 const current=tokenRowAvatars.get(id);if(current?.asset===assetID)return;if(current){current.controller?.abort();if(current.url)URL.revokeObjectURL(current.url);tokenRowAvatars.delete(id);}const image=swatch.querySelector('img'),fallback=swatch.querySelector('span');image.hidden=true;image.removeAttribute('src');fallback.hidden=false;if(!assetID)return;
 const controller=new AbortController(),entry={asset:assetID,controller,url:''};tokenRowAvatars.set(id,entry);try{const response=await fetch(avatarPath(assetID),{headers:{Authorization:`Bearer ${credentials.key}`},signal:controller.signal});if(!response.ok)throw new Error();const blob=await response.blob();if(controller.signal.aborted||tokenRowAvatars.get(id)!==entry||!swatch.isConnected)return;entry.url=URL.createObjectURL(blob);image.src=entry.url;image.hidden=false;fallback.hidden=true;}catch{}finally{if(entry.controller===controller)entry.controller=null;}
}
function updateTokenRow(t){
 let row=tokenRows.get(t.id);if(!row)row=createTokenRow(t.id);
 row.className='token-row'+(t.id===selected?' selected':'')+(t.id===state?.activeTokenId?' active':'');row.draggable=isEditorView();const [swatch,info]=row.firstElementChild.children;swatch.style.background=t.color;swatch.querySelector('span').textContent=t.name.slice(0,1)||'◆';swatch.dataset.avatarAsset=t.asset||state?.tokenListAvatars?.[t.id]||'';if(!tokenAvatarObserver||visibleTokenAvatars.has(t.id))updateTokenRowAvatar(t.id,swatch.dataset.avatarAsset,swatch);info.firstElementChild.textContent=t.name;info.lastElementChild.textContent=isEditorView()?`#${t.id.slice(0,6)}`:`${state?.floors?.[t.floorId]?.name||'Этаж'} · #${t.id.slice(0,6)}`;row.firstElementChild.setAttribute('aria-label',`Открыть лист: ${t.name}`);
 $('emptyTokens').hidden=tokenRows.size>0;
}
function removeTokenRow(id){const row=tokenRows.get(id),avatar=tokenRowAvatars.get(id);avatar?.controller?.abort();if(avatar?.url)URL.revokeObjectURL(avatar.url);tokenRowAvatars.delete(id);if(row){const swatch=row.querySelector('.swatch');if(swatch)tokenAvatarObserver?.unobserve(swatch);visibleTokenAvatars.delete(id);row.remove();tokenRows.delete(id);}if(tokenContextID===id)closeTokenContextMenu();$('emptyTokens').hidden=tokenRows.size>0;}
function renderTokenList(){
 if(!state)return;$('revision').textContent=`Ревизия ${state.revision}`;const listed=usesPlayerInteraction()?Object.values(state.ownedTokens||{}):Object.values(state.tokenCatalog||state.tokens||{}),ids=new Set(listed.map(t=>t.id));
 for(const id of tokenRows.keys())if(!ids.has(id))removeTokenRow(id);
 for(const t of listed)updateTokenRow(t);
 $('tokens').replaceChildren();
 if(isEditorView())for(const floor of orderedFloors(state)){
  const floorTokens=listed.filter(token=>token.floorId===floor.id).sort((a,b)=>a.name.localeCompare(b.name)||a.id.localeCompare(b.id));
  const group=document.createElement('section');group.className='token-floor-group';group.dataset.floorId=floor.id;
  const heading=document.createElement('div');heading.className='token-floor-heading';heading.innerHTML=`<span>${floor.name}</span><small>${floorTokens.length}</small>`;group.append(heading);
  const rows=document.createElement('div');rows.className='token-floor-rows';for(const token of floorTokens)rows.append(tokenRows.get(token.id));group.append(rows);
  group.ondragover=event=>{if(event.dataTransfer.types.includes('text/plain')){event.preventDefault();event.dataTransfer.dropEffect='move';group.classList.add('drag-over');}};
  group.ondragleave=event=>{if(!group.contains(event.relatedTarget))group.classList.remove('drag-over');};
  group.ondrop=event=>{event.preventDefault();group.classList.remove('drag-over');const id=event.dataTransfer.getData('text/plain'),token=navigationToken(id);if(token&&token.floorId!==floor.id)queueCommand('properties',{token:{id},properties:{floorId:floor.id}});};
  $('tokens').append(group);
 }else for(const token of listed.sort((a,b)=>a.name.localeCompare(b.name)||a.id.localeCompare(b.id)))$('tokens').append(tokenRows.get(token.id));
 $('emptyTokens').hidden=ids.size>0;
}
function fillCatalogSelect(control,items,emptyText){const selectedValue=control.value;control.replaceChildren();const empty=document.createElement('option');empty.value='';empty.textContent=emptyText;control.append(empty);for(const item of items){const option=document.createElement('option');option.value=item.id;option.textContent=item.name||item.id;control.append(option);}if(items.some(item=>item.id===selectedValue))control.value=selectedValue;}
function cancelCharacterStatTimers(characterID='',discard=false){for(const [key,timer]of characterStatTimers)if(!characterID||key.startsWith(`${characterID}/`)){clearTimeout(timer);characterStatTimers.delete(key);}if(discard&&characterID)characterDrafts.delete(characterID);}
function onCharacterWatchChange(snapshot,reason,characterID){if(snapshot&&characterEditSession?.characterId===snapshot.characterId&&!characterEditSession.saving&&JSON.stringify(snapshot.instance)!==JSON.stringify(characterEditSession.expectedInstance))characterEditSession.conflict=true;characterWatchReason=snapshot?'':reason;if(!snapshot){const revoked=reason==='accessRevoked'||reason==='unavailable'||reason==='closed';cancelCharacterStatTimers(characterID,revoked);if(revoked)for(const key of [...pendingNewStats])if(key.startsWith(`${characterID}/`))pendingNewStats.delete(key);if(characterAvatarDisplayController)characterAvatarDisplayController.abort();characterAvatarUploads.get(characterID)?.abort();}updateInspectorState();}
function avatarPath(assetID){const query=new URLSearchParams({session:credentials.session});return `/api/asset/${encodeURIComponent(assetID)}/avatar.png?${query}`;}
async function showCharacterAvatar(assetID,characterID){
 if(assetID===characterAvatarAsset)return;if(characterAvatarDisplayController)characterAvatarDisplayController.abort();characterAvatarDisplayController=null;if(characterAvatarURL)URL.revokeObjectURL(characterAvatarURL);characterAvatarURL='';characterAvatarAsset=assetID||'';$('characterAvatar').hidden=true;$('characterAvatarEmpty').hidden=false;if(!assetID)return;
 const controller=new AbortController();characterAvatarDisplayController=controller;try{const response=await fetch(avatarPath(assetID),{headers:{Authorization:`Bearer ${credentials.key}`},signal:controller.signal});if(!response.ok)throw new Error(`Avatar: HTTP ${response.status}`);const blob=await response.blob();if(controller.signal.aborted||characterWatch.snapshot?.characterId!==characterID||characterWatch.snapshot?.effective?.avatarAssetId!==assetID)return;characterAvatarURL=URL.createObjectURL(blob);$('characterAvatar').src=characterAvatarURL;$('characterAvatar').hidden=false;$('characterAvatarEmpty').hidden=true;}catch(error){if(error.name!=='AbortError')toast(error.message);}finally{if(characterAvatarDisplayController===controller)characterAvatarDisplayController=null;}
}
function confirmMechanicalEdit(snapshot){if(!snapshot?.permissions?.manage)return false;const instance=snapshot.instance;if(!instance?.presetId||instance.materialized||confirmedMechanicalEdits.has(instance.id))return true;if(!confirm('Изменение механики создаст самостоятельный снимок персонажа. Дальнейшие изменения механики шаблона перестанут применяться. Продолжить?'))return false;confirmedMechanicalEdits.add(instance.id);return true;}
function queueCharacterStat(characterID,statID,value,isNew=false){if(!confirmMechanicalEdit(characterWatch.snapshot))return false;if(!characterWatch.snapshot?.permissions?.edit||characterWatch.characterId!==characterID)return false;if(outbox?.data.queue.some(command=>command.type==='characterStatSet'&&command.characterId===characterID&&command.statId===statID&&sameValue(command.statValue,value)))return true;if(isNew)pendingNewStats.add(`${characterID}/${statID}`);return queueCommand('characterStatSet',{characterId:characterID,statId:statID,statValue:value});}
function scheduleCharacterStat(characterID,statID,value){characterDrafts.set(characterID,statID,value);const key=`${characterID}/${statID}`;clearTimeout(characterStatTimers.get(key));characterStatTimers.set(key,setTimeout(()=>{characterStatTimers.delete(key);if(characterWatch.characterId===characterID&&characterWatch.snapshot?.permissions?.edit)queueCharacterStat(characterID,statID,value);},FORM_AUTOSAVE_DELAY));}
function makeCharacterStatInput(snapshot,statID,definition,value){
 const input=document.createElement('input');input.dataset.statId=statID;
 input.disabled=!snapshot.permissions?.manage||!isEditorView();
 if(definition.type==='boolean'){input.type='checkbox';input.checked=!!value.value;}
 else{input.type=definition.type==='string'?'text':'number';input.value=statInputValue(value);if(definition.type==='integer')input.step='1';else if(definition.type==='number')input.step='any';}
 const remember=()=>{try{const next=statValueFromInput(definition.type,input.value,input.checked);scheduleCharacterStat(snapshot.characterId,statID,next);}catch(error){input.setCustomValidity(error.message);return false;}input.setCustomValidity('');return true;};
 input.addEventListener('input',remember);
 input.addEventListener('change',()=>{if(remember()){const key=`${snapshot.characterId}/${statID}`;clearTimeout(characterStatTimers.get(key));characterStatTimers.delete(key);queueCharacterStat(snapshot.characterId,statID,characterDrafts.get(snapshot.characterId)[statID]);}});
 input.addEventListener('blur',()=>{if(input.checkValidity()&&Object.hasOwn(characterDrafts.get(snapshot.characterId),statID))queueCharacterStat(snapshot.characterId,statID,characterDrafts.get(snapshot.characterId)[statID]);});
 return input;
}
function renderCharacterStats(snapshot){
 const container=$('characterStats'),editor=$('characterStatEditor');container.replaceChildren();editor.replaceChildren();
 const effective=snapshot.effective?.stats||{},drafts=characterDrafts.get(snapshot.characterId),ids=Object.keys(effective).sort((a,b)=>(snapshot.statDefinitions?.[a]?.name||a).localeCompare(snapshot.statDefinitions?.[b]?.name||b)||a.localeCompare(b));
 for(const statID of ids){
  const definition=snapshot.statDefinitions?.[statID]||{id:statID,name:statID,type:effective[statID].type},server=effective[statID],value=characterEditSession?server:(Object.hasOwn(drafts,statID)?drafts[statID]:server);
  const card=document.createElement('div');card.className='character-stat-card';const name=document.createElement('span');name.className='character-stat-name';name.textContent=definition.name||statID;
  const output=document.createElement('strong');output.className='character-stat-value';output.textContent=definition.type==='boolean'?(value.value?'Да':'Нет'):String(value.value??'');card.title=definition.name||statID;card.dataset.sheetItem=`stat:${statID}`;if(characterEditSession){const input=editInput(statID,'stat',definition,value);card.append(name,input,editRemoveButton(statID,'stat'));}else card.append(name,output);container.append(card);
  if(characterEditSession||!snapshot.permissions?.manage||!isEditorView())continue;
  const row=document.createElement('div');row.className='character-stat-edit';const label=document.createElement('label');label.textContent=definition.name||statID;label.append(makeCharacterStatInput(snapshot,statID,definition,value));row.append(label);
  const remove=document.createElement('button');remove.type='button';remove.className='subtle character-parameter-remove';remove.textContent='Убрать';remove.title='Убрать характеристику у этого персонажа';remove.onclick=()=>removeCharacterParameter(snapshot,statID,definition.name||statID);row.append(remove);
  editor.append(row);
 }
 if(!ids.length)container.textContent='Нет характеристик.';
 $('characterParameterAdd').hidden=!characterEditSession;
}
function createCharacterResourceInput(snapshot,statID,definition,resource,field){
 const input=document.createElement('input');input.type=definition.type==='boolean'?'checkbox':definition.type==='string'?'text':'number';
 if(input.type==='number')input.step=definition.type==='integer'?'1':'any';
 if(input.type==='checkbox')input.checked=!!resource[field].value;else input.value=statInputValue(resource[field]);
 input.disabled=field==='current'?!snapshot.permissions?.edit:!snapshot.permissions?.manage||!isEditorView();
 input.setAttribute('aria-label',`${definition.name||statID}: ${field==='current'?'текущее':'начальное'} значение`);
 input.onchange=()=>{try{const value=statValueFromInput(definition.type,input.value,input.checked);if(field==='initial'&&!confirmMechanicalEdit(snapshot))return;queueCommand(field==='current'?'characterResourceCurrentSet':'characterResourceInitialSet',{characterId:snapshot.characterId,statId:statID,statValue:value});}catch(error){toast(error.message);}};
 return input;
}
function renderCharacterResources(snapshot){
 const container=$('characterResources'),editor=$('characterResourceInitialEditor');container.replaceChildren();editor.replaceChildren();
 const resources=snapshot.effective?.resources||{};
 for(const [statID,resource] of Object.entries(resources).sort(([a],[b])=>(snapshot.statDefinitions?.[a]?.name||a).localeCompare(snapshot.statDefinitions?.[b]?.name||b)||a.localeCompare(b))){
  const definition=snapshot.statDefinitions?.[statID]||{type:resource.initial.type,name:statID};
  const row=document.createElement('section');row.className='character-resource-card';
  const head=document.createElement('div');head.className='character-resource-head';const title=document.createElement('strong');title.textContent=definition.name||statID;
  const count=document.createElement('span');count.className='character-resource-count';count.textContent=definition.type==='boolean'?(resource.current.value?'Да':'Нет'):definition.type==='string'?String(resource.current.value||'—'):`${resource.current.value} / ${resource.initial.value}`;head.append(title,count);row.append(head);
  const edit=document.createElement('div');edit.className='character-resource-controls';
  const input=createCharacterResourceInput(snapshot,statID,definition,resource,'current');
  if(input.type==='number'){
   const decrement=document.createElement('button'),increment=document.createElement('button');
   for(const [button,delta,text] of [[decrement,-1,'−'],[increment,1,'+']]){button.type='button';button.className='subtle character-resource-step';button.textContent=text;button.disabled=input.disabled;button.setAttribute('aria-label',`${definition.name||statID}: ${delta>0?'увеличить':'уменьшить'} на 1`);button.onclick=()=>{const next=Number(input.value)+delta;if(!Number.isFinite(next)||(definition.type==='integer'&&!Number.isSafeInteger(next)))return;input.value=String(next);input.onchange();};}
   edit.append(decrement,input,increment);
  }else edit.append(input);
  if(characterEditSession){edit.replaceChildren();const currentLabel=document.createElement('label');currentLabel.textContent='Текущее';currentLabel.append(editInput(statID,'resource',definition,resource.current,'current'));const initialLabel=document.createElement('label');initialLabel.textContent='Исходное';initialLabel.append(editInput(statID,'resource',definition,resource.initial,'initial'));edit.append(currentLabel,initialLabel);edit.classList.add('character-resource-inline');row.append(editRemoveButton(statID,'stat'));}row.append(edit);row.dataset.sheetItem=`stat:${statID}`;
  if(!characterEditSession&&Object.hasOwn(snapshot.instance?.resourceCurrent||{},statID)&&snapshot.permissions?.edit){const reset=document.createElement('button');reset.type='button';reset.className='subtle character-resource-reset';reset.textContent='Восстановить начальное';reset.onclick=()=>queueCommand('characterResourceCurrentReset',{characterId:snapshot.characterId,statId:statID});row.append(reset);}
  container.append(row);
  if(!characterEditSession&&snapshot.permissions?.manage&&isEditorView()){
   const row=document.createElement('div');row.className='character-resource-initial';const label=document.createElement('label');label.textContent=definition.name||statID;label.append(createCharacterResourceInput(snapshot,statID,definition,resource,'initial'));const remove=document.createElement('button');remove.type='button';remove.className='subtle character-parameter-remove';remove.textContent='Убрать';remove.onclick=()=>removeCharacterParameter(snapshot,statID,definition.name||statID);row.append(label,remove);editor.append(row);
  }
 }
 if(!container.children.length)container.textContent='Нет характеристик с отслеживаемым значением.';
 updateCharacterParameterOptions(snapshot);
}
function formatRoll(roll){const modifier=[roll.modifierStat,roll.modifierFixed?String(roll.modifierFixed):''].filter(Boolean).join(' + ');return `${roll.count}d${roll.sides}${modifier?` + ${modifier}`:''}`;}
function renderCharacterActions(snapshot){
 const container=$('characterActions'),removal=$('characterActionRemoveList');container.replaceChildren();removal.replaceChildren();
 const ids=[...(snapshot.effective?.actionIds||[])].sort((a,b)=>(snapshot.actions?.[a]?.name||a).localeCompare(snapshot.actions?.[b]?.name||b)||a.localeCompare(b));
 for(const actionID of ids){
  const action=snapshot.actions?.[actionID],row=document.createElement('section');row.className='character-action-card';row.dataset.sheetItem=`action:${actionID}`;const title=document.createElement('strong');title.textContent=action?.name||actionID;row.append(title);
  if(action?.description){const description=document.createElement('p');description.className='character-action-description';description.textContent=action.description;row.append(description);if(action.description.length>120){description.classList.add('collapsed');const toggle=document.createElement('button');toggle.type='button';toggle.className='character-description-toggle subtle';toggle.textContent='Подробнее';toggle.onclick=()=>{const collapsed=description.classList.toggle('collapsed');toggle.textContent=collapsed?'Подробнее':'Свернуть';};row.append(toggle);}}
  const status=action?dependencyStatus(action,snapshot):{missing:[],errors:[],invalidIds:[]};
  const invalid=new Set([...status.missing.map(item=>item.id),...status.invalidIds]);
  for(const roll of characterEditSession?[]:action?.rolls||[]){const button=document.createElement('button');button.type='button';button.className='subtle character-roll';button.textContent=`${roll.name||roll.id}: ${formatRoll(roll)}`;
   const blocked=invalid.has(roll.modifierStat);
   button.disabled=blocked;button.onclick=()=>dice.rollAction(snapshot.characterId,actionID,roll.id);row.append(button);}
  if(!characterEditSession&&(status.missing.length||status.errors.length)){
   const hint=document.createElement('p');hint.className='character-dependencies-hint';hint.textContent=[...status.missing.map(item=>`Нет значения: ${item.definition.name||item.id}`),...status.errors].join('; ');row.append(hint);
   if(status.missing.length&&snapshot.permissions?.manage&&isEditorView()){
    const fix=document.createElement('button');fix.type='button';fix.className='subtle';fix.textContent='Настроить характеристики';fix.onclick=()=>{openCharacterEdit();if(characterEditSession)showActionDependencies(action,editSnapshot(snapshot));};row.append(fix);
   }
  }
  if(characterEditSession)row.append(editRemoveButton(actionID,'action'));container.append(row);
  if(!characterEditSession&&snapshot.permissions?.manage&&isEditorView()){
   const removeRow=document.createElement('div');removeRow.className='character-action-remove';const name=document.createElement('span');name.textContent=action?.name||actionID;const remove=document.createElement('button');remove.type='button';remove.className='danger';remove.textContent='Убрать';remove.onclick=()=>confirmMechanicalEdit(snapshot)&&queueCommand('characterActionRemove',{characterId:snapshot.characterId,actionId:actionID});removeRow.append(name,remove);removal.append(removeRow);
  }
 }
 if(!ids.length)container.textContent='Нет действий.';
 const effective=new Set(ids),available=characterCatalog.actions.filter(action=>!effective.has(action.id));fillCatalogSelect($('characterActionAdd'),available,characterCatalog.loading.has('actions')?'Загрузка…':'Выберите действие');const manage=snapshot.permissions?.manage&&isEditorView();$('characterActionManage').hidden=true;$('characterActionAddButton').disabled=!$('characterActionAdd').value;$('characterActionMore').hidden=!manage||!characterCatalog.next.actions;$('characterActionMore').disabled=characterCatalog.loading.has('actions');
}
let characterActiveTab='stats',characterSettingsOpen=false,characterMechanicsOpen=false,characterDisplayedID='';
let characterEditSession=null;
const actionDependenciesDialog=document.createElement('dialog');actionDependenciesDialog.className='action-dependencies-dialog';
actionDependenciesDialog.innerHTML='<form method="dialog"><h3>Настройка действия</h3><p data-dependencies-intro></p><div data-dependencies-fields></div><p class="muted" data-dependencies-errors hidden></p><div class="action-dependencies-buttons"><button type="button" class="subtle" data-dependencies-skip>Пропустить</button><button type="submit" class="primary" data-dependencies-confirm>Добавить характеристики</button></div></form>';
document.body.append(actionDependenciesDialog);
actionDependenciesDialog.querySelector('[data-dependencies-skip]').onclick=()=>actionDependenciesDialog.close();
function selectedActionDefinition(id){return characterEditSession?.newActions.find(a=>a.id===id)||actionDefinitionRegistry?.[id]||characterWatch.snapshot?.actions?.[id];}
function dependencyDefinitions(snapshot){return {...(resourceDefinitionRegistry||{}),...(snapshot?.statDefinitions||{}),...Object.fromEntries((characterEditSession?.definitions||[]).map(d=>[d.id,d]))};}
function dependencyStatus(action,snapshot){return actionDependencies(action,snapshot.effective,dependencyDefinitions(snapshot));}
function showActionDependencies(action,snapshot){
 if(!characterEditSession||!action)return;
 const {missing,errors}=dependencyStatus(action,snapshot);
 if(!missing.length&&!errors.length)return;
 const d=characterEditSession,dialog=actionDependenciesDialog,fields=dialog.querySelector('[data-dependencies-fields]');fields.replaceChildren();
 dialog.querySelector('[data-dependencies-intro]').textContent=`Для «${action.name||action.id}» требуются дополнительные характеристики. Изменения будут сохранены вместе с персонажем.`;
 const problem=dialog.querySelector('[data-dependencies-errors]');problem.hidden=!errors.length;problem.textContent=errors.join('. ');
 dialog.querySelector('[data-dependencies-confirm]').disabled=!missing.length;
 const inputs=[];
 for(const {id,definition} of missing){
  const label=document.createElement('label');label.textContent=definition.name||id;
  const input=document.createElement('input');input.type=definition.type==='boolean'?'checkbox':definition.type==='string'?'text':'number';input.step=definition.type==='number'?'any':'1';input.required=input.type!=='checkbox';
  if(definition.default!==undefined&&definition.default!==null){if(input.type==='checkbox')input.checked=!!definition.default.value;else input.value=statInputValue(definition.default);}
  else if(input.type==='checkbox')input.checked=false;
  input.dataset.statId=id;label.append(input);fields.append(label);inputs.push({id,definition,input});
 }
 dialog.querySelector('form').onsubmit=e=>{
  e.preventDefault();if(characterEditSession!==d)return dialog.close();
  const values=[];
  try{for(const {id,definition,input} of inputs)values.push({id,definition,value:statValueFromInput(definition.type,input.value,input.checked)});}
  catch(error){problem.hidden=false;problem.textContent=error.message;return;}
  for(const {id,definition,value} of values){
   if(definition.kind==='resource'){d.resourceInitial[id]=value;d.resourceCurrent[id]=value;}
   else d.stats[id]=value;
   addEditPlacement(`stat:${id}`);
  }
  dialog.close();renderCharacterPanel();
 };
 if(!dialog.open)dialog.showModal();
}
async function assignActionToEdit(id){
 const d=characterEditSession;if(!d||!id||d.actionIds.includes(id))return;
 // Catalog entries are summaries; load full ruleset definitions before checking rolls.
 if(!actionDefinitionRegistry){try{await refreshResourceDefinitions();}catch{return;}}
 if(characterEditSession!==d||d.actionIds.includes(id))return;
 const action=selectedActionDefinition(id);
 if(!action){toast('Не удалось загрузить определение действия');return;}
 d.actionIds.push(id);addEditPlacement(`action:${id}`);renderCharacterPanel();showActionDependencies(action,editSnapshot(characterWatch.snapshot));
}

function discardCharacterPanelDisplay(){characterDisplayedID='';characterPanel.dataset.characterId='';characterPanel.hidden=true;characterPanel.inert=false;characterPanel.removeAttribute('aria-busy');$('characterSummary').hidden=true;showCharacterAvatar('','');}
const cloneEdit=value=>JSON.parse(JSON.stringify(value));
function makeCharacterEdit(snapshot){
 return {characterId:snapshot.characterId,revision:snapshot.revision,registryRevision:snapshot.registryRevision,
  name:snapshot.instance.name,expectedInstance:cloneEdit(snapshot.instance),stats:cloneEdit(snapshot.effective.stats||{}),
  resourceInitial:Object.fromEntries(Object.entries(snapshot.effective.resources||{}).map(([id,r])=>[id,cloneEdit(r.initial)])),
  resourceCurrent:Object.fromEntries(Object.entries(snapshot.effective.resources||{}).map(([id,r])=>[id,cloneEdit(r.current)])),
  actionIds:[...(snapshot.effective.actionIds||[])],definitions:[],newActions:[],resetAvatar:false,placeholderAvatar:false,artworkPath:'',selectedMediaAsset:'',avatarFile:null,avatarPreview:'',places:[],saving:false,invalid:new Set(),conflict:false};
}
function editSnapshot(snapshot){
 if(!characterEditSession||characterEditSession.characterId!==snapshot.characterId)return snapshot;
 const d=characterEditSession,resources={};for(const [id,initial] of Object.entries(d.resourceInitial))resources[id]={initial,current:d.resourceCurrent[id]||initial};
 return {...snapshot,effective:{...snapshot.effective,stats:d.stats,resources,actionIds:d.actionIds},statDefinitions:{...snapshot.statDefinitions,...Object.fromEntries(d.definitions.map(x=>[x.id,x]))},actions:{...snapshot.actions,...Object.fromEntries(d.actionIds.filter(id=>actionDefinitionRegistry?.[id]).map(id=>[id,actionDefinitionRegistry[id]])),...Object.fromEntries(d.newActions.map(x=>[x.id,x]))}};
}
function editChangeValue(id,kind,definition,input,field){
 const d=characterEditSession;if(!d)return;
 try{const value=statValueFromInput(definition.type,input.value,input.checked);
  if(kind==='resource'){if(field==='initial')d.resourceInitial[id]=value;else d.resourceCurrent[id]=value;}
  else d.stats[id]=value;
  d.invalid.delete(`${id}:${field}`);input.classList.remove('character-edit-invalid');
 }catch(error){d.invalid.add(`${id}:${field}`);input.classList.add('character-edit-invalid');input.title=error.message;}
 updateEditButtons();
}
function editInput(id,kind,definition,value,field='value'){
 const input=document.createElement('input');input.className='character-inline-input';input.type=definition.type==='boolean'?'checkbox':definition.type==='string'?'text':'number';
 if(input.type==='number')input.step=definition.type==='integer'?'1':'any';
 if(input.type==='checkbox')input.checked=!!value?.value;else input.value=statInputValue(value);
 input.setAttribute('aria-label',`${definition.name||id}: ${field==='initial'?'исходное значение':field==='current'?'текущее значение':'значение'}`);
 input.oninput=()=>editChangeValue(id,kind,definition,input,field);input.onchange=input.oninput;
 input.onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();input.blur();}};
 input.onblur=()=>editChangeValue(id,kind,definition,input,field);
 return input;
}
function removeEditItem(id,kind){
 const d=characterEditSession;if(!d)return;
 if(kind==='action'){d.actionIds=d.actionIds.filter(x=>x!==id);d.newActions=d.newActions.filter(x=>x.id!==id);}
 else{delete d.stats[id];delete d.resourceInitial[id];delete d.resourceCurrent[id];d.definitions=d.definitions.filter(x=>x.id!==id);}
 d.places=d.places.filter(x=>x.key!==(kind==='action'?`action:${id}`:`stat:${id}`));
 for(const key of [...d.invalid])if(key.startsWith(`${id}:`))d.invalid.delete(key);
 renderCharacterPanel();
}
function editRemoveButton(id,kind){const b=document.createElement('button');b.type='button';b.className='subtle character-edit-remove';b.title='Убрать у персонажа (определение останется в кампании)';b.textContent='Убрать';b.onclick=()=>removeEditItem(id,kind);return b;}
function updateEditButtons(){
 const d=characterEditSession;$('characterEditHeader').hidden=!d;$('characterAddToolbar').hidden=!d;characterAvatarFrame.classList.toggle('character-avatar-editable',!!d);
 $('characterMechanicsToggle').classList.toggle('active',!!d);$('characterMechanicsToggle').setAttribute('aria-expanded',String(!!d));
 if(!d){$('characterAddPopover').hidden=true;return;}
 $('characterEditSave').disabled=d.saving||d.invalid.size>0||d.conflict;
 $('characterEditCancel').disabled=d.saving;
 $('characterEditFeedback').textContent=d.conflict?'Данные изменились на сервере. Отмените редактирование и повторите.':d.saving?'Сохранение…':d.invalid.size?'Исправьте недопустимые значения':'';
}
function releaseEditSession(){if(actionDependenciesDialog.open)actionDependenciesDialog.close();const d=characterEditSession;if(d?.avatarPreview)URL.revokeObjectURL(d.avatarPreview);characterAssetPicker.close();characterEditSession=null;updateEditButtons();}
function discardEdit(){if(!characterEditSession||characterEditSession.saving)return false;releaseEditSession();renderCharacterPanel();return true;}
function confirmLeaveEdit(){if(!characterEditSession)return true;if(characterEditSession.saving){toast('Дождитесь подтверждения сохранения');return false;}if(!confirm('Несохранённые изменения персонажа будут потеряны. Выйти?'))return false;return discardEdit();}
function openCharacterEdit(){const snapshot=characterWatch.snapshot;if(!snapshot?.permissions?.manage||!isEditorView())return;
 cancelCharacterStatTimers(snapshot.characterId);characterEditSession=makeCharacterEdit(snapshot);sheetEdit=false;characterSettingsOpen=false;characterMechanicsOpen=false;
 if(!resourceDefinitionRegistry)refreshResourceDefinitions();renderCharacterPanel();
}
function addEditPlacement(key){const d=characterEditSession;if(!d)return;d.places.push({key,tab:characterActiveTab});}
function applyEditPlacements(d){
 if(!d.places.length)return;
 sheetMutate(layout=>{for(const {key,tab} of d.places){const target=layout.tabs.find(t=>t.id===tab);if(!target)continue;
  for(const t of layout.tabs)for(const section of t.sections)section.items=section.items.filter(x=>x!==key);
  layout.hidden=layout.hidden.filter(x=>x!==key);
  let section=target.sections.find(s=>s.id===`user:added:${tab}`);
  if(!section){section={id:`user:added:${tab}`,name:'Добавлено',view:'grid',collapsed:false,items:[]};target.sections.push(section);}
  section.items.push(key);
 }return layout;});
}
function characterEditMechanicsChanged(d,snapshot){
 const old=snapshot.effective||{},initial=Object.fromEntries(Object.entries(old.resources||{}).map(([id,r])=>[id,r.initial]));
 return d.definitions.length>0||d.newActions.length>0||JSON.stringify(d.stats)!==JSON.stringify(old.stats||{})||JSON.stringify(d.resourceInitial)!==JSON.stringify(initial)||JSON.stringify(d.actionIds)!==JSON.stringify(old.actionIds||[]);
}
async function saveCharacterEdit(){const d=characterEditSession;if(!d||d.saving||d.invalid.size||d.conflict)return;
 if(!$('characterRename').value.trim()){toast('Укажите имя персонажа');return;}
 d.name=$('characterRename').value.trim();
 const original=characterWatch.snapshot;if(d.characterId!==original?.characterId||JSON.stringify(d.expectedInstance)!==JSON.stringify(original.instance)){d.conflict=true;updateEditButtons();return;}
 if(characterEditMechanicsChanged(d,original)&&!confirmMechanicalEdit(original))return;
 d.saving=true;updateEditButtons();
 let preparedArtwork='';
 try{
   if(d.avatarFile){
     const result=await uploadSharedMedia(d.avatarFile);
     const attached=await presetAPI('/api/definitions/ruleset/media/attach',{assetId:result.asset.id,filename:d.avatarFile.name});
     preparedArtwork=attached.path;d.registryRevision=attached.registryRevision;
   }else if(d.selectedMediaAsset){
     const attached=await presetAPI('/api/definitions/ruleset/media/attach',{assetId:d.selectedMediaAsset,filename:d.selectedMediaAsset+'.png'});
     preparedArtwork=attached.path;d.registryRevision=attached.registryRevision;
   }else if(d.artworkPath){preparedArtwork=d.artworkPath;}
   if(characterEditSession!==d){return;}
   const request={expectedCharacterRevision:d.revision,expectedRegistryRevision:d.registryRevision,expectedInstance:d.expectedInstance,
     name:d.name,stats:d.stats,resourceInitial:d.resourceInitial,resourceCurrent:d.resourceCurrent,actionIds:d.actionIds,
     definitions:d.definitions,newActions:d.newActions,resetAvatar:d.resetAvatar,placeholderAvatar:d.placeholderAvatar,...(preparedArtwork?{artworkPath:preparedArtwork}:{})};
   if(!queueCommand('characterEdit',{characterId:d.characterId,characterEdit:request})){
     d.saving=false;toast('Не удалось отправить изменения');updateEditButtons();
   }
 }catch(error){d.saving=false;toast(`Не удалось подготовить сохранение: ${error.message}`);updateEditButtons();}
}
$('characterEditCancel').onclick=discardEdit;$('characterEditSave').onclick=saveCharacterEdit;
$('characterAddToggle').onclick=()=>{if(!characterEditSession)return;$('characterAddPopover').hidden=!$('characterAddPopover').hidden;if(!$('characterAddPopover').hidden)updateCharacterParameterOptions(editSnapshot(characterWatch.snapshot));};
window.addEventListener('beforeunload',event=>{if(characterEditSession&&!characterEditSession.saving){event.preventDefault();event.returnValue='';}});

let sheetEdit=false,sheetScope='',sheetLayout=null,sheetDrag=null,sheetDropMarker=null;
const sheetSessionFingerprint=value=>{let hash=2166136261;for(const char of String(value||'')){hash=Math.imul(hash^char.charCodeAt(0),16777619);}return (hash>>>0).toString(16);};
const sheetSavedScope=()=>{
 // The browser profile owns these preferences; campaign scope is a safe fallback.
 const metadata=state?.ruleset?.metadata||state?.rulesetMetadata||state?.definitions?.ruleset;
 const rulesetID=metadata?.id||state?.ruleset?.id;
 return rulesetID?`ruleset:${rulesetID}`:`campaign:${campaign?.id||state?.campaign?.id||state?.campaignId||sheetSessionFingerprint(credentials?.session)}`;
};
function sheetEntryList(snapshot){
 const definitions=snapshot.statDefinitions||{},entries=[];
 for(const [id,value] of Object.entries(snapshot.effective?.stats||{}))entries.push({key:`stat:${id}`,type:'stat',name:definitions[id]?.name||id,group:definitions[id]?.group||'',resource:false});
 for(const id of Object.keys(snapshot.effective?.resources||{}))entries.push({key:`stat:${id}`,type:'stat',name:definitions[id]?.name||id,group:definitions[id]?.group||'',resource:true});
 for(const id of snapshot.effective?.actionIds||[])entries.push({key:`action:${id}`,type:'action',name:snapshot.actions?.[id]?.name||id,group:snapshot.actions?.[id]?.group||'',resource:false});
 return entries;
}
function sheetMutate(callback){if(!sheetLayout)return;sheetLayout=normalizeLayout(callback(sheetLayout));saveLayout(sheetScope,sheetLayout);if(characterWatch.snapshot)renderCharacterPanel();}
function ensureSheetSection(layout,tab,section){const target=layout.tabs.find(t=>t.id===tab.id);if(target&&!target.sections.some(s=>s.id===section.id))target.sections.push({...section,items:[...section.items]});return layout;}
function sheetSectionMenu(tab,section,entries){
 const actions=['Переименовать','Список / сетка','Свернуть / развернуть','Переместить раздел','Удалить раздел'];
 const answer=prompt(`Раздел «${section.name}»:\n${actions.map((name,i)=>`${i+1}. ${name}`).join('\n')}`);
 if(!answer)return;
 const index=Number(answer);
 if(index===1){const name=prompt('Название раздела',section.name);if(name?.trim())sheetMutate(layout=>{ensureSheetSection(layout,tab,section);layout.tabs.find(t=>t.id===tab.id).sections.find(s=>s.id===section.id).name=name.trim().slice(0,120);return layout;});}
 if(index===2)sheetMutate(layout=>{ensureSheetSection(layout,tab,section);const target=layout.tabs.find(t=>t.id===tab.id).sections.find(s=>s.id===section.id);target.view=target.view==='grid'?'list':'grid';return layout;});
 if(index===3)sheetMutate(layout=>{ensureSheetSection(layout,tab,section);const target=layout.tabs.find(t=>t.id===tab.id).sections.find(s=>s.id===section.id);target.collapsed=!target.collapsed;return layout;});
 if(index===4){const options=sheetLayout.tabs.map(t=>`${t.id}: ${t.name}`).join('\n');const destination=prompt(`Перенести в какую вкладку?\n${options}`,tab.id);if(destination&&destination!==tab.id)sheetMutate(layout=>{ensureSheetSection(layout,tab,section);const from=layout.tabs.find(t=>t.id===tab.id),to=layout.tabs.find(t=>t.id===destination);if(!to)return layout;const pos=from.sections.findIndex(s=>s.id===section.id);if(pos>=0)to.sections.push(...from.sections.splice(pos,1));return layout;});}
 if(index===5)sheetMutate(layout=>{ensureSheetSection(layout,tab,section);const from=layout.tabs.find(t=>t.id===tab.id),old=from.sections.find(s=>s.id===section.id);if(!old)return layout;from.sections=from.sections.filter(s=>s!==old);const target=layout.tabs.find(t=>t.id==='stats'||t.id==='actions');for(const key of old.items){const destination=key.startsWith('action:')?'actions':'stats';const t=layout.tabs.find(t=>t.id===destination);let section=t.sections.find(s=>s.id===`auto:${destination}:${encodeURIComponent(destination==='stats'?'Характеристики':'Прочие действия')}`);if(!section){section={id:`auto:${destination}:${encodeURIComponent(destination==='stats'?'Характеристики':'Прочие действия')}`,name:destination==='stats'?'Характеристики':'Прочие действия',view:'grid',collapsed:false,items:[]};t.sections.push(section);}section.items.push(key);}return layout;});
}
function sheetEntryMenu(key,entries){
 const options=buildSheet(sheetLayout,entries).tabs.flatMap(tab=>tab.sections.map(section=>({tab,section})));
 const names=options.map((option,index)=>`${index+1}. ${option.tab.name} / ${option.section.name}`).join('\n');
 const answer=prompt(`Элемент ${key}\n0. Скрыть\n${names}`);if(answer===null)return;
 if(answer==='0')sheetMutate(layout=>{for(const tab of layout.tabs)for(const section of tab.sections)section.items=section.items.filter(item=>item!==key);layout.hidden.push(key);return layout;});
 else{const option=options[Number(answer)-1];if(option)sheetMutate(layout=>{const tab=layout.tabs.find(t=>t.id===option.tab.id);if(!tab.sections.some(s=>s.id===option.section.id))tab.sections.push({...option.section,items:[]});return moveEntry(layout,key,option.tab.id,option.section.id);});}
}
function clearSheetDrag(){
 sheetDropMarker?.remove();sheetDropMarker=null;
 document.querySelectorAll('.sheet-entry-moving,.sheet-drop').forEach(node=>node.classList.remove('sheet-entry-moving','sheet-drop'));
 sheetDrag=null;
}
function previewSheetDrop(event,listing){
 if(sheetDrag?.kind!=='item')return;
 event.preventDefault();event.stopPropagation();event.dataTransfer.dropEffect='move';
 if(!sheetDropMarker){sheetDropMarker=document.createElement('div');sheetDropMarker.className='sheet-drop-placeholder';sheetDropMarker.textContent='Переместить сюда';}
 const grid=listing.classList.contains('sheet-grid');let before=null;
 for(const node of listing.querySelectorAll(':scope > .sheet-entry')){
  const box=node.getBoundingClientRect();
  if(event.clientY<box.top+box.height/2 || grid&&event.clientY<box.bottom&&event.clientX<box.left+box.width/2){before=node;break;}
 }
 if(sheetDropMarker.parentElement!==listing||sheetDropMarker.nextElementSibling!==before){sheetDropMarker.parentElement?.classList.remove('sheet-drop');listing.insertBefore(sheetDropMarker,before);}
 listing.classList.add('sheet-drop');
}
function finishSheetDrop(tab,section){
 if(sheetDrag?.kind!=='item')return;
 const key=sheetDrag.key;
 let next=sheetDropMarker?.nextElementSibling;
 while(next?.dataset?.sheetItem===key)next=next.nextElementSibling;
 const before=next?.dataset?.sheetItem||'';
 clearSheetDrag();
 sheetMutate(layout=>{ensureSheetSection(layout,tab,section);return moveEntry(layout,key,tab.id,section.id,before);});
}
function renderSheetLayout(snapshot){
 const scope=sheetSavedScope();if(scope!==sheetScope){sheetScope=scope;sheetLayout=loadLayout(scope);characterActiveTab='stats';}
 const entries=sheetEntryList(snapshot);
 const full=buildSheet(sheetLayout,entries);
 if(characterEditSession){for(const {key,tab} of characterEditSession.places){for(const t of full.tabs)for(const section of t.sections)section.items=section.items.filter(x=>x!==key);let target=full.tabs.find(t=>t.id===tab);if(!target)continue;let group=target.sections.find(x=>x.id===`user:added:${tab}`);if(!group){group={id:`user:added:${tab}`,name:'Добавлено',view:'grid',collapsed:false,items:[]};target.sections.push(group);}group.items.push(key);}}
 const tabs=$('characterLayoutTabs'),body=$('characterLayoutContent');
 const elements=new Map([...document.querySelectorAll('#characterLayoutContent [data-sheet-item],#characterStats [data-sheet-item],#characterResources [data-sheet-item],#characterActions [data-sheet-item]')].map(node=>[node.dataset.sheetItem,node]));
 tabs.replaceChildren();body.replaceChildren();
 if(!full.tabs.some(tab=>tab.id===characterActiveTab))characterActiveTab='stats';
 for(const tab of full.tabs){
  const button=document.createElement('button');button.type='button';button.textContent=tab.name;button.setAttribute('role','tab');button.setAttribute('aria-selected',String(characterActiveTab===tab.id));button.onclick=()=>{characterActiveTab=tab.id;renderCharacterPanel();};
  if(sheetEdit){button.draggable=true;button.ondragend=clearSheetDrag;button.ondragstart=event=>{clearSheetDrag();sheetDrag={kind:'tab',id:tab.id};event.dataTransfer.setData('text/plain',tab.id);};button.ondragover=event=>event.preventDefault();button.ondrop=event=>{event.preventDefault();sheetDropMarker?.remove();sheetDropMarker=null;if(sheetDrag?.kind==='item')sheetMutate(layout=>{const t=layout.tabs.find(t=>t.id===tab.id);let bucket=t.sections.find(s=>s.id===`user:drop:${tab.id}`);if(!bucket){bucket={id:`user:drop:${tab.id}`,name:'Перемещённые',view:'list',collapsed:false,items:[]};t.sections.push(bucket);}characterActiveTab=tab.id;return moveEntry(layout,sheetDrag.key,tab.id,bucket.id);});else if(sheetDrag?.kind==='section')sheetMutate(layout=>{const from=layout.tabs.find(t=>t.id===sheetDrag.tab),to=layout.tabs.find(t=>t.id===tab.id);const section=from?.sections.find(s=>s.id===sheetDrag.id);if(section&&to&&from!==to){from.sections=from.sections.filter(s=>s!==section);to.sections.push(section);}characterActiveTab=tab.id;return layout;});else if(sheetDrag?.kind==='tab')sheetMutate(layout=>{const before=layout.tabs.findIndex(t=>t.id===tab.id),from=layout.tabs.findIndex(t=>t.id===sheetDrag.id);if(from>=0&&before>=0)layout.tabs.splice(before,0,...layout.tabs.splice(from,1));return layout;});};button.oncontextmenu=event=>{event.preventDefault();if(['stats','actions'].includes(tab.id))return;const answer=prompt('1. Переименовать\n2. Удалить вкладку');if(answer==='1'){const name=prompt('Новое название',tab.name);if(name?.trim())sheetMutate(layout=>{layout.tabs.find(t=>t.id===tab.id).name=name.trim().slice(0,120);return layout;});}if(answer==='2')sheetMutate(layout=>{const removed=layout.tabs.find(t=>t.id===tab.id);layout.tabs=layout.tabs.filter(t=>t!==removed);for(const section of removed.sections)for(const key of section.items){const target=key.startsWith('action:')?'actions':'stats';let bucket=layout.tabs.find(t=>t.id===target).sections.find(s=>s.id===`auto:${target}:${encodeURIComponent('Восстановлено')}`);if(!bucket){bucket={id:`auto:${target}:${encodeURIComponent('Восстановлено')}`,name:'Восстановлено',view:'list',collapsed:false,items:[]};layout.tabs.find(t=>t.id===target).sections.push(bucket);}bucket.items.push(key);}return layout;});};}
  tabs.append(button);
 }
 if(sheetEdit){const add=document.createElement('button');add.type='button';add.textContent='+';add.title='Новая вкладка';add.onclick=()=>{const name=prompt('Название новой вкладки');if(name?.trim())sheetMutate(layout=>{const id=`user:${Date.now().toString(36)}${Math.random().toString(36).slice(2,7)}`;layout.tabs.push({id,name:name.trim().slice(0,120),sections:[]});characterActiveTab=id;return layout;});};tabs.append(add);}
 const selected=full.tabs.find(t=>t.id===characterActiveTab);
 if(!selected)return;
 if(sheetEdit){const toolbar=document.createElement('div');toolbar.className='sheet-edit-toolbar';const add=document.createElement('button');add.type='button';add.textContent='+ Раздел';add.onclick=()=>{const name=prompt('Название раздела');if(name?.trim())sheetMutate(layout=>{layout.tabs.find(t=>t.id===selected.id).sections.push({id:`user:${Date.now().toString(36)}${Math.random().toString(36).slice(2,7)}`,name:name.trim().slice(0,120),view:'list',collapsed:false,items:[]});return layout;});};toolbar.append(add);const hidden=entries.filter(entry=>sheetLayout.hidden.includes(entry.key));if(hidden.length){const restore=document.createElement('button');restore.type='button';restore.textContent=`Скрытые (${hidden.length})`;restore.onclick=()=>{const answer=prompt(hidden.map((entry,i)=>`${i+1}. ${entry.name}`).join('\n'));const entry=hidden[Number(answer)-1];if(entry)sheetMutate(layout=>{layout.hidden=layout.hidden.filter(item=>item!==entry.key);return layout;});};toolbar.append(restore);}body.append(toolbar);}
 for(const section of selected.sections){
  const wrapper=document.createElement('section');wrapper.className='character-section sheet-section';wrapper.dataset.section=section.id;
  const heading=document.createElement('div');heading.className='sheet-heading';if(sheetEdit){heading.draggable=true;heading.ondragend=clearSheetDrag;heading.ondragstart=event=>{clearSheetDrag();sheetDrag={kind:'section',id:section.id,tab:selected.id};event.dataTransfer.setData('text/plain',section.id);};heading.ondragover=event=>event.preventDefault();heading.ondrop=event=>{event.preventDefault();if(sheetDrag?.kind==='section')sheetMutate(layout=>{const from=layout.tabs.find(t=>t.id===sheetDrag.tab),to=layout.tabs.find(t=>t.id===selected.id);let moving=from?.sections.find(s=>s.id===sheetDrag.id);if(!moving){moving=buildSheet(layout,entries).tabs.find(t=>t.id===sheetDrag.tab)?.sections.find(s=>s.id===sheetDrag.id);if(moving)moving={...moving,items:[...moving.items]};}if(!moving||!to)return layout;for(const t of layout.tabs)t.sections=t.sections.filter(s=>s.id!==moving.id);const index=to.sections.findIndex(s=>s.id===section.id);to.sections.splice(index<0?to.sections.length:index,0,moving);return layout;});sheetDrag=null;};}const collapse=document.createElement('button');collapse.type='button';collapse.className='subtle';collapse.textContent=`${section.collapsed?'▸':'▾'} ${section.name}`;collapse.onclick=()=>sheetMutate(layout=>{const target=layout.tabs.find(t=>t.id===selected.id)?.sections.find(s=>s.id===section.id);if(!target){const targetSection={...section,collapsed:!section.collapsed};layout.tabs.find(t=>t.id===selected.id).sections.push(targetSection);}else target.collapsed=!target.collapsed;return layout;});heading.append(collapse);
  if(sheetEdit){const more=document.createElement('button');more.type='button';more.textContent='⋯';more.className='subtle';more.onclick=()=>sheetSectionMenu(selected,section,entries);heading.append(more);heading.oncontextmenu=event=>{event.preventDefault();sheetSectionMenu(selected,section,entries);};}
  wrapper.append(heading);const listing=document.createElement('div');listing.className=section.view==='grid'?'sheet-grid':'sheet-list';listing.hidden=section.collapsed;if(sheetEdit)listing.style.minHeight='28px';
  if(sheetEdit){
   listing.ondragover=event=>previewSheetDrop(event,listing);
   listing.ondrop=event=>{if(sheetDrag?.kind!=='item')return;event.preventDefault();event.stopPropagation();finishSheetDrop(selected,section);};
  }
  for(const key of section.items){const item=elements.get(key);if(!item)continue;const frame=document.createElement('div');frame.className='sheet-entry';frame.dataset.sheetItem=key;
   if(sheetEdit){const drag=document.createElement('button');drag.type='button';drag.className='subtle sheet-handle';drag.innerHTML='<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 2v20m0-20-4 4m4-4 4 4m-4 16-4-4m4 4 4-4M2 12h20m-20 0 4-4m-4 4 4 4m16-4-4-4m4 4-4 4"/></svg>';drag.setAttribute('aria-label','Переместить карточку');drag.draggable=true;drag.title='Перетащить';drag.ondragstart=event=>{clearSheetDrag();sheetDrag={kind:'item',key};event.dataTransfer.effectAllowed='move';event.dataTransfer.setData('text/plain',key);frame.classList.add('sheet-entry-moving');};drag.ondragend=clearSheetDrag;const hide=document.createElement('button');hide.type='button';hide.className='subtle sheet-entry-hide';hide.innerHTML='<svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 3 21 21M10.6 6.2A10.5 10.5 0 0 1 12 6c5 0 9 6 9 6a15 15 0 0 1-3 3.4M6.4 6.4C3.8 8 3 12 3 12s3 6 9 6a10 10 0 0 0 3.6-.6M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>';hide.title='Скрыть элемент';hide.setAttribute('aria-label','Скрыть элемент');hide.onclick=()=>sheetMutate(layout=>{for(const tab of layout.tabs)for(const section of tab.sections)section.items=section.items.filter(item=>item!==key);layout.hidden.push(key);return layout;});const controls=document.createElement('div');controls.className='sheet-entry-controls';controls.append(drag,hide);frame.append(item,controls);frame.ondragover=event=>previewSheetDrop(event,listing);frame.ondrop=event=>{if(sheetDrag?.kind!=='item')return;event.preventDefault();event.stopPropagation();finishSheetDrop(selected,section);};}
   frame.append(item);listing.append(frame);
  }
  wrapper.append(listing);body.append(wrapper);
 }
}
function updateCharacterPanelMode(){
 $('characterStatsTab').hidden=true;$('characterActionsTab').hidden=true;
 $('characterSettingsPanel').hidden=!characterSettingsOpen;
 $('characterSettingsToggle').setAttribute('aria-expanded',String(characterSettingsOpen));
 $('characterMechanicsToggle').setAttribute('aria-expanded',String(!!characterEditSession));
 $('characterMechanicsToggle').classList.toggle('active',!!characterEditSession);
 $('characterLayoutToggle').classList.toggle('active',sheetEdit);
}
$('characterLayoutToggle').onclick=()=>{if(characterEditSession)return toast('Завершите редактирование персонажа');sheetEdit=!sheetEdit;if(characterWatch.snapshot)renderCharacterPanel();};
$('characterSettingsToggle').onclick=()=>{if(characterEditSession)return toast('Сначала сохраните или отмените редактирование');characterSettingsOpen=!characterSettingsOpen;if(!characterSettingsOpen)characterMechanicsOpen=false;updateCharacterPanelMode();};
$('characterMechanicsToggle').onclick=()=>characterEditSession?confirmLeaveEdit():openCharacterEdit();
function renderCharacterPanel(){
 const token=navigationToken(selected),snapshot=characterWatch.snapshot,linkedID=token?.characterInstanceId||'',gm=isEditorView(),ready=!!linkedID&&snapshot?.characterId===linkedID;
 if(!gm)characterSettingsOpen=false;
 const loading=!!linkedID&&!ready&&(characterWatchReason==='pending'||characterWatchReason==='replaced'),retainDisplayed=!!token&&loading&&!!characterDisplayedID&&!$('characterSummary').hidden,deferInitial=!!token&&loading&&!characterDisplayedID;
 characterPanel.hidden=!token||deferInitial;
 characterPanel.inert=retainDisplayed;if(retainDisplayed)characterPanel.setAttribute('aria-busy','true');else characterPanel.removeAttribute('aria-busy');
 if(!token){if(characterEditSession)releaseEditSession();sheetEdit=false;characterSettingsOpen=false;characterMechanicsOpen=false;updateEditButtons();$('characterSettingsPanel').hidden=true;characterRecoveryToggle.hidden=true;return;}
 if(retainDisplayed||deferInitial)return;
 if(characterDisplayedID!==linkedID){if(characterEditSession)releaseEditSession();characterDisplayedID=linkedID;characterSettingsOpen=false;characterMechanicsOpen=false;characterActiveTab='stats';sheetEdit=false;$('characterMechanicsSection').open=false;}
 characterPanel.dataset.characterId=linkedID;
 $('characterSettingsToggle').hidden=!gm;

 const instance=ready?snapshot.instance:null;
 const unavailable=!!linkedID&&!ready&&['accessRevoked','unavailable','closed'].includes(characterWatchReason);$('characterUnavailable').hidden=!unavailable;$('characterPending').hidden=ready||unavailable;$('characterSummary').hidden=!ready;characterRecoveryToggle.hidden=!unavailable||!gm;$('characterIdentitySection').hidden=true;$('characterLinkSection').hidden=!gm;$('characterTechnicalSection').hidden=!gm||!ready;$('characterMechanicsSection').hidden=true;if(!ready)$('characterSettingsPanel').hidden=!gm||!characterSettingsOpen;
 if(ready){
  const canManage=!!snapshot.permissions?.manage&&gm;
  $('editCharacterPreset').hidden=!canManage||!instance.presetId;
  $('characterMechanicsToggle').hidden=!canManage;
  $('characterMechanicsSection').hidden=true;
  $('characterLinkSection').hidden=!gm;
  $('characterTechnicalSection').hidden=!gm;
  $('characterName').textContent=characterDisplayTitle(snapshot.effective?.name||instance.name||'Без имени',snapshot.characterId);
  $('characterSubtitle').textContent=snapshot.preset?.name||'';
  $('characterRenameRow').hidden=!snapshot.permissions?.edit;
  if(document.activeElement!==$('characterRename'))$('characterRename').value=characterEditSession?.name??instance.name??'';
  $('characterTechnicalID').textContent=snapshot.characterId;
  $('characterTechnicalPreset').textContent=instance.presetId?`${snapshot.preset?.name||instance.presetId} (${instance.presetId})`:'Без пресета';
  $('characterTechnicalMechanics').textContent=instance.materialized?'Самостоятельная (materialized)':instance.presetId?'Следует шаблону':'Самостоятельная';
  $('characterTechnicalArtwork').textContent=snapshot.effective?.artwork?.source==='character'?'Собственный artwork':snapshot.effective?.artwork?.status==='missing'?'Artwork отсутствует':snapshot.effective?.artwork?.source==='preset'?'Artwork шаблона':'Placeholder';
  const view=editSnapshot(snapshot);renderCharacterStats(view);renderCharacterResources(view);renderCharacterActions(view);renderSheetLayout(view);updateEditButtons();
  $('characterAvatarControls').hidden=!characterEditSession;
  $('characterAvatarReset').disabled=!characterEditSession||(!Object.hasOwn(instance,'avatarAssetId')&&!instance.artworkPath&&!characterEditSession?.avatarFile&&!characterEditSession?.selectedMediaAsset);
  showCharacterAvatar(characterEditSession?.resetAvatar||characterEditSession?.placeholderAvatar?'':snapshot.effective?.hasAvatar?snapshot.effective.avatarAssetId:'',snapshot.characterId);if(characterEditSession?.avatarPreview){$('characterAvatar').src=characterEditSession.avatarPreview;$('characterAvatar').hidden=false;$('characterAvatarEmpty').hidden=true;}
  if(!canManage){characterMechanicsOpen=false;}
  updateCharacterPanelMode();
 }else{showCharacterAvatar('','');}
 fillCatalogSelect($('characterPreset'),characterCatalog.presets,characterCatalog.loading.has('presets')?'Загрузка…':'Выберите пресет');
 fillCatalogSelect($('characterRoster'),characterCatalog.roster,characterCatalog.loading.has('roster')?'Загрузка…':'Выберите персонажа');
 $('characterCreatePreset').disabled=!gm||!$('characterPreset').value;$('characterLink').disabled=!gm||!$('characterRoster').value;
 $('characterPresetMore').hidden=!gm||!characterCatalog.next.presets;$('characterPresetMore').disabled=characterCatalog.loading.has('presets');
 $('characterRosterMore').hidden=!gm||!characterCatalog.next.roster;$('characterRosterMore').disabled=characterCatalog.loading.has('roster');
 $('characterPersistentToggle').hidden=!gm||!ready;$('characterPersistentToggle').classList.toggle('active',!!instance?.persistent);$('characterPersistentToggle').textContent=instance?.persistent?'✓ Сохранён в кампании':'＋ Сохранить в кампании';
}
function hasTransitionSelection(){return isEditorView()&&(!!transitionDraft||!!state?.transitions?.[selectedTransition]);}
function updateInspectorState(){renderCharacterPanel();const tokenVisible=!!navigationToken(selected)&&!characterPanel.hidden,heading=document.querySelector('.inspector .panel-heading');heading.hidden=tokenVisible;heading.textContent='СВОЙСТВА ОБЪЕКТА';const visible=tokenVisible||!!state?.elements?.[selectedElement]||isEditorView()&&!!state?.elementCatalog?.[selectedElement]||hasTransitionSelection();$('noSelection').hidden=visible;document.querySelector('.inspector').classList.toggle('has-selection',visible);inspectorFrame.classList.toggle('has-selection',visible);}
function elementRotationAllowed(element){if(!element)return false;const lowest=orderedLayers(state,element.floorId).find(layer=>layer.kind==='visual');return lowest?.id!==element.layerId||rotationOverrides.has(element.id);}
function cancelTransitionDraft(){transitionDraft=null;transitionCursor=null;if(drag?.type?.startsWith('transition'))drag=null;fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition,isEditorView());dirty=true;}
function beginTransition(){if(!isEditorView())return;select(null);selectElement(null);selectedTransition='';transitionDraft={phase:'placingA',endpointA:null,endpointB:null,direction:'bidirectional',showToPlayers:false};transitionCursor=null;fillTransitionProperties();sceneTree.update(state,selectedElement,selectedTransition,true);canvas.focus();toast('Поставьте точку A перехода');dirty=true;}
function setWalkableTool(tool){if(!isEditorView())return;walkableTool=tool;selectedWalkableComponent='';renderBoundsTool='';renderBoundsDraft=null;selectedRenderBoundsVertex=-1;if(drag?.type?.startsWith('walkable')||drag?.type?.startsWith('renderBounds'))drag=null;select(null);selectElement(null);selectedTransition='';transitionDraft=null;sceneTree.update(state,selectedElement,selectedTransition,true);canvas.focus();dirty=true;}
function setRenderBoundsTool(tool){if(!isEditorView())return;renderBoundsTool=tool;renderBoundsDraft=null;selectedRenderBoundsVertex=-1;walkableTool='';selectedWalkableComponent='';if(drag?.type?.startsWith('walkable')||drag?.type?.startsWith('renderBounds'))drag=null;select(null);selectElement(null);selectedTransition='';transitionDraft=null;sceneTree.update(state,selectedElement,selectedTransition,true);canvas.focus();dirty=true;}
function renderBoundsPolygon(points){return {outer:points.map(point=>({x:point.x,y:point.y})),holes:[]};}
function cloneRenderBounds(polygon){return renderBoundsPolygon(polygon?.outer||[]);}
function finishRenderBoundsDraft(){if(!renderBoundsDraft)return;const polygon=renderBoundsPolygon(renderBoundsDraft.points);if(!validRenderBounds(polygon)){renderBoundsDraft.invalid=true;toast('Граница должна быть простым полигоном ненулевой площади');dirty=true;return;}if(queueCommand('setRenderBounds',{floorId:renderBoundsDraft.floorId,expectedGeometryRevision:renderBoundsDraft.geometryRevision,renderBounds:polygon}))renderBoundsDraft=null;dirty=true;}
function deleteSelectedRenderBoundsVertex(){const floor=state?.floors?.[currentFloorID],polygon=floor?.renderBounds;if(renderBoundsTool!=='edit'||!polygon||selectedRenderBoundsVertex<0)return false;if(polygon.outer.length<=3){toast('У границы должно остаться не меньше трёх вершин');return true;}const next=cloneRenderBounds(polygon),changedEdge=selectedRenderBoundsVertex-1;next.outer.splice(selectedRenderBoundsVertex,1);if(!validRenderBoundsChange(next,[changedEdge])){toast('Удаление создаёт недопустимую границу');return true;}if(queueCommand('setRenderBounds',{floorId:floor.id,expectedGeometryRevision:floor.geometryRevision,renderBounds:next}))selectedRenderBoundsVertex=-1;dirty=true;return true;}
function syncCharacterWatch(){const token=navigationToken(selected);if(token?.characterInstanceId)characterWatch.watch(token.characterInstanceId);else characterWatch.unwatch();}
function select(id){if(id!==selected&&characterEditSession&&!confirmLeaveEdit())return;const previous=selected;selected=id;if(id){selectedElement=null;selectedTransition='';transitionDraft=null;}if(previous&&tokenRows.has(previous))tokenRows.get(previous).classList.remove('selected');if(id&&tokenRows.has(id))tokenRows.get(id).classList.add('selected');syncCharacterWatch();fillProperties();fillElementProperties();fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition,isEditorView());dirty=true;}
function selectElement(id){if(characterEditSession&&!confirmLeaveEdit())return;if(id&&!isEditorView())return;if(id){select(null);selectedTransition='';transitionDraft=null;}selectedElement=id;fillProperties();fillElementProperties();fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition,isEditorView());dirty=true;}
function selectTransition(id){if(id&&!isEditorView())return;select(null);selectedElement=null;transitionDraft=null;selectedTransition=id||'';fillProperties();fillElementProperties();fillTransitionProperties();if(state)sceneTree.update(state,selectedElement,selectedTransition,isEditorView());dirty=true;}
function setFloor(id,preserveTransition=false){if(!state?.floors?.[id]||!isEditorView()||id===currentFloorID)return;if(!preserveTransition&&!transitionDraft){select(null);selectElement(null);selectedTransition='';}renderBoundsDraft=null;selectedRenderBoundsVertex=-1;if(drag?.type?.startsWith('renderBounds'))drag=null;const region=boundedRegion(currentViewRegion());if(send({type:'view',sceneId:state.scene.id,viewFloorId:id,region}))regionInFlight=true;}
function focusElement(element){if(!element||!state?.elementCatalog?.[element.id])return;selectElement(element.id);const t=element.transform,angle=t.rotation*Math.PI/180,width=Math.abs(t.width*Math.cos(angle))+Math.abs(t.height*Math.sin(angle)),height=Math.abs(t.width*Math.sin(angle))+Math.abs(t.height*Math.cos(angle)),fitScale=Math.max(.015,Math.min(4,(viewport.w-100)/Math.max(1,width),(viewport.h-100)/Math.max(1,height)));camera.scale=Math.min(camera.scale,fitScale);camera.x=t.x+t.width/2-viewport.w/(2*camera.scale);camera.y=t.y+t.height/2-viewport.h/(2*camera.scale);scheduleViewportSave();if(element.floorId!==currentFloorID)setFloor(element.floorId,true);else ensureSceneRegion(currentViewRegion());dirty=true;}
function fillProperties(){
 const source=navigationToken(selected),t=source?{...source,...drafts.get(selected)}:null,gm=isEditorView();$('properties').hidden=!t||!gm;updateInspectorState();if(!t||!gm)return;
 $('tokenSize').value=t.size;$('tokenOpacity').value=t.opacity??1;$('tokenHidden').checked=t.hidden;
 const ownerList=$('tokenOwners'),owners=new Set(t.ownerIds||[]);ownerList.replaceChildren();
 const players=Object.values(state.members).filter(member=>!isGM(member));
 if(!players.length){const empty=document.createElement('p');empty.className='muted';empty.textContent='В сцене пока нет игроков';ownerList.append(empty);}
 for(const member of players){const label=document.createElement('label');label.className='token-owner-option';const input=document.createElement('input');input.type='checkbox';input.checked=owners.has(member.id);input.onchange=()=>{const next=new Set(navigationToken(selected)?.ownerIds||[]);if(input.checked)next.add(member.id);else next.delete(member.id);drafts.set(selected,'ownerIds',[...next]);commitTokenProperties();};const text=document.createElement('span');text.textContent=member.name;label.append(input,text);ownerList.append(label);}
 for(const el of $('properties').querySelectorAll('input'))el.disabled=false;
}
function fillElementProperties(){const loaded=state?.elements?.[selectedElement],source=isEditorView()?loaded||state?.elementCatalog?.[selectedElement]:null;$('elementProperties').hidden=!source;updateInspectorState();if(!source)return;const draft=elementDrafts.get(selectedElement),element={...source,...draft},t={...source.transform,...draft};$('elementName').value=element.name;$('elementX').value=t.x;$('elementY').value=t.y;$('elementWidth').value=t.width;$('elementHeight').value=t.height;$('elementRotation').value=t.rotation;$('elementOpacity').value=element.opacity;$('elementVisible').checked=element.visible;$('elementLocked').checked=element.locked;$('elementLayer').replaceChildren();for(const layer of orderedLayers(state,element.floorId).filter(layer=>layer.kind==='visual')){const option=document.createElement('option');option.value=layer.id;option.textContent=layer.name;$('elementLayer').append(option);}$('elementLayer').value=element.layerId;elementOffscreenNote.hidden=!!loaded;for(const id of ['elementX','elementY','elementWidth','elementHeight','elementRotation'])$(id).disabled=!loaded;for(const id of ['elementName','elementOpacity','elementLayer','elementVisible','elementLocked','deleteElement'])$(id).disabled=false;$('duplicateElement').disabled=!loaded;const rotationAllowed=loaded&&elementRotationAllowed(loaded);$('allowElementRotation').hidden=!!loaded&&rotationAllowed;$('allowElementRotation').disabled=!loaded;}
function transitionForInspector(){return transitionDraft||state?.transitions?.[selectedTransition]||null;}
function fillTransitionProperties(){const transition=isEditorView()?transitionForInspector():null;$('transitionProperties').hidden=!transition;updateInspectorState();if(!transition)return;const draft=!!transitionDraft,waiting=draft&&transition.phase==='awaitingCreate',a=transition.endpointA,b=transition.endpointB;$('transitionDirection').value=transition.direction||'bidirectional';$('transitionShowPlayers').checked=!!transition.showToPlayers;$('transitionShowPlayers').disabled=draft;for(const [select,endpoint]of [[$('transitionFloorA'),a],[$('transitionFloorB'),b]]){select.replaceChildren();for(const floor of orderedFloors(state)){const option=document.createElement('option');option.value=floor.id;option.textContent=floor.name;select.append(option);}select.value=endpoint?.floorId||currentFloorID;select.disabled=!endpoint||waiting;}$('transitionRadiusA').value=a?.radius??'';$('transitionRadiusB').value=b?.radius??'';$('transitionRadiusA').disabled=!a||waiting;$('transitionRadiusB').disabled=!b||waiting;$('transitionGoA').disabled=!a;$('transitionGoB').disabled=!b;$('transitionDirection').disabled=draft;$('transitionConfirm').hidden=!(draft&&(transition.phase==='editingA'||transition.phase==='editingB'));$('transitionCancel').hidden=!draft||waiting;$('transitionSave').hidden=draft;$('deleteTransition').hidden=draft;}
function currentTransitionEndpoint(key){return transitionForInspector()?.[key]||null;}
function cloneTransitionEndpoint(endpoint){return endpoint?{...endpoint,position:{...endpoint.position}}:endpoint;}
function transitionPosition(_floorId,position){return {...position};}
function navigateTransitionEndpoint(key){const endpoint=currentTransitionEndpoint(key);if(!endpoint)return;camera.x=endpoint.position.x-viewport.w/(2*camera.scale);camera.y=endpoint.position.y-viewport.h/(2*camera.scale);scheduleViewportSave();if(endpoint.floorId!==currentFloorID)setFloor(endpoint.floorId,true);dirty=true;}
function sameEndpoint(a,b){return a&&b&&a.floorId===b.floorId&&a.radius===b.radius&&a.position.x===b.position.x&&a.position.y===b.position.y;}
function resolveCreatedTransition(){if(!transitionDraft||transitionDraft.phase!=='awaitingCreate')return;const prior=new Set(transitionDraft.priorIds||[]),found=Object.values(state?.transitions||{}).find(item=>!prior.has(item.id)&&sameEndpoint(item.endpointA,transitionDraft.endpointA)&&sameEndpoint(item.endpointB,transitionDraft.endpointB));if(found){selectedTransition=found.id;transitionDraft=null;transitionCursor=null;}}
function confirmTransitionEndpoint(){if(!transitionDraft)return;if(transitionDraft.phase==='editingA'){transitionDraft.phase='placingB';transitionCursor={x:transitionDraft.endpointA.position.x,y:transitionDraft.endpointA.position.y};toast('Поставьте точку B перехода');}else if(transitionDraft.phase==='editingB'){const payload={name:'Переход',endpointA:cloneTransitionEndpoint(transitionDraft.endpointA),endpointB:cloneTransitionEndpoint(transitionDraft.endpointB),direction:'bidirectional',showToPlayers:false};if(queueCommand('transitionCreate',{transition:payload}))transitionDraft={...transitionDraft,phase:'awaitingCreate',priorIds:Object.keys(state?.transitions||{})};}fillTransitionProperties();dirty=true;}
$('transitionConfirm').onclick=confirmTransitionEndpoint;
$('transitionCancel').onclick=cancelTransitionDraft;
$('transitionGoA').onclick=()=>navigateTransitionEndpoint('endpointA');
$('transitionGoB').onclick=()=>navigateTransitionEndpoint('endpointB');
$('transitionFloorA').onchange=()=>{if(transitionDraft?.endpointA){const floorId=$('transitionFloorA').value;transitionDraft.endpointA={...transitionDraft.endpointA,floorId,position:transitionPosition(floorId,transitionDraft.endpointA.position)};dirty=true;}};
$('transitionFloorB').onchange=()=>{if(transitionDraft?.endpointB){const floorId=$('transitionFloorB').value;transitionDraft.endpointB={...transitionDraft.endpointB,floorId,position:transitionPosition(floorId,transitionDraft.endpointB.position)};dirty=true;}};
$('transitionRadiusA').oninput=()=>{if(transitionDraft?.endpointA){const radius=Math.max(8,Number($('transitionRadiusA').value)||8);transitionDraft.endpointA={...transitionDraft.endpointA,radius};dirty=true;}};
$('transitionRadiusB').oninput=()=>{if(transitionDraft?.endpointB){const radius=Math.max(8,Number($('transitionRadiusB').value)||8);transitionDraft.endpointB={...transitionDraft.endpointB,radius};dirty=true;}};
$('transitionProperties').onsubmit=e=>{e.preventDefault();const transition=state?.transitions?.[selectedTransition];if(!transition)return;const floorA=$('transitionFloorA').value,floorB=$('transitionFloorB').value,next={...transition,showToPlayers:$('transitionShowPlayers').checked,direction:$('transitionDirection').value,endpointA:{...transition.endpointA,floorId:floorA,position:transitionPosition(floorA,transition.endpointA.position),radius:Math.max(8,Number($('transitionRadiusA').value)||8)},endpointB:{...transition.endpointB,floorId:floorB,position:transitionPosition(floorB,transition.endpointB.position),radius:Math.max(8,Number($('transitionRadiusB').value)||8)}};queueCommand('transitionUpdate',{transition:next});};
$('deleteTransition').onclick=()=>{const transition=state?.transitions?.[selectedTransition];if(transition&&confirm('Удалить переход?')){queueCommand('transitionDelete',{transition:{id:transition.id}});selectTransition(null);}};
$('properties').onsubmit=e=>{e.preventDefault();commitTokenProperties();};
$('delete').onclick=()=>{if(selected)queueCommand('delete',{token:{id:selected}});select(null);};
$('characterPreset').onchange=renderCharacterPanel;
$('characterRoster').onchange=renderCharacterPanel;
$('characterPresetMore').onclick=()=>characterCatalog.more('presets').catch(error=>toast(error.message));
$('characterCreatePreset').onclick=()=>{const token=state?.tokens?.[selected],presetId=$('characterPreset').value;if(token&&presetId&&isEditorView())queueCommand('characterCreate',{sceneId:state.scene.id,tokenId:token.id,presetId});};
$('characterLink').onclick=()=>{const token=state?.tokens?.[selected],characterId=$('characterRoster').value;if(token&&characterId&&isEditorView())queueCommand('characterLink',{sceneId:state.scene.id,tokenId:token.id,characterId});};
$('characterPersistentToggle').onclick=()=>{const token=navigationToken(selected),instance=characterWatch.snapshot?.instance;if(token?.characterInstanceId&&instance&&isEditorView())queueCommand('characterSetPersistent',{characterId:token.characterInstanceId,persistent:!instance.persistent});};
$('characterRosterMore').onclick=()=>characterCatalog.more('roster').catch(error=>toast(error.message));
$('characterActionAdd').onchange=()=>{$('characterActionAddButton').disabled=!$('characterActionAdd').value;};
$('characterActionAddButton').onclick=()=>assignActionToEdit($('characterActionAdd').value);
$('characterActionMore').onclick=()=>characterCatalog.more('actions').catch(error=>toast(error.message));
function removeCharacterParameter(snapshot,id,name){
 if(!snapshot?.permissions?.manage||!isEditorView()||!confirm(`Убрать «${name}» только у этого персонажа? Значение будет удалено.`)||!confirmMechanicalEdit(snapshot))return;
 const linkedActions=Object.values(snapshot.actions||{}).filter(action=>(action.rolls||[]).some(roll=>roll.modifierStat===id)).map(action=>action.name||action.id);if(linkedActions.length&&!confirm(`Этот параметр используется в бросках действий: ${linkedActions.join(', ')}. После удаления модификатор станет недоступен. Продолжить?`))return;
 const key=`${snapshot.characterId}/${id}`;clearTimeout(characterStatTimers.get(key));characterStatTimers.delete(key);
 if(queueCommand('characterParameterRemove',{characterId:snapshot.characterId,statId:id}))characterDrafts.confirm(snapshot.characterId,{[id]:characterDrafts.get(snapshot.characterId)[id]});
}
function updateCharacterParameterOptions(snapshot=characterWatch.snapshot){
 if(!snapshot)return;
 const selection=$('characterParameterSelect'),previous=selection.value,kind=$('characterParameterKind').value,query=$('characterParameterSearch').value.trim().toLocaleLowerCase('ru');
 const existing=new Set([...Object.keys(snapshot.effective?.stats||{}),...Object.keys(snapshot.effective?.resources||{})]);
 const definitions={...(resourceDefinitionRegistry||{}),...(snapshot.statDefinitions||{})};
 const available=Object.values(definitions).filter(d=>d.type!=='number'&&!existing.has(d.id)&&(kind==='all'||kind==='stat')&&`${d.name||''} ${d.id}`.toLocaleLowerCase('ru').includes(query)).sort((a,b)=>(a.name||a.id).localeCompare(b.name||b.id,'ru'));
 selection.replaceChildren();for(const def of available){const option=document.createElement('option');option.value=def.id;option.textContent=`${def.name||def.id} · Характеристика${def.kind==='resource'?' · отслеживаемая':''}`;selection.append(option);}
 if(available.some(d=>d.id===previous))selection.value=previous;
 selection.disabled=!available.length;
 const action=$('characterExistingAction'),old=action.value;action.replaceChildren();const actions=[...Object.values(actionDefinitionRegistry||{}),...(characterEditSession?.newActions||[])].filter(a=>!characterEditSession?.actionIds.includes(a.id)&&`${a.name||a.id} ${a.id}`.toLocaleLowerCase('ru').includes(query));for(const a of actions){const o=document.createElement('option');o.value=a.id;o.textContent=a.name||a.id;action.append(o);}if(actions.some(a=>a.id===old))action.value=old;
 $('characterExistingActionLabel').hidden=kind!=='action';selection.closest('label').hidden=kind==='action';
 $('characterParameterValueLabel').hidden=kind==='action';$('characterParameterBooleanRow').hidden=kind==='action';
 selection.required=kind!=='action';$('characterParameterAdd').querySelector('[type=submit]').disabled=kind==='action'?!actions.length:!available.length;
 if(kind!=='action')updateCharacterParameterValue();
}
function updateCharacterParameterValue(newDefinition=false){
 const prefix=newDefinition?'characterParameterNew':'characterParameter',definition=newDefinition?{type:$('characterParameterNewType').value,kind:$('characterParameterNewKind').value==='resource'?'resource':''}:(resourceDefinitionRegistry?.[$('characterParameterSelect').value]||characterWatch.snapshot?.statDefinitions?.[$('characterParameterSelect').value]);
 const type=definition?.type||'integer',input=$(prefix+'Value'),bool=$(prefix+'BooleanRow');
 input.type=type==='string'?'text':'number';input.step=type==='number'?'any':'1';input.hidden=type==='boolean';bool.hidden=type!=='boolean';
 $(prefix+'ValueLabel').firstChild.textContent=(definition?.kind==='resource'?'Исходное значение':'Значение');
 if(type==='boolean')return;
 if(type==='string'&&input.value==='0')input.value='';
}
function clearCharacterParameterDraft(newDefinition){
 $('characterParameterSearch').value='';
 if(!newDefinition)return;
 $('characterParameterNew').open=false;$('characterParameterName').value='';$('characterParameterNewID').value='';$('characterParameterNewValue').value='0';$('characterParameterNewBoolean').checked=false;
}
async function refreshResourceDefinitions(){
 try{const definitions=await loadDefinitions();resourceDefinitionRegistry=definitions.effective?.stats||{};actionDefinitionRegistry=definitions.effective?.actions||{};if(characterWatch.snapshot)renderCharacterPanel();}
 catch(error){toast(`Не удалось получить определения: ${error.message}`);}
}
$('characterParameterKind').onchange=()=>updateCharacterParameterOptions();
$('characterParameterSearch').oninput=()=>updateCharacterParameterOptions();
$('characterParameterSelect').onchange=()=>updateCharacterParameterValue();
$('characterParameterNewType').onchange=()=>updateCharacterParameterValue(true);
$('characterParameterNewKind').onchange=()=>{const action=$('characterParameterNewKind').value==='action';$('characterParameterTrackedLabel').hidden=action;$('characterNewActionDescription').hidden=!action;$('characterParameterNewType').closest('label').hidden=action;$('characterParameterNewValueLabel').hidden=action;$('characterParameterNewBooleanRow').hidden=action;if(!action)updateCharacterParameterValue(true);};
$('characterParameterAdd').onsubmit=e=>{
 e.preventDefault();const d=characterEditSession;if(!d)return;
 if($('characterParameterKind').value==='action'){
  const id=$('characterExistingAction').value;if(!id||d.actionIds.includes(id))return;
  assignActionToEdit(id);return;
 }
 const id=$('characterParameterSelect').value,definition=resourceDefinitionRegistry?.[id]||characterWatch.snapshot?.statDefinitions?.[id];if(!definition)return;
 try{const value=statValueFromInput(definition.type,$('characterParameterValue').value,$('characterParameterBoolean').checked);
  if(definition.kind==='resource'){d.resourceInitial[id]=value;d.resourceCurrent[id]=value;}else d.stats[id]=value;
  addEditPlacement(`stat:${id}`);renderCharacterPanel();
 }catch(error){toast(error.message);}
};
$('characterParameterNew').onkeydown=e=>{if(e.key==='Enter'&&e.target instanceof HTMLInputElement){e.preventDefault();$('characterParameterCreate').click();}};
$('characterParameterCreate').onclick=()=>{
 const d=characterEditSession;if(!d)return;
 const name=$('characterParameterName').value.trim(),id=$('characterParameterNewID').value.trim()||name.toLowerCase().replace(/\s+/g,'_');
 if(!name||!id){toast('Укажите название и технический ID');return;}
 if((resourceDefinitionRegistry?.[id])||d.definitions.some(x=>x.id===id)||d.newActions.some(x=>x.id===id)){toast('Этот ID уже существует');return;}
 const action=$('characterParameterNewKind').value==='action';
 try{
  if(action){d.newActions.push({id,name,description:$('characterActionDescription').value,rolls:[]});d.actionIds.push(id);addEditPlacement(`action:${id}`);}
  else {const type=$('characterParameterNewType').value,value=statValueFromInput(type,$('characterParameterNewValue').value,$('characterParameterNewBoolean').checked),tracked=$('characterParameterTracked').checked;
   d.definitions.push({id,name,type,...(tracked?{kind:'resource'}:{})});if(tracked){d.resourceInitial[id]=value;d.resourceCurrent[id]=value;}else d.stats[id]=value;addEditPlacement(`stat:${id}`);}
  $('characterParameterNew').open=false;renderCharacterPanel();
 }catch(error){toast(error.message);}
};
updateCharacterParameterValue(true);
$('characterAvatarUpload').onchange=e=>{
 const file=e.target.files?.[0],d=characterEditSession;e.target.value='';if(!file||!d)return;
 if(file.size>10*1024*1024||!['image/png','image/jpeg','image/webp'].includes(file.type)){toast('Выберите PNG или JPEG до 10 МиБ');return;}
 if(d.avatarPreview)URL.revokeObjectURL(d.avatarPreview);
 if(characterAvatarDisplayController)characterAvatarDisplayController.abort();
 d.avatarFile=file;d.avatarPreview=URL.createObjectURL(file);d.resetAvatar=false;d.placeholderAvatar=false;d.artworkPath='';d.selectedMediaAsset='';
 $('characterAvatar').src=d.avatarPreview;$('characterAvatar').hidden=false;$('characterAvatarEmpty').hidden=true;
};
$('characterRename').oninput=()=>{if(characterEditSession)characterEditSession.name=$('characterRename').value;};$('characterRename').onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();$('characterRename').blur();}};
function clearCharacterImageDraft(d){
 d.avatarFile=null;d.selectedMediaAsset='';d.artworkPath='';if(d.avatarPreview){URL.revokeObjectURL(d.avatarPreview);d.avatarPreview='';}
 characterAssetPicker.close();
}
$('characterAvatarReset').onclick=()=>{const d=characterEditSession;if(!d)return;clearCharacterImageDraft(d);d.resetAvatar=true;d.placeholderAvatar=false;renderCharacterPanel();};
$('characterAvatarPlaceholder').onclick=()=>{const d=characterEditSession;if(!d)return;clearCharacterImageDraft(d);d.resetAvatar=false;d.placeholderAvatar=true;renderCharacterPanel();};
async function selectCharacterLibraryAsset(d,file){
 if(!d||!file?.assetId||!file.path)return;
 d.selectedMediaAsset='';d.artworkPath=file.path;d.avatarFile=null;d.resetAvatar=false;d.placeholderAvatar=false;
 if(d.avatarPreview)URL.revokeObjectURL(d.avatarPreview);d.avatarPreview='';
 const blob=await loadRulesetMediaThumbnail(file.assetId);
 if(characterEditSession!==d||d.artworkPath!==file.path)return;
 d.avatarPreview=URL.createObjectURL(blob);renderCharacterPanel();
}
$('characterMediaPick').onclick=()=>{
 const d=characterEditSession;if(!d||!isEditorView())return;
 characterAssetPicker.open({title:'Выберите аватар персонажа',actionLabel:'Выбрать',onSelect:file=>selectCharacterLibraryAsset(d,file)}).catch(error=>toast(error.message));
};
function rememberElementField(id,field){const element=state?.elements?.[selectedElement]||state?.elementCatalog?.[selectedElement];if(!element)return;const control=$(id);let value=control.type==='checkbox'?control.checked:control.type==='number'||control.type==='range'?Number(control.value):control.value;if(field==='layerId'){const layer=state.layers[value];if(!layer)return;elementDrafts.set(element.id,'floorId',layer.floorId);}elementDrafts.set(element.id,field,value);}
function commitElementProperties(id=selectedElement){clearTimeout(elementAutosaveTimer);elementAutosaveTimer=null;const loaded=state?.elements?.[id],element=loaded||state?.elementCatalog?.[id];if(!element||!isEditorView()||!$('elementProperties').checkValidity())return;const draft={...elementDrafts.get(id)},properties={},transform={},unchanged={},unavailable={};for(const [field,raw] of Object.entries(draft)){const transformField=field in element.transform;if(transformField&&!loaded){unavailable[field]=raw;continue;}const value=field==='name'?String(raw).trim():raw,base=transformField?element.transform[field]:element[field],type=transformField?'elementTransform':'elementUpdate',box=transformField?positionOutbox:outbox;if(value!==raw)elementDrafts.set(id,field,value);if(base===value)unchanged[field]=value;else if(!commandAlreadyQueued(box,type,id,field,value))(transformField?transform:properties)[field]=value;}if(Object.keys(unavailable).length)elementDrafts.confirm(id,unavailable);if(Object.keys(unchanged).length)elementDrafts.confirm(id,unchanged);if(Object.keys(properties).length)queueCommand('elementUpdate',{element:{id},elementProperties:properties});if(Object.keys(transform).length)queueTransform('elementTransform',{element:{id,transform:{...element.transform,...transform}}});if(!Object.keys(properties).length&&!Object.keys(transform).length&&id===selectedElement)fillElementProperties();}
function scheduleElementProperties(id=selectedElement){clearTimeout(elementAutosaveTimer);elementAutosaveTimer=setTimeout(()=>commitElementProperties(id),FORM_AUTOSAVE_DELAY);}
for(const [id,field] of [['elementName','name'],['elementX','x'],['elementY','y'],['elementWidth','width'],['elementHeight','height'],['elementRotation','rotation'],['elementOpacity','opacity'],['elementLayer','layerId'],['elementVisible','visible'],['elementLocked','locked']]){const control=$(id);control.addEventListener('input',()=>{rememberElementField(id,field);scheduleElementProperties();dirty=true;});control.addEventListener('change',()=>{rememberElementField(id,field);commitElementProperties();dirty=true;});control.addEventListener('blur',()=>commitElementProperties());}
$('elementProperties').onsubmit=e=>{e.preventDefault();commitElementProperties();};
$('allowElementRotation').onclick=()=>{if(selectedElement){rotationOverrides.add(selectedElement);fillElementProperties();dirty=true;}};
$('deleteElement').onclick=()=>{if(selectedElement&&confirm('Удалить элемент сцены?'))queueCommand('elementDelete',{element:{id:selectedElement}});selectElement(null);};
$('duplicateElement').onclick=()=>{const element=state?.elements?.[selectedElement];if(!element)return;queueCommand('elementCreate',{element:{...element,id:'',name:`${element.name} копия`,transform:{...element.transform,x:element.transform.x+30,y:element.transform.y+30}}});};
function addToken(character={},x=camera.x+viewport.w/(2*camera.scale),y=camera.y+viewport.h/(2*camera.scale)){const queued=queueCommand('create',{...character,token:{floorId:currentFloorID,x,y,size:80,rotation:0,color:'#c2d89b',opacity:1,ownerIds:[],hidden:false}});if(!queued)toast('Дождитесь подключения к серверу');return queued;}
let tokenCatalogKind='roster',tokenCatalogSelection='',tokenCatalogAnchor=null,tokenCatalogAvatarObserver=null;
function tokenCatalogItems(){return tokenCatalogKind==='presets'?characterCatalog.presets:characterCatalog.roster;}
function clearTokenCatalogAvatars(){tokenCatalogAvatarObserver?.disconnect();tokenCatalogAvatarObserver=null;for(const image of $('tokenCatalogList').querySelectorAll('.token-catalog-avatar img')){image._avatarController?.abort();if(image.src.startsWith('blob:'))URL.revokeObjectURL(image.src);}}
async function loadTokenCatalogAvatar(image,assetID){
 if(!assetID||!image.isConnected)return;image._avatarController?.abort();if(image.src.startsWith('blob:'))URL.revokeObjectURL(image.src);image.removeAttribute('src');image.hidden=true;image.nextElementSibling.hidden=false;
 const controller=new AbortController();image._avatarController=controller;try{const response=await fetch(avatarPath(assetID),{headers:{Authorization:`Bearer ${credentials.key}`},signal:controller.signal});if(!response.ok)throw new Error();const blob=await response.blob();if(controller.signal.aborted||!image.isConnected||image.dataset.avatarAsset!==assetID)return;image.src=URL.createObjectURL(blob);image.hidden=false;image.nextElementSibling.hidden=true;}catch{}finally{if(image._avatarController===controller)image._avatarController=null;}
}
function renderTokenCreateOptions(){
 const picker=$('tokenCatalogPicker');if(!picker||picker.hidden)return;
 const query=$('tokenCatalogSearch').value.trim().toLocaleLowerCase(),items=tokenCatalogItems().filter(item=>`${item.name||''} ${item.id||''} ${item.kind||''}`.toLocaleLowerCase().includes(query));
 $('tokenCatalogRosterTab').classList.toggle('active',tokenCatalogKind==='roster');$('tokenCatalogRosterTab').setAttribute('aria-selected',String(tokenCatalogKind==='roster'));
 $('tokenCatalogPresetsTab').classList.toggle('active',tokenCatalogKind==='presets');$('tokenCatalogPresetsTab').setAttribute('aria-selected',String(tokenCatalogKind==='presets'));
 const list=$('tokenCatalogList');clearTokenCatalogAvatars();list.replaceChildren();
 for(const item of items){const row=document.createElement('button');row.type='button';row.className='token-catalog-row';row.dataset.catalogId=item.id;row.setAttribute('role','option');row.setAttribute('aria-selected',String(item.id===tokenCatalogSelection));const avatar=document.createElement('span');avatar.className='token-catalog-avatar';avatar.dataset.avatarAsset=item.avatarAssetId||'';const image=document.createElement('img');image.alt='';image.hidden=true;image.dataset.avatarAsset=item.avatarAssetId||'';const fallback=document.createElement('span');fallback.textContent=(item.name||'?').slice(0,1).toLocaleUpperCase();avatar.append(image,fallback);const text=document.createElement('span');text.className='token-catalog-row-text';const name=document.createElement('strong');name.textContent=item.name||item.id;const detail=document.createElement('small');if(tokenCatalogKind==='presets')detail.textContent=item.kind||'Шаблон';else{const preset=characterCatalog.presets.find(value=>value.id===item.presetId);detail.textContent=preset?.name||'Сохранённый персонаж';}text.append(name,detail);row.append(avatar,text);row.onclick=()=>{tokenCatalogSelection=item.id;for(const option of list.children)option.setAttribute('aria-selected',String(option===row));$('tokenCatalogPlace').disabled=false;};row.ondblclick=()=>placeTokenCatalogSelection();list.append(row);}
 const avatarFrames=list.querySelectorAll('.token-catalog-avatar[data-avatar-asset]:not([data-avatar-asset=""])');if(typeof IntersectionObserver==='undefined'){for(const frame of avatarFrames)loadTokenCatalogAvatar(frame.querySelector('img'),frame.dataset.avatarAsset);}else{tokenCatalogAvatarObserver=new IntersectionObserver(entries=>{for(const entry of entries){const frame=entry.target,image=frame.querySelector('img');if(entry.isIntersecting)loadTokenCatalogAvatar(image,frame.dataset.avatarAsset);else{image._avatarController?.abort();if(image.src.startsWith('blob:'))URL.revokeObjectURL(image.src);image.removeAttribute('src');image.hidden=true;image.nextElementSibling.hidden=false;}}},{root:list,rootMargin:'64px'});for(const frame of avatarFrames)tokenCatalogAvatarObserver.observe(frame);}
 $('tokenCatalogEmpty').hidden=items.length>0||characterCatalog.loading.has(tokenCatalogKind);
 const loading=characterCatalog.loading.has(tokenCatalogKind),more=characterCatalog.next[tokenCatalogKind];$('tokenCatalogMore').hidden=!more&&!loading;$('tokenCatalogMore').disabled=loading;$('tokenCatalogMore').textContent=loading?'Загрузка…':'Показать ещё';$('tokenCatalogPlace').disabled=!tokenCatalogSelection||!isEditorView();if(tokenCatalogAnchor)positionTokenCatalogPicker(tokenCatalogAnchor);
}
function positionTokenCatalogPicker(anchor){const picker=$('tokenCatalogPicker'),rect=anchor.getBoundingClientRect(),width=picker.offsetWidth,height=picker.offsetHeight;picker.style.left=`${Math.max(8,Math.min(rect.right+12,window.innerWidth-width-8))}px`;picker.style.top=`${Math.max(8,Math.min(rect.top,window.innerHeight-height-8))}px`;}
function openTokenCatalogPicker(kind,anchor){tokenCatalogKind=kind;tokenCatalogSelection='';tokenCatalogAnchor=anchor;$('tokenCatalogSearch').value='';$('tokenCatalogPicker').hidden=false;renderTokenCreateOptions();$('tokenCatalogSearch').focus();}
function closeTokenCatalogPicker(){clearTokenCatalogAvatars();$('tokenCatalogPicker').hidden=true;tokenCatalogSelection='';tokenCatalogAnchor=null;for(const choice of tokenCreateControls.querySelectorAll('[data-token-catalog]'))choice.setAttribute('aria-expanded','false');}
function placeTokenCatalogSelection(){if(!isEditorView()||!tokenCatalogSelection)return;const character=tokenCatalogKind==='presets'?{presetId:tokenCatalogSelection}:{characterId:tokenCatalogSelection};if(addToken(character))closeTokenCreatePanel();}
function closeTokenCreatePanel(){closeTokenCatalogPicker();tokenCreateControls.hidden=true;$('add').setAttribute('aria-expanded','false');}
$('add').onclick=()=>{
 if(!isEditorView())return;
 if(!tokenCreateControls.hidden){closeTokenCreatePanel();return;}
 renderTokenCreateOptions();tokenCreateControls.hidden=false;$('add').setAttribute('aria-expanded','true');
};
for(const button of tokenCreateControls.querySelectorAll('[data-token-catalog]'))button.onclick=()=>{for(const choice of tokenCreateControls.querySelectorAll('[data-token-catalog]'))choice.setAttribute('aria-expanded',String(choice===button));openTokenCatalogPicker(button.dataset.tokenCatalog,button);};
$('tokenCatalogRosterTab').onclick=()=>{tokenCatalogKind='roster';tokenCatalogSelection='';renderTokenCreateOptions();};
$('tokenCatalogPresetsTab').onclick=()=>{tokenCatalogKind='presets';tokenCatalogSelection='';renderTokenCreateOptions();};
$('tokenCatalogSearch').oninput=renderTokenCreateOptions;
$('tokenCatalogMore').onclick=()=>characterCatalog.more(tokenCatalogKind).catch(error=>toast(error.message));
$('tokenCatalogCancel').onclick=closeTokenCatalogPicker;
$('tokenCatalogPlace').onclick=placeTokenCatalogSelection;
$('addEmptyToken').onclick=()=>{if(isEditorView()&&addToken())closeTokenCreatePanel();};
$('addImageToken').onclick=()=>{if(isEditorView()){$('tokenUpload').click();closeTokenCreatePanel();}};
async function upload(file,kind){if(!file)return;if(!state?.scene?.id)throw new Error('Сначала откройте сцену');if(file.size>UPLOAD_LIMIT_BYTES)throw new Error('Размер файла не должен превышать 256 МиБ');const sceneID=state.scene.id,controller=new AbortController(),activeElement=state.elements?.[selectedElement],layer=activeElement?.layerId||orderedLayers(state,currentFloorID).find(item=>item.kind==='visual')?.id||'';uploadControllers.add(controller);toast('Подготовка изображения на сервере…');try{const query=new URLSearchParams({session:credentials.session,scene:sceneID,kind,name:file.name,floor:currentFloorID,layer});const a=await api(`/api/upload?${query}`,file,true,controller.signal);if(controller.signal.aborted||state?.scene?.id!==sceneID)throw new DOMException('Scene changed','AbortError');toast(kind==='scene'?(a.renderMode==='tiled'?'Изображение добавлено. Загружаются видимые тайлы.':'Изображение добавлено.'):'Изображение персонажа загружено');if(kind==='token')addToken({avatarAssetId:a.id});return a;}finally{uploadControllers.delete(controller);}}
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
 if(e.button===1){e.preventDefault();drag={type:'pan',px:p.x,py:p.y,x:camera.x,y:camera.y};return;}
 if(gm&&renderBoundsTool&&e.button===0&&!e.altKey){const floor=state.floors?.[currentFloorID];if(!floor)return;if(renderBoundsTool==='rect')drag={type:'renderBoundsRect',floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},current:{x:wx,y:wy},moved:false};else if(renderBoundsTool==='polygon'){const closeRadius=10/camera.scale;if(!renderBoundsDraft)renderBoundsDraft={floorId:currentFloorID,geometryRevision:floor.geometryRevision,points:[{x:wx,y:wy}],cursor:{x:wx,y:wy},invalid:false};else if(renderBoundsDraft.points.length>=3&&Math.hypot(wx-renderBoundsDraft.points[0].x,wy-renderBoundsDraft.points[0].y)<=closeRadius)finishRenderBoundsDraft();else{const last=renderBoundsDraft.points.at(-1);if(Math.hypot(wx-last.x,wy-last.y)>.0005)renderBoundsDraft.points.push({x:wx,y:wy});const polygon=renderBoundsPolygon(renderBoundsDraft.points),n=polygon.outer.length;renderBoundsDraft.invalid=n>=3&&!validRenderBoundsChange(polygon,[n-2,n-1]);}}else if(renderBoundsTool==='edit'&&floor.renderBounds){const hit=renderBoundsHit(floor.renderBounds,wx,wy,camera.scale),original=cloneRenderBounds(floor.renderBounds);if(hit?.part==='vertex'){selectedRenderBoundsVertex=hit.index;drag={type:'renderBoundsEdit',mode:'vertex',vertexIndex:hit.index,floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},original,preview:cloneRenderBounds(original),invalid:false,moved:false};}else if(hit?.part==='edge'){selectedRenderBoundsVertex=hit.index+1;original.outer.splice(selectedRenderBoundsVertex,0,{x:wx,y:wy});drag={type:'renderBoundsEdit',mode:'insert',vertexIndex:selectedRenderBoundsVertex,floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},original,preview:cloneRenderBounds(original),invalid:false,moved:true};}else if(hit?.part==='polygon'){selectedRenderBoundsVertex=-1;drag={type:'renderBoundsEdit',mode:'move',vertexIndex:-1,floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},original,preview:cloneRenderBounds(original),invalid:false,moved:false};}else selectedRenderBoundsVertex=-1;}dirty=true;return;}
 if(gm&&walkableTool&&e.button===0&&!e.altKey){const floor=state.floors?.[currentFloorID];if(!floor)return;if(walkableTool==='add'||walkableTool==='subtract')drag={type:'walkableRect',operation:walkableTool==='add'?'addWalkableRect':'subtractWalkableRect',floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},current:{x:wx,y:wy},moved:false};else{const component=walkableComponentAt(state,currentFloorID,wx,wy);selectedWalkableComponent=component?.id||'';drag=component?{type:'walkableComponent',componentId:component.id,floorId:currentFloorID,geometryRevision:floor.geometryRevision,start:{x:wx,y:wy},dx:0,dy:0,moved:false}:null;}dirty=true;return;}
 if(gm&&transitionDraft&&transitionDraft.phase!=='awaitingCreate'&&e.button===0){if(transitionDraft.phase==='placingA'||transitionDraft.phase==='placingB'){const key=transitionDraft.phase==='placingA'?'endpointA':'endpointB',floorId=pickTransitionFloor(wx,wy),endpoint={floorId,position:transitionPosition(floorId,{x:wx,y:wy}),radius:transitionDefaultRadius()};transitionDraft={...transitionDraft,[key]:endpoint,phase:key==='endpointA'?'editingA':'editingB'};drag={type:'transitionDraftRadius',key,start:{x:wx,y:wy},moved:false};fillTransitionProperties();dirty=true;return;}const hit=draftTransitionHit(wx,wy);if(hit?.part==='confirm'){confirmTransitionEndpoint();return;}if(hit?.part==='cancel'){cancelTransitionDraft();return;}if(hit){drag={type:hit.part==='radius'?'transitionDraftRadius':'transitionDraftCenter',key:hit.key,startEndpoint:cloneTransitionEndpoint(transitionDraft[hit.key]),start:{x:wx,y:wy},moved:false};return;}dirty=true;return;}const playerPointVisible=gm||pointInRenderBounds(state,currentFloorID,wx,wy),t=playerPointVisible?tokenIndex.query(wx,wy,wx,wy).reverse().find(t=>(gm?t.floorId===currentFloorID:canMove(t))&&Math.hypot(wx-t.x,wy-t.y)<=t.size/2):null;const transitionHitResult=gm&&!t?transitionHit(state,currentFloorID,wx,wy,camera.scale,selectedTransition):null;if(transitionHitResult&&e.button===0&&!e.altKey){if(transitionHitResult.transition.id!==selectedTransition){selectTransition(transitionHitResult.transition.id);}else if(transitionHitResult.part==='center'||transitionHitResult.part==='radius'){drag={type:transitionHitResult.part==='center'?'transitionCenter':'transitionRadius',id:selectedTransition,key:transitionHitResult.key,startEndpoint:cloneTransitionEndpoint(transitionHitResult.transition[transitionHitResult.key]),start:{x:wx,y:wy},moved:false};}dirty=true;return;}const chosen=state.elements?.[selectedElement],chosenUnlocked=chosen&&!state.layers[chosen.layerId]?.locked,handle=gm&&chosenUnlocked?elementHandleAt(chosen,wx,wy,camera.scale,elementRotationAllowed(chosen)):null,element=gm?(handle?chosen:hitElement(state,currentFloorID,wx,wy,sceneRenderIndex,true)):null;if(t&&e.button===0&&!e.altKey){if(!gm)activatePlayerToken(t,false);else select(t.id);if(canMove(t))drag={type:'token',id:t.id,floorId:t.floorId,dx:wx-t.x,dy:wy-t.y,x:t.x,y:t.y,networkX:t.x,networkY:t.y,pointerX:wx-t.x,pointerY:wy-t.y,pointerDirty:false,moved:false};}else if(element&&e.button===0&&!e.altKey){selectElement(element.id);const layer=state.layers[element.layerId];if(!element.locked&&!layer.locked)drag={type:'element',id:element.id,mode:handle||'move',start:{...element.transform,pointerX:wx,pointerY:wy},pointerX:wx,pointerY:wy,transform:{...element.transform},moved:false};}else if(e.button===0){selectElement(null);selectedTransition='';fillTransitionProperties();drag={type:'pan',px:p.x,py:p.y,x:camera.x,y:camera.y};}dirty=true;};
function processTokenDrag(now=performance.now()){
 if(drag?.type!=='token'||!drag.pointerDirty)return;drag.pointerDirty=false;const target={x:drag.pointerX,y:drag.pointerY},gm=isEditorView(),position=gm?target:constrainTokenMovement(state,drag.floorId,{x:drag.networkX,y:drag.networkY},target);if(!position)return;const {x,y}=position;
 if(x!==drag.x||y!==drag.y){drag.x=x;drag.y=y;drag.moved=true;dirty=true;}
 if(drag.moved&&(x!==drag.networkX||y!==drag.networkY)&&now-lastMove>50&&!positionOutbox?.hasQueuedBehindInflight()&&send({type:'move',token:{id:drag.id,floorId:drag.floorId,x,y}})){drag.networkX=x;drag.networkY=y;lastMove=now;if(x!==target.x||y!==target.y)drag.pointerDirty=true;}
}
 canvas.onpointermove=e=>{const p=point(e),wx=camera.x+p.x/camera.scale,wy=camera.y+p.y/camera.scale;if(transitionDraft?.phase==='placingB'){transitionCursor={x:wx,y:wy};dirty=true;}if(renderBoundsDraft){renderBoundsDraft.cursor={x:wx,y:wy};dirty=true;}if(!drag)return;if(drag.type==='renderBoundsRect'){drag.current={x:wx,y:wy};drag.moved=Math.abs(wx-drag.start.x)>=.001&&Math.abs(wy-drag.start.y)>=.001;}else if(drag.type==='renderBoundsEdit'){const dx=wx-drag.start.x,dy=wy-drag.start.y;drag.preview=cloneRenderBounds(drag.original);if(drag.mode==='move')for(const point of drag.preview.outer){point.x+=dx;point.y+=dy;}else drag.preview.outer[drag.vertexIndex]={x:wx,y:wy};drag.invalid=!validRenderBoundsChange(drag.preview,drag.mode==='move'?[]:[drag.vertexIndex-1,drag.vertexIndex]);drag.moved=drag.mode==='insert'||Math.abs(dx)>=.001||Math.abs(dy)>=.001;}else if(drag.type==='walkableRect'){drag.current={x:wx,y:wy};drag.moved=Math.abs(wx-drag.start.x)>=.001&&Math.abs(wy-drag.start.y)>=.001;}else if(drag.type==='walkableComponent'){drag.dx=wx-drag.start.x;drag.dy=wy-drag.start.y;drag.moved=Math.abs(drag.dx)>=.001||Math.abs(drag.dy)>=.001;}else if(drag.type==='pan'){camera.x=drag.x-(p.x-drag.px)/camera.scale;camera.y=drag.y-(p.y-drag.py)/camera.scale;scheduleViewportSave();}else if(drag.type==='token'){drag.pointerX=wx-drag.dx;drag.pointerY=wy-drag.dy;drag.pointerDirty=true;}else if(drag.type==='element'){const transform=transformedFromDrag(drag.start,drag.mode,wx-drag.pointerX,wy-drag.pointerY,e.shiftKey);drag.transform=transform;drag.moved=true;state.elements[drag.id]={...state.elements[drag.id],transform};if(performance.now()-lastMove>50&&!positionOutbox?.hasQueuedBehindInflight()){send({type:'elementPreview',element:{id:drag.id,transform}});lastMove=performance.now();}}else if(drag.type==='transitionDraftRadius'){const endpoint=transitionDraft?.[drag.key];if(endpoint){const radius=Math.max(8,Math.hypot(wx-endpoint.position.x,wy-endpoint.position.y));if(Math.abs(radius-endpoint.radius)>.01)drag.moved=true;transitionDraft[drag.key]={...endpoint,radius};}}else if(drag.type==='transitionDraftCenter'){const start=drag.startEndpoint,dx=wx-drag.start.x,dy=wy-drag.start.y;transitionDraft[drag.key]={...start,position:transitionPosition(start.floorId,{x:start.position.x+dx,y:start.position.y+dy})};drag.moved=true;}else if(drag.type==='transitionRadius'||drag.type==='transitionCenter'){const transition=state.transitions?.[drag.id],start=drag.startEndpoint;if(transition){const endpoint=drag.type==='transitionRadius'?{...start,radius:Math.max(8,Math.hypot(wx-start.position.x,wy-start.position.y))}:{...start,position:transitionPosition(start.floorId,{x:start.position.x+wx-drag.start.x,y:start.position.y+wy-drag.start.y})};state.transitions[drag.id]={...transition,[drag.key]:endpoint};drag.moved=true;}}dirty=true;};
function endDrag(commit=true){if(commit&&drag?.type==='token')processTokenDrag();const completed=drag;drag=null;if(commit&&completed?.type==='renderBoundsRect'&&completed.moved){const left=Math.min(completed.start.x,completed.current.x),top=Math.min(completed.start.y,completed.current.y),right=Math.max(completed.start.x,completed.current.x),bottom=Math.max(completed.start.y,completed.current.y),polygon=renderBoundsPolygon([{x:left,y:top},{x:right,y:top},{x:right,y:bottom},{x:left,y:bottom}]);if(validRenderBounds(polygon))queueCommand('setRenderBounds',{floorId:completed.floorId,expectedGeometryRevision:completed.geometryRevision,renderBounds:polygon});}else if(commit&&completed?.type==='renderBoundsEdit'&&completed.moved){if(!completed.invalid){if(queueCommand('setRenderBounds',{floorId:completed.floorId,expectedGeometryRevision:completed.geometryRevision,renderBounds:completed.preview}))selectedRenderBoundsVertex=-1;}else toast('Недопустимое изменение границы отменено');}else if(commit&&completed?.type==='walkableRect'&&completed.moved){const x=Math.min(completed.start.x,completed.current.x),y=Math.min(completed.start.y,completed.current.y),width=Math.abs(completed.current.x-completed.start.x),height=Math.abs(completed.current.y-completed.start.y);queueCommand(completed.operation,{floorId:completed.floorId,expectedGeometryRevision:completed.geometryRevision,walkableBounds:{x,y,width,height}});}else if(commit&&completed?.type==='walkableComponent'&&completed.moved)queueCommand('moveWalkableComponent',{floorId:completed.floorId,componentId:completed.componentId,expectedGeometryRevision:completed.geometryRevision,deltaX:completed.dx,deltaY:completed.dy});else if(completed?.type==='token'&&completed.moved)queuePosition({token:{id:completed.id,floorId:completed.floorId,x:completed.x,y:completed.y}});else if(completed?.type==='element'&&completed.moved)queueTransform('elementTransform',{element:{id:completed.id,transform:completed.transform}});else if((completed?.type==='transitionRadius'||completed?.type==='transitionCenter')&&completed.moved){const transition=state?.transitions?.[completed.id];if(transition)queueCommand('transitionUpdate',{transition});}fillElementProperties();fillTransitionProperties();dirty=true;}
 canvas.onpointerup=()=>endDrag(true);canvas.onpointercancel=()=>endDrag(false);canvas.onlostpointercapture=()=>endDrag(false);canvas.onauxclick=e=>{if(e.button===1)e.preventDefault();};canvas.oncontextmenu=e=>{e.preventDefault();if(!state)return;const p=point(e),wx=camera.x+p.x/camera.scale,wy=camera.y+p.y/camera.scale,gm=isEditorView(),token=tokenIndex.query(wx,wy,wx,wy).reverse().find(item=>(gm?item.floorId===currentFloorID:canMove(item))&&Math.hypot(wx-item.x,wy-item.y)<=item.size/2);if(token)openTokenContextMenu(token.id,e.clientX,e.clientY);else closeTokenContextMenu();};
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
 if(state){const floor=state.floors?.[currentFloorID],bounds=renderBoundsAABB(floor?.renderBounds),selectedSceneElement=state.elements?.[selectedElement];gm=isEditorView();ctx.save();ctx.fillStyle='#182322';if(bounds){clipFloorRenderBounds(ctx,state,floor,renderBoundsPaths);ctx.fillRect(bounds.left,bounds.top,bounds.width,bounds.height);}else ctx.fillRect(camera.x,camera.y,right-camera.x,bottom-camera.y);ctx.restore();drawSceneStack({ctx,state,currentFloorId:currentFloorID,view:{left:camera.x,top:camera.y,right,bottom},camera,dpr,requestImage,peekImage,selectedElement,editor:gm,renderIndex:sceneRenderIndex,renderBoundsPaths,resourceBounds:renderBoundsResources,showRotationHandle:elementRotationAllowed(selectedSceneElement),drawTokenLayer:(floorId,alpha)=>{const tokenFloor=state.floors?.[floorId],resourceVisible=!gm&&tokenFloor?.renderBounds?(x,y,radius)=>renderBoundsResources.intersectsCircle(state,tokenFloor,x,y,radius):null;const outOfBounds=gm?(x,y)=>!pointInPlayableArea(state,floorId,x,y):null;tokenAnimating=drawTokens({ctx,camera,viewport,dpr,tokens:state.tokens,index:tokenIndex,visuals,moving:movingTokens,drag,selected,queue:[...(outbox?.data.queue||[]),...(positionOutbox?.data.queue||[])],requestImage,artwork,delta:now-lastFrame,floorId,alpha,outOfBounds,resourceVisible,characterDisplays:state.characterDisplays,resourceOverlays:state.resourceOverlays,resourceOverlayDefinitions:state.resourceOverlayDefinitions,showCharacterIDs:characterIDsEnabled()})||tokenAnimating;}});}
 if(grid){const step=100;const screenStep=step*camera.scale;if(screenStep>=12){ctx.save();const floor=state?.floors?.[currentFloorID];if(!gm&&floor?.renderBounds)clipFloorRenderBounds(ctx,state,floor,renderBoundsPaths);ctx.strokeStyle='#8ea18a16';ctx.lineWidth=1/camera.scale;ctx.beginPath();for(let x=Math.floor(camera.x/step)*step;x<right;x+=step){ctx.moveTo(x,camera.y);ctx.lineTo(x,bottom);}for(let y=Math.floor(camera.y/step)*step;y<bottom;y+=step){ctx.moveTo(camera.x,y);ctx.lineTo(right,y);}ctx.stroke();ctx.restore();}}
 if(gm){drawWalkableEditor(ctx,state,currentFloorID,camera,{drag,selectedId:selectedWalkableComponent});drawRenderBoundsEditor(ctx,state,currentFloorID,camera,{tool:renderBoundsTool,draft:renderBoundsDraft,drag,selectedVertex:selectedRenderBoundsVertex});drawTransitionOverlay(ctx,state,currentFloorID,camera,{selectedId:selectedTransition,draft:transitionDraft,cursor:transitionCursor});}else {drawPlayerWalkableOverlay(ctx,state,currentFloorID,camera);drawPlayerTransitionMarkers(ctx,state?.transitionMarkers,currentFloorID,camera);}dirty=tokenAnimating||dirty;
 ctx.restore();scheduleImages();$('zoom').textContent=`${Math.round(camera.scale*100)}%`;frameTime=performance.now()-start;rendered++;
}
function loop(now){processTokenDrag(now);if(!$('app').hidden&&(dirty||$('continuous').checked))draw(now);lastFrame=now;requestAnimationFrame(loop);}requestAnimationFrame(loop);
setInterval(()=>{fps=rendered;rendered=0;trimImages();if(failures.size)dirty=true;$('diagnostics').textContent=`${fps} кадров/с · ${frameTime.toFixed(1)} мс/кадр\nRAM изображений ${((memoryBytes+artwork.bytes)/MIB).toFixed(1)} / ${memoryLimit/MIB} МБ${imageMemorySetting==='auto'?' · Auto':''}\nДиск ${(diskBytes/MIB).toFixed(1)} / ${DISK_LIMIT/MIB} МБ${diskEnabled?'':' · недоступен'} · R/W ${diskReads}/${diskWrites} · evict ${diskEvictions} · drop ${diskDrops}\nТайлы ↓ ${(tileBytes/MIB).toFixed(2)} МБ · кэш ${cacheHits} · LOD fallback ${lodFallbacks}\nDecode ${decodeCount} · ${decodeMs.toFixed(0)} мс суммарно · edge ${decodeEdge} · degrade ${imageDegradations}\nWS ↓ ${(bytesIn/1024).toFixed(1)} ↑ ${(bytesOut/1024).toFixed(1)} КБ\nЗагрузка ${activeLoads} · ошибки ${failures.size}`;},1000);
