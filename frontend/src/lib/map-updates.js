const fields = ['path', 'name', 'bytes', 'files', 'directories', 'deltaBytes', 'deltaKnown', 'deltaSince', 'deltaGrowthBytes', 'deltaShrinkBytes', 'deltaActivityKnown', 'sizeKnown', 'sizeComplete', 'sizeStale', 'modifiedAt', 'directory', 'symlink', 'measured'];

const sameEntry = (left, right) => fields.every(field => left?.[field] === right?.[field]);

// Measurement timestamps alone do not require rerendering an unchanged map.
export function sameVisibleMap(left, right) {
  if (!left || !right || !sameEntry(left, right)) return false;
  const children = left.children || [];
  const nextChildren = right.children || [];
  return children.length === nextChildren.length && children.every((child, index) => sameEntry(child, nextChildren[index]));
}
