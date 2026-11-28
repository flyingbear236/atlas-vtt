// Ephemeral, server-confirmed action feedback. No map state is mutated.
const FLIGHT=300,HIT=1050,MAX_ACTIVE=32,MAX_TARGETS=8;
const clamp=(v,min,max)=>Math.max(min,Math.min(max,v));
const progress=(value,duration)=>clamp(value/duration,0,1);
export function impactLabel(change){
 const delta=String(change.delta);
 return `${change.negative?'−':'+'}${delta.replace(/^[-+]/,'')} ${change.name}`;
}
export function impactShake(elapsed,negative){
 if(!negative||elapsed<0||elapsed>=320)return 0;
 return Math.sin(elapsed*.075)*5*(1-elapsed/320);
}
export class ActionImpacts{
 constructor(){this.entries=[];this.offsets=new Map();this.seen=new Set();}
 get active(){return this.entries.length>0;}
 clear(){this.entries.length=0;this.offsets.clear();this.seen.clear();}
 add(event,tokens,now){
  if(!event.executionId||this.seen.has(event.executionId)||!Array.isArray(event.changes)||!event.changes.length)return false;
  this.seen.add(event.executionId);if(this.seen.size>128)this.seen.delete(this.seen.values().next().value);
  const changes=event.changes.filter(c=>typeof c.name==='string'&&typeof c.delta==='string'&&Number.isFinite(Number(c.delta))&&Number(c.delta)!==0).map(c=>({...c,label:impactLabel(c).slice(0,160)}));
  if(!changes.length)return false;
  const actors=[],targets=[];
  for(const token of Object.values(tokens||{})){
   if(token.characterInstanceId===event.actorId)actors.push(token);
   if(token.characterInstanceId===event.targetId)targets.push(token);
  }
  // One character can have several token representations. Play on each loaded
  // representation, choosing the closest actor on the same floor.
  for(const target of targets.slice(0,MAX_TARGETS)){
   let source=null,distance=Infinity;
   for(const actor of actors){if(actor.floorId!==target.floorId)continue;const d=(actor.x-target.x)**2+(actor.y-target.y)**2;if(d<distance){distance=d;source=actor;}}
   const flight=source&&source.id!==target.id?FLIGHT:0;
   const stagger=target.id===this.entries.at(-1)?.targetId?70:0;
   this.entries.push({targetId:target.id,sourceId:source?.id,floorId:target.floorId,start:now+stagger,flight,changes,negative:changes.some(c=>c.negative),positive:changes.some(c=>!c.negative)});
  }
  if(this.entries.length>MAX_ACTIVE)this.entries.splice(0,this.entries.length-MAX_ACTIVE);
  return targets.length>0;
 }
 update(now){
  this.offsets.clear();
  if(!this.entries.length)return;
  this.entries=this.entries.filter(e=>now-e.start-e.flight<HIT);
  for(const entry of this.entries){const shake=impactShake(now-entry.start-entry.flight,entry.negative);if(shake)this.offsets.set(entry.targetId,clamp((this.offsets.get(entry.targetId)||0)+shake,-7,7));}
 }
 draw({ctx,camera,viewport,dpr,tokens,visuals,floorId,alpha,resourceVisible,now}){
  if(!this.entries.length)return;
  const position=id=>{const t=tokens[id];if(!t||t.floorId!==floorId)return null;const p=visuals.get(id)||t;
   if(resourceVisible&&!resourceVisible(p.x,p.y,t.size/2))return null;
   return {x:(p.x-camera.x)*camera.scale,y:(p.y-camera.y)*camera.scale,r:t.size*camera.scale/2,alpha:(t.hidden?.45:1)*(t.opacity??1)};};
  ctx.save();ctx.setTransform(dpr,0,0,dpr,0,0);
  for(const e of this.entries){
   if(e.floorId!==floorId||now<e.start)continue;
   const target=position(e.targetId);if(!target||target.alpha<=0)continue;
   const elapsed=now-e.start,hit=elapsed-e.flight;
   ctx.globalAlpha=alpha*target.alpha;
   if(hit<0){
    const source=position(e.sourceId);if(!source)continue;
    const p=progress(elapsed,e.flight),tail=Math.max(0,p-.16),dx=target.x-source.x,dy=target.y-source.y;
    const x=source.x+dx*p,y=source.y+dy*p;
    ctx.strokeStyle='#ff534b';ctx.lineWidth=3;ctx.lineCap='round';ctx.beginPath();ctx.moveTo(source.x+dx*tail,source.y+dy*tail);ctx.lineTo(x,y);ctx.stroke();
    const glow=ctx.createRadialGradient(x,y,0,x,y,10);glow.addColorStop(0,'#fff2c4');glow.addColorStop(.28,'#ff453d');glow.addColorStop(1,'#ff453d00');ctx.fillStyle=glow;ctx.beginPath();ctx.arc(x,y,10,0,Math.PI*2);ctx.fill();continue;
   }
   if(target.x+target.r<0||target.y+target.r<0||target.x-target.r>viewport.w||target.y-target.r>viewport.h)continue;
   const p=progress(hit,HIT),fade=1-progress(hit-650,HIT-650);
   if(e.positive){
    const radius=clamp(target.r,10,90)+8+16*p;
    ctx.fillStyle='#b9ffb9';ctx.globalAlpha=alpha*target.alpha*fade;
    for(let i=0;i<8;i++){const angle=i*Math.PI/4+p*.7,twinkle=.5+.5*Math.sin(i*2.7+hit*.018),x=target.x+Math.cos(angle)*radius,y=target.y+Math.sin(angle)*radius;
     const size=2+2*twinkle;ctx.beginPath();ctx.moveTo(x,y-size);ctx.lineTo(x+size*.65,y);ctx.lineTo(x,y+size);ctx.lineTo(x-size*.65,y);ctx.closePath();ctx.fill();}
   }
   ctx.globalAlpha=alpha*target.alpha*fade;ctx.font='700 16px system-ui';ctx.textAlign='left';ctx.textBaseline='middle';ctx.lineJoin='round';ctx.lineWidth=3;ctx.strokeStyle='#14201e';
   const x=clamp(target.x+Math.min(target.r*.4,30)+8,6,Math.max(6,viewport.w-180));
   const y=target.y-target.r-14-36*p;
   e.changes.forEach((c,i)=>{ctx.fillStyle=c.negative?'#ff6861':'#80efa0';ctx.strokeText(c.label,x,y-i*19,Math.max(80,viewport.w-x-6));ctx.fillText(c.label,x,y-i*19,Math.max(80,viewport.w-x-6));});
  }
  ctx.restore();
 }
}
