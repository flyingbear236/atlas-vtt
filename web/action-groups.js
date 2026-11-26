// Presentation only. The executor continues to address an action by its
// definition ID and the exact inventory item instance ID (sourceId).
export function groupSheetActionSources(snapshot, sections) {
  const byEntry = new Map(), handled = new Set(), groups = new Map();
  const available = new Map();
  for (const choice of [...(snapshot.effective?.actionSources || []), ...(snapshot.inactiveActionSources || [])]) {
    if (!Object.hasOwn(snapshot.actionSelectors || {}, choice.actionId)) continue;
    if (!available.has(choice.actionId)) available.set(choice.actionId, []);
    available.get(choice.actionId).push(choice);
  }
  for (const section of sections) for (const key of section.items) {
    if (!key.startsWith('action:')) continue;
    const actionId = key.slice(7);
    if (!snapshot.actions?.[actionId]?.v04) continue;
    const choices = available.get(actionId);
    if (!choices?.length) continue;
    handled.add(key);
    for (const choice of choices) {
      const groupId = choice.sourceId ? `item:${choice.sourceId}` : `action:${actionId}`;
      let group = groups.get(groupId);
      if (!group) {
        group = {sourceId: choice.sourceId || '', choices: []};
        groups.set(groupId, group);
        if (!byEntry.has(key)) byEntry.set(key, []);
        byEntry.get(key).push(group);
      }
      if (!group.choices.some(row => row.actionId === actionId)) group.choices.push(choice);
    }
  }
  for (const group of groups.values()) group.choices.sort((a, b) => {
    const rank = id => id.endsWith('.hit') ? 0 : id.endsWith('.damage') ? 1 : 2;
    return rank(a.actionId) - rank(b.actionId);
  });
  return {byEntry, handled};
}

// Only offer a simplified total when every preview part is additive, known,
// and numeric. The displayed full expression remains available in the chips.
export function shortActionFormula(preview) {
  if (!preview?.parts?.length || preview.notes?.length) return '';
  const dice = [];
  let modifier = 0;
  for (const part of preview.parts) {
    const sign = part.sign === '-' ? -1 : 1;
    if (part.value === '' || part.value == null) {
      if (!/^\d+d\d+$/i.test(part.label) || sign === -1) return '';
      dice.push(part.label.replace(/^1d/i, 'd'));
    } else {
      const number = Number(part.value);
      if (!Number.isFinite(number)) return '';
      modifier += sign * number;
    }
  }
  return dice.join(' + ') + (modifier > 0 ? `${dice.length ? ' + ' : ''}${modifier}` : modifier < 0 ? ` − ${-modifier}` : '') || '0';
}

// Show the known additive portion separately from terms that require execution.
// Preserve the strict shortActionFormula contract for callers that need a total.
export function knownActionFormula(preview) {
  if (!preview?.parts?.length) return null;
  const parts = [];
  let partial = Boolean(preview.notes?.length);
  for (const part of preview.parts) {
    const isDice = part.value == null || part.value === '';
    if (isDice ? /^\d+d\d+$/i.test(part.label) && part.sign !== '-'
               : Number.isSafeInteger(Number(part.value)) && /^-?\d+$/.test(String(part.value))) {
      parts.push(part);
    } else {
      partial = true;
    }
  }
  if (!parts.length) return null;
  const text = shortActionFormula({parts});
  return text ? {text, partial} : null;
}
