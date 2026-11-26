async credentials => {
  const wait = async (predicate, label) => {
    for (let i = 0; i < 200; i++) {
      const result = predicate();
      if (result) return result;
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    throw Error(typeof label==='function'?label():label);
  };
  const query = selector => document.querySelector(selector);
  const all = selector => [...document.querySelectorAll(selector)];
  const click = async selector => (await wait(() => query(selector), selector)).click();
  const stat = id => query(`#characterLayoutContent .character-stat-card[data-sheet-item="stat:${id}"] .character-stat-value`)?.textContent;
  const commands = [], received = [];
  const observed = new WeakSet();
  const original = WebSocket.prototype.send;
  WebSocket.prototype.send = function(data) {
    if (!observed.has(this)) {
      observed.add(this);
      this.addEventListener('message',event=>{try{received.push(JSON.parse(event.data))}catch{}});
    }
    try{commands.push(JSON.parse(data))}catch{}
    return original.call(this,data);
  };
  const tab = async id => click(`#characterLayoutTabs [data-tab-id="${id}"]`);
  const sectionButtons = () => all('#characterLayoutContent .character-action-section-tabs [role="tab"]');
  // Only the selected subsection is mounted. Navigate using the actual tab
  // controls, which also work if a definition has an explicit display group.
  const reveal = async (tabId, selector, label) => {
    await tab(tabId);
    const count = sectionButtons().length;
    let element = query(selector);
    for (let i=0; !element && i<count; i++) {
      const button=sectionButtons()[i]; // render replaces the tab nodes
      if(button && button.getAttribute('aria-selected')!=='true')button.click();
      element=query(selector);
    }
    if(!element)throw Error(label+' not found; sections='+JSON.stringify(sectionButtons().map(b=>b.textContent))+'; visible entries='+JSON.stringify(all('#characterLayoutContent [data-sheet-item]').map(e=>e.dataset.sheetItem)));
    return element;
  };
  const tokenRow = name => all('.token-row').find(row => row.querySelector('.row-name')?.textContent===name);
  await click('#resumeList button');
  await click('.scene-card .primary');
  await wait(()=>!query('#workspace').hidden,'scene ready');
  await click('#objectsTab');
  await wait(()=>received.some(m=>m.type==='rollHistory'),'action generation');
  const pickCharacter = async name => {
    const token = await wait(() => tokenRow(name), name);
    token.querySelector('.token-row-main').click();
    await wait(() => !query('#characterPanel').hidden&&!query('#characterPanel').inert&&query('#characterName').textContent===name&&!query('#characterSummary').hidden, 'sheet '+name);
    await tab('stats');
  };
  await pickCharacter('Герой');
  const strength = '#characterLayoutContent .character-stat-card[data-sheet-item="stat:basic.strength"]';
  await reveal('stats',strength,'strength card');
  await wait(() => stat('basic.strength')==='18', ()=> 'equipped strength (expected 18, observed '+stat('basic.strength')+')');
  const hero = query('#characterPanel').dataset.characterId;
  const initial = received.find(m=>m.type==='characterSnapshot'&&m.characterId===hero);
  if(!initial?.characterSequence||!initial.traceAvailable||Object.keys(initial.statTraces||{}).length)throw Error('initial snapshot includes traces or lacks delta protocol');
  if (query(strength+' .character-stat-derived-value')?.textContent!=='4') throw Error('derived bonus missing');
  if (query('#characterLayoutContent .character-stat-card[data-sheet-item="stat:basic.proficiency"] input')) throw Error('computed default is editable');
  // Trace is loaded through the real WebSocket only when requested and reused
  // when opened a second time at the same projection sequence.
  await click(strength+' .character-stat-value');
  await wait(()=>query('.character-trace-shell:not([hidden]) .character-trace-calculation'),'trace render');
  if(!commands.some(m=>m.type==='characterStatTrace'&&m.statId==='basic.strength')||!received.some(m=>m.type==='characterStatTrace'&&m.statId==='basic.strength'&&!m.error))throw Error('on-demand trace was not requested');
  if(!query('.character-trace-shell').textContent.includes('16'))throw Error('base value missing in trace');
  await click('.character-trace-close');
  const traceRequests=commands.filter(m=>m.type==='characterStatTrace').length;
  await click(strength+' .character-stat-value');
  await wait(()=>query('.character-trace-shell:not([hidden]) .character-trace-calculation'),'cached trace');
  if(commands.filter(m=>m.type==='characterStatTrace').length!==traceRequests)throw Error('unchanged trace was fetched again');
  await click('.character-trace-close');
  const ring = '#characterInventoryBrowse [data-inventory-id="item-001"] input[type=checkbox]';
  const setEquipped = async equipped => {
    await tab('inventory');
    const control=await wait(()=>query(ring)&&!query(ring).disabled&&query(ring),'ring ready');
    if(control.checked===equipped)throw Error('unexpected initial ring state');
    const before=received.length;
    control.click();
    await wait(()=>query(ring)&&query(ring).checked===equipped&&!query(ring).disabled,'equipment committed');
    await wait(()=>received.slice(before).some(m=>m.type==='characterDelta'&&m.characterId===hero),'equipment delta');
    await reveal('stats',strength,'strength after equipment');
    await wait(()=>stat('basic.strength')===(equipped?'18':'16'),()=> 'effective strength (expected '+(equipped?'18':'16')+', observed '+stat('basic.strength')+')');
  };
  await setEquipped(false);
  await setEquipped(true);
  // The selected actor remains selected. Center on the actor before entering
  // target mode; the fixture tokens have known x offsets in world coordinates.
  // Clicking a target's navigator would change the actor and cancel targeting.
  const locations={'Герой':300,'Ученица':400,'Хранитель кинжала':500};
  const perform = async (id, source, actor, target) => {
    const actorRow=await wait(()=>tokenRow(actor),actor);
    actorRow.querySelector('.token-row-focus').click();
    const choice=await reveal('actions',`#characterLayoutContent [data-action-id="${id}"][data-source-id="${source}"]`,id+' action');
    const execute=choice.matches('button')?choice:choice.querySelector('.character-source-roll-content');
    if(!execute||execute.disabled)throw Error('action not executable '+id);
    const prior=commands.filter(command=>command.type==='actionExecute').length;
    const before=received.length;
    execute.click();
    await wait(()=>query('.character-action-target-prompt:not([hidden])'),id+' target prompt');
    await wait(()=>received.slice(before).some(m=>m.type==='actionTargets'&&!m.error),id+' target choices');
    const board=query('#board'),rect=board.getBoundingClientRect();
    // The camera zoom label is rounded to whole percent. The fixtures are
    // 100 units apart and token radius is 35, so this is ample accuracy.
    const scale=Number.parseFloat(query('#zoom').textContent)/100;
    if(!Number.isFinite(scale)||scale<=0)throw Error('invalid camera scale');
    const offset=target?(locations[target]-locations[actor])*scale:0;
    board.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,cancelable:true,button:0,pointerId:71,clientX:rect.left+rect.width/2+offset,clientY:rect.top+rect.height/2+(target?0:90*scale)}));
    await wait(()=>commands.filter(command=>command.type==='actionExecute').length===prior+1,id+' submitted');
    await wait(()=>query('.character-action-target-prompt').hidden,id+' target prompt closed');
    const action=commands.filter(command=>command.type==='actionExecute').at(-1).action;
    if(target&&!action.target?.characterId)throw Error('target not selected');
    if(!target&&action.target)throw Error('optional target selected');
    return action;
  };
  const journalLast=()=>query('#diceJournal .roll-entry')?.textContent||'';
  await perform('weapon.attack.hit','item-002','Герой','Ученица');
  await wait(() => journalLast().includes('Попадание!'), 'successful attack');
  await perform('weapon.attack.damage','item-002','Герой','Ученица');
  await wait(() => journalLast().includes('100 → 92'), 'resource change in journal');
  await perform('weapon.attack.damage','item-002','Герой',null);
  await wait(() => journalLast().includes('Бросок урона: 5'), 'target-free roll');
  await pickCharacter('Ученица');
  const hpCard='#characterLayoutContent .character-resource-card[data-sheet-item="stat:hp"]';
  await reveal('stats',hpCard,'HP card');
  await wait(()=>query(hpCard+' .character-resource-value-input')?.value==='92'&&query(hpCard+' [role=progressbar]')?.getAttribute('aria-valuemax')==='100','HP projection');
  const trainee=query('#characterPanel').dataset.characterId;
  const hpInput=query(hpCard+' .character-resource-value-input');
  const beforeHp=received.length;
  await click(hpCard+' button[aria-label$="уменьшить на 1"]');
  await wait(()=>received.slice(beforeHp).some(m=>m.type==='characterDelta'&&m.characterId===trainee)&&query(hpCard+' [role=progressbar]')?.getAttribute('aria-valuenow')==='91','live HP delta');
  if(query(hpCard+' .character-resource-value-input')!==hpInput)throw Error('HP delta replaced input instead of patching it');
  await click(hpCard+' button[aria-label$="увеличить на 1"]');
  await wait(()=>query(hpCard+' [role=progressbar]')?.getAttribute('aria-valuenow')==='92','restore HP');
  await reveal('actions','#characterLayoutContent [data-action-id="weapon.attack.hit"].is-runnable','weapon actions');
  if (all('#characterLayoutContent [data-action-id="weapon.attack.hit"].is-runnable').length!==3) throw Error('equipment sources collapsed');
  const trainedInitial=received.find(m=>m.type==='characterSnapshot'&&m.characterId===trainee);
  if (!trainedInitial || Object.hasOwn(trainedInitial.actionSelectors||{},'weapon.attack') || query('#characterLayoutContent [data-action-id="weapon.attack"]')) throw Error('group is executable or trained snapshot missing');
  const inspect=await perform('inspect','','Ученица','Хранитель кинжала');
  if(inspect.target?.itemId!=='browser-dagger')throw Error('wrong item target');
  await wait(() => journalLast().includes('Вы осматриваете Кинжал'), 'message-only action');
  if (journalLast().includes('Результат')) throw Error('inspect rolled');
  await click('#diceJournalToggle');
  const rows = all('#diceJournal .roll-entry');
  if (rows.length!==4 || new Set(rows.map(row=>row.dataset.eventId)).size!==4) throw Error('journal event IDs');
  const manual = query('#diceExpression');
  manual.value='2d6 + 1d4 + 3';
  manual.dispatchEvent(new Event('input',{bubbles:true}));
  await click('[data-dice-submit]');
  await wait(() => query('#diceJournal .roll-entry .roll-result')?.textContent.includes('= 12'), 'manual dice result');
  const command = commands.findLast(entry => entry.type==='roll');
  if (command.roll.manualModifier!==3 || command.roll.groups.length!==2 || 'actionId' in command.roll || 'rollSpecId' in command.roll) {
    throw Error('manual dice command used action fields');
  }
  await click('#definitionsOpen');
  const fileOrFolder=await wait(()=>query('button[aria-label="Открыть rules/examples.toml"]')||query('button[aria-label="Раскрыть rules"]'),'rules folder');
  if(fileOrFolder.getAttribute('aria-label')==='Раскрыть rules')fileOrFolder.click();
  await click('button[aria-label="Открыть rules/examples.toml"]');
  const definition = await wait(() => all('.lb-definition-link').find(button => button.textContent.includes('weapon.attack.damage')), 'action in library');
  definition.click();
  const editor = await wait(() => {
    const textarea=query('[data-definition-toml] textarea');
    return textarea?.value.includes('dice(source.damage)') && textarea;
  }, 'original TOML in editor');
  const originalSource=editor.value;
  editor.value += '\n[action.\n';
  editor.dispatchEvent(new Event('input',{bubbles:true}));
  await click('[data-toml-preview]');
  await wait(() => query('dialog[open] .lb-import-errors'), 'invalid TOML preview');
  await click('[data-lb-dialog-close]');
  if (!editor.value.endsWith('[action.\n')) throw Error('invalid draft lost after preview');
  editor.value=originalSource+'\n# Browser acceptance\n';
  editor.dispatchEvent(new Event('input',{bubbles:true}));
  await click('[data-toml-preview]');
  const apply = await wait(() => all('dialog[open] button').find(button => button.textContent==='Применить'&&!button.disabled), 'TOML preview');
  apply.click();
  await wait(() => !query('dialog[open]'), 'TOML apply');
  const response = await fetch('/api/definitions/file?session='+encodeURIComponent(credentials.session)+'&name=rules%2Fexamples.toml',{headers:{Authorization:'Bearer '+credentials.key}});
  if (!(await response.text()).includes('# Browser acceptance')) throw Error('changed source not saved');
  await wait(() => query('.lb-message')?.textContent==='Импорт завершён', 'registry refreshed');
  await click('button[aria-label="Открыть rules/examples.toml"]');
  (await wait(() => all('.lb-definition-link').find(button => button.textContent.includes('weapon.attack.damage')), 'action list')).click();
  await click('[data-definition-new]');
  query('[data-toml-name]').value='rules/new-action.toml';
  query('[data-definition-toml] textarea').value='[action.new_action]\nmessage = "Новое действие"\n';
  await click('[data-toml-preview]');
  const add = await wait(() => all('dialog[open] button').find(button => button.textContent==='Применить'&&!button.disabled), 'new TOML preview');
  add.click();
  await wait(() => !query('dialog[open]'), 'new TOML apply');
  const registry = await fetch('/api/definitions?session='+encodeURIComponent(credentials.session),{headers:{Authorization:'Bearer '+credentials.key}}).then(response=>response.json());
  if (!registry.ruleset.registry.actions.new_action) throw Error('new action not installed');
  return true;
}
