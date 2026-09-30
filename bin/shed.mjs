#!/usr/bin/env node
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { fileURLToPath } from 'node:url';
import { scan, summarize, formatBytes, TIERS } from '../src/core.mjs';
import { startServer } from '../src/server.mjs';

const cwd = process.cwd();
const stateDir = path.join(cwd, '.shed');
const lastScanPath = path.join(stateDir, 'last-scan.json');
const historyPath = path.join(stateDir, 'history.jsonl');
const args = process.argv.slice(2);
const command = args[0] || 'scan';

async function ensureState() { await fs.mkdir(stateDir, { recursive: true }); }
function flag(name) { return args.includes(name); }
function value(name) { const i = args.indexOf(name); return i >= 0 ? args[i + 1] : null; }
function pathsFromArgs() { return args.slice(1).filter((x) => !x.startsWith('-') && !['safe', 'caution', 'review', 'protected'].includes(x)); }
function printJson(value) { process.stdout.write(`${JSON.stringify(value, null, 2)}\n`); }
function tierIcon(tier) { return { safe: '🟢', caution: '🟡', review: '🟠', protected: '🔴' }[tier]; }
function insideHome(p) { const home = os.homedir(); const resolved = path.resolve(p); return resolved === home || resolved.startsWith(`${home}${path.sep}`); }

async function loadScan() {
  try { return JSON.parse(await fs.readFile(lastScanPath, 'utf8')); }
  catch { return null; }
}

function printSummary(result) {
  const summary = summarize(result.items);
  console.log(`Scanned ${result.items.length} reclaim candidates (${result.roots.join(', ')})`);
  console.log('');
  for (const tier of TIERS) {
    const s = summary[tier];
    if (!s.count) continue;
    console.log(`  ${tierIcon(tier)} ${tier.padEnd(10)} ${formatBytes(s.bytes).padStart(10)}  ${s.count} item${s.count === 1 ? '' : 's'}`);
  }
  console.log('');
  console.log('Top opportunities');
  result.items.filter((item) => item.tier !== 'protected').slice(0, 10).forEach((item) => {
    console.log(`  ${tierIcon(item.tier)} ${formatBytes(item.bytes).padStart(10)}  ${item.name}  ${item.path}`);
  });
  console.log('\nNext: shed report --json or shed plan');
}

async function run() {
  if (command === 'gui') return startServer({ port: Number(value('--port') || 4173), open: !flag('--no-open') });
  if (command === 'scan') {
    await ensureState();
    const result = await scan(pathsFromArgs().length ? pathsFromArgs() : [os.homedir()]);
    await fs.writeFile(lastScanPath, JSON.stringify(result, null, 2));
    if (flag('--json')) printJson(result); else printSummary(result);
    return;
  }
  const result = await loadScan();
  if (!result) { console.error('No scan found. Run `shed scan` first.'); process.exitCode = 1; return; }
  if (command === 'report') {
    let items = result.items;
    const tier = value('--tier');
    if (tier) items = items.filter((item) => item.tier === tier);
    if (flag('--json')) printJson(items); else items.forEach((item) => console.log(`${tierIcon(item.tier)} ${formatBytes(item.bytes).padStart(10)} ${item.name}\n   ${item.path}`));
    return;
  }
  if (command === 'explain') {
    const needle = args[1];
    const item = result.items.find((candidate) => candidate.path === needle || candidate.ruleId === needle || candidate.id === needle);
    if (!item) { console.error(`No matching item: ${needle || '(missing path or rule)'}`); process.exitCode = 1; return; }
    if (flag('--json')) printJson(item); else {
      console.log(`${tierIcon(item.tier)} ${item.tier} · ${item.ruleId} · ${formatBytes(item.bytes)} · ${item.path}`);
      console.log(`\nWhy\n  ${item.explanation}`);
      console.log(`\nHow to rebuild\n  ${item.rebuild}  (cost: ${item.cost})`);
      console.log(`\nHow Shed handles it\n  ${item.clean === 'blocked' ? 'Protected: selection is blocked.' : 'Moves it to a local quarantine; no permanent delete is performed.'}`);
    }
    return;
  }
  if (command === 'plan') {
    const plan = result.items.filter((item) => item.tier === 'safe').sort((a, b) => b.bytes - a.bytes).slice(0, 20);
    if (flag('--json')) printJson(plan); else {
      console.log('Recommended low-risk cleanup plan (safe tier only)');
      plan.forEach((item, index) => console.log(`${String(index + 1).padStart(2)}. ${formatBytes(item.bytes).padStart(10)} ${item.name} — ${item.path}`));
    }
    return;
  }
  if (command === 'clean') {
    const candidates = result.items.filter((item) => item.tier === 'safe' && item.clean !== 'blocked');
    if (flag('--json')) return printJson({ dryRun: true, candidates });
    const total = candidates.reduce((sum, item) => sum + item.bytes, 0);
    if (flag('--quarantine') && flag('--yes')) {
      await ensureState();
      const batch = `${Date.now()}`;
      const quarantineRoot = path.join(stateDir, 'quarantine', batch);
      const manifest = [];
      for (const item of candidates) {
        // A user must explicitly scan HOME before a move is permitted. This
        // excludes /System, /tmp fixtures, and arbitrary external volumes.
        if (!insideHome(item.path)) continue;
        const source = path.resolve(item.path);
        const destination = path.join(quarantineRoot, encodeURIComponent(source));
        try {
          await fs.mkdir(path.dirname(destination), { recursive: true });
          await fs.rename(source, destination);
          manifest.push({ ...item, quarantinedAt: new Date().toISOString(), quarantinePath: destination });
        } catch (error) { manifest.push({ ...item, error: error.message }); }
      }
      await fs.writeFile(path.join(quarantineRoot, 'manifest.json'), JSON.stringify(manifest, null, 2));
      await fs.appendFile(historyPath, `${JSON.stringify({ at: new Date().toISOString(), action: 'quarantine', items: manifest })}\n`);
      console.log(`Quarantined ${manifest.filter((item) => !item.error).length} item(s). They remain restorable in ${quarantineRoot}.`);
      return;
    }
    console.log(`Dry run: ${candidates.length} safe item(s), ${formatBytes(total)} potential reclaim.`);
    console.log('No files were changed. Use --quarantine --yes to move HOME artifacts into recoverable quarantine.');
    if (flag('--permanent')) { console.error('Permanent deletion is disabled by design.'); process.exitCode = 2; }
    return;
  }
  if (command === 'restore') {
    const requested = args[1];
    const root = path.join(stateDir, 'quarantine');
    let batches = [];
    try { batches = (await fs.readdir(root, { withFileTypes: true })).filter((entry) => entry.isDirectory()).map((entry) => entry.name).sort().reverse(); } catch { /* empty */ }
    const batch = requested && requested !== '--last' ? requested : batches[0];
    if (!batch) { console.log('No quarantined batch found.'); return; }
    const manifestPath = path.join(root, batch, 'manifest.json');
    let manifest;
    try { manifest = JSON.parse(await fs.readFile(manifestPath, 'utf8')); } catch { console.error(`No manifest for quarantine batch ${batch}`); process.exitCode = 1; return; }
    let restored = 0;
    for (const item of manifest) {
      if (item.error || !item.quarantinePath) continue;
      try {
        await fs.access(item.path); console.error(`Skipped (destination exists): ${item.path}`); continue;
      } catch { /* expected */ }
      try { await fs.mkdir(path.dirname(item.path), { recursive: true }); await fs.rename(item.quarantinePath, item.path); restored += 1; } catch (error) { console.error(`Skipped ${item.path}: ${error.message}`); }
    }
    console.log(`Restored ${restored} item(s) from ${batch}.`);
    return;
  }
  if (command === 'quarantine') {
    const subcommand = args[1] || 'list';
    if (subcommand === 'purge') { console.error('Permanent purge is disabled by design. Remove quarantine data manually only after reviewing it.'); process.exitCode = 2; return; }
    const root = path.join(stateDir, 'quarantine');
    try {
      const batches = (await fs.readdir(root, { withFileTypes: true })).filter((entry) => entry.isDirectory());
      if (!batches.length) console.log('Quarantine is empty.');
      else for (const batch of batches) console.log(batch.name);
    } catch { console.log('Quarantine is empty.'); }
    return;
  }
  if (command === 'history') {
    try { process.stdout.write(await fs.readFile(historyPath, 'utf8')); } catch { console.log('No cleanup history yet.'); }
    return;
  }
  if (command === 'doctor') {
    console.log(`Platform: ${process.platform} ${process.arch}`);
    console.log(`Home: ${os.homedir()}`);
    console.log(`Node: ${process.version}`);
    console.log('Safety mode: permanent deletion disabled; scan and clean preview are available.');
    return;
  }
  console.log('shed scan|report|explain|plan|clean|history|doctor|gui');
}

run().catch((error) => { console.error(error.message); process.exitCode = 1; });
