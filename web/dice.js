export const DICE_SIDES=[4,6,8,10,12,20];
export const MAX_DICE_COUNT=100;
export const MAX_ROLL_JOURNAL=500;
export const MAX_ANIMATED_DICE=30;
export const MAX_PENDING_ANIMATIONS=8;
export const ROLL_ANIMATION_TIMEOUT_MS=20000;

export class DiceAnimator {
  constructor({createRenderer,timeoutMs=ROLL_ANIMATION_TIMEOUT_MS,onError=()=>{}}={}){
    this.createRenderer=createRenderer;this.timeoutMs=timeoutMs;this.onError=onError;this.sceneId='';this.userId='';this.policy='all';this.pending=[];this.active=null;this.renderer=null;this.working=false;this.generation=0;this.cancelCurrent=null;this.cleanupPromise=Promise.resolve();
  }

  setContext(sceneId,userId=''){
    sceneId=String(sceneId||'');userId=String(userId||'');
    if(sceneId!==this.sceneId){this.sceneId=sceneId;this.userId=userId;this.#interrupt([]);return;}
    this.userId=userId;
  }

  setPolicy(policy){
    if(!['all','self','off'].includes(policy)||policy===this.policy)return;
    this.policy=policy;const retained=this.pending.filter(event=>this.#eligible(event));
    if(policy==='off'||this.active&&!this.#eligible(this.active))this.#interrupt(retained);else this.pending=retained;
  }

  enqueue(event){
    if(!this.#eligible(event)||!Number.isSafeInteger(event.count)||event.count<1||event.count>MAX_ANIMATED_DICE)return false;
    if(this.working&&this.pending.length>=MAX_PENDING_ANIMATIONS)return false;
    this.pending.push(event);this.#drain();return true;
  }

  cancel(){this.#interrupt([]);return this.cleanupPromise;}
  destroy(){return this.cancel();}

  #eligible(event){return !!this.sceneId&&event?.sceneId===this.sceneId&&(this.policy==='all'||this.policy==='self'&&event.userId===this.userId);}

  #interrupt(retained){
    this.generation++;this.pending=retained;this.cancelCurrent?.(new DOMException('Dice animation cancelled','AbortError'));this.cancelCurrent=null;this.active=null;this.#queueCleanup();
  }

  #queueCleanup(){
    const renderer=this.renderer;this.renderer=null;
    if(renderer)this.cleanupPromise=this.cleanupPromise.then(()=>renderer.cleanup()).catch(error=>this.onError(error));
    this.cleanupPromise.finally(()=>{if(this.pending.length)this.#drain();});
  }

  async #dropRenderer(){const renderer=this.renderer;this.renderer=null;if(renderer)try{await renderer.cleanup();}catch(error){this.onError(error);}}

  async #drain(){
    if(this.working)return;this.working=true;
    try{
      while(this.pending.length){
        const event=this.pending.shift();if(!this.#eligible(event)||event.count>MAX_ANIMATED_DICE)continue;
        const generation=this.generation;await this.cleanupPromise;if(generation!==this.generation)continue;
        if(!this.renderer)this.renderer=this.createRenderer();this.active=event;
        let timeout,wasCancelled=false;
        const cancellation=new Promise((_,reject)=>{this.cancelCurrent=error=>{wasCancelled=true;reject(error);};});
        const timed=new Promise((_,reject)=>{timeout=setTimeout(()=>reject(new Error('3D dice animation timed out')),this.timeoutMs);});
        try{await Promise.race([this.renderer.renderPredetermined(event),cancellation,timed]);}
        catch(error){if(!wasCancelled&&generation===this.generation)this.onError(error);await this.#dropRenderer();}
        finally{clearTimeout(timeout);if(generation===this.generation){this.cancelCurrent=null;this.active=null;}}
      }
    }finally{this.working=false;if(this.pending.length)queueMicrotask(()=>this.#drain());}
  }
}

export function createDicePanel(inspector=document.querySelector('.inspector')){
  const root=document.createElement('section');root.id='dicePanel';root.className='dice-panel';root.hidden=true;
  root.innerHTML=`<div class="panel-heading">КУБИКИ</div><form class="dice-manual"><button class="subtle" type="button" data-dice-minus aria-label="Уменьшить число кубиков">−</button><input type="number" min="1" max="100" step="1" value="1" inputmode="numeric" data-dice-count aria-label="Число кубиков"><button class="subtle" type="button" data-dice-plus aria-label="Увеличить число кубиков">+</button><button class="dice-sides" type="button" data-dice-sides title="ЛКМ — следующий кубик, ПКМ — предыдущий">d20</button><button class="primary" type="submit">Бросить</button></form><label class="dice-animation">Анимации<select data-roll-animation><option value="all">Все броски</option><option value="self">Только мои</option><option value="off">Выключены</option></select></label><details class="roll-journal"><summary>Журнал бросков <span data-roll-count></span></summary><div class="roll-entries" data-roll-entries aria-live="polite"></div></details>`;
  const note=inspector?.querySelector('.inspector-note');
  if(note)inspector.insertBefore(root,note);else inspector?.append(root);
  return root;
}

export function normalizeDiceCount(value){
  const count=Number(value);
  return Number.isSafeInteger(count)&&count>=1&&count<=MAX_DICE_COUNT?count:null;
}

export function cycleDiceSides(current,direction=1){
  const index=DICE_SIDES.indexOf(Number(current));
  const start=index<0?DICE_SIDES.length-1:index;
  return DICE_SIDES[(start+(direction<0?-1:1)+DICE_SIDES.length)%DICE_SIDES.length];
}

function numberText(value){return Number.isInteger(value)?String(value):String(Number(value));}
function modifierText(value){return value>0?` + ${numberText(value)}`:value<0?` − ${numberText(-value)}`:'';}

export class DiceRuntime {
  constructor(root,{queue,getCharacterId=()=>'',toast=()=>{},onAnimate=()=>{},animator=null,storage=localStorage}={}){
    this.root=root;this.queue=queue;this.getCharacterId=getCharacterId;this.toast=toast;this.onAnimate=onAnimate;this.animator=animator;this.storage=storage;
    this.sceneId='';this.userId='';this.events=[];this.eventIds=new Set();this.sides=20;
    this.count=root.querySelector('[data-dice-count]');this.sidesButton=root.querySelector('[data-dice-sides]');this.entries=root.querySelector('[data-roll-entries]');this.journalCount=root.querySelector('[data-roll-count]');this.animation=root.querySelector('[data-roll-animation]');
    this.animation.value=this.#loadAnimationMode();this.animator?.setPolicy(this.animation.value);
    root.querySelector('[data-dice-minus]').addEventListener('click',()=>this.#adjustCount(-1));
    root.querySelector('[data-dice-plus]').addEventListener('click',()=>this.#adjustCount(1));
    this.sidesButton.addEventListener('click',()=>this.#cycleSides(1));
    this.sidesButton.addEventListener('contextmenu',event=>{event.preventDefault();this.#cycleSides(-1);});
    this.count.addEventListener('input',()=>this.#validateCount());
    this.count.addEventListener('blur',()=>this.#clampCount());
    this.animation.addEventListener('change',()=>{this.#saveAnimationMode();this.animator?.setPolicy(this.animation.value);});
    root.querySelector('form').addEventListener('submit',event=>{event.preventDefault();this.rollManual();});
    this.#renderSides();this.#updateCount();root.hidden=true;
  }

  setScene(sceneId,userId=''){
    sceneId=String(sceneId||'');
    if(sceneId!==this.sceneId){this.sceneId=sceneId;this.events=[];this.eventIds.clear();this.entries.replaceChildren();this.#updateCount();}
    this.userId=String(userId||'');this.animator?.setContext(sceneId,this.userId);this.root.hidden=!sceneId;
  }

  disconnect(){return this.animator?.cancel();}

  rollManual(){
    if(!this.sceneId)return false;
    const count=this.#validateCount();
    if(count===null){this.count.reportValidity();return false;}
    const roll={count,sides:this.sides},characterId=this.getCharacterId();
    if(characterId)roll.characterInstanceId=characterId;
    if(!this.queue?.('roll',{sceneId:this.sceneId,roll})){this.toast('Бросок не отправлен: нет соединения с сервером');return false;}
    return true;
  }

  rollAction(characterInstanceId,actionId,rollSpecId){
    if(!this.sceneId||!characterInstanceId||!actionId||!rollSpecId)return false;
    if(!this.queue?.('roll',{sceneId:this.sceneId,roll:{characterInstanceId,actionId,rollSpecId}})){this.toast('Бросок не отправлен: нет соединения с сервером');return false;}
    return true;
  }

  handle(message){
    if(message?.type!=='rollEvent'&&message?.type!=='rollHistory')return false;
    if(!this.sceneId||message.sceneId!==this.sceneId)return true;
    if(message.type==='rollHistory'){
      let changed=false;
      for(const event of message.events||[])changed=this.#remember(event)||changed;
      if(changed)this.#renderAll();
      return true;
    }
    const added=this.#remember(message.event);
    if(added){this.#append(this.events.at(-1));if(!message.replayed){if(this.animator)this.animator.enqueue(message.event);else if(this.#shouldAnimate(message.event))this.onAnimate(message.event);}}
    return true;
  }

  handleReceipt(command,message){
    if(command?.type!=='roll'||!message?.rollEvent||message.sceneId!==this.sceneId)return;
    if(this.#remember(message.rollEvent))this.#append(this.events.at(-1));
  }

  #remember(event){
    if(!event||event.sceneId!==this.sceneId||typeof event.id!=='string'||!event.id||this.eventIds.has(event.id)||!Array.isArray(event.results))return false;
    this.events.push(event);this.eventIds.add(event.id);
    if(this.events.length>MAX_ROLL_JOURNAL){const removed=this.events.shift();this.eventIds.delete(removed.id);}
    return true;
  }

  #append(event){
    this.entries.append(this.#row(event));
    while(this.entries.children.length>MAX_ROLL_JOURNAL)this.entries.firstElementChild.remove();
    this.#updateCount();
  }

  #renderAll(){
    const fragment=document.createDocumentFragment();
    for(const event of this.events)fragment.append(this.#row(event));
    this.entries.replaceChildren(fragment);this.#updateCount();
  }

  #row(event){
    const row=document.createElement('article');row.className='roll-entry';row.dataset.eventId=event.id;
    const heading=document.createElement('strong');heading.textContent=[event.authorName,event.characterName].filter(Boolean).join(' · ')||'Бросок';
    const source=document.createElement('span');source.className='roll-source';source.textContent=[event.actionName,event.rollSpecName].filter(Boolean).join(' · ')||'Ручной бросок';
    const notation=document.createElement('span');notation.className='roll-notation';notation.textContent=`${event.count}d${event.sides}${modifierText(Number(event.modifier)||0)}`;
    const result=document.createElement('span');result.className='roll-result';result.textContent=`[${event.results.join(', ')}]${modifierText(Number(event.modifier)||0)} = ${numberText(event.total)}`;
    row.append(heading,source,notation,result);return row;
  }

  #adjustCount(delta){const current=normalizeDiceCount(this.count.value)??1;this.count.value=String(Math.min(MAX_DICE_COUNT,Math.max(1,current+delta)));this.#validateCount();}
  #clampCount(){const raw=Number(this.count.value);this.count.value=String(Number.isFinite(raw)?Math.min(MAX_DICE_COUNT,Math.max(1,Math.trunc(raw))):1);this.#validateCount();}
  #validateCount(){const count=normalizeDiceCount(this.count.value);this.count.setCustomValidity(count===null?`Введите целое число от 1 до ${MAX_DICE_COUNT}`:'');return count;}
  #cycleSides(direction){this.sides=cycleDiceSides(this.sides,direction);this.#renderSides();}
  #renderSides(){this.sidesButton.textContent=`d${this.sides}`;this.sidesButton.dataset.sides=String(this.sides);}
  #updateCount(){this.journalCount.textContent=this.events.length?`(${this.events.length})`:'';}
  #shouldAnimate(event){return this.animation.value==='all'||this.animation.value==='self'&&event.userId===this.userId;}
  #loadAnimationMode(){try{const value=this.storage.getItem('atlas-dice-animation');return ['all','self','off'].includes(value)?value:'all';}catch{return 'all';}}
  #saveAnimationMode(){try{this.storage.setItem('atlas-dice-animation',this.animation.value);}catch{}}
}
