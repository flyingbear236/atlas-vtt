import {statInputValue,statValueFromInput} from './characters.js';

export class PresetEditor {
  constructor({load,loadMedia,loadAvatar,uploadMedia,save,toast,onSaved}) {
    Object.assign(this,{load,loadMedia,loadAvatar,uploadMedia,save,toast,onSaved});
    this.urls=[];this.artworkChange=undefined;this.preset=null;this.data=null;
    this.backdrop=document.createElement('div');
    this.backdrop.className='preset-editor-backdrop';
    this.backdrop.hidden=true;
    this.backdrop.innerHTML=`<section class="preset-editor-panel" role="dialog" aria-modal="true" aria-labelledby="presetEditorTitle">
      <header><div><small>RULESET / ПРЕСЕТ</small><h2 id="presetEditorTitle">Шаблон персонажа</h2><p data-owner class="muted"></p></div><button type="button" data-close class="subtle" aria-label="Закрыть">✕</button></header>
      <p data-error class="definition-error" hidden></p>
      <form data-form><label>Имя шаблона<input data-name required maxlength="200"></label>
        <label>Вид<select data-kind><option value="">Не указан</option><option value="character">Character</option><option value="npc">NPC</option><option value="monster">Monster</option></select></label>
        <h3>Характеристики и initial ресурсов</h3><div data-stats></div>
        <h3>Действия</h3><div data-actions class="preset-action-list"></div>
        <h3>Artwork шаблона</h3><div data-artwork class="muted"></div>
        <div class="preset-editor-tools"><button type="button" data-picker class="subtle">Выбрать из медиа</button><label class="file-button">Загрузить изображение<input data-upload type="file" accept="image/png,image/jpeg,image/webp" hidden></label><button type="button" data-reset class="subtle">Сбросить artwork</button></div>
        <div data-media-picker class="preset-media-picker" hidden></div>
        <footer><p data-impact class="muted"></p><button type="button" data-cancel class="subtle">Отмена</button><button type="submit" data-save class="primary">Сохранить</button></footer>
      </form></section>`;
    document.body.append(this.backdrop);
    this.q('[data-close]').onclick=()=>this.close();
    this.q('[data-cancel]').onclick=()=>this.close();
    this.backdrop.addEventListener('click',event=>{if(event.target===this.backdrop)this.close();});
    this.q('[data-picker]').onclick=()=>this.showPicker();
    this.q('[data-reset]').onclick=()=>this.selectArtwork('');
    this.q('[data-upload]').onchange=event=>this.upload(event.target);
    this.q('[data-form]').onsubmit=event=>{event.preventDefault();this.submit();};
  }
  q(selector){return this.backdrop.querySelector(selector);}
  error(message){this.q('[data-error]').textContent=String(message);this.q('[data-error]').hidden=false;}
  clearError(){this.q('[data-error]').hidden=true;this.q('[data-error]').textContent='';}
  close(){this.backdrop.hidden=true;this.preset=null;this.artworkChange=undefined;for(const url of this.urls)URL.revokeObjectURL(url);this.urls=[];this.q('[data-media-picker]').hidden=true;this.q('[data-media-picker]').replaceChildren();}
  async open(presetID){
    this.close();this.backdrop.hidden=false;this.clearError();this.q('[data-save]').disabled=true;
    try{
      const data=await this.load();
      if(this.backdrop.hidden)return;
      const preset=data.ruleset?.registry?.presets?.[presetID];
      if(!preset)throw new Error(`Шаблон ${presetID} отсутствует в текущем ruleset`);
      const owner=Object.entries(data.ruleset?.files||{}).find(([,file])=>(file.presets||[]).includes(presetID))?.[0];
      if(!owner)throw new Error('Неизвестен TOML-файл-владелец шаблона');
      this.data=data;this.preset=structuredClone(preset);this.owner=owner;
      this.q('#presetEditorTitle').textContent=`Шаблон персонажа — ${preset.name||presetID}`;
      this.q('[data-owner]').textContent=`ID: ${presetID} · TOML: ${owner}`;
      this.q('[data-name]').value=preset.name||'';
      this.q('[data-kind]').value=preset.kind||'';
      const usage=data.presetUsage?.[presetID]||{};
      this.q('[data-impact]').textContent=`Изменения получат ${usage.linked||0} персонажей. ${usage.materialized||0} самостоятельных персонажей останутся без изменений.`;
      this.renderStats();this.renderActions();
      this.q('[data-artwork]').textContent=preset.artwork||'Placeholder (без изображения)';
      if(preset.artwork&&!data.ruleset?.media?.[preset.artwork])this.q('[data-artwork]').textContent+= ' · изображение отсутствует';
      this.q('[data-save]').disabled=false;
    }catch(error){this.error(error.message);}
  }
  renderStats(){
    const list=this.q('[data-stats]');list.replaceChildren();
    const definitions=this.data.effective?.stats||{};
    const sorted=Object.entries(this.preset.stats||{}).sort(([a],[b])=>a.localeCompare(b));
    for(const [id,value] of sorted){
      const definition=definitions[id]||{type:value.type,name:id};
      const row=document.createElement('label');row.className='preset-edit-stat';row.dataset.id=id;
      const name=document.createElement('span');name.textContent=(definition.name||id)+(definition.kind==='resource'?' · initial':'');
      const input=document.createElement('input');input.dataset.type=definition.type;
      if(definition.type==='boolean'){input.type='checkbox';input.checked=!!value.value;}
      else {input.type=definition.type==='string'?'text':'number';input.value=statInputValue(value);if(definition.type==='integer')input.step='1';else if(definition.type==='number')input.step='any';}
      row.append(name,input);list.append(row);
    }
    if(!list.children.length)list.textContent='Характеристики отсутствуют.';
  }
  renderActions(){
    const list=this.q('[data-actions]');list.replaceChildren();const selected=new Set(this.preset.actionIds||[]);
    for(const [id,action] of Object.entries(this.data.effective?.actions||{}).sort(([a],[b])=>a.localeCompare(b))){
      const label=document.createElement('label');label.className='check';
      const input=document.createElement('input');input.type='checkbox';input.value=id;input.checked=selected.has(id);
      label.append(input,document.createTextNode(' '+(action.name||id)+' ('+id+')'));list.append(label);
    }
    if(!list.children.length)list.textContent='Действия отсутствуют.';
  }
  selectArtwork(assetID){
    this.artworkChange=assetID;
    this.q('[data-artwork]').textContent=assetID?'Выбрано медиа: '+assetID:'Placeholder (без изображения)';
    this.q('[data-media-picker]').hidden=true;
  }
  async showPicker(){
    this.clearError();const picker=this.q('[data-media-picker]');picker.hidden=false;picker.replaceChildren();
    const status=document.createElement('p');status.textContent='Загрузка медиа…';picker.append(status);
    try{
      const assets=await this.loadMedia();
      if(this.backdrop.hidden)return;
      picker.replaceChildren();
      for(const asset of assets.slice(0,100)){
        const button=document.createElement('button');button.type='button';button.className='preset-media-choice';
        const img=document.createElement('img');img.alt=asset.filename||asset.id;
        img.loading='lazy';
        const caption=document.createElement('small');caption.textContent=asset.filename||asset.id;
        button.append(img,caption);button.onclick=()=>this.selectArtwork(asset.id);picker.append(button);
        this.loadAvatar(asset.id).then(blob=>{
          if(this.backdrop.hidden||!button.isConnected)return;
          const url=URL.createObjectURL(blob);this.urls.push(url);img.src=url;
        }).catch(()=>{});
      }
      if(!picker.children.length)picker.textContent='В кампании пока нет изображений.';
    }catch(error){this.error(error.message);}
  }
  async upload(input){
    const file=input.files?.[0];input.value='';if(!file)return;
    if(file.size>10*1024*1024){this.error('Изображение превышает 10 МиБ');return;}
    this.clearError();input.disabled=true;
    try{
      const asset=await this.uploadMedia(file);
      if(!this.backdrop.hidden)this.selectArtwork(asset.id);
    }catch(error){this.error(error.message);}
    finally{input.disabled=false;}
  }
  async submit(){
    if(!this.preset||!this.q('[data-form]').reportValidity())return;
    const preset=structuredClone(this.preset);
    preset.name=this.q('[data-name]').value.trim();preset.kind=this.q('[data-kind]').value;
    const stats={};
    try{
      for(const row of this.q('[data-stats]').querySelectorAll('[data-id]')){
        const input=row.querySelector('input');
        stats[row.dataset.id]=statValueFromInput(input.dataset.type,input.value,input.checked);
      }
      preset.stats=stats;
      preset.actionIds=[...this.q('[data-actions]').querySelectorAll('input:checked')].map(input=>input.value);
    }catch(error){this.error(error.message);return;}
    const payload={expectedRevision:this.data.registryRevision,presetId:preset.id,preset};
    if(this.artworkChange!==undefined)payload.artworkAssetId=this.artworkChange;
    this.q('[data-save]').disabled=true;this.clearError();
    try{
      await this.save(payload);this.close();this.toast('Шаблон сохранён');await this.onSaved?.();
    }catch(error){this.error(error.message);}
    finally{this.q('[data-save]').disabled=false;}
  }
}
