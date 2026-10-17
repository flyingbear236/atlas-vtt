export const DICE_SIDES=[4,6,8,10,12,20];
export const MAX_DICE_COUNT=100;
export const MAX_ROLL_JOURNAL=500;
export const MAX_ANIMATED_DICE=30;
export const MAX_PENDING_ANIMATIONS=8;
export const ROLL_ANIMATION_TIMEOUT_MS=20000;
export const MAX_DICE_GROUPS=16;

function eventDiceCount(event){return Array.isArray(event?.groups)&&event.groups.length?event.groups.reduce((sum,group)=>sum+Number(group.count||0),0):Number(event?.count);}

export class DiceAnimator {
  constructor({createRenderer,timeoutMs=ROLL_ANIMATION_TIMEOUT_MS,onError=()=>{},onFinished=()=>{}}={}){
    this.createRenderer=createRenderer;this.timeoutMs=timeoutMs;this.onError=onError;this.onFinished=onFinished;this.sceneId='';this.userId='';this.policy='all';this.pending=[];this.active=null;this.renderer=null;this.working=false;this.generation=0;this.cancelCurrent=null;this.cleanupPromise=Promise.resolve();this.warming=null;
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
    const count=eventDiceCount(event);if(!this.#eligible(event)||!Number.isSafeInteger(count)||count<1||count>MAX_ANIMATED_DICE)return false;
    if(this.working&&this.pending.length>=MAX_PENDING_ANIMATIONS)return false;
    this.pending.push(event);this.#drain();return true;
  }

  warmup(){
    if(!this.sceneId||this.policy==='off'||this.renderer||this.working)return this.warming||Promise.resolve();
    if(this.warming)return this.warming;
    const generation=this.generation,priorCleanup=this.cleanupPromise;
    const warming=(async()=>{
      await priorCleanup;
      if(generation!==this.generation||this.renderer||this.working)return;
      const renderer=this.createRenderer();this.renderer=renderer;
      try{await renderer.init();}
      catch(error){if(generation===this.generation)this.onError(error);if(this.renderer===renderer)await this.#dropRenderer();}
    })();
    this.warming=warming;warming.finally(()=>{if(this.warming===warming)this.warming=null;});
    return warming;
  }

  cancel(){this.#interrupt([]);return this.cleanupPromise;}
  destroy(){return this.cancel();}

  #eligible(event){return !!this.sceneId&&event?.sceneId===this.sceneId&&(this.policy==='all'||this.policy==='self'&&event.userId===this.userId);}

  #interrupt(retained){
    this.generation++;this.pending=retained;this.cancelCurrent?.(new DOMException('Dice animation cancelled','AbortError'));this.cancelCurrent=null;this.active=null;this.#queueCleanup();
  }

  #queueCleanup(){
    const renderer=this.renderer,warming=this.warming;this.renderer=null;
    if(renderer)this.cleanupPromise=this.cleanupPromise.then(()=>warming).catch(()=>{}).then(()=>renderer.cleanup()).catch(error=>this.onError(error));
    this.cleanupPromise.finally(()=>{if(this.pending.length)this.#drain();});
  }

  async #dropRenderer(){const renderer=this.renderer;this.renderer=null;if(renderer)try{await renderer.cleanup();}catch(error){this.onError(error);}}

  async #drain(){
    if(this.working)return;this.working=true;
    try{
      while(this.pending.length){
        const event=this.pending.shift();if(!this.#eligible(event)||eventDiceCount(event)>MAX_ANIMATED_DICE)continue;
        const generation=this.generation;await this.cleanupPromise;if(generation!==this.generation)continue;
        if(!this.renderer)this.renderer=this.createRenderer();this.active=event;
        let timeout,wasCancelled=false;
        const cancellation=new Promise((_,reject)=>{this.cancelCurrent=error=>{wasCancelled=true;reject(error);};});
        const timed=new Promise((_,reject)=>{timeout=setTimeout(()=>reject(new Error('3D dice animation timed out')),this.timeoutMs);});
        try{await Promise.race([this.renderer.renderPredetermined(event),cancellation,timed]);}
        catch(error){if(!wasCancelled&&generation===this.generation)this.onError(error);await this.#dropRenderer();}
        finally{if(!wasCancelled&&generation===this.generation)this.onFinished(event);clearTimeout(timeout);if(generation===this.generation){this.cancelCurrent=null;this.active=null;}}
      }
    }finally{this.working=false;if(this.pending.length)queueMicrotask(()=>this.#drain());}
  }
}

const diceIcons={
  4:'<path d="M12 3 2 21h20z"/>',
  6:'<rect x="4" y="4" width="16" height="16" rx="2"/><path d="m4 9 8 5 8-5M12 14v6"/>',
  8:'<path d="M12 2 3 12l9 10 9-10z"/><path d="m3 12 9-4 9 4M12 8v14"/>',
  10:'<path d="m12 2 10 8-4 12H6L2 10z"/>',
  12:'<path d="m8 2 8 0 6 7v6l-6 7H8l-6-7V9z"/>',
  20:'<path d="M6 3h12l4 9-4 9H6l-4-9z"/><path d="m6 3 6 9 6-9M2 12h20m-16 9 6-9 6 9"/>'
};
const diceIcon=sides=>`<svg viewBox="0 0 24 24" width="23" height="23" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${diceIcons[sides]}</svg>`;

export function createDicePanel(inspector=document.querySelector('.inspector')){
  const root=document.createElement('section');root.id='dicePanel';root.className='dice-panel';root.hidden=true;
  root.innerHTML=`<div class="panel-heading">БЫСТРЫЙ БРОСОК</div><form class="dice-manual"><label class="dice-expression-label" for="diceExpression">Формула<input id="diceExpression" data-dice-expression value="1d20" maxlength="512" inputmode="text" autocomplete="off" spellcheck="false" aria-describedby="diceExpressionHint"></label><small id="diceExpressionHint" data-dice-error aria-live="polite">Например: 2d20 + 1d6 + 3 · максимум 100 кубиков</small><div class="dice-options" role="group" aria-label="Количество кубиков">${DICE_SIDES.map(sides=>`<div class="dice-pick" data-dice-pick="${sides}"><button class="dice-adjust" type="button" data-dice-plus="${sides}" aria-label="Добавить d${sides}">+</button><output data-dice-count="${sides}" aria-label="Количество d${sides}">0</output><span class="dice-option" aria-label="d${sides}">${diceIcon(sides)}<span>d${sides}</span></span><button class="dice-adjust" type="button" data-dice-minus="${sides}" aria-label="Убрать d${sides}">−</button></div>`).join('')}</div><button class="primary dice-submit" data-dice-submit type="submit">Бросить</button></form><div class="dice-last-result" data-dice-last hidden><small>Результат броска</small><strong data-dice-total></strong><span data-dice-values></span></div><label class="dice-animation">Анимации<select data-roll-animation><option value="all">Все броски</option><option value="self">Только мои</option><option value="off">Выключены</option></select></label>`;
  const note=inspector?.querySelector('.inspector-note');if(note)inspector.insertBefore(root,note);else inspector?.append(root);
  const journal=document.createElement('section');journal.id='diceJournal';journal.className='dice-journal-popover';journal.hidden=true;
  journal.innerHTML='<div class="dice-journal-header"><strong>Журнал бросков <span data-roll-count></span></strong><button type="button" class="subtle" data-journal-close aria-label="Закрыть журнал">×</button></div><div class="roll-entries" data-roll-entries aria-live="polite"></div>';
  document.body.append(journal);
  const overlay=document.createElement('div');overlay.className='dice-total-overlay';overlay.hidden=true;overlay.setAttribute('aria-live','polite');document.body.append(overlay);
  const toggle=document.getElementById('diceJournalToggle');
  const setJournal=open=>{journal.hidden=!open;if(toggle){toggle.setAttribute('aria-expanded',String(open));toggle.classList.toggle('active',open);}if(open)journal.querySelector('[data-roll-entries]').scrollTop=0;};
  toggle?.addEventListener('click',()=>setJournal(journal.hidden));journal.querySelector('[data-journal-close]').onclick=()=>setJournal(false);
  root.diceUI={journal,overlay,toggle,setJournal};return root;
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

// A bounded additive grammar: positive dice terms and signed integer constants.
// The text is only reformatted by explicit stepper interaction, never while typing.
export function parseDiceFormula(value){
  const input=String(value??'').trim();
  if(!input)return {error:'Введите формулу броска'};
  const groups=[];let modifier=0,total=0,pos=0;
  const token= /\s*([+-]?)\s*(?:(\d*)\s*[dD]\s*(\d+)|(\d+))\s*/gy;
  while(pos<input.length){
    token.lastIndex=pos;const match=token.exec(input);
    if(!match||(!match[1]&&pos>0))return {error:'Используйте кубики d4–d20, +, − и целые числа'};
    if(pos===0&&match[1]==='-'&&match[3])return {error:'Количество кубиков должно быть положительным'};
    if(match[3]!==undefined){
      if(match[1]==='-')return {error:'Вычитание кубиков не поддерживается'};
      const count=normalizeDiceCount(match[2]||'1'),sides=Number(match[3]);
      if(count===null||!DICE_SIDES.includes(sides))return {error:'Неверный тип или количество кубиков'};
      total+=count;if(total>MAX_DICE_COUNT||groups.length>=MAX_DICE_GROUPS)return {error:`Не более ${MAX_DICE_COUNT} кубиков и ${MAX_DICE_GROUPS} групп`};
      groups.push({count,sides});
    }else{
      const number=Number(match[4])*(match[1]==='-'?-1:1);
      if(!Number.isSafeInteger(number)||!Number.isSafeInteger(modifier+number))return {error:'Числовой модификатор слишком большой'};
      modifier+=number;
    }
    pos=token.lastIndex;
  }
  if(!groups.length)return {error:'Добавьте хотя бы один кубик'};
  return {groups,modifier};
}

export function normalizeDiceGroups(value){
  const parsed=parseDiceFormula(value);return parsed.error?null:parsed.groups;
}

export function changeDiceCount(parsed,sides,delta){
  if(parsed.error||!DICE_SIDES.includes(sides)||![-1,1].includes(delta))return null;
  const groups=parsed.groups.map(group=>({...group}));
  const match=groups.find(group=>group.sides===sides);
  const count=groups.reduce((sum,group)=>sum+group.count,0);
  if(delta>0&&(count>=MAX_DICE_COUNT||!match&&groups.length>=MAX_DICE_GROUPS))return null;
  if(match){match.count+=delta;if(match.count===0)groups.splice(groups.indexOf(match),1);}
  else if(delta>0)groups.push({count:1,sides});
  if(!groups.length)return {groups:[],modifier:parsed.modifier,expression:''};
  const expression=groups.map(group=>`${group.count}d${group.sides}`).join(' + ')+modifierText(parsed.modifier).replace(' − ',' - ');
  return {groups,modifier:parsed.modifier,expression};
}

export function diceGroups(event){
  if(Array.isArray(event?.groups)&&event.groups.length)return event.groups;
  return [{count:Number(event?.count),sides:Number(event?.sides),results:Array.isArray(event?.results)?event.results:[]}];
}

export function diceNotation(event){return diceGroups(event).map(group=>`${group.count}d${group.sides}`).join(' + ');}

function numberText(value){return Number.isInteger(value)?String(value):String(Number(value));}
function modifierText(value){return value>0?` + ${numberText(value)}`:value<0?` − ${numberText(-value)}`:'';}

export class DiceRuntime {
  constructor(root,{queue,getCharacterId=()=>'',toast=()=>{},onAnimate=()=>{},animator=null,storage=localStorage}={}){
    this.root=root;this.queue=queue;this.getCharacterId=getCharacterId;this.toast=toast;this.onAnimate=onAnimate;this.animator=animator;this.storage=storage;
    this.sceneId='';this.userId='';this.events=[];this.eventIds=new Set();this.groups=[{count:1,sides:20}];this.modifier=0;this.awaitingManual=false;this.awaitingRoll=false;this.overlayTimer=null;
    this.expression=root.querySelector('[data-dice-expression]');this.error=root.querySelector('[data-dice-error]');this.submit=root.querySelector('[data-dice-submit]');this.entries=root.diceUI.journal.querySelector('[data-roll-entries]');this.journalCount=root.diceUI.journal.querySelector('[data-roll-count]');this.animation=root.querySelector('[data-roll-animation]');this.last=root.querySelector('[data-dice-last]');this.overlay=root.diceUI.overlay;
    try{const saved=JSON.parse(this.storage.getItem('atlas-dice-choice')||'null'),expression=saved?.expression||(saved?.count&&saved?.sides?`${saved.count}d${saved.sides}`:'');if(expression&&!parseDiceFormula(expression).error)this.expression.value=expression;}catch{}
    this.animation.value=this.#loadAnimationMode();this.animator?.setPolicy(this.animation.value);
    root.querySelectorAll('[data-dice-plus], [data-dice-minus]').forEach(button=>button.addEventListener('click',()=>this.#adjustDice(Number(button.dataset.dicePlus||button.dataset.diceMinus),button.hasAttribute('data-dice-plus')?1:-1)));
    this.expression.addEventListener('input',()=>this.#readExpression());
    this.animation.addEventListener('change',()=>{this.#saveAnimationMode();this.animator?.setPolicy(this.animation.value);if(this.animation.value!=='off')this.animator?.warmup();});
    root.querySelector('form').addEventListener('submit',event=>{event.preventDefault();this.rollManual();});
    this.#readExpression();this.#updateCount();root.hidden=true;
  }

  setScene(sceneId,userId=''){
    sceneId=String(sceneId||'');
    if(sceneId!==this.sceneId){this.sceneId=sceneId;this.events=[];this.eventIds.clear();this.entries.replaceChildren();this.awaitingManual=false;this.awaitingRoll=false;this.#setSending(false);this.last.hidden=true;this.#updateCount();clearTimeout(this.overlayTimer);this.overlay.hidden=true;this.root.diceUI.setJournal(false);}
    this.userId=String(userId||'');this.animator?.setContext(sceneId,this.userId);this.root.hidden=!sceneId;this.root.diceUI.toggle.hidden=!sceneId;if(sceneId&&this.animation.value!=='off')this.animator?.warmup();
  }

  disconnect(){return this.animator?.cancel();}

  rollManual(){
    if(!this.sceneId||this.awaitingRoll)return false;
    const parsed=this.#readExpression();
    if(!parsed){this.expression.reportValidity();return false;}
    const {groups,modifier}=parsed;
    const roll=groups.length===1?{...groups[0]}:{groups:groups.map(group=>({...group}))};
    if(modifier)roll.manualModifier=modifier;
    const characterId=this.getCharacterId();if(characterId)roll.characterInstanceId=characterId;
    if(!this.queue?.('roll',{sceneId:this.sceneId,roll})){this.toast('Бросок не отправлен: нет соединения с сервером');return false;}
    this.awaitingManual=true;this.awaitingRoll=true;this.#setSending(true);return true;
  }

  rollAction(characterInstanceId,actionId,rollSpecId){
    if(!this.sceneId||this.awaitingRoll||!characterInstanceId||!actionId||!rollSpecId)return false;
    if(!this.queue?.('roll',{sceneId:this.sceneId,roll:{characterInstanceId,actionId,rollSpecId}})){this.toast('Бросок не отправлен: нет соединения с сервером');return false;}
    this.awaitingRoll=true;return true;
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
    if(added){this.#append(this.events.at(-1));if(!message.replayed){
      if(message.event.userId===this.userId){this.awaitingManual=false;this.awaitingRoll=false;this.#setSending(false);}
      const animated=this.#shouldAnimate(message.event)&&this.animator?.enqueue(message.event);
      if(!animated){if(message.event.userId===this.userId)this.#lastResult(message.event);this.showTotal(message.event,false);this.onAnimate(message.event);}
    }}
    return true;
  }

  handleReceipt(command,message){
    if(command?.type!=='roll'||message?.sceneId!==this.sceneId)return;
    this.awaitingManual=false;this.awaitingRoll=false;this.#setSending(false);
    if(message.rollEvent&&this.#remember(message.rollEvent)){this.#append(message.rollEvent);this.#lastResult(message.rollEvent);if(this.animation.value==='off')this.showTotal(message.rollEvent,false);}
  }

  finishAnimation(event){if(event?.userId===this.userId)this.#lastResult(event);this.showTotal(event,true);}

  #remember(event){
    if(!event||event.sceneId!==this.sceneId||typeof event.id!=='string'||!event.id||this.eventIds.has(event.id)||!Array.isArray(event.results))return false;
    this.events.push(event);this.eventIds.add(event.id);
    if(this.events.length>MAX_ROLL_JOURNAL){const removed=this.events.shift();this.eventIds.delete(removed.id);}
    return true;
  }

  #append(event){
    const wasReading=this.entries.scrollTop>12,oldHeight=this.entries.scrollHeight;
    this.entries.prepend(this.#row(event));
    while(this.entries.children.length>MAX_ROLL_JOURNAL)this.entries.lastElementChild.remove();
    if(wasReading)this.entries.scrollTop+=this.entries.scrollHeight-oldHeight;
    this.#updateCount();
  }

  #renderAll(){
    const fragment=document.createDocumentFragment();
    for(let i=this.events.length-1;i>=0;i--)fragment.append(this.#row(this.events[i]));
    this.entries.replaceChildren(fragment);this.#updateCount();
  }

  #row(event){
    const row=document.createElement('article');row.className='roll-entry';row.dataset.eventId=event.id;
    const heading=document.createElement('strong');heading.textContent=[event.authorName,event.characterName].filter(Boolean).join(' · ')||'Бросок';
    const source=document.createElement('span');source.className='roll-source';source.textContent=[event.actionName,event.rollSpecName].filter(Boolean).join(' · ')||'Ручной бросок';
    const notation=document.createElement('span');notation.className='roll-notation';notation.textContent=`${diceNotation(event)}${modifierText(Number(event.modifier)||0)}`;
    const result=document.createElement('span');result.className='roll-result';result.textContent=`[${event.results.join(', ')}]${modifierText(Number(event.modifier)||0)} = ${numberText(event.total)}`;
    row.append(heading,source,notation,result);return row;
  }

  #setSending(sending){this.submit.disabled=!!sending||!!parseDiceFormula(this.expression.value).error;this.submit.textContent=sending?'Ожидание результата…':'Бросить';}
  #saveChoice(){try{this.storage.setItem('atlas-dice-choice',JSON.stringify({expression:this.expression.value}));}catch{}}
  #lastResult(event){if(event.userId!==this.userId)return;this.last.hidden=false;this.last.querySelector('[data-dice-total]').textContent=numberText(event.total);const groups=diceGroups(event),shown=groups.map(group=>`d${group.sides}: ${(group.results||[]).join(' + ')}`).join(' · ');this.last.querySelector('[data-dice-values]').textContent=`${shown}${modifierText(Number(event.modifier)||0)}`;}
  showTotal(event,animated=true){if(!this.sceneId||event.sceneId!==this.sceneId)return;if(!this.#shouldAnimate(event)&&event.userId!==this.userId)return;clearTimeout(this.overlayTimer);this.overlay.hidden=false;this.overlay.replaceChildren();const info=document.createElement('small');info.textContent=`${event.authorName} · ${diceNotation(event)}${modifierText(Number(event.modifier)||0)}`;const total=document.createElement('strong');total.textContent=numberText(event.total);this.overlay.append(info,total);this.overlay.classList.remove('dice-total-animate');if(animated){void this.overlay.offsetWidth;this.overlay.classList.add('dice-total-animate');}this.overlayTimer=setTimeout(()=>{this.overlay.hidden=true;},animated?1900:1300);}
  #adjustDice(sides,delta){
    const parsed=this.expression.value.trim()?parseDiceFormula(this.expression.value):{groups:[],modifier:0},next=changeDiceCount(parsed,sides,delta);
    if(!next)return;
    // The final minus leaves an empty expression; the next plus can start a new roll.
    this.expression.value=next.expression;
    this.#readExpression();
  }
  #readExpression(){
    const parsed=parseDiceFormula(this.expression.value);
    const message=parsed.error||'';this.expression.setCustomValidity(message);
    this.error.textContent=message||'Например: 2d20 + 1d6 + 3 · максимум 100 кубиков';
    this.error.classList.toggle('dice-error',!!message);
    const counts=new Map();if(!message){this.groups=parsed.groups;this.modifier=parsed.modifier;for(const group of parsed.groups)counts.set(group.sides,(counts.get(group.sides)||0)+group.count);this.#saveChoice();}
    const count=this.expression.value.trim()?this.groups.reduce((sum,group)=>sum+group.count,0):0;
    for(const sides of DICE_SIDES){
      const amount=message?this.groups.filter(item=>item.sides===sides).reduce((sum,item)=>sum+item.count,0):counts.get(sides)||0,group=this.root.querySelector(`[data-dice-pick="${sides}"]`);
      group.querySelector('[data-dice-count]').textContent=String(amount);
      group.classList.toggle('active',amount>0);
      group.querySelector('[data-dice-minus]').disabled=!!message||amount===0;
      group.querySelector('[data-dice-plus]').disabled=!!message&&!!this.expression.value.trim()||count>=MAX_DICE_COUNT||!amount&&this.groups.length>=MAX_DICE_GROUPS;
    }
    this.#setSending(this.awaitingManual);return message?null:parsed;
  }
  #updateCount(){this.journalCount.textContent=this.events.length?`(${this.events.length})`:'';}
  #shouldAnimate(event){return this.animation.value==='all'||this.animation.value==='self'&&event.userId===this.userId;}
  #loadAnimationMode(){try{const value=this.storage.getItem('atlas-dice-animation');return ['all','self','off'].includes(value)?value:'all';}catch{return 'all';}}
  #saveAnimationMode(){try{this.storage.setItem('atlas-dice-animation',this.animation.value);}catch{}}
}
