import {inversePoint, tileAvailable} from './scene-content.js';

export const isSceneObject = element => !!element?.interaction && !element.interaction.disabled;
const branches = [['any','Любое'],['success','Успех'],['fail','Неудача']];
const hasText = message => !!message?.text?.trim();
const hasImage = message => !!message?.imageAssetId;
const emptyMessage = message => !hasText(message) && !hasImage(message);
const clone = value => structuredClone(value);
const node = (tag, cls, text) => {const e=document.createElement(tag);if(cls)e.className=cls;if(text!==undefined)e.textContent=text;return e;};
const button = (text, fn, cls='subtle') => {const e=node('button',cls,text);e.type='button';e.onclick=fn;return e;};

// One small mask per asset, including shared images. Keep a bounded LRU; paths
// are constructed only on first draw, not on every pointer event or frame.
export class ObjectMasks {
  constructor(limit=8*1024*1024,edge=256){this.limit=limit;this.edge=edge;this.entries=new Map();this.bytes=0;this.revision=0;}
  clear(){this.entries.clear();this.bytes=0;this.revision++;}
  get(asset){const m=this.entries.get(asset?.id);if(m){this.entries.delete(asset.id);this.entries.set(asset.id,m);}return m;}
  prepare(asset,requestImage){
    if(!asset)return null;const cached=this.get(asset);if(cached)return cached;
    const tiled=asset.renderMode==='tiled',z=asset.levels-1;
    const empty=tiled&&!tileAvailable(asset,z,0,0,1);
    const bitmap=empty?null:requestImage(tiled?`${asset.id}/${z}_0_0.png`:`${asset.id}/image.png`,this.edge);
    // Do not permanently cache a low-zoom 4x4 bitmap. Wait for the mask LOD.
    if(!empty&&(!bitmap||Math.max(bitmap.width,bitmap.height)<Math.min(this.edge,Math.max(asset.width,asset.height),tiled?Math.ceil(Math.max(asset.width,asset.height)/2**z):Infinity)))return null;
    const scale=Math.min(1,this.edge/Math.max(asset.width,asset.height)),w=Math.max(1,Math.round(asset.width*scale)),h=Math.max(1,Math.round(asset.height*scale));
    const c=typeof OffscreenCanvas!=='undefined'?new OffscreenCanvas(w,h):Object.assign(document.createElement('canvas'),{width:w,height:h});
    const ctx=c.getContext('2d',{willReadFrequently:true});if(bitmap)ctx.drawImage(bitmap,0,0,w,h);
    const pixels=ctx.getImageData(0,0,w,h).data,alpha=new Uint8Array(Math.ceil(w*h/8));for(let i=0;i<w*h;i++)if(pixels[i*4+3]>0)alpha[i>>3]|=1<<(i&7);
    const m={w,h,alpha,path:null,bytes:alpha.byteLength};this.entries.set(asset.id,m);this.revision++;this.bytes+=m.bytes;this.trim();return m;
  }
  trim(){while(this.bytes>this.limit&&this.entries.size>1){const [id,m]=this.entries.entries().next().value;this.bytes-=m.bytes;this.entries.delete(id);}}
  hit(element,asset,x,y){const m=this.get(asset);if(!m)return null;const p=inversePoint(element.transform,x,y),ix=Math.floor(p.x*m.w/element.transform.width),iy=Math.floor(p.y*m.h/element.transform.height);return ix>=0&&iy>=0&&ix<m.w&&iy<m.h&&!!(m.alpha[(iy*m.w+ix)>>3]&(1<<((iy*m.w+ix)&7)));}
  outline(m){
    if(m.path)return m.path;
    const {w,h,alpha}=m,path=new Path2D();let segments=0;
    const solid=(x,y)=>x>=0&&y>=0&&x<w&&y<h&&!!(alpha[(y*w+x)>>3]&(1<<((y*w+x)&7)));
    // Merge consecutive edges into line segments. This bounds the usual path
    // size to perimeter rather than area, even for opaque images.
    for(let y=0;y<=h;y++)for(let x=0;x<w;){if(solid(x,y-1)===solid(x,y)){x++;continue;}const start=x++;while(x<w&&solid(x,y-1)!==solid(x,y))x++;path.moveTo(start,y);path.lineTo(x,y);segments++;}
    for(let x=0;x<=w;x++)for(let y=0;y<h;){if(solid(x-1,y)===solid(x,y)){y++;continue;}const start=y++;while(y<h&&solid(x-1,y)!==solid(x,y))y++;path.moveTo(x,start);path.lineTo(x,y);segments++;}
    m.path=path;m.bytes+=segments*64;this.bytes+=segments*64;this.trim();return path;
  }
  draw(ctx,element,asset,camera,requestImage,glow,hovered){
    if(!isSceneObject(element))return;
    const mode=glow?.mode||'hover';
    // Masks are also needed for targeting with disabled glow. Loading remains
    // restricted to visible interactive elements by the scene render loop.
    const m=this.prepare(asset,requestImage);if(!m||mode==='never'||mode==='hover'&&element.id!==hovered)return;
    const t=element.transform,sx=t.width/m.w,sy=t.height/m.h,scale=Math.sqrt(sx*sy)*camera.scale;
    ctx.save();ctx.scale(sx,sy);ctx.strokeStyle=glow?.color||'#ffbc93';ctx.lineCap='round';ctx.lineJoin='round';const path=this.outline(m);const base=ctx.globalAlpha;
    ctx.globalAlpha=base*.22;ctx.lineWidth=7/scale;ctx.stroke(path);ctx.globalAlpha=base*.48;ctx.lineWidth=4/scale;ctx.stroke(path);ctx.globalAlpha=base;ctx.lineWidth=1.6/scale;ctx.stroke(path);ctx.restore();
  }
}

// Preview one selected cell at a time; large tiled handouts are composed at a
// bounded LOD (never load a full-size map for a modal or a hover tooltip).
export async function objectImageBlob(media,maxSide,fetchPart,signal){
  const a=media?.asset;if(!a)throw Error('Изображение недоступно');
  if(a.representationVersion==='raster-v3-shared-png-tile512')return fetchPart(media,maxSide<=256?'media-thumb.png':maxSide<=320?'hover.png':'artwork.png',signal);
  if(a.renderMode!=='tiled')return fetchPart(media,'image.png',signal);
  const z=Math.max(0,Math.min(a.levels-1,Math.ceil(Math.log2(Math.max(a.width,a.height)/maxSide)))),unit=512*2**z;
  const nx=Math.ceil(a.width/unit),ny=Math.ceil(a.height/unit);
  if(nx===1&&ny===1&&tileAvailable(a,z,0,0,nx))return fetchPart(media,`${z}_0_0.png`,signal);
  const w=Math.ceil(a.width/2**z),h=Math.ceil(a.height/2**z),c=Object.assign(document.createElement('canvas'),{width:w,height:h}),ctx=c.getContext('2d');
  // At most a few tiles at the selected overview LOD, decoded sequentially.
  for(let y=0;y<ny;y++)for(let x=0;x<nx;x++)if(tileAvailable(a,z,x,y,nx)){
    const blob=await fetchPart(media,`${z}_${x}_${y}.png`,signal),bitmap=await createImageBitmap(blob);try{if(signal.aborted)throw new DOMException('Aborted','AbortError');ctx.drawImage(bitmap,x*512,y*512);}finally{bitmap.close();}
  }
  return new Promise((resolve,reject)=>c.toBlob(blob=>blob?resolve(blob):reject(Error('Не удалось подготовить изображение')),'image/png'));
}
class MediaView {
  constructor(fetchPart){this.fetchPart=fetchPart;this.controller=null;this.url='';}
  clear(){this.controller?.abort();this.controller=null;if(this.url)URL.revokeObjectURL(this.url);this.url='';}
  async show(media,img,maxSide,onReady,onError){this.clear();const controller=this.controller=new AbortController();try{const blob=await objectImageBlob(media,maxSide,this.fetchPart,controller.signal);if(controller.signal.aborted)return;this.url=URL.createObjectURL(blob);img.src=this.url;await img.decode();if(!controller.signal.aborted)onReady?.();}catch(e){if(!controller.signal.aborted)onError?.(e);} }
}

// All private fields stay in this editor only. The scene snapshot contains
// the interaction flag, not the contents of the matrix.
export class SceneObjectEditor {
  constructor({read,upload,pick,save,optimistic,fetchPart,toast,context}){
    Object.assign(this,{read,upload,pick,save,optimistic,toast,context});this.avatarMedia=new MediaView(fetchPart);this.cellMedia=new MediaView(fetchPart);this.pending=new Map();this.request=0;this.model=null;this.active=['hover','any'];this.query='';this.busy=false;
    this.dialog=node('dialog','scene-object-dialog');this.dialog.setAttribute('aria-label','Настройки объекта');document.body.append(this.dialog);
    this.dialog.addEventListener('cancel',e=>{e.preventDefault();this.close();});
    this.dialog.addEventListener('click',e=>{if(e.target===this.dialog){const r=this.dialog.getBoundingClientRect();if(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom)this.close();}});
  }
  key(scene,id){return `${scene}/${id}`;}
  async open(id,enable=false){
    const scene=this.context();if(!scene||this.busy)return;if(this.model&&!this.close())return;
    const request=++this.request;try{await this.pending.get(this.key(scene,id))?.promise;const data=await this.read(scene,id);if(request!==this.request||scene!==this.context())return;
      const pending=this.pending.get(this.key(scene,id));this.model={scene,id,element:clone(pending?.element||data.element),actions:data.actions||[],assets:{...data.assets,...pending?.assets}};
      if(!this.model.element.interaction)this.model.element.interaction={};if(enable)this.model.element.interaction.disabled=false;
      this.model.element.interaction.reactions||={};this.active=['hover','any'];this.query='';this.changed=!!enable||!!pending;this.render();this.dialog.showModal();
      // Conversion is immediate in the map; persistence follows the same
      // reliable close/save path as the rest of the form.
      if(enable)this.optimistic(this.model.element,this.model.assets);
    }catch(e){if(request===this.request)this.toast(e.message);}
  }
  async refresh(){const model=this.model;if(!model)return;try{const data=await this.read(model.scene,model.id);if(this.model!==model)return;model.actions=data.actions||[];if(this.active[0]!=='hover'&&!model.actions.some(a=>a.id===this.active[0]))this.active=['hover','any'];this.renderMatrix();this.renderCell();}catch(e){if(this.model===model)this.toast(e.message);}}
  get message(){const [id,branch]=this.active,i=this.model.element.interaction;if(id==='hover')return i.hover||{};return i.reactions[id]?.[branch]||{};}
  setMessage(message){const previous=this.message,changed=hasText(previous)!==hasText(message)||hasImage(previous)!==hasImage(message);const [id,branch]=this.active,i=this.model.element.interaction;if(id==='hover')i.hover=message;else{i.reactions[id]||={};i.reactions[id][branch]=message;}this.changed=true;if(changed)this.renderMatrix();}
  render(){
    this.avatarMedia.clear();this.cellMedia.clear();this.dialog.replaceChildren();const e=this.model.element;
    const top=node('div','scene-object-heading');top.append(node('h2','', 'Настройки объекта'),button('×',()=>this.close(),'icon subtle'));this.dialog.append(top);
    const meta=node('div','scene-object-meta'),avatar=button('',()=>this.pickFile('avatar'),'scene-object-avatar subtle');avatar.title='Заменить изображение объекта';const img=node('img');img.alt='Изображение объекта';avatar.append(img);meta.append(avatar);this.avatarImg=img;
    const title=node('label','scene-object-name','Название'),input=node('input');input.maxLength=120;input.value=e.name||'';input.placeholder='Объект';input.oninput=()=>{e.name=input.value;this.changed=true;};title.append(input);meta.append(title);this.dialog.append(meta);
    const media=this.model.assets[e.assetId];if(media)this.avatarMedia.show({asset:media,editor:true},img,256,null,()=>{});
    const toolbar=node('div','scene-object-toolbar');toolbar.append(node('strong','','Реакции'));const search=node('input');search.type='search';search.placeholder='Найти действие';search.value=this.query;search.oninput=()=>{this.query=search.value;this.renderMatrix();};toolbar.append(search);this.dialog.append(toolbar);
    this.matrix=node('div','scene-object-matrix');this.cell=node('div','scene-object-cell-editor');this.dialog.append(this.matrix,this.cell);this.status=node('small','scene-object-status','Изменения сохраняются при закрытии.');this.dialog.append(this.status);this.renderMatrix();this.renderCell();
  }
  renderMatrix(){
    if(!this.matrix||!this.model)return;const scroll=this.matrix.scrollTop;this.matrix.replaceChildren();const table=node('table','scene-object-reactions'),head=node('thead'),header=node('tr');for(const label of ['Действие',...branches.map(b=>b[1])])header.append(node('th','',label));head.append(header);table.append(head);const body=node('tbody');
    const add=(id,name,cells)=>{const tr=node('tr'),label=node('th');label.scope='row';label.append(node('span','',name));if(id!=='hover')label.append(node('small','',id));tr.append(label);
      for(const [branch] of branches){const td=node('td');if(id==='hover'&&branch!=='any'){td.className='scene-object-na';td.textContent='—';}else{const m=cells?.[branch]||{},selected=this.active[0]===id&&this.active[1]===branch;const b=button('',()=>{this.active=[id,branch];this.renderMatrix();this.renderCell();},`scene-object-slot${selected?' active':''}${emptyMessage(m)?'':' filled'}`);b.title=[hasText(m)?'Текст':null,hasImage(m)?'Изображение':null].filter(Boolean).join(' и ')||'Добавить реакцию';b.setAttribute('aria-label',`${name} · ${branch} · ${b.title}`);if(emptyMessage(m))b.textContent='＋';else{if(hasText(m))b.append(node('span','scene-object-text-icon','Т'));if(hasImage(m))b.append(node('span','scene-object-image-icon','▧'));}td.append(b);}tr.append(td);}body.append(tr);};
    add('hover','Alt + наведение',{any:this.model.element.interaction.hover});
    const q=this.query.trim().toLocaleLowerCase('ru');for(const a of this.model.actions)if(!q||`${a.name} ${a.id}`.toLocaleLowerCase('ru').includes(q))add(a.id,a.name,this.model.element.interaction.reactions[a.id]);
    if(!this.model.actions.length){const tr=node('tr'),td=node('td','scene-object-na','В ruleset нет действий с select(object).');td.colSpan=4;tr.append(td);body.append(tr);}table.append(body);this.matrix.append(table);this.matrix.scrollTop=scroll;
  }
  renderCell(){
    this.cellMedia.clear();this.cell.replaceChildren();const [id,branch]=this.active,m=this.message,name=id==='hover'?'Alt + наведение':this.model.actions.find(a=>a.id===id)?.name||id;
    const top=node('div','scene-object-cell-head');top.append(node('strong','',id==='hover'?name:`${name} · ${branches.find(b=>b[0]===branch)[1]}`));this.cell.append(top);
    const fields=node('div','scene-object-cell-fields'),text=node('textarea');text.rows=3;text.maxLength=8192;text.placeholder='Текст сообщения (необязательно)';text.value=m.text||'';text.oninput=()=>this.setMessage({...this.message,text:text.value});fields.append(text);
    const image=node('div','scene-object-image-slot');if(m.imageAssetId){const preview=node('img');preview.alt='Изображение сообщения';image.append(preview);const controls=node('div','scene-object-image-controls');controls.append(button('Заменить',()=>this.pickFile('message')),button('×',()=>{this.setMessage({...this.message,imageAssetId:''});this.renderCell();},'subtle'));image.append(controls);const asset=this.model.assets[m.imageAssetId];if(asset)this.cellMedia.show({asset,editor:true},preview,192,null,e=>this.toast(e.message));}else image.append(button('＋ Артворк',()=>this.pickFile('message'),'scene-object-add-image subtle'));fields.append(image);this.cell.append(fields);
  }
  pickFile(kind){if(this.busy||!this.model)return;this.uploadFile(kind);}
  async uploadFile(kind,file){const model=this.model,active=[...this.active];this.busy=true;this.status.textContent='Подготовка изображения…';try{const asset=this.pick?(await this.pick({title:kind==='avatar'?'Изображение объекта':'Артворк реакции',variant:kind==='avatar'?'scene':active[0]==='hover'?'hover.png':'artwork.png'}))?.asset:await this.upload(model.scene,file);if(this.model!==model||!asset)return;model.assets[asset.id]=asset;this.changed=true;if(kind==='avatar'){model.element.assetId=asset.id;this.optimistic(model.element,model.assets);this.avatarMedia.show({asset,editor:true},this.avatarImg,256,null,e=>this.toast(e.message));}else{const old=this.active;this.active=active;this.setMessage({...this.message,imageAssetId:asset.id});this.active=old;this.renderMatrix();this.renderCell();}}catch(e){this.toast(e.message);}finally{this.busy=false;if(this.model===model)this.status.textContent='Изменения сохраняются при закрытии.';}}
  close(force=false){
    if(!this.model){this.request++;return true;}if(this.busy&&!force){this.toast('Дождитесь окончания загрузки изображения.');return false;}
    const model=this.model;if(this.changed){const e=clone(model.element),i=e.interaction;e.name=e.name.trim()||'Объект';if(i.hover&&emptyMessage(i.hover))delete i.hover;for(const [id,r] of Object.entries(i.reactions||{})){for(const [key] of branches)if(emptyMessage(r[key]))delete r[key];if(!Object.keys(r).length)delete i.reactions[id];}
      const key=this.key(model.scene,model.id),draft={element:e,assets:model.assets};this.pending.set(key,draft);
      if(model.scene===this.context())this.optimistic(e,model.assets);
      draft.promise=Promise.resolve(this.save(model.scene,model.id,{name:e.name,assetId:e.assetId,interaction:i})).then(()=>{if(this.pending.get(key)===draft)this.pending.delete(key);}).catch(error=>{this.toast(`Настройки не сохранены: ${error.message}. Откройте объект повторно для восстановления черновика.`);});
    }
    this.request++;this.avatarMedia.clear();this.cellMedia.clear();this.dialog.close();this.model=null;return true;
  }
  hasPendingAsset(id){return this.model?.element.assetId===id||Array.from(this.pending.values()).some(d=>d.element.assetId===id);}
  reset(){this.close(true);this.request++;}
}

export class ObjectPresentation {
  constructor({send,context,fetchPart,toast,onHover}){
    Object.assign(this,{send,context,toast,onHover});this.hoverMedia=new MediaView(fetchPart);this.resultMedia=new MediaView(fetchPart);this.hovered=null;this.alt=false;this.seq=0;this.openTimer=null;this.closeTimer=null;this.last=null;this.imageQueue=[];this.seenImages=new Set();
    this.tooltip=node('div','scene-object-tooltip');this.tooltip.hidden=true;document.body.append(this.tooltip);
    this.dialog=node('dialog','scene-object-image-dialog');this.dialog.setAttribute('aria-label','Изображение результата действия');document.body.append(this.dialog);
    this.dialog.addEventListener('click',e=>{if(e.target===this.dialog)this.closeImage();});this.dialog.addEventListener('cancel',e=>{e.preventDefault();this.closeImage();});
  }
  pointer(element,x,y,alt){
    const id=element?.id||'',scene=this.context(),previous=this.hovered;this.alt=alt;this.last={element,x,y};this.hovered=id;
    if(id!==previous)this.onHover?.(element?.characterTokenId?'':id);
    if(!alt){this.hideHover();return;}
    if(!id){if(this.tooltip.hidden){this.hideHover();return;}clearTimeout(this.openTimer);this.openTimer=null;if(!this.tooltip.hidden&&!this.closeTimer)this.closeTimer=setTimeout(()=>this.hideHover(),500);return;}
    clearTimeout(this.closeTimer);this.closeTimer=null;
    if(this.currentID===id&&this.currentScene===scene){if(!this.tooltip.hidden)this.position(x,y);return;}
    this.hideHover();this.currentID=id;this.currentScene=scene;const seq=++this.seq;
    this.openTimer=setTimeout(()=>{this.openTimer=null;if(this.alt&&this.hovered===id&&this.context()===scene)this.send(element?.characterTokenId?{type:'characterHover',sceneId:scene,seq,tokenId:element.characterTokenId}:{type:'objectHover',sceneId:scene,seq,element:{id}});},150);
  }
  setAlt(value){if(value===this.alt)return;if(!value){this.alt=false;this.hideHover();}else if(this.last)this.pointer(this.last.element,this.last.x,this.last.y,true);else this.alt=true;}
  position(x,y){const r=this.tooltip.getBoundingClientRect();let left=x+16,top=y+16;if(left+r.width>innerWidth-8)left=x-r.width-12;if(top+r.height>innerHeight-8)top=y-r.height-12;this.tooltip.style.left=`${Math.max(8,Math.min(left,innerWidth-r.width-8))}px`;this.tooltip.style.top=`${Math.max(8,Math.min(top,innerHeight-r.height-8))}px`;}
  hideHover(){if(!this.currentID&&this.tooltip.hidden)return;clearTimeout(this.openTimer);clearTimeout(this.closeTimer);this.openTimer=this.closeTimer=null;this.currentID='';this.currentScene='';this.seq++;this.tooltip.hidden=true;this.tooltip.replaceChildren();this.hoverMedia.clear();}
  handle(msg){
    if(msg.type==='objectHover'){if(msg.sceneId!==this.context()||msg.seq!==this.seq||!this.alt||msg.id!==this.hovered)return true;if(!msg.text&&!msg.media)return true;this.tooltip.replaceChildren();if(msg.text)this.tooltip.append(node('div','scene-object-hover-text',msg.text));if(msg.media){const img=node('img');img.alt=msg.id.startsWith('character:')?'Артворк персонажа':'Описание объекта';this.tooltip.append(img);this.hoverMedia.show(msg.media,img,320,()=>{if(this.last)this.position(this.last.x,this.last.y);},()=>img.remove());}this.tooltip.hidden=false;if(this.last)this.position(this.last.x,this.last.y);return true;}
    if(msg.type==='objectImage'){if(msg.sceneId===this.context()&&!this.seenImages.has(msg.executionId)){if(msg.executionId){this.seenImages.add(msg.executionId);if(this.seenImages.size>128)this.seenImages.delete(this.seenImages.values().next().value);}if(this.dialog.open){if(this.imageQueue.length<8)this.imageQueue.push({...msg.media,reactionText:msg.text||''});}else this.showImage({...msg.media,reactionText:msg.text||''});}return true;}return false;
  }
  showImage(media){this.dialog.replaceChildren();const frame=node('div','scene-object-result-frame'),img=node('img');img.alt='Изображение результата действия';const status=node('span','scene-object-image-status','Загрузка изображения…');frame.append(button('×',()=>this.closeImage(),'scene-object-result-close subtle'),status,img);if(media.reactionText)frame.append(node('div','scene-object-result-caption',media.reactionText));this.dialog.append(frame);this.dialog.showModal();this.resultMedia.show(media,img,1536,()=>status.remove(),e=>{status.textContent=e.message;});}
  closeImage(){this.resultMedia.clear();if(this.dialog.open)this.dialog.close();const next=this.imageQueue.shift();if(next)this.showImage(next);}
  reset(){this.alt=false;this.last=null;this.hovered=null;this.onHover?.('');this.hideHover();this.imageQueue=[];this.resultMedia.clear();if(this.dialog.open)this.dialog.close();}
}
