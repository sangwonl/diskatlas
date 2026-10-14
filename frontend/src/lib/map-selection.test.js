import test from 'node:test';
import assert from 'node:assert/strict';
import { singleSelection, selectMapCell, refreshMapSelection, selectionTargets, trashSelection } from './map-selection.js';

const cells = ['a', 'b', 'c', 'd'].map((name, index) => ({ name, path: `/root/${name}`, bytes: index + 1 }));

test('plain selection replaces the existing multi-selection', () => {
  const multiple = selectMapCell(singleSelection(cells[0]), cells[2], true);
  assert.deepEqual(selectMapCell(multiple, cells[1]), singleSelection(cells[1]));
});

test('measurement updates retain selected items and remove disappeared ones', () => {
  const selection = selectMapCell(selectMapCell(singleSelection(cells[0]), cells[1], true), cells[2], true);
  const updated = refreshMapSelection(selection, item => item.name === 'b' ? null : { ...item, bytes: 100 });
  assert.deepEqual(updated.items.map(item => item.name), ['a', 'c']);
  assert.equal(updated.active.path, cells[2].path);
  assert.ok(updated.items.every(item => item.bytes === 100));
});

test('virtual groups expand to unique actual deletion targets', () => {
  const group = { path: 'virtual:a', virtual: true, children: [cells[0], cells[1]] };
  const selection = { items: [group, cells[1], cells[2]] };
  assert.deepEqual(selectionTargets(selection), cells.slice(0, 3));
});

test('batch trash continues after an item fails and reports both outcomes', async () => {
  const calls = [];
  let moving = false;
  const outcome = await trashSelection(cells, async (path, bytes) => {
    assert.equal(moving, false);
    moving = true;
    calls.push({ path, bytes });
    await Promise.resolve();
    moving = false;
    if (path === cells[1].path) throw new Error('permission denied');
    return { pendingSizeTracked: true };
  });
  assert.deepEqual(calls.map(call => call.path), cells.map(item => item.path));
  assert.deepEqual(outcome.moved.map(({ item }) => item.name), ['a', 'c', 'd']);
  assert.deepEqual(outcome.failed.map(({ item }) => item.name), ['b']);
});

test('Ctrl/Cmd toggles only the clicked item and preserves other selections', () => {
  let selection = singleSelection(cells[0]);
  selection = selectMapCell(selection, cells[2], true);
  assert.deepEqual(selection.items, [cells[0], cells[2]]);
  selection = selectMapCell(selection, cells[0], true);
  assert.deepEqual(selection.items, [cells[2]]);
  assert.equal(selection.active.path, cells[2].path);
  selection = selectMapCell(selection, cells[2], true);
  assert.deepEqual(selection, singleSelection(cells[2]));
});
