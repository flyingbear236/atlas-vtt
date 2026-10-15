import {statValueFromInput,statInputValue} from './characters.js';

const IMAGE_TYPES=new Set(['image/png','image/jpeg','image/webp']);

function statControl(type,value){
  const input=document.createElement('input');
  if(type==='boolean'){input.type='checkbox';input.checked=!!value?.value;}
  else {input.type=type==='string'?'text':'number';input.value=statInputValue(value);if(type==='integer')input.step='1';else if(type==='number')input.step='any';}
  return input;
}

// Owns one disposable draft. Both cancel and backdrop dismissal discard all
// logical media changes; physical uploads are retained by shared storage.
export class PresetEditor {
  constructor({request,upload,thumbnail,toast,onSaved}){
    this.request=request;this.upload=upload;this.thumbnail=thumbnail;this.toast=toast;this.onSaved=onSaved;
    this.mount=document.createElement('div');this.mount.className='preset-editor-backdrop';this.mount.hidden=true;
    this.mount.innerHTML=`<section class="preset-editor-modal" role="dialog" aria-modal="true" aria-labelledby="presetEditorTitle"><header><div><small>RULESET / ШАБЛОН</small><h2 id="presetEditorTitle">Шаблон персонажа</h2></div><button data-close class="subtle" type="button" aria-label="Закрыть">×</button></header><div class="preset-editor-body"><p data-meta class="muted"></p><p data-error class="definition-error" hidden></p><label>Имя шаблона<input data-name maxlength="200" required></label><label>Вид<select data-kind><option value="">Не указан</option><option value="character">Character</option><option value="npc">NPC</option><option value="monster">Monster</option></select></label><h3>Характеристики и initial ресурсов</h3><div data-stats></div><div class="preset-editor-add"><select data-add-stat></select><button data-add type="button" class="subtle">Добавить stat</button></div><h3>Действия</h3><div data-actions class="preset-editor-actions"></div><h3>Artwork</h3><div data-artwork class="preset-editor-artwork"></div><div class="preset-editor-actions"><button data-pick class="subtle" type="button">Выбрать из медиа</button><label class="file-button">Загрузить изображение<input data-upload type="file" accept="image/png,image/jpeg,image/webp" hidden></label><button data-clear class="subtle" type="button">Сбросить</button></div><div data-picker hidden class="preset-editor-picker"></div><p data-warning class="muted"></p></div><div class="preset-editor-footer"><small data-count></small><div><button data-cancel class="subtle" type="button">Отмена</button><button data-save class="primary" type="button">Сохранить</button></div></div></section>`;
    document.body.append(this.mount);
    this.q=selector=>this.mount.querySelector(selector);
    this.q('[data-close]').onclick=()=>this.close();this.q('[data-cancel]').onclick=()=>this.close();
    this.mount.addEventListener('click',event=>{if(event.target===this.mount)this.close();});
    this.q('[data-save]').onclick=()=>this.save();this.q('[data-pick]').onclick=()=>this.openPicker();
    this.q('[data-clear]').onclick=()=>{this.draft.artwork='';this.updateArtwork();};
    this.q('[data-upload]').onchange=async e=>{const file=e.target.files?.[0];e.target.value='';if(file)await this.uploadArtwork(file);};
    this.q('[data-add]').onclick=()=>{const id=this.q('[data-add-stat]').value,definition=this.data?.statDefinitions?.[id];if(!definition||Object.hasOwn(this.draft.stats,id))return;this.draft.stats[id]={type:definition.type,value:definition.type==='boolean'?false:definition.type==='string'?'':0};this.renderStats();};
    this.images=new Set();this.pickerURLs=new Set();this.pickerGeneration=0;this.generation=0;
  }
  async open(id){
    this.close();this.q('[data-error]').hidden=true;
    const generation=++this.generation;
    try{
      const data=await this.request(`/api/definitions/preset?id=${encodeURIComponent(id)}`);
      if(generation!==this.generation)return;
      this.mount.hidden=false;this.data=data;this.draft=structuredClone(data.preset);this.draft.stats||={};this.draft.actionIds||=[];
      this.mapping={};this.media=null;this.assetName='';this.q('[data-name]').value=this.draft.name;this.q('[data-kind]').value=this.draft.kind||'';
      this.q('[data-meta]').textContent=`ID: ${id} · ${data.owner}`;
      this.q('[data-count]').textContent=`Изменения получат ${data.linked} персонажей. ${data.materialized} самостоятельных персонажей останутся без изменений.`;
      this.q('#presetEditorTitle').textContent=`Шаблон персонажа — ${data.preset.name}`;
      this.renderStats();this.renderActions();this.updateArtwork();
    }catch(error){if(generation===this.generation){this.toast(error.message);this.close();}}
  }
  close(){++this.generation;this.data=null;this.draft=null;this.media=null;this.mapping={};this.mount.hidden=true;this.q('[data-picker]').hidden=true;for(const url of this.images)URL.revokeObjectURL(url);this.images.clear();this.pickerGeneration++;for(const url of this.pickerURLs)URL.revokeObjectURL(url);this.pickerURLs.clear();this.q('[data-picker]').replaceChildren();this.q('[data-artwork]').replaceChildren();}
  error(message){this.q('[data-error]').textContent=String(message);this.q('[data-error]').hidden=false;}
  renderStats(){
    const container=this.q('[data-stats]');container.replaceChildren();
    const definitions=this.data?.statDefinitions||{};
    for(const id of Object.keys(this.draft.stats).sort()){
      const definition=definitions[id],value=this.draft.stats[id];if(!definition)continue;
      const row=document.createElement('label');row.className='preset-editor-stat';
      const label=document.createElement('span');label.textContent=(definition.name||id)+(definition.kind==='resource'?' · initial':'');
      const input=statControl(definition.type,value);input.dataset.statId=id;
      input.addEventListener('change',()=>{try{this.draft.stats[id]=statValueFromInput(definition.type,input.value,input.checked);input.setCustomValidity('');}catch(error){input.setCustomValidity(error.message);input.reportValidity();}});
      const remove=document.createElement('button');remove.type='button';remove.className='subtle';remove.textContent='Убрать';remove.onclick=()=>{delete this.draft.stats[id];this.renderStats();};
      row.append(label,input,remove);container.append(row);
    }
    const select=this.q('[data-add-stat]');select.replaceChildren();
    for(const [id,definition] of Object.entries(definitions).sort(([a],[b])=>a.localeCompare(b))){if(Object.hasOwn(this.draft.stats,id))continue;const option=document.createElement('option');option.value=id;option.textContent=definition.name||id;select.append(option);}
    this.q('[data-add]').disabled=!select.options.length;
  }
  renderActions(){
    const container=this.q('[data-actions]');container.replaceChildren();
    for(const [id,action] of Object.entries(this.data.actions||{}).sort(([a],[b])=>a.localeCompare(b))){
      const label=document.createElement('label');label.className='check';const input=document.createElement('input');input.type='checkbox';input.checked=this.draft.actionIds.includes(id);
      input.onchange=()=>{const set=new Set(this.draft.actionIds);input.checked?set.add(id):set.delete(id);this.draft.actionIds=[...set].sort();};label.append(input,document.createTextNode(action.name||id));container.append(label);
    }
  }
  updateArtwork(){
    const path=this.draft?.artwork||'';const target=this.q('[data-artwork]');target.replaceChildren();
    const info=document.createElement('span');info.textContent=path||'Placeholder';target.append(info);
    const matching=this.assetForArtwork(path);
    if(matching)this.drawThumbnail(target,matching);
    this.q('[data-warning]').textContent=path&&!matching?'Artwork отсутствует в ruleset media: после сохранения будет placeholder.':'';
  }
  assetForArtwork(path){return this.mapping[path]||this.media?.rulesetMedia?.[path]||(!this.data||this.data.artwork?.path!==path?'':this.data.artwork?.assetId)||'';}
  async drawThumbnail(target,assetID,pickerGeneration=0){
    const generation=this.generation;
    try{const blob=await this.thumbnail(assetID);if(generation!==this.generation||(pickerGeneration&&pickerGeneration!==this.pickerGeneration))return;const url=URL.createObjectURL(blob);(pickerGeneration?this.pickerURLs:this.images).add(url);const img=document.createElement('img');img.src=url;img.alt='Artwork';target.prepend(img);}catch(error){if(generation===this.generation)this.error(error.message);}
  }
  async ensureMedia(){if(this.media)return this.media;this.media=await this.request('/api/definitions/ruleset/media/assets');return this.media;}
  async openPicker(){
    if(!this.data)return;const target=this.q('[data-picker]');target.hidden=!target.hidden;if(target.hidden){this.pickerGeneration++;for(const url of this.pickerURLs)URL.revokeObjectURL(url);this.pickerURLs.clear();target.replaceChildren();return;}
    try{const media=await this.ensureMedia();this.pickerPage=0;this.renderPickerPage(media.assets||[]);}catch(error){this.error(error.message);}
  }
  renderPickerPage(assets){
    const target=this.q('[data-picker]');const generation=++this.pickerGeneration;
    for(const url of this.pickerURLs)URL.revokeObjectURL(url);this.pickerURLs.clear();target.replaceChildren();
    const size=40,start=this.pickerPage*size,end=Math.min(assets.length,start+size);
    for(const asset of assets.slice(start,end)){
      const button=document.createElement('button');button.type='button';button.className='preset-editor-tile';button.title=asset.id;
      const label=document.createElement('span');label.textContent=asset.id.slice(0,12);button.append(label);
      button.onclick=async()=>{try{await this.selectAsset(asset.id,asset.id+'.png');target.hidden=true;}catch(error){this.error(error.message);}};
      target.append(button);this.drawThumbnail(button,asset.id,generation);
    }
    if(assets.length>size){const pager=document.createElement('div');pager.className='preset-editor-actions';
      const back=document.createElement('button');back.type='button';back.textContent='Назад';back.disabled=this.pickerPage===0;back.onclick=()=>{this.pickerPage--;this.renderPickerPage(assets);};
      const next=document.createElement('button');next.type='button';next.textContent='Далее';next.disabled=end>=assets.length;next.onclick=()=>{this.pickerPage++;this.renderPickerPage(assets);};
      const count=document.createElement('small');count.textContent=`${start+1}–${end} из ${assets.length}`;pager.append(back,count,next);target.append(pager);
    }
    if(!assets.length)target.textContent='Изображений пока нет.';
  }
  async selectAsset(assetID,filename,suggestedPath=''){
    const media=await this.ensureMedia();
    let path=Object.entries({...media.rulesetMedia,...this.mapping}).find(([,id])=>id===assetID)?.[0];
    if(!path){
      if(!suggestedPath||Object.hasOwn(this.mapping,suggestedPath)){const used=Object.keys({...media.rulesetMedia,...this.mapping});const query=new URLSearchParams({filename});for(const key of used)query.append('used',key);const result=await this.request(`/api/definitions/ruleset/media/path?${query}`);suggestedPath=result.path;}
      path=suggestedPath;this.mapping[path]=assetID;
    }
    if(!this.draft)return;this.draft.artwork=path;this.updateArtwork();
  }
  async uploadArtwork(file){
    if(!IMAGE_TYPES.has(file.type)||file.size>10*1024*1024){this.error('Допускаются PNG, JPEG, WebP до 10 МиБ');return;}
    const generation=this.generation;
    try{const result=await this.upload(file);if(generation!==this.generation)return;await this.selectAsset(result.asset.id,file.name.replace(/\.[^.]+$/,'.png'));if(generation===this.generation)this.toast('Изображение добавлено в черновик');}catch(error){if(generation===this.generation)this.error(error.message);}
  }
  async save(){
    if(!this.data)return;const button=this.q('[data-save]');button.disabled=true;this.q('[data-error]').hidden=true;
    try{
      this.draft.name=this.q('[data-name]').value.trim();this.draft.kind=this.q('[data-kind]').value;
      if(!this.draft.name)throw new Error('Укажите имя шаблона');
      // Read every field before Save, including an unblurred final edit.
      for(const row of this.q('[data-stats]').children){const id=row.querySelector('input')?.dataset?.statId;if(!id)continue;const def=this.data.statDefinitions[id],input=row.querySelector('input');this.draft.stats[id]=statValueFromInput(def.type,input.value,input.checked);}
      await this.request('/api/definitions/preset',{preset:this.draft,media:this.mapping,expectedRevision:this.data.registryRevision});
      this.close();this.toast('Шаблон сохранён');this.onSaved?.();
    }catch(error){this.error(error.message);}finally{button.disabled=false;}
  }
}
