import { defineConfig } from 'vite';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const frontendDir = dirname(fileURLToPath(import.meta.url));
const { version } = JSON.parse(readFileSync(resolve(frontendDir, '../package.json'), 'utf8'));

export default defineConfig({
  define: {
    __DISKATLAS_VERSION__: JSON.stringify(version),
    __DISKATLAS_UPDATE_PREVIEW__: JSON.stringify(process.env.DISKATLAS_UPDATE_PREVIEW === '1'),
  },
});
