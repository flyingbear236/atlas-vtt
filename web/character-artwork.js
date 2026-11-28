// View-only preferences are local to the authenticated user. Art/text are
// character data and are committed separately from the mechanical edit draft.
export const artworkModes=['header','background','none'];
export const accentColors=[['#c2d89b','Шалфей'],['#769d82','Зелёный'],['#86b6d6','Синий'],['#b59bcf','Лавандовый'],['#d49a91','Терракотовый'],['#d7bc83','Золотой'],['#a8b3b4','Серый']];
const clamp=(v,min,max)=>Math.max(min,Math.min(max,v));
const finite=(v,fallback)=>typeof v==='number'&&Number.isFinite(v)?v:fallback;
export function normalizeArtworkPlacement(value,mode='header'){
 return {x:clamp(finite(value?.x,mode==='header'?.67:.5),-128,128),y:clamp(finite(value?.y,.5),-128,128),zoom:clamp(finite(value?.zoom,1),.2,12),opacity:clamp(finite(value?.opacity,mode==='header'?.88:.48),.1,1)};
}
export function artworkGeometry(frame,image,placement,mode){
 const {width:w,height:h}=frame,{width:iw,height:ih}=image;if(!(w>0&&h>0&&iw>0&&ih>0))return null;
 const p=normalizeArtworkPlacement(placement,mode);
 // The header starts width-fitted (up to 6x contain for extreme panoramas).
 const base=mode==='header'?Math.min(w/iw,h/ih*6):Math.min(w/iw,h/ih);
 const width=iw*base*p.zoom,height=ih*base*p.zoom;
 const vx=Math.min(w*.16,width*.4,36),vy=Math.min(h*.16,height*.4,36);
 const cx=clamp(p.x*w,vx-width/2,w-vx+width/2),cy=clamp(p.y*h,vy-height/2,h-vy+height/2);
 return {width,height,left:cx-width/2,top:cy-height/2,x:cx/w,y:cy/h,zoom:p.zoom,opacity:p.opacity,feather:Math.min(32,width*.24,height*.24)};
}
export function zoomArtwork(frame,image,placement,mode,factor,anchor){
 const old=artworkGeometry(frame,image,placement,mode);if(!old)return normalizeArtworkPlacement(placement,mode);
 const zoom=clamp(old.zoom*factor,.2,12),ratio=zoom/old.zoom;
 const x=(anchor.x+(old.x*frame.width-anchor.x)*ratio)/frame.width;
 const y=(anchor.y+(old.y*frame.height-anchor.y)*ratio)/frame.height;
 const next=artworkGeometry(frame,image,{...placement,x,y,zoom},mode);
 return {x:next.x,y:next.y,zoom,opacity:old.opacity};
}
export function artworkPreferences(value){
 return {mode:artworkModes.includes(value?.mode)?value.mode:'header',header:normalizeArtworkPlacement(value?.header,'header'),background:normalizeArtworkPlacement(value?.background,'background')};
}
const node=(tag,cls,text)=>{const el=document.createElement(tag);if(cls)el.className=cls;if(text!==undefined)el.textContent=text;return el;};
const button=(text,handler,cls='subtle')=>{const el=node('button',cls,text);el.type='button';el.onclick=handler;return el;};
const read=(key,fallback)=>{try{return JSON.parse(localStorage.getItem(key))??fallback;}catch{return fallback;}};
const write=(key,value)=>{try{localStorage.setItem(key,JSON.stringify(value));return true;}catch{return false;}};
function makeLayer(){const layer=node('div','character-artwork-layer'),img=node('img','character-artwork-image');img.alt='';img.draggable=false;layer.append(img);layer.setAttribute('aria-hidden','true');return {layer,img};}
function geometryFor(layer,img,placement,mode){return artworkGeometry({width:layer.clientWidth,height:layer.clientHeight},{width:img.naturalWidth,height:img.naturalHeight},placement,mode);}
function paint(layer,img,placement,mode){const g=geometryFor(layer,img,placement,mode);if(!g)return;Object.assign(img.style,{width:`${g.width}px`,height:`${g.height}px`,left:`${g.left}px`,top:`${g.top}px`,opacity:g.opacity});img.style.setProperty('--art-feather',`${g.feather}px`);}
// Holds at most one decoded sheet image and one draft preview; abort and revoke
// on selection changes, cancellation and scene transitions.
class ArtworkImage {
 constructor(img,load,ready){Object.assign(this,{img,load,ready});this.id='';this.url='';}
 clear(){this.controller?.abort();this.controller=null;if(this.url)URL.revokeObjectURL(this.url);this.url='';this.id='';this.img.removeAttribute('src');this.img.hidden=true;}
 async set(id,scope,onError){if(this.id===id&&this.scope===scope)return;this.clear();this.scope=scope;this.id=id;if(!id)return;const controller=this.controller=new AbortController();try{const blob=await this.load(id,controller.signal);if(controller.signal.aborted)return;this.url=URL.createObjectURL(blob);this.img.src=this.url;await this.img.decode();if(controller.signal.aborted)return;this.img.hidden=false;this.ready();}catch(e){if(!controller.signal.aborted){this.clear();this.id=id;this.scope=scope;onError?.(e);}}finally{if(this.controller===controller)this.controller=null;}}
}
export class CharacterArtwork {
 constructor({summary,head,inspector,trigger,load,pick,save,toast,context,cancelPick}){
  Object.assign(this,{summary,head,inspector,trigger,load,pick,save,toast,context,cancelPick});this.prefs=artworkPreferences();this.snapshot=null;this.scope='';this.asset='';
  const {layer,img}=makeLayer();this.layer=layer;this.img=img;this.backgroundHost=node('div','character-artwork-background-host');summary.prepend(this.backgroundHost);head.prepend(layer);layer.hidden=true;
  this.media=new ArtworkImage(img,load,()=>this.render());this.observer=new ResizeObserver(()=>this.resize());this.observer.observe(head);this.observer.observe(inspector);this.observer.observe(layer);
  trigger.onclick=()=>this.open();
  this.dialog=node('dialog','character-artwork-dialog');this.dialog.setAttribute('aria-label','Артворк и оформление');document.body.append(this.dialog);
  this.dialog.addEventListener('cancel',e=>{e.preventDefault();this.close();});this.dialog.addEventListener('click',e=>{if(e.target===this.dialog)this.close();});
 }
 storageKey(scope){return `atlas-character-artwork-v1/${scope}`;}
 loadPreferences(scope,id){const entries=read(this.storageKey(scope),[]);return artworkPreferences(Array.isArray(entries)?entries.find(e=>e?.id===id)?.prefs:null);}
 storePreferences(scope,id,prefs){const entries=read(this.storageKey(scope),[]);return write(this.storageKey(scope),[{id,prefs},...(Array.isArray(entries)?entries.filter(e=>e?.id!==id):[])].slice(0,128));}
 sync(snapshot,editable){const scope=this.context(),id=snapshot?.characterId||'';this.trigger.hidden=!id||!editable; // avoid racing an open mechanics draft
  if(!id||!scope){this.reset();return;}
  const changed=this.scope!==scope||this.snapshot?.characterId!==id;
  const asset=snapshot.portraitAssetId||'',assetChanged=asset!==this.asset;
  if(changed){this.close(true);this.scope=scope;this.prefs=this.loadPreferences(scope,id);}
  this.snapshot=snapshot;this.asset=asset;
  if(!changed&&!assetChanged)return;
  this.render();
  if(this.prefs.mode==='none'){this.media.clear();return;}
  this.media.set(this.asset,scope,e=>this.toast(e.message));
 }
 reset(){if(!this.snapshot&&!this.scope&&!this.draft)return;this.close(true);this.snapshot=null;this.scope='';this.asset='';this.media.clear();this.layer.hidden=true;this.backgroundHost.hidden=true;this.summary.classList.remove('has-character-artwork-background');}
 resize(){if(this.prefs.mode==='background'&&this.asset){const h=Math.max(160,this.inspector.clientHeight-40);this.summary.style.setProperty('--character-artwork-height',`${h}px`);}this.renderImage();}
 renderImage(){if(!this.layer.hidden)paint(this.layer,this.img,this.prefs[this.prefs.mode],this.prefs.mode);}
 render(){const mode=this.prefs.mode,visible=!!this.asset&&mode!=='none';this.layer.hidden=!visible;this.backgroundHost.hidden=!visible||mode!=='background';this.summary.classList.toggle('has-character-artwork-background',visible&&mode==='background');
  if(mode==='background'){if(this.layer.parentElement!==this.backgroundHost)this.backgroundHost.append(this.layer);this.layer.classList.add('is-background');}else{if(this.layer.parentElement!==this.head)this.head.prepend(this.layer);this.layer.classList.remove('is-background');}this.resize();
 }
 close(force=false){if(!this.draft||this.draft.saving&&!force)return;if(this.draft.picking)this.cancelPick?.();this.previewObserver?.disconnect();this.previewObserver=null;this.previewMedia?.clear();this.previewMedia=null;this.draft=null;if(this.dialog.open)this.dialog.close();this.dialog.replaceChildren();}
 open(){if(!this.snapshot||this.draft)return;const snapshot=this.snapshot;const d=this.draft={id:snapshot.characterId,scope:this.scope,prefs:artworkPreferences(this.prefs),asset:this.asset,text:snapshot.instance?.portraitText||'',initialText:snapshot.instance?.portraitText||'',keepImage:true,saving:false,picking:false};
  this.dialog.replaceChildren();const title=node('h3','','Артворк и оформление'),desc=node('p','muted','Изображение и публичный текст видны при Alt-наведении. Кадрирование меняет только ваш чар-лист.');
  const mode=node('select');mode.setAttribute('aria-label','Отображение артворка');for(const [v,t] of [['header','В шапке'],['background','Фон чар-листа'],['none','Не показывать в чар-листе']]){const opt=node('option','',t);opt.value=v;mode.append(opt);}mode.value=d.prefs.mode;
  const frame=node('div','character-artwork-preview-frame');frame.tabIndex=0;frame.setAttribute('aria-label','Предпросмотр. Стрелки перемещают изображение, плюс и минус меняют масштаб.');const {layer,img}=makeLayer();frame.append(layer);const overlay=node('div','character-artwork-preview-content');const identity=node('div','character-artwork-preview-identity');const avatar=document.getElementById('characterAvatar');if(avatar&&!avatar.hidden&&avatar.src){const small=node('img');small.src=avatar.src;small.alt='';identity.append(small);}else identity.append(node('span','character-artwork-preview-avatar','◇'));const text=node('div');text.append(node('strong','',snapshot.effective?.name||snapshot.instance?.name||'Персонаж'),node('small','',snapshot.effective?.description||''));identity.append(text);overlay.append(identity);const mock=node('div','character-artwork-preview-rows');for(const text of ['Характеристики','Ресурсы','Действия'])mock.append(node('div','',text));overlay.append(mock);frame.append(overlay);
  const hint=node('small','muted','Колесо мыши — масштаб, зажать и двигать — положение. Края автоматически затухают.');
  const zoom=node('input');zoom.type='range';zoom.min='.2';zoom.max='12';zoom.step='.05';zoom.setAttribute('aria-label','Масштаб артворка');const opacity=node('input');opacity.type='range';opacity.min='.1';opacity.max='1';opacity.step='.05';opacity.setAttribute('aria-label','Яркость артворка');const zoomLabel=node('label','','Масштаб '),zoomOut=node('output');zoomLabel.append(zoomOut,zoom);const opacityLabel=node('label','','Яркость');opacityLabel.append(opacity);
  const status=node('span','muted');status.setAttribute('role','status');const caption=node('textarea');caption.rows=3;caption.maxLength=8192;caption.placeholder='Что видят окружающие при наведении с Alt';caption.value=d.text;const captionLabel=node('label','','Публичный текст при Alt-наведении');captionLabel.append(caption);
  const choose=button('Выбрать / загрузить артворк',async()=>{if(d.saving||d.picking)return;d.picking=true;choose.disabled=true;save.disabled=true;try{const id=await this.pick(d.id);if(this.draft!==d||!id)return;if(id!==d.asset){d.asset=id;d.keepImage=false;d.prefs.header=normalizeArtworkPlacement(null,'header');d.prefs.background=normalizeArtworkPlacement(null,'background');loadPreview();}}catch(e){if(this.draft===d)status.textContent=e.message;}finally{if(this.draft===d){d.picking=false;choose.disabled=false;save.disabled=false;}}});
  const remove=button('Убрать',()=>{d.asset='';d.keepImage=false;loadPreview();});const reset=button('Сбросить кадр',()=>{if(d.prefs.mode==='none')return;d.prefs[d.prefs.mode]=normalizeArtworkPlacement(null,d.prefs.mode);draw();});
  const save=button('Сохранить',async()=>{if(d.saving||d.picking)return;d.saving=true;for(const b of [choose,remove,save,cancel,mode,caption,zoom,opacity,reset])b.disabled=true;try{if(!d.keepImage||caption.value.trim()!==d.initialText){const result=await this.save(d.id,d.asset,caption.value,d.keepImage);if(this.draft!==d)return;this.asset=result.assetId||'';}if(!this.storePreferences(d.scope,d.id,d.prefs))this.toast('Браузер не сохранил настройки оформления');this.prefs=artworkPreferences(d.prefs);this.close(true);this.render();if(this.prefs.mode==='none')this.media.clear();else this.media.set(this.asset,this.scope,e=>this.toast(e.message));}catch(e){if(this.draft===d){status.textContent=e.message;d.saving=false;for(const b of [choose,remove,save,cancel,mode,caption])b.disabled=false;draw();}}},'primary');const cancel=button('Отмена',()=>this.close());
  const pickerRow=node('div','character-artwork-dialog-controls');pickerRow.append(choose,remove);const placementRow=node('div','character-artwork-dialog-controls');placementRow.append(reset);const footer=node('div','character-artwork-dialog-controls character-artwork-footer');footer.append(status,cancel,save);this.dialog.append(title,desc,pickerRow,mode,frame,hint,zoomLabel,opacityLabel,placementRow,captionLabel,footer);
  const draw=()=>{const m=d.prefs.mode,p=d.prefs[m];frame.classList.toggle('is-background',m==='background');mock.hidden=m!=='background';layer.hidden=m==='none'||!d.asset;const w=Math.max(160,this.head.clientWidth),h=m==='background'?Math.max(160,this.inspector.clientHeight-40):Math.max(76,this.head.clientHeight);frame.style.width=`min(100%, ${Math.min(w,420*w/h,420)}px)`;frame.style.aspectRatio=`${w} / ${h}`;zoom.disabled=opacity.disabled=reset.disabled=d.saving||m==='none'||!d.asset;remove.disabled=!d.asset&&!snapshot.instance?.portraitPath;if(p){zoom.value=p.zoom;zoomOut.textContent=`${Math.round(p.zoom*100)}%`;opacity.value=p.opacity;paint(layer,img,p,m);}};
  this.previewMedia=new ArtworkImage(img,this.load,draw);this.previewObserver=new ResizeObserver(()=>{if(d.prefs.mode!=='none')paint(layer,img,d.prefs[d.prefs.mode],d.prefs.mode);});this.previewObserver.observe(frame);
  const loadPreview=()=>{status.textContent='';draw();this.previewMedia.set(d.asset,d.scope,e=>{status.textContent=e.message;});};
  mode.onchange=()=>{d.prefs.mode=mode.value;draw();};zoom.oninput=()=>{const m=d.prefs.mode;if(m==='none')return;const p=d.prefs[m];d.prefs[m]={...p,zoom:Number(zoom.value)};draw();};opacity.oninput=()=>{const m=d.prefs.mode;if(m==='none')return;d.prefs[m].opacity=Number(opacity.value);draw();};
  const size=()=>({width:frame.clientWidth,height:frame.clientHeight}),imageSize=()=>({width:img.naturalWidth,height:img.naturalHeight});
  const zoomAt=(factor,x,y)=>{const m=d.prefs.mode;if(d.saving||m==='none'||!img.naturalWidth)return;d.prefs[m]=zoomArtwork(size(),imageSize(),d.prefs[m],m,factor,{x,y});draw();};
  const move=(dx,dy)=>{const m=d.prefs.mode;if(d.saving||m==='none'||!img.naturalWidth)return;const f=size(),p=d.prefs[m],g=artworkGeometry(f,imageSize(),p,m);if(!g)return;const next=artworkGeometry(f,imageSize(),{...p,x:g.x+dx/f.width,y:g.y+dy/f.height},m);d.prefs[m]={...p,x:next.x,y:next.y};draw();};
  frame.addEventListener('wheel',e=>{if(d.prefs.mode==='none'||!img.naturalWidth)return;e.preventDefault();const r=frame.getBoundingClientRect();zoomAt(Math.exp(-clamp(e.deltaY*(e.deltaMode===1?16:e.deltaMode===2?120:1),-240,240)*.0025),e.clientX-r.left,e.clientY-r.top);},{passive:false});
  let drag=null;frame.onpointerdown=e=>{if(e.button!==0||d.prefs.mode==='none'||!img.naturalWidth)return;drag={id:e.pointerId,x:e.clientX,y:e.clientY};frame.setPointerCapture(e.pointerId);frame.classList.add('dragging');e.preventDefault();};frame.onpointermove=e=>{if(drag?.id!==e.pointerId)return;move(e.clientX-drag.x,e.clientY-drag.y);drag.x=e.clientX;drag.y=e.clientY;};frame.onpointerup=frame.onpointercancel=frame.onlostpointercapture=()=>{drag=null;frame.classList.remove('dragging');};
  frame.onkeydown=e=>{const offsets={ArrowLeft:[-10,0],ArrowRight:[10,0],ArrowUp:[0,-10],ArrowDown:[0,10]};if(offsets[e.key]){e.preventDefault();move(...offsets[e.key]);}else if(['+','=','-'].includes(e.key)){e.preventDefault();zoomAt(e.key==='-'?.9:1.1,frame.clientWidth/2,frame.clientHeight/2);}};
  this.dialog.showModal();loadPreview();
 }
}
export function installAccentControl(trigger){
 const key='atlas-interface-accent-v1',dialog=node('dialog','atlas-accent-dialog');dialog.setAttribute('aria-label','Цвет интерфейса');document.body.append(dialog);const title=node('h3','','Акцентный цвет интерфейса'),choices=node('div','atlas-accent-choices');let selected=read(key,accentColors[0][0]);if(!accentColors.some(([color])=>color===selected))selected=accentColors[0][0];
 const apply=color=>{selected=color;document.documentElement.style.setProperty('--accent',color);for(const b of choices.children)b.setAttribute('aria-pressed',String(b.dataset.color===color));};
 for(const [color,name] of accentColors){const b=button(name,()=>{apply(color);write(key,color);});b.dataset.color=color;b.style.setProperty('--swatch',color);choices.append(b);}dialog.append(title,node('p','muted','Применяется ко всему интерфейсу в этом браузере.'),choices,button('Готово',()=>dialog.close()));dialog.onclick=e=>{if(e.target===dialog)dialog.close();};trigger.onclick=()=>dialog.showModal();apply(selected);
}
