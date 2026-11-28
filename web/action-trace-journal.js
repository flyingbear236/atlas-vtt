const PAGE_SIZE=100,MAX_ENTRIES=500;

export function actionTraceSummary(event){
 if(!event?.trace||!event.executionId)return null;
 return {
  id:event.executionId,
  sceneId:event.sceneId,
  title:event.actionName||event.actionId||'Действие',
  actor:event.characterName||event.characterId||'',
  time:event.timestamp||'',
  revision:event.actionRevision||0,
  roll:event.roll||null,
  success:event.success,
  trace:event.trace,
  messages:event.messages||[],
 };
}

function text(tag,content,cls){const el=document.createElement(tag);if(cls)el.className=cls;el.textContent=String(content??'');return el;}
function line(label,value){const row=document.createElement('div');row.className='action-trace-line';row.append(text('span',label),text('strong',value));return row;}
function section(title,container){const s=document.createElement('section');s.className='action-trace-section';s.append(text('h3',title));container.append(s);return s;}
function valueText(value){if(value&&typeof value==='object')return value.value??JSON.stringify(value);return String(value??'—');}

export class ActionTraceJournal {
 constructor(mount=document.body,sendPage=()=>false){
  this.sceneId='';this.enabled=false;this.events=new Map();this.selected='';
  this.sendPage=sendPage;this.historyCursor=0;this.hasMore=false;this.loading=false;this.requestedCursor=0;this.loadedPages=1;
  this.toggle=document.createElement('button');this.toggle.type='button';this.toggle.className='subtle action-trace-toggle';this.toggle.textContent='Трассировка';this.toggle.title='Журнал вычислений действий';this.toggle.hidden=true;this.toggle.setAttribute('aria-expanded','false');
  document.getElementById('diceJournalToggle')?.after(this.toggle);
  this.panel=document.createElement('aside');this.panel.className='action-trace-panel';this.panel.hidden=true;this.panel.setAttribute('aria-label','Журнал трассировки действий');
  const header=document.createElement('header');header.className='action-trace-header';header.append(text('strong','Трассировка действий'));
  this.closeButton=document.createElement('button');this.closeButton.type='button';this.closeButton.className='subtle action-trace-close';this.closeButton.textContent='×';this.closeButton.setAttribute('aria-label','Закрыть журнал');header.append(this.closeButton);
  this.list=document.createElement('div');this.list.className='action-trace-list';this.list.setAttribute('aria-label','Выполненные действия');
  this.list.addEventListener('scroll',()=>{if(!this.panel.hidden&&this.list.scrollTop+this.list.clientHeight>=this.list.scrollHeight-24)this.loadMore();});
  this.detail=document.createElement('div');this.detail.className='action-trace-detail';
  this.panel.append(header,this.list,this.detail);mount.append(this.panel);
  this.toggle.onclick=()=>this.setOpen(this.panel.hidden);this.closeButton.onclick=()=>this.setOpen(false);
  this.keyDown=event=>{if(event.key==='Escape'&&!this.panel.hidden&&event.target.closest('.action-trace-panel'))this.setOpen(false)};
  document.addEventListener('keydown',this.keyDown);
 }
 setContext(sceneId,enabled){
  sceneId=String(sceneId||'');enabled=!!enabled&&!!sceneId;
  if(this.sceneId!==sceneId||this.enabled!==enabled){this.events.clear();this.selected='';this.historyCursor=0;this.hasMore=false;this.loading=false;this.requestedCursor=0;this.loadedPages=1;this.sceneId=sceneId;this.enabled=enabled;this.setOpen(false);this.render();}
  this.toggle.hidden=!enabled;
 }
 loadMore(){
  if(!this.enabled||!this.hasMore||!this.historyCursor||this.loading)return;
  const cursor=this.historyCursor;
  if(this.sendPage({type:'actionHistoryPage',sceneId:this.sceneId,beforeActionRevision:cursor})===false)return;
  this.loading=true;this.requestedCursor=cursor;this.render();
 }
 setOpen(open){this.panel.hidden=!open||!this.enabled;this.toggle.classList.toggle('active',!this.panel.hidden);this.toggle.setAttribute('aria-expanded',String(!this.panel.hidden));if(!this.panel.hidden)this.render();}
 handle(message){
  if(!this.enabled)return false;
  if((message?.type==='rollHistory'||message?.type==='actionTraceHistory')&&message.sceneId===this.sceneId){
   const next=new Map();for(const event of message.actions||[]){const row=actionTraceSummary(event);if(row&&row.sceneId===this.sceneId)next.set(row.id,row);}
   this.loadedPages=1;this.events=new Map([...next.entries()].slice(-PAGE_SIZE));this.selected=this.events.has(this.selected)?this.selected:[...this.events.keys()].at(-1)||'';
   this.historyCursor=message.actions?.[0]?.actionRevision||0;this.hasMore=!!message.actionHistoryHasMore;this.loading=false;this.requestedCursor=0;
   if(!this.panel.hidden)this.render();return true;
  }
  if(message?.type==='actionHistoryPage'&&message.sceneId===this.sceneId){
   if(!this.loading||message.beforeActionRevision!==this.requestedCursor)return true;
   const rows=[...this.events.values()];
   for(const event of message.actions||[]){const row=actionTraceSummary(event);if(row)rows.push(row);}
   rows.sort((a,b)=>a.revision-b.revision);
   this.loadedPages=Math.min(MAX_ENTRIES/PAGE_SIZE,this.loadedPages+1);
   this.events=new Map(rows.slice(-PAGE_SIZE*this.loadedPages).map(row=>[row.id,row]));
   this.historyCursor=message.actions?.[0]?.actionRevision||0;this.hasMore=!!message.actionHistoryHasMore;this.loading=false;this.requestedCursor=0;
   if(!this.panel.hidden)this.render();return true;
  }
  if(message?.type==='actionTrace'){
   const row=actionTraceSummary(message.event);if(!row||row.sceneId!==this.sceneId)return true;
   this.events.delete(row.id);this.events.set(row.id,row);
   // While an older page is in flight, keep a bounded overlap. Otherwise
   // pruning the oldest displayed record would create a gap at the cursor.
   const limit=this.loading?MAX_ENTRIES:PAGE_SIZE*this.loadedPages;
   let pruned=false;
   while(this.events.size>limit){this.events.delete(this.events.keys().next().value);pruned=true;}
   if(pruned&&!this.loading){
    this.historyCursor=[...this.events.values()].reduce((min,event)=>Math.min(min,event.revision||Infinity),Infinity);
    this.hasMore=true;
   }
   this.selected=row.id;
   if(!this.panel.hidden)this.render();return true;
  }
  return false;
 }
 render(){
  if(this.panel.hidden)return;
  const scrollTop=this.list.scrollTop;
  this.list.replaceChildren();
  const rows=[...this.events.values()].reverse();
  if(!rows.length)this.list.append(text('p','Выполненных действий пока нет. Журнал обновляется автоматически.','action-trace-empty'));
  for(const row of rows){
   const btn=document.createElement('button');btn.type='button';btn.className='action-trace-entry'+(this.selected===row.id?' active':'');
   btn.append(text('strong',row.title),text('small',[row.actor,row.time?new Date(row.time).toLocaleTimeString('ru-RU',{hour:'2-digit',minute:'2-digit'}):''].filter(Boolean).join(' · ')));
   if(row.roll)btn.append(text('b',`Бросок: ${row.roll.total}`));
   btn.onclick=()=>{this.selected=row.id;this.render();};this.list.append(btn);
  }
  if(this.hasMore){const more=document.createElement('button');more.type='button';more.className='subtle action-trace-more';more.textContent=this.loading?'Загрузка…':'Показать более ранние';more.disabled=this.loading;more.onclick=()=>this.loadMore();this.list.append(more);}
  this.list.scrollTop=scrollTop;
  this.renderDetail(this.events.get(this.selected));
 }
 renderDetail(entry){
  const container=this.detail;container.replaceChildren();if(!entry)return;
  container.append(text('h2',entry.title));
  const trace=entry.trace;
  if(entry.roll){
   const block=section('Бросок',container);
   if(trace.rollFormula)block.append(text('code',trace.rollFormula,'action-trace-formula'));
   for(const dice of entry.roll.dice||[])block.append(line(`${dice.count}d${dice.sides}`, (dice.results||[]).join(', ')));
   if(trace.rollBase!==undefined&&trace.rollBase!=='')block.append(line('До эффектов на бросок',trace.rollBase));
   block.append(line('Итог',trace.rollTotal||entry.roll.total));
  }
  if(trace.successFormula){const cond=section('Проверка успеха',container);cond.append(text('code',trace.successFormula));if(typeof entry.success==='boolean')cond.append(line('Результат',entry.success?'Успех':'Провал'));}
  if((trace.reads||[]).length){
   const block=section('Прочитанные значения',container);
   for(const row of trace.reads){
    const detail=document.createElement('details');detail.className='action-trace-read';
    const summary=document.createElement('summary');summary.append(text('strong',row.name||row.path),text('b',row.value));detail.append(summary);
    detail.append(text('code',row.path));
    if(row.formula)detail.append(text('code',row.formula));
    if(row.initial!==''&&row.initial!==undefined)detail.append(line('До эффектов',row.initial));
    if(row.origin){
      const origin=row.origin;const nested=document.createElement('div');nested.className='action-trace-origin';
      nested.append(line(origin.name||origin.path,origin.current),text('code',origin.path));
      if(origin.initial!==''&&origin.initial!==undefined)nested.append(line('Исходное значение',origin.initial));
      for(const effect of origin.effects||[])nested.append(line(`effect.${effect.effectId} · ${effect.sourceId}`,`${valueText(effect.before)} → ${valueText(effect.after)}`));
      for(const ignored of origin.skipped||[])nested.append(line(`Пропущен effect.${ignored.effectId}`,ignored.reason));
      detail.append(nested);
    }
    for(const effect of row.effects||[]){detail.append(line(`${effect.item?'Предмет '+effect.sourceId:'Персонаж'} · effect.${effect.effectId}`,`${valueText(effect.before)} → ${valueText(effect.after)}`));if(effect.formula)detail.append(text('code',effect.formula));}
    for(const ignored of row.skipped||[])detail.append(line(`Пропущен effect.${ignored.effectId}`,ignored.reason));
    block.append(detail);
   }
  }
  if((trace.effects||[]).length){
   const block=section('Эффекты на действие',container);
   for(const effect of trace.effects){const row=document.createElement('div');row.className='action-trace-effect';
    row.append(text('strong',effect.sourceName||effect.sourceId||'У персонажа'),text('small',`effect.${effect.effectId} · ${effect.sourceId}`),line('Изменение',`${effect.before} → ${effect.after}`),text('code',effect.formula));block.append(row);}
  }
  if((trace.skippedOperations||[]).length){const block=section('Пропущенные операции',container);for(const op of trace.skippedOperations)block.append(line(op.operation,op.message));}
  if((trace.skipped||[]).length){const block=section('Не применились',container);for(const skip of trace.skipped)block.append(line(`effect.${skip.effectId}`,skip.reason));}
  if((trace.variables&&Object.keys(trace.variables).length)){const block=section('Переменные действия',container);for(const [name,value] of Object.entries(trace.variables))block.append(line(name,valueText(value)));}
  if((trace.changes||[]).length){const block=section('Изменения после действия',container);for(const change of trace.changes)block.append(line([change.instance?.characterId,change.instance?.itemId,change.field].filter(Boolean).join(' · '),`${valueText(change.before)} → ${valueText(change.after)}`));}
  if(entry.messages?.length){const block=section('Сообщения',container);for(const msg of entry.messages)block.append(text('p',msg));}
  if(trace.truncated)container.append(text('p','Часть подробностей скрыта из-за ограничения размера журнала.','action-trace-empty'));
 }
 destroy(){document.removeEventListener('keydown',this.keyDown);this.toggle.remove();this.panel.remove();}
}
