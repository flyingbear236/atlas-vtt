// View-only inventory helpers. No action definitions or mechanics are changed.
export function itemActionsForInventory(snapshot, itemId) {
  const result = new Map();
  for (const choice of [...(snapshot.effective?.actionSources || []), ...(snapshot.inactiveActionSources || [])]) {
    if (choice.sourceId !== itemId || !Object.hasOwn(snapshot.actionSelectors || {}, choice.actionId)) continue;
    result.set(choice.actionId, choice);
  }
  const priority = id => id.endsWith('.hit') ? 0 : id.endsWith('.damage') ? 1 : 2;
  return [...result.values()].sort((a, b) => priority(a.actionId) - priority(b.actionId) || a.actionId.localeCompare(b.actionId));
}

// Input is a flattened, server-provided property list with path components.
export function nestInventoryFields(fields = []) {
  const root = {name: '', groups: new Map(), fields: []};
  for (const field of fields) {
    if (!Array.isArray(field.path) || !field.path.length) continue;
    let parent = root;
    for (const part of field.path.slice(0, -1)) {
      if (!parent.groups.has(part)) parent.groups.set(part, {name: part, groups: new Map(), fields: []});
      parent = parent.groups.get(part);
    }
    parent.fields.push({name: field.path.at(-1), value: String(field.value ?? '')});
  }
  return root;
}
