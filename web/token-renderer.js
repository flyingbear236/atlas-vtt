import {artworkSpec} from './rendering.js';

export function shortCharacterID(id){return String(id||'').slice(0,8);}
export function tokenDisplayName(token,showCharacterIDs=false){const name=token?.name||'';return showCharacterIDs&&token?.characterInstanceId?`[${shortCharacterID(token.characterInstanceId)}] ${name}`:name;}

function displayScalar(value){if(!value)return '—';if(value.type==='boolean')return value.value?'✓':'□';if(value.type==='string')return value.value===''?'—':String(value.value);return String(value.value);}

export function resourceOverlayPresentation(definition,resource,config){
 if(!definition||!resource||!config)return null;const current=displayScalar(resource.current),initial=displayScalar(resource.initial),prefix=config.showName?`${definition.name||config.statId}: `:'';
 if(definition.type==='boolean')return {kind:'text',text:`${definition.name||config.statId}: ${resource.current?.value?'Да':'Нет'}`,color:config.color};
 if(definition.type==='string')return {kind:'text',text:`${definition.name||config.statId}: ${current}`,color:config.color};
 if((definition.type==='number'||definition.type==='integer')&&config.mode==='bar'){const currentNumber=Number(resource.current?.value),initialNumber=Number(resource.initial?.value);if(Number.isFinite(currentNumber)&&Number.isFinite(initialNumber)&&initialNumber>0)return {kind:'bar',ratio:Math.max(0,Math.min(1,currentNumber/initialNumber)),label:config.showName?(definition.name||config.statId):'',color:config.color};}
 return {kind:'text',text:`${prefix}${current} / ${initial}`,color:config.color};
}

export function truncateOverlayText(ctx,text,maxWidth){
 // Bound per-frame text measurement cost for unusually long user-defined strings.
 if(text.length>180)text=text.slice(0,180)+'…';
 if(ctx.measureText(text).width<=maxWidth)return text;
 const suffix='…';let lo=0,hi=text.length;while(lo<hi){const mid=Math.ceil((lo+hi)/2);if(ctx.measureText(text.slice(0,mid)+suffix).width<=maxWidth)lo=mid;else hi=mid-1;}return text.slice(0,lo)+suffix;
}

function drawResourceOverlay(ctx,item,config,definitions,display){
 const resource=display?.resources?.[config.statId],definition=definitions?.[config.statId],presentation=resourceOverlayPresentation(definition,resource,config);if(!presentation)return;
 const slot=Math.max(1,Math.min(2,config.slot||1)),top=config.position==='top',baseY=top?item.y-item.diameter/2-9-(slot-1)*16:item.y+item.diameter/2+34+(slot-1)*16;ctx.save();ctx.globalAlpha=item.alpha;ctx.font='10px system-ui';ctx.textBaseline='middle';
 if(presentation.kind==='bar'){const width=Math.max(36,Math.min(92,item.diameter)),height=7,left=item.x-width/2,y=baseY-height/2;ctx.fillStyle='#0c1517dd';ctx.fillRect(left-1,y-1,width+2,height+2);ctx.fillStyle='#33413f';ctx.fillRect(left,y,width,height);ctx.fillStyle=presentation.color;ctx.fillRect(left,y,width*presentation.ratio,height);if(presentation.label){ctx.fillStyle='#e9eadc';ctx.textAlign='center';ctx.fillText(presentation.label,item.x,baseY-8);}}
 else{const text=truncateOverlayText(ctx,presentation.text,172),width=Math.max(28,Math.min(180,ctx.measureText(text).width+8));ctx.fillStyle='#0c1517dd';ctx.fillRect(item.x-width/2,baseY-7,width,14);ctx.fillStyle=presentation.color;ctx.textAlign='center';ctx.fillText(text,item.x,baseY);}ctx.restore();
}

export function drawTokens({ctx,camera,viewport,dpr,tokens,index,visuals,moving,drag,selected,queue,requestImage,artwork,delta,floorId,alpha=1,outOfBounds,resourceVisible,characterDisplays={},resourceOverlays=[],resourceOverlayDefinitions={},showCharacterIDs=false}){
 const margin=20/camera.scale;
 const candidates=new Map(index.query(camera.x-margin,camera.y-margin,camera.x+viewport.w/camera.scale+margin,camera.y+viewport.h/camera.scale+margin).filter(t=>t.floorId===floorId).map(t=>[t.id,t]));
 const finals=new Map();for(const c of queue)if(c.type==='final'&&tokens[c.token.id]?.floorId===floorId){finals.set(c.token.id,c.token);if(tokens[c.token.id])candidates.set(c.token.id,tokens[c.token.id]);}
 for(const id of moving)if(tokens[id]?.floorId===floorId)candidates.set(id,tokens[id]);if(drag?.type==='token'&&tokens[drag.id]?.floorId===floorId)candidates.set(drag.id,tokens[drag.id]);
 const items=[];let animating=false;
 for(const t of candidates.values()){
  const v=visuals.get(t.id)||{x:t.x,y:t.y},final=finals.get(t.id);if(drag?.id===t.id){v.x=drag.x;v.y=drag.y;}else if(final){v.x=final.x;v.y=final.y;}else{const a=Math.min(1,delta/70);v.x+=(t.x-v.x)*a;v.y+=(t.y-v.y)*a;if(Math.abs(t.x-v.x)+Math.abs(t.y-v.y)>.1){animating=true;moving.add(t.id);}else{v.x=t.x;v.y=t.y;moving.delete(t.id);}}
  visuals.set(t.id,v);const diameter=t.size*camera.scale,x=(v.x-camera.x)*camera.scale,y=(v.y-camera.y)*camera.scale;if(x+diameter/2<0||y+diameter/2<0||x-diameter/2>viewport.w||y-diameter/2>viewport.h||resourceVisible&&!resourceVisible(v.x,v.y,t.size/2))continue;
  const bitmap=t.asset?requestImage(`${t.asset}/token.png`,diameter*dpr):null,spec=artworkSpec(t,bitmap,diameter*dpr),outside=!!outOfBounds?.(v.x,v.y),name=tokenDisplayName(t,showCharacterIDs);items.push({t,x,y,diameter,spec,outside,name,alpha:alpha*(t.hidden?.45:1)*(t.opacity??1)});
 }
 artwork.begin(items.flatMap(i=>i.diameter>=24||i.t.id===selected?[i.spec.key,`label:${i.name}`]:[i.spec.key]));ctx.save();ctx.setTransform(dpr,0,0,dpr,0,0);
 for(const item of items){const {t,x,y,diameter,spec,outside,name}=item;ctx.globalAlpha=item.alpha;const image=artwork.get(spec.key,spec.w,spec.h,spec.paint),scale=diameter/spec.d,pad=spec.pad*scale;if(image)ctx.drawImage(image,x-diameter/2-pad,y-diameter/2-pad,diameter+2*pad,diameter+2*pad);else{ctx.save();ctx.translate(x-diameter/2-pad,y-diameter/2-pad);ctx.scale(scale,scale);spec.paint(ctx);ctx.restore();}
  if(t.id===selected||outside){ctx.beginPath();ctx.arc(x,y,diameter/2+5,0,Math.PI*2);ctx.strokeStyle=outside?'#ff6b63':'#edf8d5';ctx.lineWidth=outside?3:2;ctx.stroke();}
  if(diameter>=24||t.id===selected){const width=Math.max(16,Math.min(650,name.length*9+10)),label=artwork.get(`label:${name}`,width,20,g=>{g.font='11px system-ui';g.fillStyle='#0c1517cc';g.fillRect(0,0,width,20);g.fillStyle='#e9eadc';g.textAlign='center';g.textBaseline='middle';g.fillText(name,width/2,10);});if(label)ctx.drawImage(label,x-width/2,y+diameter/2+7);else{ctx.font='11px system-ui';ctx.fillStyle='#e9eadc';ctx.textAlign='center';ctx.fillText(name,x,y+diameter/2+19);}}
  const display=characterDisplays?.[t.characterInstanceId];for(const config of resourceOverlays||[])if(config.enabled)drawResourceOverlay(ctx,item,config,resourceOverlayDefinitions,display);
 }
 ctx.restore();return animating;
}
