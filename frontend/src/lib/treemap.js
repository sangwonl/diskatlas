function layoutWeight(item) {
  return Math.max(0, Number(item.layoutBytes ?? item.bytes));
}

function worstAspect(row, side) {
  if (!row.length || side <= 0) return Infinity;
  const sum = row.reduce((total, entry) => total + entry.area, 0);
  const largest = Math.max(...row.map(entry => entry.area));
  const smallest = Math.min(...row.map(entry => entry.area));
  if (sum <= 0 || smallest <= 0) return Infinity;
  const sideSquared = side * side;
  return Math.max(
    sideSquared * largest / (sum * sum),
    sum * sum / (sideSquared * smallest),
  );
}

function placeRow(row, frame, output) {
  const rowArea = row.reduce((sum, entry) => sum + entry.area, 0);
  if (frame.width >= frame.height) {
    const width = frame.height > 0 ? rowArea / frame.height : 0;
    let y = frame.y;
    row.forEach((entry, index) => {
      const height = width > 0 ? entry.area / width : 0;
      output.push({
        ...entry.item,
        x: frame.x,
        y,
        width,
        height: index === row.length - 1 ? frame.y + frame.height - y : height,
      });
      y += height;
    });
    return { x: frame.x + width, y: frame.y, width: Math.max(0, frame.width - width), height: frame.height };
  }

  const height = frame.width > 0 ? rowArea / frame.width : 0;
  let x = frame.x;
  row.forEach((entry, index) => {
    const width = height > 0 ? entry.area / height : 0;
    output.push({
      ...entry.item,
      x,
      y: frame.y,
      width: index === row.length - 1 ? frame.x + frame.width - x : width,
      height,
    });
    x += width;
  });
  return { x: frame.x, y: frame.y + height, width: frame.width, height: Math.max(0, frame.height - height) };
}

function squarify(items, frame) {
  const totalWeight = items.reduce((sum, item) => sum + layoutWeight(item), 0);
  if (!totalWeight) return [];

  const scale = frame.width * frame.height / totalWeight;
  const remaining = items.map(item => ({ item, area: layoutWeight(item) * scale }));
  const output = [];
  let bounds = { ...frame };
  let row = [];

  while (remaining.length) {
    const candidate = remaining[0];
    const side = Math.min(bounds.width, bounds.height);
    if (!row.length || worstAspect([...row, candidate], side) <= worstAspect(row, side)) {
      row.push(candidate);
      remaining.shift();
      continue;
    }
    bounds = placeRow(row, bounds, output);
    row = [];
  }
  if (row.length) placeRow(row, bounds, output);
  return output;
}

// Work in screen pixels, not a normalized square: aspect ratios must match
// the rendered window. Groups are last in the same mosaic, never a footer.
export function layoutTreemap(source, viewport = { width: 1100, height: 600 }) {
  const frame = { x: 0, y: 0, width: viewport.width || 1100, height: viewport.height || 600 };
  const regular = source.filter(item => !item.virtual && layoutWeight(item) > 0).sort((a, b) => layoutWeight(b) - layoutWeight(a));
  const virtual = source.filter(item => item.virtual && layoutWeight(item) > 0).sort((a, b) => layoutWeight(b) - layoutWeight(a));
  const regularWeight = regular.reduce((sum, item) => sum + layoutWeight(item), 0);
  const area = frame.width * frame.height;
  const share = Math.min(0.08, 160 * 64 / area);
  const groups = virtual.map(item => ({ ...item, layoutBytes: regularWeight ? regularWeight * share / (1 - share) : layoutWeight(item) }));
  let output = squarify([...regular, ...groups], frame);
  // A very uneven two-item distribution otherwise produces a hairline.
  // Reserve enough width/height for the group label and recompute geometry.
  for (let pass = 0; groups.length && regularWeight && pass < 12; pass += 1) {
    const group = output.find(cell => cell.virtual);
    const factor = Math.max(Math.min(112, frame.width / 3) / group.width,
      Math.min(48, frame.height / 3) / group.height);
    if (factor <= 1.01) break;
    groups[0].layoutBytes *= Math.min(2, factor * 1.05);
    output = squarify([...regular, ...groups], frame);
  }
  return output.map(cell => ({ ...cell,
    x: cell.x / frame.width * 100, y: cell.y / frame.height * 100,
    width: cell.width / frame.width * 100, height: cell.height / frame.height * 100,
  }));
}
