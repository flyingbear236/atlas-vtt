// Build once per immutable character snapshot. Duplicate action IDs are
// intentional when several item instances grant the same action.
const indices = new WeakMap();

function indexFor(snapshot) {
  let index = indices.get(snapshot);
  if (index) return index;
  index = new Map();
  for (const preview of snapshot.actionPreviews || []) {
    let group = index.get(preview.actionId);
    if (!group) {
      group = {all: [], sources: new Map()};
      index.set(preview.actionId, group);
    }
    group.all.push(preview);
    const source = preview.sourceId || '';
    if (!group.sources.has(source)) group.sources.set(source, preview);
  }
  indices.set(snapshot, index);
  return index;
}

export function actionFormulaPreviews(snapshot, actionId) {
  return indexFor(snapshot).get(actionId)?.all || [];
}

export function actionPreviewForSource(snapshot, actionId, sourceId = '') {
  return indexFor(snapshot).get(actionId)?.sources.get(sourceId || '');
}
