import test from 'node:test';
import assert from 'node:assert/strict';
import { sameVisibleMap } from './map-updates.js';

const map = () => ({ path: '/root', bytes: 100, generatedAt: '2026-01-01', children: [{ path: '/root/child', bytes: 100, deltaKnown: true, deltaBytes: 0 }] });

test('polling unchanged measurements does not trigger map updates', () => {
 const before = map();
 const next = { ...map(), generatedAt: '2026-02-01' };
 assert.equal(sameVisibleMap(before, next), true);
});

test('child growth with an unchanged parent total still updates the map', () => {
 const before = map();
 const next = map();
 next.children[0].bytes = 120;
 next.children[0].deltaBytes = 20;
 assert.equal(sameVisibleMap(before, next), false);
});

test('navigation, deletions and comparison changes update the map', () => {
 for (const modify of [next => { next.path = '/elsewhere'; }, next => { next.children = []; }, next => { next.children[0].deltaSince = '2026-02-01'; }, next => { next.children[0].sizeComplete = false; }]) {
  const next = map();
  modify(next);
  assert.equal(sameVisibleMap(map(), next), false);
 }
});
