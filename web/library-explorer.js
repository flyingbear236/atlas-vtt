// The server's /api/library response owns paths and file kinds. Only navigation
// state, selection and pending user actions live in this component.
export const libraryParent = path => path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '';
export const libraryBasename = path => path.slice(path.lastIndexOf('/') + 1);
export function libraryHierarchy(tree) {
  const directories = new Set(tree?.directories || []);
  for (const f of tree?.files || []) for (let dir = libraryParent(f.path); dir; dir = libraryParent(dir)) directories.add(dir);
  const folders = new Map([['', {path: '', children: [], files: []}]]);
  for (const dir of [...directories].sort((a,b)=>a.split('/').length-b.split('/').length || a.localeCompare(b))) folders.set(dir,{path:dir,children:[],files:[]});
  for (const dir of folders.keys()) if (dir) folders.get(libraryParent(dir))?.children.push(folders.get(dir));
  for (const file of tree?.files || []) folders.get(libraryParent(file.path))?.files.push(file);
  const cmp=(a,b)=>libraryBasename(a.path).localeCompare(libraryBasename(b.path),'ru',{sensitivity:'base'});
  for (const folder of folders.values()) {folder.children.sort(cmp);folder.files.sort(cmp);}
  return folders.get('');
}
export function libraryGalleryGroups(tree) {
  const groups = new Map();
  for (const file of tree?.files || []) if (file.type === 'image') {
    const dir = libraryParent(file.path);
    if (!groups.has(dir)) groups.set(dir, []);
    groups.get(dir).push(file);
  }
  return [...groups.entries()].sort(([a],[b])=>a.localeCompare(b,'ru')).map(([path,files])=>({path,files:files.sort((a,b)=>a.path.localeCompare(b.path,'ru'))}));
}
function element(tag, className='', content='') {
  const el=document.createElement(tag);if(className)el.className=className;if(content)el.textContent=content;return el;
}
function button(label,action,className='subtle') {
  const el=element('button',className,label);el.type='button';el.onclick=action;return el;
}
function humanSize(bytes){return bytes>=1048576?(bytes/1048576).toFixed(1)+' МиБ':bytes>=1024?(bytes/1024).toFixed(0)+' КиБ':(bytes||0)+' Б';}
function iconButton(icon,label,action){const b=button(icon,action,'subtle lb-icon');b.setAttribute('aria-label',label);b.title=label;return b;}

// Blob parts reference original File objects. Only one file is read into RAM
// at a time for CRC32; the archive is not duplicated in a large byte array.
function crc32(data){let crc=0xffffffff;for(const b of data){crc^=b;for(let i=0;i<8;i++)crc=(crc>>>1)^((crc&1)?0xedb88320:0);}return (crc^0xffffffff)>>>0;}
export async function libraryFilesZip(files, prefix='') {
  const encoder=new TextEncoder(), local=[],central=[];let offset=0;
  for(const file of files){
    const name=prefix+file.name,encoded=encoder.encode(name);
    if(!name || name.includes('\\') || name.startsWith('/') || name.split('/').some(part=>!part||part==='.'||part==='..') || encoded.length>65535 || file.size>0xffffffff)throw Error(`Недопустимый файл ${name}`);
    const checksum=crc32(new Uint8Array(await file.arrayBuffer()));
    const header=new Uint8Array(30+encoded.length),h=new DataView(header.buffer);
    h.setUint32(0,0x04034b50,true);h.setUint16(4,20,true);h.setUint16(6,0x800,true);h.setUint16(8,0,true);h.setUint32(14,checksum,true);h.setUint32(18,file.size,true);h.setUint32(22,file.size,true);h.setUint16(26,encoded.length,true);header.set(encoded,30);
    local.push(header,file);
    const record=new Uint8Array(46+encoded.length),c=new DataView(record.buffer);
    c.setUint32(0,0x02014b50,true);c.setUint16(4,20,true);c.setUint16(6,20,true);c.setUint16(8,0x800,true);c.setUint32(16,checksum,true);c.setUint32(20,file.size,true);c.setUint32(24,file.size,true);c.setUint16(28,encoded.length,true);c.setUint32(42,offset,true);record.set(encoded,46);central.push(record);
    offset+=header.length+file.size;
  }
  if(files.length>65535||offset>0xffffffff)throw Error('Слишком много файлов для одного импорта');
  const end=new Uint8Array(22),view=new DataView(end.buffer),centralSize=central.reduce((n,p)=>n+p.length,0);
  view.setUint32(0,0x06054b50,true);view.setUint16(8,files.length,true);view.setUint16(10,files.length,true);view.setUint32(12,centralSize,true);view.setUint32(16,offset,true);
  return new File([...local,...central,end],'library-files.zip',{type:'application/zip'});
}

export class ViewportPreviews {
  constructor(load){this.load=load;this.pending=[];this.queued=new Set();this.active=new Map();this.urls=new Map();this.visible=new Set();this.limit=4;this.closed=false;
    this.observer=typeof IntersectionObserver==='undefined'?null:new IntersectionObserver(items=>{
      for(const item of items){const el=item.target;const id=el.dataset.previewAsset;if(item.isIntersecting){this.visible.add(el);if(!this.queued.has(el)){if(this.pending.length>=64)this.queued.delete(this.pending.shift());this.pending.push(el);this.queued.add(el);}}else{this.visible.delete(el);this.queued.delete(el);this.release(el);}}
      this.pump();
    },{rootMargin:'160px 0px'});
  }
  observe(el,assetId){el.dataset.previewAsset=assetId;if(this.observer)this.observer.observe(el);else{this.visible.add(el);if(!this.queued.has(el)){if(this.pending.length>=64)this.queued.delete(this.pending.shift());this.pending.push(el);this.queued.add(el);}this.pump();}}
  pump(){if(this.closed)return;while(this.active.size<this.limit&&this.pending.length){const el=this.pending.shift();this.queued.delete(el);if(!el.isConnected||!this.visible.has(el)||this.active.has(el)||this.urls.has(el))continue;const controller=new AbortController();this.active.set(el,controller);
      this.load(el.dataset.previewAsset,controller.signal).then(blob=>{if(this.closed||!this.visible.has(el)||!el.isConnected)return;const url=URL.createObjectURL(blob);this.urls.set(el,url);el.src=url;}).catch(()=>{if(!controller.signal.aborted&&el.isConnected)el.alt='Изображение недоступно';}).finally(()=>{this.active.delete(el);this.pump();});
    }}
  release(el){const controller=this.active.get(el);if(controller)controller.abort();const url=this.urls.get(el);if(url){URL.revokeObjectURL(url);this.urls.delete(el);el.removeAttribute('src');}}
  destroy(){this.closed=true;this.observer?.disconnect();for(const el of this.active.keys())this.active.get(el).abort();for(const el of this.urls.keys())this.release(el);this.pending=[];this.queued.clear();this.active.clear();this.visible.clear();}
}

export class LibraryBrowser {
  constructor(mount,api){this.mount=mount;this.api=api;this.tree=null;this.registry=null;this.mode='explorer';this.selection='';this.activeDirectory='';this.scrollPositions={explorer:0,gallery:0};this.expanded=new Set();this.collapsedGallery=new Set();this.generation=0;this.pending=false;this.previewLoader=null;this.zip=null;this.zipPreview=null;this.choices={characters:{},media:{},unknown:{}};this.dialogKind='';this.destroyed=false;
    mount.innerHTML=`<div class="lb-top"><div class="lb-switch" role="group" aria-label="Режим библиотеки"><button data-lb-mode="explorer" class="active" type="button">Explorer</button><button data-lb-mode="gallery" type="button">Галерея</button></div><div class="lb-actions"><button type="button" data-lb-add>＋ Добавить</button><button type="button" data-lb-import>Импорт ZIP</button><button type="button" data-lb-export class="primary">Экспорт ZIP</button></div></div><div class="lb-message" role="status" aria-live="polite" hidden></div><div class="lb-columns"><div class="lb-left" data-lb-left></div><div class="lb-right" data-lb-right></div></div><div class="lb-menu" data-lb-menu hidden></div><input type="file" data-lb-files multiple accept=".toml,image/png,image/jpeg,image/webp" hidden><dialog class="lb-dialog" data-lb-dialog aria-label="Операция с библиотекой"><div data-lb-dialog-body></div><div class="lb-dialog-actions"><button type="button" data-lb-dialog-close>Закрыть</button></div></dialog>`;
    this.left=mount.querySelector('[data-lb-left]');this.right=mount.querySelector('[data-lb-right]');this.menu=mount.querySelector('[data-lb-menu]');this.dialog=mount.querySelector('[data-lb-dialog]');
    mount.querySelectorAll('[data-lb-mode]').forEach(b=>b.onclick=()=>this.setMode(b.dataset.lbMode));
    mount.querySelector('[data-lb-add]').onclick=e=>this.openMenu(e.currentTarget,[['Загрузить файлы',()=>mount.querySelector('[data-lb-files]').click()],['Создать папку',()=>this.createFolder()],...['stat','action','preset'].map(type=>['Создать '+({stat:'характеристику',action:'действие',preset:'шаблон'}[type]),()=>this.api.openDefinition(type,'')])]);
    mount.querySelector('[data-lb-import]').onclick=()=>this.openImport();mount.querySelector('[data-lb-export]').onclick=()=>this.openExport();
    mount.querySelector('[data-lb-files]').onchange=async e=>{const files=[...e.target.files];e.target.value='';if(files.length)await this.uploadFiles(files);};
    mount.querySelector('[data-lb-dialog-close]').onclick=()=>this.dialog.close();
    this.dialog.addEventListener('close',()=>{this.zipPreview=null;this.dialogKind='';});
    this.onOutside=e=>{if(!this.menu.hidden&&!this.menu.contains(e.target)&&!e.target.closest?.('[data-lb-add],.lb-more'))this.hideMenu();};
    this.onEscape=e=>{if(e.key==='Escape')this.hideMenu();};
    document.addEventListener('pointerdown',this.onOutside);document.addEventListener('keydown',this.onEscape);
  }
  setRegistry(registry){this.registry=registry;this.renderInspector();}
  async refresh(){const n=++this.generation;try{const data=await this.api.list();if(n!==this.generation||this.destroyed)return;if(!this.tree)for(const p of data.directories||[])if(!p.includes('/'))this.expanded.add(p);this.tree=data;
      if(this.selection&&!data.files.some(f=>f.path===this.selection))this.selection='';
      this.render();
    }catch(e){if(n===this.generation)this.message(e.message,true);}}
  close(){this.previewLoader?.destroy();this.previewLoader=null;this.hideMenu();if(this.dialog.open)this.dialog.close();}
  destroy(){this.destroyed=true;this.close();document.removeEventListener('pointerdown',this.onOutside);document.removeEventListener('keydown',this.onEscape);}
  message(msg,error=false){const el=this.mount.querySelector('.lb-message');el.textContent=msg||'';el.hidden=!msg;el.classList.toggle('error',error);}
  setMode(mode){if(this.mode===mode)return;this.scrollPositions[this.mode]=this.left.scrollTop;this.mode=mode;this.render(true);}
  render(modeChanged=false){if(!modeChanged)this.scrollPositions[this.mode]=this.left.scrollTop;this.previewLoader?.destroy();this.previewLoader=null;this.hideMenu();this.mount.querySelectorAll('[data-lb-mode]').forEach(b=>{b.classList.toggle('active',b.dataset.lbMode===this.mode);b.setAttribute('aria-pressed',String(b.dataset.lbMode===this.mode));});
    this.mount.querySelector('.lb-columns').classList.toggle('gallery',this.mode==='gallery');this.left.replaceChildren();this.right.replaceChildren();
    if(!this.tree){this.left.textContent='Загрузка библиотеки…';return;}
    if(this.mode==='gallery')this.renderGallery();else{this.renderExplorer();this.renderInspector();}
    this.left.scrollTop=this.scrollPositions[this.mode];
  }
  renderExplorer(){const root=libraryHierarchy(this.tree),scroll=element('div','lb-tree-scroll');this.left.append(scroll);
    const node=(folder,depth)=>{if(folder.path){const row=element('div','lb-entry lb-folder');row.style.paddingLeft=`${10+depth*14}px`;const exp=this.expanded.has(folder.path),toggle=button(exp?'▾':'▸',()=>{this.activeDirectory=folder.path;if(exp)this.expanded.delete(folder.path);else this.expanded.add(folder.path);this.render();},'lb-toggle');toggle.setAttribute('aria-label',(exp?'Свернуть ':'Раскрыть ')+folder.path);row.append(toggle,button('▣ '+libraryBasename(folder.path),()=>{this.activeDirectory=folder.path;},'lb-folder-name'));row.draggable=true;row.ondragstart=e=>{e.dataTransfer.effectAllowed='move';e.dataTransfer.setData('application/x-atlas-library',JSON.stringify({path:folder.path,type:'directory'}));};row.append(iconButton('⋯','Действия с папкой',e=>this.folderMenu(e,folder.path)));scroll.append(row);
      this.droppable(row,folder.path);if(!exp)return;}
      for(const sub of folder.children)node(sub,depth+1);
      for(const file of folder.files){const row=element('div','lb-entry lb-file'+(this.selection===file.path?' selected':''));row.style.paddingLeft=`${28+depth*14}px`;
        const select=button((file.type==='image'?'▧ ':'◇ ')+libraryBasename(file.path),()=>{this.left.querySelector('.lb-file.selected')?.classList.remove('selected');this.selection=file.path;this.activeDirectory=libraryParent(file.path);row.classList.add('selected');this.renderInspector();},'lb-name');select.setAttribute('aria-label','Открыть '+file.path);row.append(select,iconButton('⋯','Действия с файлом',e=>this.fileMenu(e,file)));
        row.draggable=true;row.ondragstart=e=>{e.dataTransfer.effectAllowed='move';e.dataTransfer.setData('application/x-atlas-library',JSON.stringify({path:file.path,type:'file'}));};scroll.append(row);
      }
    };
    node(root,0);
    const rootTarget=element('div','lb-root-drop','⌂ Перетащите сюда для перемещения в корень');rootTarget.title='Перетащите файл или папку сюда для перемещения в корень';this.droppable(rootTarget,'');scroll.prepend(rootTarget);
    if(!this.tree.files.length&&!this.tree.directories.length)scroll.append(element('p','muted','Библиотека пуста. Добавьте TOML или изображения.'));
  }
  droppable(el,directory){el.ondragover=e=>{if(!e.dataTransfer.types.includes('application/x-atlas-library'))return;e.preventDefault();el.classList.add('lb-drop-target');};el.ondragleave=()=>el.classList.remove('lb-drop-target');el.ondrop=e=>{el.classList.remove('lb-drop-target');const raw=e.dataTransfer.getData('application/x-atlas-library');if(!raw)return;e.preventDefault();let info;try{info=JSON.parse(raw);}catch{return;}const newPath=(directory?directory+'/':'')+libraryBasename(info.path);if(newPath===info.path||info.type==='directory'&&(directory===info.path||directory.startsWith(info.path+'/')))return;this.operate(info.type==='directory'?'moveDirectory':'moveFile',info.path,newPath);};}
  renderInspector(){if(!this.right||this.mode!=='explorer'||!this.tree)return;this.previewLoader?.destroy();this.previewLoader=null;this.right.replaceChildren();const file=this.tree.files.find(f=>f.path===this.selection);
    if(!file){const counts={definitions:0,character:0,image:0,'unknown-toml':0};for(const f of this.tree.files)counts[f.type]=(counts[f.type]||0)+1;
      const box=element('section','lb-inspector');box.append(element('h3','','Библиотека'),element('p','muted',`${this.tree.files.length} файлов · ${this.tree.directories.length} папок`));for(const [key,label] of [['definitions','TOML правил'],['character','Персонажи'],['image','Изображения'],['unknown-toml','Неинтерпретируемые TOML']])box.append(element('p','',`${label}: ${counts[key]}`));this.right.append(box);return;}
    const box=element('section','lb-inspector');this.right.append(box);box.append(element('h3','',libraryBasename(file.path)),element('p','muted lb-path',file.path));
    const actions=element('div','lb-inspector-actions');actions.append(button('Скачать',()=>this.action('download',file.path)),button('Копировать путь',()=>this.copy(file.path)),button('⋯ Действия',e=>this.fileMenu(e,file)));box.append(actions);
    if(file.type==='definitions'){const maps=this.registry?.effective||this.registry?.ruleset?.registry||{};const mapKinds=[['stat','Характеристики',file.statIds],['action','Действия',file.actionIds],['preset','Шаблоны',file.presetIds]];
      for(const [kind,label,ids] of mapKinds){const section=element('section','lb-details');section.append(element('h4','',`${label} (${ids?.length||0})`));for(const id of ids||[]){const def=maps[`${kind}s`]?.[id];section.append(button((def?.name||id)+(def?.name&&def.name!==id?` · ${id}`:''),()=>this.api.openDefinition(kind,id),'lb-definition-link'));}box.append(section);}if(!file.sourcePreserved)box.append(element('p','muted','Создано в интерфейсе ATLAS'));}
    else if(file.type==='character'){box.append(element('h4','',file.name||file.characterId),element('p','muted','ID: '+file.characterId.slice(0,12)),element('p','',`Шаблон: ${file.presetId||'нет'}`),element('p','',`Persistent: ${file.persistent?'да':'нет'} · Materialized: ${file.materialized?'да':'нет'}`));const resources=element('section','lb-details');resources.append(element('h4','','Ресурсы'));const resourceList=element('div','', 'Загрузка…');resources.append(resourceList);const inspectedPath=file.path;this.api.inspect(inspectedPath).then(detail=>{if(this.selection!==inspectedPath||!resourceList.isConnected)return;resourceList.replaceChildren();for(const [id,value] of Object.entries(detail.resourceCurrent||{})){const initial=detail.resourceInitial?.[id];resourceList.append(element('p','',`${id}: ${value?.value??'—'}${initial?'/'+initial.value:''}`));}if(!resourceList.childElementCount)resourceList.textContent='Нет текущих ресурсов';}).catch(error=>{if(resourceList.isConnected)resourceList.textContent=error.message;});box.append(resources,element('p','muted',`Изображение: ${file.hasArtwork?'задано':'наследуется'}`),button('Открыть чарлист',()=>this.api.openCharacter(file.characterId)));}
    else if(file.type==='image'){const image=element('img','lb-preview');image.alt='Превью '+file.path;image.loading='lazy';box.append(image);this.previewLoader=new ViewportPreviews(this.api.thumbnail);this.previewLoader.observe(image,file.assetId);box.append(element('p','muted',`${file.mimeType||'Изображение'} · ${file.width||'?'} × ${file.height||'?'} · ${humanSize(file.size)}`));}
    else box.append(element('p','muted','Неинтерпретируемый TOML. Хранится без влияния на игровые определения.'));
  }
  renderGallery(){const groups=libraryGalleryGroups(this.tree);const container=element('div','lb-gallery-scroll');this.left.append(container);this.previewLoader=new ViewportPreviews(this.api.thumbnail);
    for(const group of groups){const section=element('section','lb-gallery-group');const header=button((this.collapsedGallery.has(group.path)?'▸ ':'▾ ')+(group.path||'Корень')+` · ${group.files.length}`,()=>{if(this.collapsedGallery.has(group.path))this.collapsedGallery.delete(group.path);else this.collapsedGallery.add(group.path);this.render();},'lb-gallery-heading');header.setAttribute('aria-expanded',String(!this.collapsedGallery.has(group.path)));section.append(header);
      if(!this.collapsedGallery.has(group.path)){const grid=element('div','lb-gallery-grid');for(const file of group.files){const card=element('article','lb-gallery-card'),img=element('img','lb-gallery-image');img.alt=libraryBasename(file.path);img.loading='lazy';const preview=button('',()=>{this.mode='explorer';this.selection=file.path;for(let dir=libraryParent(file.path);dir;dir=libraryParent(dir))this.expanded.add(dir);this.render();},'lb-gallery-open');preview.setAttribute('aria-label','Открыть '+file.path);preview.append(img);card.append(preview);
        const bottom=element('div','lb-gallery-footer'),title=element('span','lb-gallery-filename',libraryBasename(file.path));title.title=file.path;bottom.append(title,iconButton('⧉','Скопировать путь',()=>this.copy(file.path)),iconButton('×','Удалить изображение',()=>this.action('deleteFile',file.path)));card.append(bottom);grid.append(card);this.previewLoader.observe(img,file.assetId);}
        section.append(grid);}
      container.append(section);
    }
    if(!groups.length)container.append(element('p','muted','В библиотеке пока нет изображений.'));
  }
  async operate(operation,path,target=''){if(this.pending)return;this.pending=true;this.message('Сохранение…');try{const t=this.tree;if(!t)throw Error('Библиотека не загружена');await this.api.operate({operation,path,target,expectedLibraryRevision:t.libraryRevision,expectedRegistryRevision:t.registryRevision,expectedCharacterRevision:t.characterRevision});if(path===this.selection&&target)this.selection=target;if(operation==='deleteFile'&&path===this.selection)this.selection='';if(operation.endsWith('Directory')&&target&&this.selection.startsWith(path+'/'))this.selection=target+this.selection.slice(path.length);if(operation.endsWith('Directory')&&target)this.expanded.add(target);
      await this.refresh();this.message('Сохранено');
    }catch(e){if(e.status===409){await this.refresh();this.message('Библиотека изменилась. Проверьте актуальное дерево и повторите действие.',true);}else this.message(e.message,true);}finally{this.pending=false;}}
  async copy(path){try{if(!navigator.clipboard?.writeText)throw Error('Буфер обмена недоступен');await navigator.clipboard.writeText(path);this.message('Путь скопирован');}catch{this.message('Не удалось скопировать. Путь: '+path,true);}}
  hideMenu(){this.menu.hidden=true;this.menu.replaceChildren();}
  openMenu(anchor,items){this.hideMenu();for(const [label,fn] of items){this.menu.append(button(label,()=>{this.hideMenu();fn();},'lb-menu-item'));}this.menu.hidden=false;const r=anchor.getBoundingClientRect();const host=this.mount.getBoundingClientRect();this.menu.style.left=Math.max(0,Math.min(r.left-host.left,host.width-200))+'px';this.menu.style.top=(r.bottom-host.top+3)+'px';}
  fileMenu(event,file){this.openMenu(event.currentTarget,[['Открыть',()=>{this.selection=file.path;this.setMode('explorer');this.render();}],['Переименовать',()=>this.action('renameFile',file.path)],['Переместить',()=>this.action('moveFile',file.path)],['Скачать',()=>this.action('download',file.path)],['Скопировать путь',()=>this.copy(file.path)],['Удалить',()=>this.action('deleteFile',file.path)]]);}
  folderMenu(event,path){this.openMenu(event.currentTarget,[['Создать подпапку',()=>this.createFolder(path)],['Переименовать',()=>this.action('renameDirectory',path)],['Переместить',()=>this.action('moveDirectory',path)],['Удалить',()=>this.action('deleteDirectory',path)]]);}
  createFolder(parent=''){const name=prompt('Имя новой папки:');if(!name)return;this.operate('createDirectory',parent?parent+'/'+name:name);}
  action(operation,path){if(operation==='download'){this.api.download(path).catch(e=>this.message(e.message,true));return;}if(operation.startsWith('delete')){if(!confirm('Удалить «'+path+'»? Используемые файлы или непустые папки сервер не позволит удалить.'))return;this.operate(operation,path);return;}
    const old=libraryBasename(path),target=operation.startsWith('rename')?((libraryParent(path)?libraryParent(path)+'/':'')+(prompt('Новое имя:',old)||'')):prompt('Полный новый путь:',path);
    if(target&&target!==path)this.operate(operation,path,target);
  }
  showDialog(kind,body){this.dialogKind=kind;this.dialog.querySelector('[data-lb-dialog-body]').replaceChildren();this.dialog.querySelector('[data-lb-dialog-body]').append(body);if(!this.dialog.open)this.dialog.showModal();}
  openExport(){const body=element('section','lb-dialog-content');body.append(element('h3','','Экспорт библиотеки ZIP'));const label=element('label','lb-check'),check=element('input');check.type='checkbox';check.checked=true;label.append(check,document.createTextNode(' Включить медиа'));body.append(label);body.append(button('Скачать ZIP',async()=>{if(this.pending)return;this.pending=true;try{await this.api.exportPackage(check.checked);this.dialog.close();this.message('ZIP экспортирован');}catch(e){this.message(e.message,true);}finally{this.pending=false;}},'primary'));this.showDialog('export',body);}
  openImport(file=null,presetFiles=null){this.zip=file;this.zipPreview=null;this.choices={characters:{},media:{},unknown:{}};this.importSelection=presetFiles;const body=element('section','lb-dialog-content');body.append(element('h3','','Импорт ZIP'));const input=element('input');input.type='file';input.accept='.zip,application/zip';if(!file)body.append(input);else body.append(element('p','',file.name));const mode=element('select');for(const [value,label] of [['selected','Добавить / выборочная замена'],['replace','Полная замена definitions']]){const o=element('option');o.value=value;o.textContent=label;mode.append(o);}body.append(element('label','','Режим импорта'),mode);const listing=element('div','lb-import-files');const conflicts=element('div','lb-import-conflicts');const report=element('pre','lb-import-report');body.append(listing,report,conflicts);const row=element('div','lb-dialog-actions');const preview=button('Проверить',()=>runPreview()),apply=button('Применить',()=>runApply(),'primary');apply.disabled=true;row.append(preview,apply);body.append(row);
    const options=()=>({mode:mode.value,selectedFiles:mode.value==='selected'?[...listing.querySelectorAll('input:checked')].map(el=>el.value):[],characters:{...this.choices.characters},media:{...this.choices.media},unknown:{...this.choices.unknown}});
    const renderSelections=paths=>{const previous=new Map([...listing.querySelectorAll('input')].map(el=>[el.value,el.checked]));listing.replaceChildren();for(const path of paths){const l=element('label','lb-check'),c=element('input');c.type='checkbox';c.value=path;c.checked=previous.get(path)??(this.importSelection?this.importSelection.has(path):true);c.onchange=()=>{apply.disabled=true;this.zipPreview=null;report.textContent+='\nВыбор изменён — повторите проверку.';};l.append(c,document.createTextNode(' '+path));listing.append(l);}};
    const runPreview=async()=>{const source=this.zip||input.files[0];if(!source){this.message('Выберите архив',true);return;}preview.disabled=true;apply.disabled=true;this.zipPreview=null;report.textContent='Проверка…';try{if(mode.value==='selected'&&!listing.querySelector('input')){const inventory=await this.api.inventoryImport(source);renderSelections(inventory.availableFiles||[]);}const result=await this.api.previewImport(source,options());this.zip=source;this.zipPreview=result;renderSelections(result.availableFiles||[]);const diff=result.rulesetDiff||{};const changes=kind=>[...(diff[kind]?.added||[]).map(id=>'+'+id),...(diff[kind]?.changed||[]).map(id=>'~'+id),...(diff[kind]?.removed||[]).map(id=>'−'+id)].join(', ')||'нет';const parts=[`Файлы: +${result.addedFiles?.length||0} / ~${result.replacedFiles?.length||0} / −${result.removedFiles?.length||0}`,`Новые: ${(result.addedFiles||[]).join(', ')||'нет'}`,`Замена: ${(result.replacedFiles||[]).join(', ')||'нет'}`,`Удаление: ${(result.removedFiles||[]).join(', ')||'нет'}`,`Характеристики: ${changes('stats')}; действия: ${changes('actions')}; шаблоны: ${changes('presets')}`,`Персонажи: +${result.addedCharacters?.length||0}; конфликты: ${result.conflictingCharacters?.length||0}`,`Изображения: +${result.addedMedia?.length||0}; конфликты: ${result.conflictingMedia?.length||0}`,`Opaque TOML: ${(result.unknownFiles||[]).join(', ')||'нет'}`,`Пропущенные файлы: ${(result.skippedFiles||[]).join(', ')||'нет'}`,`Отсутствующие artwork: ${(result.missingMedia||[]).join(', ')||'нет'}`];report.textContent=parts.join('\n');conflicts.replaceChildren();let unresolved=false;
      for(const [kind,choices,labels] of [['characters',['skip','replace'],['Пропустить','Заменить']],['media',['keep','replace'],['Сохранить','Заменить']],['unknown',['keep','replace'],['Сохранить','Заменить']]])for(const path of result['conflicting'+kind[0].toUpperCase()+kind.slice(1)]||[]){const label=element('label','lb-conflict',kind+': '+path);const select=element('select');const empty=element('option','','Выберите действие');empty.value='';select.append(empty);for(let i=0;i<choices.length;i++){const o=element('option','',labels[i]);o.value=choices[i];select.append(o);}select.value=this.choices[kind][path]||'';if(!select.value)unresolved=true;select.onchange=()=>{this.choices[kind][path]=select.value;apply.disabled=true;this.zipPreview=null;};label.append(select);conflicts.append(label);}
      if(unresolved){report.textContent+='\nВыберите решения конфликтов и повторите проверку.';return;}
      apply.disabled=false;
    }catch(e){report.textContent=e.message;this.message(e.message,true);}finally{preview.disabled=false;}};
    const runApply=async()=>{if(!this.zipPreview||this.pending)return;this.pending=true;apply.disabled=true;try{await this.api.applyImport(this.zip,options(),this.zipPreview);this.dialog.close();await this.refresh();await this.api.reloadRegistry();this.message('Импорт завершён');}catch(e){this.zipPreview=null;this.message(e.message,true);report.textContent+='\n'+e.message;}finally{this.pending=false;}};
    mode.onchange=()=>{apply.disabled=true;this.zipPreview=null;};input.onchange=()=>{this.zip=null;apply.disabled=true;this.zipPreview=null;listing.replaceChildren();report.textContent='';};this.showDialog('import',body);if(file)runPreview();
  }
  async uploadFiles(files){if(this.pending)return;const total=files.reduce((n,f)=>n+f.size,0);if(total>64*1048576){this.message('За один раз можно добавить не больше 64 МиБ. Для больших библиотек используйте импорт ZIP.',true);return;}
    this.pending=true;this.message('Подготовка файлов…');try{const selected=this.tree?.files.find(f=>f.path===this.selection);const prefix=this.activeDirectory|| (selected?libraryParent(selected.path):'');const zip=await libraryFilesZip(files,prefix?prefix+'/':'');this.openImport(zip,new Set(files.map(f=>(prefix?prefix+'/':'')+f.name)));}catch(e){this.message(e.message,true);}finally{this.pending=false;}}
}
