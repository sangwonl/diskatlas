export function singleSelection(item) {
  return { items: item ? [item] : [], active: item || null };
}

export function selectMapCell(selection, item, toggle = false) {
  if (!toggle) return singleSelection(item);
  const exists = selection.items.some(selected => selected.path === item.path);
  if (exists && selection.items.length === 1) return selection;
  const items = exists ? selection.items.filter(selected => selected.path !== item.path) : [...selection.items, item];
  const active = exists ? items.find(selected => selected.path === selection.active?.path) || items.at(-1) || null : item;
  return { items, active };
}

export function refreshMapSelection(selection, resolve) {
  const items = selection.items.map(resolve).filter(Boolean);
  const active = items.find(item => item.path === selection.active?.path) || items.at(-1) || null;
  return { items, active };
}

export function selectionTargets(selection) {
  const targets = new Map();
  const add = item => {
    if (item.virtual) item.children.forEach(add);
    else targets.set(item.path, item);
  };
  selection.items.forEach(add);
  return [...targets.values()];
}

// Keep successful moves when an individual item cannot be moved. Callers can
// refresh the listing once and retain the failed items for retry.
export async function trashSelection(targets, move) {
  const moved = [];
  const failed = [];
  for (const item of targets) {
    try {
      moved.push({ item, result: await move(item.path, Number(item.bytes || 0)) });
    } catch (reason) {
      failed.push({ item, reason });
    }
  }
  return { moved, failed };
}
