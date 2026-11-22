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
  const frame = document.createElement('iframe');
  frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;border:0';
  frame.src = '/';
  const loaded = new Promise(resolve => frame.addEventListener('load', resolve, {once:true}));
  document.body.append(frame);
  await loaded;
  const view = frame.contentWindow, doc = frame.contentDocument;
  const one = selector => doc.querySelector(selector);
  const all = selector => [...doc.querySelectorAll(selector)];
  const click = async selector => (await wait(() => one(selector), selector)).click();
  const assert = (condition, label) => { if (!condition) throw Error(label); };
  const startDrag = handle => {
    assert(handle?.draggable, 'missing drag handle');
    const dataTransfer = new view.DataTransfer();
    return (type, target=handle, position={}) => target.dispatchEvent(
      new view.DragEvent(type, {bubbles:true, cancelable:true, dataTransfer, ...position}));
  };
  const dragOnto = (handle, target, position={}) => {
    const drag = startDrag(handle);
    drag('dragstart');
    drag('dragover', target, position);
    assert(target.classList.contains('sheet-tab-drop-target'), 'tab drop outline');
    drag('drop', target, position);
    drag('dragend');
  };
  const create = async (button, name) => {
    button.click();
    const dialog = await wait(() => one('.sheet-create-dialog[open]'), 'sheet dialog');
    dialog.querySelector('input').value = name;
    dialog.querySelector('[type=submit]').click();
    assert(!dialog.open, 'sheet dialog closes');
  };
  const actionsTab = '[data-sheet-tab="actions"]';
  const statsTab = '[data-sheet-tab="stats"]';
  const hit = '.character-source-roll[data-action-id="weapon.attack.hit"][data-source-id="item-002"]';
  const damage = '.character-source-roll[data-action-id="weapon.attack.damage"][data-source-id="item-002"]';
  const stat = '#characterLayoutContent .character-stat-card[data-sheet-item="stat:basic.strength"]';
  const actionSection = '.character-action-section-tabs > button';
  const selectedSection = actionSection + '[aria-selected=true]';
  const toggleLayout = () => one('#characterLayoutToggle').click();
  const statHandle = () => one(stat).parentElement.querySelector('.sheet-handle');
  await click('#resumeList button');
  await click('.scene-card .primary');
  await click('#objectsTab');
  (await wait(() => all('.token-row').find(row => row.textContent.includes('Герой')), 'hero')).click();
  await wait(() => one(stat)?.closest('.sheet-stat-list'), 'stat list in view');
  toggleLayout();
  assert(one(stat)?.closest('.sheet-stat-list') && statHandle(), 'stat list in layout mode');
  await click(actionsTab);
  const initial = await wait(() => one(hit), 'grouped weapon action');
  const groupId = one(selectedSection).dataset.sheetSection;
  assert(initial.closest('.character-source-item') && !one('#characterLayoutContent .character-action-pick'), 'source card in layout mode');
  const row = initial.closest('.sheet-source-entry');
  const damageRow = one(damage).closest('.sheet-source-entry');
  const drag = startDrag(damageRow.querySelector('.sheet-handle'));
  drag('dragstart');
  assert(damageRow.classList.contains('sheet-entry-moving'), 'source action fades while dragging');
  drag('dragover', row, {clientY:row.getBoundingClientRect().top+1});
  const marker = one('.character-source-body.sheet-drop .sheet-drop-placeholder');
  assert(marker?.nextElementSibling===row && view.getComputedStyle(marker).animationName==='sheet-slot-in', 'animated insertion point inside a source');
  drag('drop', row);
  await wait(() => one(damage)?.closest('.sheet-source-entry').nextElementSibling?.contains(one(hit)), 'order changed within source');
  toggleLayout();
  assert(one(damage).nextElementSibling===one(hit), 'order in view mode');
  toggleLayout();

  const sectionName = 'Новая группа действий';
  await create(all('.sheet-edit-toolbar button').find(button => button.textContent==='+ Раздел'), sectionName);
  assert(one(selectedSection).textContent===sectionName && one('.sheet-drop-zone').clientHeight>0, 'new section is an available destination');
  const newGroup = one(selectedSection).dataset.sheetSection;
  one(actionSection + '[data-sheet-section="'+groupId+'"]').click();
  const sectionTarget = one(actionSection + '[data-sheet-section="'+newGroup+'"]');
  dragOnto(one(hit).closest('.sheet-source-entry').querySelector('.sheet-handle'), sectionTarget);
  assert(one(hit) && !one(damage) && one(selectedSection).dataset.sheetSection===newGroup, 'single action moved to another group');
  dragOnto(one(selectedSection), one(actionSection + '[data-sheet-section="'+groupId+'"]'));
  assert(all(actionSection).findIndex(button => button.dataset.sheetSection===newGroup) <
    all(actionSection).findIndex(button => button.dataset.sheetSection===groupId), 'action sections reordered');
  toggleLayout();
  assert(one(hit) && !one(damage), 'moved action remains in its section in view mode');

  toggleLayout();
  const customName = 'Вкладка с длинным названием без разрыва слов';
  await create(one('#characterLayoutAddTab'), customName);
  const customTab = one('#characterLayoutTabs [aria-selected=true]');
  const tabBox = customTab.getBoundingClientRect(), tabStrip = customTab.parentElement.getBoundingClientRect();
  assert(customTab.textContent===customName && tabBox.left>=tabStrip.left-1 && tabBox.right<=tabStrip.right+1,
    'created tab is selected and visible');
  const custom = '[data-sheet-tab="'+customTab.dataset.sheetTab+'"]';
  one(actionsTab).click();
  dragOnto(one(hit).closest('.sheet-source-entry').querySelector('.sheet-handle'), one(custom));
  assert(one(hit)?.closest('.character-source-item'), 'action remains grouped in a custom tab');
  one(statsTab).click();
  const statLabel = one(stat).querySelector('.character-stat-name');
  statLabel.textContent='Сопротивление сверхъестественному воздействию';
  one('.inspector-frame').style.setProperty('--inspector-width','260px');
  const style = view.getComputedStyle(statLabel);
  assert(style.overflowWrap==='normal' && style.wordBreak==='normal' && style.hyphens==='none' &&
    statLabel.getBoundingClientRect().height>parseFloat(style.lineHeight), 'stat text wraps at word boundaries');
  const section = one(stat).closest('.sheet-section');
  const adjacent = [...section.querySelectorAll('.sheet-entry')].find(entry=>entry.querySelector('.character-stat-card') && !entry.contains(one(stat)));
  const cancel = startDrag(statHandle());
  cancel('dragstart');
  cancel('dragover', adjacent, {clientY:adjacent.getBoundingClientRect().top+1});
  assert(one('.sheet-stat-list.sheet-drop > .sheet-drop-placeholder'), 'stat drop marker');
  cancel('dragend');
  assert(!one('.sheet-drop-placeholder,.sheet-entry-moving,.sheet-tab-drop-target'), 'cancel clears the drag visuals');
  dragOnto(statHandle(), one(custom));
  assert(one(stat)?.closest('.sheet-source-actions.sheet-stat-list') && one(hit), 'mixed custom tab preserves action and stat views');
  toggleLayout();
  assert(one(stat) && one(hit)?.closest('.character-source-item') && !one('.sheet-handle'), 'custom tab in view mode');
  return true;
}
