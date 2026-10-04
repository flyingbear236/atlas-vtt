const SUPPORTED_SIDES=new Set([4,6,8,10,12,20]);
let rendererSequence=0;

export class DiceOutcomeMismatchError extends Error {
  constructor(expected,actual){super(`3D dice outcome mismatch: expected [${expected.join(', ')}], got [${actual.join(', ')}]`);this.name='DiceOutcomeMismatchError';this.expected=expected;this.actual=actual;}
}

export function predeterminedNotation(event){
  const count=Number(event?.count),sides=Number(event?.sides),results=event?.results;
  if(!Number.isSafeInteger(count)||count<1||!SUPPORTED_SIDES.has(sides)||!Array.isArray(results)||results.length!==count||results.some(value=>!Number.isSafeInteger(value)||value<1||value>sides))throw new Error('Invalid server RollEvent for 3D rendering');
  return `${count}d${sides}@${results.join(',')}`;
}

export function renderedValues(result,diceList){
  if(!Array.isArray(result?.sets))return [];
  return result.sets.flatMap(set=>Array.isArray(set?.rolls)?set.rolls.map(roll=>{
    // 0.0.12 leaves the pre-swap value in a d4 result record. Reading the
    // current face by roll id verifies the face which is actually rendered.
    const face=diceList?.[roll.id]?.getFaceValue?.();
    return Number(face?.value??roll.value);
  }):[]);
}

export function sameOrderedResults(expected,actual){return expected.length===actual.length&&expected.every((value,index)=>value===actual[index]);}
export function sameResultMultiset(expected,actual){return expected.length===actual.length&&[...expected].sort((a,b)=>a-b).every((value,index)=>value===[...actual].sort((a,b)=>a-b)[index]);}

function disposeMaterial(material,textures){
  if(!material)return;
  for(const value of Object.values(material))if(value&&typeof value==='object'&&value.isTexture&&typeof value.dispose==='function'&&!textures.has(value)){textures.add(value);value.dispose();}
  material.dispose?.();
}

export class DiceRendererAdapter {
  constructor({host=document.body,moduleLoader=()=>import('./vendor-dice-box-threejs.es.js'),windowObject=window,documentObject=document,sounds=true}={}){
    this.host=host;this.moduleLoader=moduleLoader;this.window=windowObject;this.document=documentObject;this.sounds=sounds;
    this.box=null;this.overlay=null;this.stage=null;this.initializing=null;this.resizeListeners=[];this.rafIds=new Set();
  }

  async init(){
    if(this.box?.initialized)return this;
    if(this.initializing)return this.initializing;
    this.initializing=this.#initialize();
    try{await this.initializing;return this;}finally{this.initializing=null;}
  }

  async #initialize(){
    const module=await this.moduleLoader(),DiceBox=module.default;
    if(typeof DiceBox!=='function')throw new Error('Invalid local dice renderer bundle');
    const overlay=this.document.createElement('div'),stage=this.document.createElement('div'),id=`atlas-dice-stage-${++rendererSequence}`;
    overlay.className='dice-overlay';overlay.setAttribute('aria-hidden','true');stage.id=id;stage.className='dice-stage';overlay.append(stage);this.host.append(overlay);this.overlay=overlay;this.stage=stage;
    const originalAdd=this.window.addEventListener;
    this.window.addEventListener=(type,listener,options)=>{if(type==='resize')this.resizeListeners.push({listener,options});return originalAdd.call(this.window,type,listener,options);};
    let initialization;
    try{
      this.box=new DiceBox(`#${id}`,{assetPath:'/dice-assets/',sounds:this.sounds,shadows:true,theme_colorset:'white',theme_material:'plastic'});
      this.#trackAnimationFrames(this.box);
      initialization=this.box.initialize();
    }catch(error){await this.cleanup();throw error;}finally{this.window.addEventListener=originalAdd;}
    try{await initialization;this.overlay.hidden=true;}catch(error){await this.cleanup();throw new Error(`3D renderer initialization failed: ${error?.message||error}`);}
  }

  #trackAnimationFrames(box){
    const raw=box.animateThrow;
    if(typeof raw!=='function')return;
    box.adaptive_timestep=true;
    const adapter=this;
    box.animateThrow=function(...args){
      const original=adapter.window.requestAnimationFrame;
      adapter.window.requestAnimationFrame=callback=>{let id;id=original.call(adapter.window,time=>{adapter.rafIds.delete(id);callback(time);});adapter.rafIds.add(id);return id;};
      try{return raw.apply(this,args);}finally{adapter.window.requestAnimationFrame=original;}
    };
  }

  async renderPredetermined(event){
    await this.init();
    const notation=predeterminedNotation(event),box=this.box;this.overlay.hidden=false;
    try{
      const result=await box.roll(notation),actual=renderedValues(result,box.diceList),reported=renderedValues(result),expected=[...event.results];
      if(!sameOrderedResults(expected,actual))throw new DiceOutcomeMismatchError(expected,actual);
      return {notation,values:actual,reportedValues:reported,result};
    }finally{if(this.box===box&&this.overlay)this.overlay.hidden=true;}
  }

  async cleanup(){
    const box=this.box;this.box=null;
    for(const id of this.rafIds)this.window.cancelAnimationFrame(id);this.rafIds.clear();
    for(const {listener,options} of this.resizeListeners)this.window.removeEventListener('resize',listener,options);this.resizeListeners=[];
    if(box){
      box.running=false;box.rolling=false;
      const textures=new Set(),materials=new Set(),geometries=new Set();
      box.scene?.traverse?.(object=>{
        if(object.geometry&&!geometries.has(object.geometry)){geometries.add(object.geometry);object.geometry.dispose?.();}
        const list=Array.isArray(object.material)?object.material:[object.material];
        for(const material of list)if(material&&!materials.has(material)){materials.add(material);disposeMaterial(material,textures);}
      });
      for(const body of [...(box.world?.bodies||[])])box.world.removeBody(body);
      box.renderer?.renderLists?.dispose?.();box.renderer?.dispose?.();box.renderer?.forceContextLoss?.();box.renderer?.domElement?.remove?.();
      box.diceList=[];box.meshes=[];box.bodies=[];box.renderer=null;box.scene=null;box.world=null;
    }
    this.overlay?.remove();this.overlay=null;this.stage=null;
  }
}
