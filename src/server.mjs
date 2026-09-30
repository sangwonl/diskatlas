import http from 'node:http';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { scan, summarize } from './core.mjs';

const publicDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'public');

export function startServer({ port = 4173, open = true } = {}) {
  let current = null;
  const stateDir = path.join(process.cwd(), '.shed');
  const server = http.createServer(async (req, res) => {
    try {
      const url = new URL(req.url, `http://${req.headers.host}`);
      if (url.pathname === '/api/scan') {
        current = await scan([url.searchParams.get('path') || process.env.HOME || process.cwd()]);
        return sendJson(res, { ...current, summary: summarize(current.items) });
      }
      if (url.pathname === '/api/items') {
        return sendJson(res, current?.items || []);
      }
      if (url.pathname === '/api/history') {
        return sendJson(res, { entries: await readJsonLines(path.join(stateDir, 'history.jsonl')) });
      }
      if (url.pathname === '/api/quarantine') {
        return sendJson(res, { batches: await quarantineBatches(stateDir) });
      }
      if (url.pathname === '/api/plan') {
        const items = (current?.items || []).filter((item) => item.tier === 'safe').sort((a, b) => b.bytes - a.bytes);
        return sendJson(res, { items, totalBytes: items.reduce((sum, item) => sum + item.bytes, 0) });
      }
      if (url.pathname === '/api/clean' && req.method === 'POST') {
        const body = await readBody(req);
        const selected = new Set(Array.isArray(body.ids) ? body.ids : []);
        const items = (current?.items || []).filter((item) => selected.has(item.id) && (item.tier === 'safe' || (body.unlockReview === true && item.tier === 'review')) && item.clean !== 'blocked');
        if (body.confirm !== true) return sendJson(res, { dryRun: true, items, totalBytes: items.reduce((sum, item) => sum + item.bytes, 0) });
        return sendJson(res, { dryRun: true, blocked: true, message: 'GUI mutations are disabled in this build. Use the CLI quarantine command with explicit --yes.', items });
      }
      const requested = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
      if (requested.includes('..')) return sendJson(res, { error: 'invalid path' }, 400);
      const file = path.join(publicDir, requested);
      const data = await fs.readFile(file);
      res.writeHead(200, { 'content-type': requested.endsWith('.html') ? 'text/html; charset=utf-8' : 'text/plain; charset=utf-8' });
      res.end(data);
    } catch (error) { sendJson(res, { error: error.message }, 500); }
  });
  server.listen(port, '127.0.0.1', () => {
    const address = `http://127.0.0.1:${port}`;
    console.log(`Shed GUI: ${address}`);
    if (open) import('node:child_process').then(({ exec }) => exec(`open ${address}`));
  });
  return server;
}

async function readBody(req) {
  let data = '';
  for await (const chunk of req) data += chunk;
  try { return JSON.parse(data || '{}'); } catch { return {}; }
}

async function readJsonLines(file) {
  try {
    const text = await fs.readFile(file, 'utf8');
    return text.split('\n').filter(Boolean).map((line) => JSON.parse(line));
  } catch { return []; }
}

async function quarantineBatches(stateDir) {
  const root = path.join(stateDir, 'quarantine');
  try {
    const entries = await fs.readdir(root, { withFileTypes: true });
    const batches = [];
    for (const entry of entries.filter((value) => value.isDirectory()).sort((a, b) => b.name.localeCompare(a.name))) {
      try {
        const manifest = JSON.parse(await fs.readFile(path.join(root, entry.name, 'manifest.json'), 'utf8'));
        batches.push({ id: entry.name, items: manifest, count: manifest.length });
      } catch { batches.push({ id: entry.name, items: [], count: 0 }); }
    }
    return batches;
  } catch { return []; }
}

function sendJson(res, body, status = 200) {
  res.writeHead(status, { 'content-type': 'application/json; charset=utf-8', 'access-control-allow-origin': '*' });
  res.end(JSON.stringify(body));
}
