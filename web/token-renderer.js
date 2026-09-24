import {artworkSpec} from './rendering.js';

export function drawTokens({ctx,camera,viewport,dpr,tokens,index,visuals,moving,drag,selected,queue,requestImage,artwork,delta,floorId,alpha=1,walkableBounds,showOutOfBounds=false}){
 const margin=20/camera.scale;
 const candidates=new Map(index.query(camera.x-margin,camera.y-margin,camera.x+viewport.w/camera.scale+margin,camera.y+viewport.h/camera.scale+margin).filter(t=>t.floorId===floorId).map(t=>[t.id,t]));
 const finals=new Map();for(const c of queue)if(c.type==='final'&&tokens[c.token.id]?.floorId===floorId){finals.set(c.token.id,c.token);if(tokens[c.token.id])candidates.set(c.token.id,tokens[c.token.id]);}
 for(const id of moving)if(tokens[id]?.floorId===floorId)candidates.set(id,tokens[id]);
 if(drag?.type==='token'&&tokens[drag.id]?.floorId===floorId)candidates.set(drag.id,tokens[drag.id]);
 const items=[];let animating=false;
 for(const t of candidates.values()){
  const v=visuals.get(t.id)||{x:t.x,y:t.y},final=finals.get(t.id);
  if(drag?.id===t.id){v.x=drag.x;v.y=drag.y;}else if(final){v.x=final.x;v.y=final.y;}else{const a=Math.min(1,delta/70);v.x+=(t.x-v.x)*a;v.y+=(t.y-v.y)*a;if(Math.abs(t.x-v.x)+Math.abs(t.y-v.y)>.1){animating=true;moving.add(t.id);}else{v.x=t.x;v.y=t.y;moving.delete(t.id);}}
  visuals.set(t.id,v);const diameter=t.size*camera.scale,x=(v.x-camera.x)*camera.scale,y=(v.y-camera.y)*camera.scale;if(x+diameter/2<0||y+diameter/2<0||x-diameter/2>viewport.w||y-diameter/2>viewport.h)continue;
  const bitmap=t.asset?requestImage(`${t.asset}/token.png`,diameter*dpr):null,spec=artworkSpec(t,bitmap,diameter*dpr),outside=showOutOfBounds&&walkableBounds&&(v.x<walkableBounds.x||v.y<walkableBounds.y||v.x>walkableBounds.x+walkableBounds.width||v.y>walkableBounds.y+walkableBounds.height);items.push({t,x,y,diameter,spec,outside});
 }
 artwork.begin(items.flatMap(i=>i.diameter>=24||i.t.id===selected?[i.spec.key,`label:${i.t.name}`]:[i.spec.key]));ctx.save();ctx.setTransform(dpr,0,0,dpr,0,0);
 for(const {t,x,y,diameter,spec,outside}of items){
  ctx.globalAlpha=alpha*(t.hidden?.45:1);const image=artwork.get(spec.key,spec.w,spec.h,spec.paint),scale=diameter/spec.d,pad=spec.pad*scale;if(image)ctx.drawImage(image,x-diameter/2-pad,y-diameter/2-pad,diameter+2*pad,diameter+2*pad);else{ctx.save();ctx.translate(x-diameter/2-pad,y-diameter/2-pad);ctx.scale(scale,scale);spec.paint(ctx);ctx.restore();}
  if(t.id===selected||outside){ctx.beginPath();ctx.arc(x,y,diameter/2+5,0,Math.PI*2);ctx.strokeStyle=outside?'#ff6b63':'#edf8d5';ctx.lineWidth=outside?3:2;ctx.stroke();}
  if(diameter>=24||t.id===selected){const width=Math.max(16,Math.min(650,t.name.length*9+10)),label=artwork.get(`label:${t.name}`,width,20,g=>{g.font='11px system-ui';g.fillStyle='#0c1517cc';g.fillRect(0,0,width,20);g.fillStyle='#e9eadc';g.textAlign='center';g.textBaseline='middle';g.fillText(t.name,width/2,10);});if(label)ctx.drawImage(label,x-width/2,y+diameter/2+7);else{ctx.font='11px system-ui';ctx.fillStyle='#e9eadc';ctx.textAlign='center';ctx.fillText(t.name,x,y+diameter/2+19);}}
 }
 ctx.restore();return animating;
}
