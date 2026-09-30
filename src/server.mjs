import http from 'node:http';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { scan, summarize } from './core.mjs';

const publicDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'public');

export function startServer({ port = 4173, open = true } = {}) {
  let current = null;
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

function sendJson(res, body, status = 200) {
  res.writeHead(status, { 'content-type': 'application/json; charset=utf-8', 'access-control-allow-origin': '*' });
  res.end(JSON.stringify(body));
}
