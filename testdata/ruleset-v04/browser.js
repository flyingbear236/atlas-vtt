async credentials => {
  const wait = async (predicate, label) => {
    for (let i = 0; i < 200; i++) {
      const result = predicate();
      if (result) return result;
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    throw Error(label);
  };
  localStorage.setItem('atlas-sessions', JSON.stringify([credentials]));
  localStorage.setItem('atlas-dice-animation', 'off');
  const frame = document.createElement('iframe');
  frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;border:0;z-index:1000';
  frame.src = '/';
  document.body.append(frame);
  const query = selector => frame.contentDocument?.querySelector(selector);
  const all = selector => [...(frame.contentDocument?.querySelectorAll(selector)||[])];
  const click = async selector => (await wait(() => query(selector), selector)).click();
  const stat = id => query(`#characterLayoutContent [data-sheet-item="stat:${id}"] strong`)?.textContent;
  const commands = [];
  await new Promise(resolve => frame.addEventListener('load',resolve,{once:true}));
  const original = frame.contentWindow.WebSocket.prototype.send;
  frame.contentWindow.WebSocket.prototype.send = function(data) {
    commands.push(JSON.parse(data));
    return original.call(this,data);
  };
  await click('#resumeList button');
  await click('.scene-card .primary');
  await click('#objectsTab');
  const pickCharacter = async name => {
    const token = await wait(() => all('.token-row').find(row => row.textContent.includes(name)), name);
    token.click();
    await wait(() => query('#characterName').textContent===name, 'sheet '+name);
  };
  await pickCharacter('Герой');
  await wait(() => stat('basic.strength')==='18', 'equipped strength');
  if (!query('[data-sheet-item="stat:basic.strength"] dl').textContent.includes('bonus4')) throw Error('derived bonus missing');
  if (query('#characterLayoutContent [data-sheet-item="stat:basic.proficiency"] input')) throw Error('computed default is editable');
  if (query('#characterLayoutContent [data-sheet-item="stat:basic.strength"] input').value!=='16') throw Error('base displays effective');
  await click('#characterInventory summary');
  const ring = '#characterInventory input[aria-label$="item-001"]';
  await click(ring);
  await wait(() => stat('basic.strength')==='16', 'unequipped strength');
  await wait(() => !query(ring).disabled, 'equip ACK');
  await click(ring);
  await wait(() => stat('basic.strength')==='18', 'equipped again');
  const showActions = async () => (await wait(() => all('#characterLayoutTabs button').find(button => button.textContent==='Действия'), 'Actions tab')).click();
  await showActions();
  const perform = async (id, source, target) => {
    const selector = `button[data-action-id="${id}"][data-source-id="${source}"]`;
    await click(selector);
    const dialog = await wait(() => query('.character-action-target[open]'), id+' target dialog');
    await wait(() => !dialog.querySelector('[type=submit]').disabled, id+' targets');
    if (target) {
      const select = dialog.querySelector('select');
      const option = [...select.options].find(option => option.textContent.includes(target));
      if (!option) throw Error('Missing target '+target);
      select.value=option.value;
    }
    dialog.querySelector('[type=submit]').click();
    await wait(() => !dialog.open, id+' submitted');
  };
  await perform('weapon.attack.hit','item-002','Ученица');
  await wait(() => query('[data-action-last]').textContent.includes('Попадание!'), 'successful attack');
  await perform('weapon.attack.damage','item-002','Ученица');
  await wait(() => query('[data-action-last]').textContent.includes('100 → 92'), 'resource change in journal');
  await perform('weapon.attack.damage','item-002',null);
  await wait(() => query('[data-action-last]').textContent.includes('Бросок урона: 5'), 'target-free roll');
  if (commands.filter(command => command.type==='actionExecute').at(-1).action.target) throw Error('optional target selected');
  await pickCharacter('Ученица');
  await wait(() => query('#characterLayoutContent [data-sheet-item="stat:hp"] .character-resource-count')?.textContent==='92 / 100', 'HP projection');
  await showActions();
  if (all('button[data-action-id="weapon.attack.hit"]').length!==3) throw Error('equipment sources collapsed');
  if (query('button[data-action-id="weapon.attack"]')) throw Error('group is executable');
  await perform('inspect','','training-dagger');
  await wait(() => query('[data-action-last]').textContent.includes('Вы осматриваете Кинжал'), 'message-only action');
  if (query('[data-action-last]').textContent.includes('Результат')) throw Error('inspect rolled');
  await click('#diceJournalToggle');
  const rows = all('#diceJournal .roll-entry');
  if (rows.length!==4 || new Set(rows.map(row=>row.dataset.eventId)).size!==4) throw Error('journal event IDs');
  const manual = query('#diceExpression');
  manual.value='2d6 + 1d4 + 3';
  manual.dispatchEvent(new frame.contentWindow.Event('input',{bubbles:true}));
  await click('[data-dice-submit]');
  await wait(() => query('#diceJournal .roll-entry .roll-result')?.textContent.includes('= 12'), 'manual dice result');
  const command = commands.findLast(entry => entry.type==='roll');
  if (command.roll.manualModifier!==3 || command.roll.groups.length!==2 || 'actionId' in command.roll || 'rollSpecId' in command.roll) {
    throw Error('manual dice command used action fields');
  }
  await click('#definitionsOpen');
  await click('button[aria-label="Открыть rules/examples.toml"]');
  const definition = await wait(() => all('.lb-definition-link').find(button => button.textContent.includes('weapon.attack.damage')), 'action in library');
  definition.click();
  const editor = await wait(() => {
    const textarea=query('[data-definition-toml] textarea');
    return textarea?.value.includes('dice(source.damage)') && textarea;
  }, 'original TOML in editor');
  const originalSource=editor.value;
  editor.value += '\n[action.\n';
  editor.dispatchEvent(new frame.contentWindow.Event('input',{bubbles:true}));
  await click('[data-toml-preview]');
  await wait(() => query('dialog[open] .lb-import-errors'), 'invalid TOML preview');
  await click('[data-lb-dialog-close]');
  if (!editor.value.endsWith('[action.\n')) throw Error('invalid draft lost after preview');
  editor.value=originalSource+'\n# Browser acceptance\n';
  editor.dispatchEvent(new frame.contentWindow.Event('input',{bubbles:true}));
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
