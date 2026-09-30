import fs from 'node:fs/promises';
import fsSync from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { spawnSync } from 'node:child_process';

export const TIERS = ['safe', 'caution', 'review', 'protected'];

const projectRules = [
  { id: 'node.modules', name: 'Node.js dependencies', markers: ['package.json'], targets: ['node_modules'], tier: 'safe', lockfiles: ['package-lock.json', 'pnpm-lock.yaml', 'yarn.lock', 'bun.lockb'], rebuild: 'npm ci', cost: 'low', category: 'Project output' },
  { id: 'node.build', name: 'Node.js build cache', markers: ['package.json'], targets: ['.next', '.nuxt', '.turbo', '.parcel-cache'], tier: 'safe', rebuild: 'npm run build', cost: 'low', category: 'Project output' },
  { id: 'rust.target', name: 'Rust build output', markers: ['Cargo.toml'], targets: ['target'], tier: 'safe', rebuild: 'cargo build', cost: 'low', category: 'Project output' },
  { id: 'python.cache', name: 'Python build cache', markers: ['pyproject.toml', 'requirements.txt'], targets: ['__pycache__', '.pytest_cache', '.mypy_cache', '.ruff_cache'], tier: 'safe', rebuild: 'python -m pip install -r requirements.txt', cost: 'low', category: 'Project output' },
  { id: 'python.venv', name: 'Python virtual environment', markers: ['pyproject.toml', 'requirements.txt'], targets: ['.venv', 'venv'], tier: 'caution', rebuild: 'python -m venv .venv', cost: 'medium', category: 'Project output' },
  { id: 'java.build', name: 'Java/Kotlin build output', markers: ['build.gradle', 'build.gradle.kts', 'pom.xml'], targets: ['build', '.gradle', 'target'], tier: 'safe', rebuild: './gradlew build', cost: 'medium', category: 'Project output' },
  { id: 'swift.build', name: 'Swift package build output', markers: ['Package.swift'], targets: ['.build'], tier: 'safe', rebuild: 'swift build', cost: 'low', category: 'Project output' },
  { id: 'dotnet.build', name: '.NET build output', markers: ['*.csproj', '*.sln'], targets: ['bin', 'obj'], tier: 'safe', rebuild: 'dotnet build', cost: 'low', category: 'Project output' },
  { id: 'unity.cache', name: 'Unity generated data', markers: ['ProjectSettings'], targets: ['Library', 'Temp', 'Obj'], tier: 'safe', rebuild: 'Open the project in Unity', cost: 'medium', category: 'Project output' },
  { id: 'flutter.build', name: 'Flutter build output', markers: ['pubspec.yaml'], targets: ['.dart_tool', 'build'], tier: 'safe', rebuild: 'flutter pub get', cost: 'low', category: 'Project output' },
];

function home() { return os.homedir(); }
function expand(p) {
  if (!p) return p;
  const envMatch = p.match(/^%([^%]+)%(.*)$/);
  if (envMatch && process.env[envMatch[1]]) return path.resolve(process.env[envMatch[1]], envMatch[2].replace(/^[/\\]/, ''));
  if (p === '~') return home();
  if (p.startsWith('~/')) return path.join(home(), p.slice(2));
  return path.resolve(p);
}

function platformPath({ darwin, linux, windows, env }) {
  if (env && process.env[env]) return expand(process.env[env]);
  if (process.platform === 'darwin') return expand(darwin);
  if (process.platform === 'win32') return expand(windows);
  return expand(linux);
}

const globalRules = [
  { id: 'npm.cache', name: 'npm cache', tier: 'safe', category: 'Package cache', paths: () => platformPath({ darwin: '~/.npm/_cacache', linux: '~/.npm/_cacache', windows: '%LocalAppData%/npm-cache', env: 'npm_config_cache' }), rebuild: 'npm install', cost: 'low' },
  { id: 'pnpm.store', name: 'pnpm store', tier: 'safe', category: 'Package cache', paths: () => process.env.PNPM_STORE_PATH ? expand(process.env.PNPM_STORE_PATH) : platformPath({ darwin: '~/Library/pnpm/store', linux: '~/.local/share/pnpm/store', windows: '%LocalAppData%/pnpm/store' }), rebuild: 'pnpm install', cost: 'low' },
  { id: 'pip.cache', name: 'pip cache', tier: 'safe', category: 'Package cache', paths: () => platformPath({ darwin: '~/Library/Caches/pip', linux: '~/.cache/pip', windows: '%LocalAppData%/pip/Cache' }), rebuild: 'python -m pip install', cost: 'low' },
  { id: 'go.build-cache', name: 'Go build cache', tier: 'safe', category: 'Build cache', paths: () => process.env.GOCACHE ? expand(process.env.GOCACHE) : platformPath({ darwin: '~/Library/Caches/go-build', linux: '~/.cache/go-build', windows: '%LocalAppData%/go-build' }), rebuild: 'go build ./...', cost: 'low' },
  { id: 'go.mod-cache', name: 'Go module cache', tier: 'safe', category: 'Package cache', paths: () => process.env.GOMODCACHE ? expand(process.env.GOMODCACHE) : platformPath({ darwin: '~/go/pkg/mod', linux: '~/go/pkg/mod', windows: '%UserProfile%/go/pkg/mod' }), rebuild: 'go mod download', cost: 'medium' },
  { id: 'cargo.registry', name: 'Cargo registry', tier: 'safe', category: 'Package cache', paths: () => expand(path.join(process.env.CARGO_HOME || '~/.cargo', 'registry')), rebuild: 'cargo fetch', cost: 'medium' },
  { id: 'maven.repository', name: 'Maven repository', tier: 'safe', category: 'Package cache', paths: () => expand('~/.m2/repository'), rebuild: 'mvn dependency:resolve', cost: 'medium' },
  { id: 'gradle.cache', name: 'Gradle cache', tier: 'safe', category: 'Build cache', paths: () => expand('~/.gradle/caches'), rebuild: './gradlew build', cost: 'medium' },
  { id: 'xcode.derived-data', name: 'Xcode DerivedData', tier: 'safe', category: 'IDE cache', paths: () => process.platform === 'darwin' ? expand('~/Library/Developer/Xcode/DerivedData') : null, rebuild: 'Build the Xcode project again', cost: 'medium' },
  { id: 'xcode.device-support', name: 'iOS DeviceSupport', tier: 'caution', category: 'IDE cache', paths: () => process.platform === 'darwin' ? expand('~/Library/Developer/Xcode/iOS DeviceSupport') : null, rebuild: 'Reconnect the device in Xcode', cost: 'high' },
  { id: 'xcode.archives', name: 'Xcode Archives', tier: 'review', category: 'IDE data', paths: () => process.platform === 'darwin' ? expand('~/Library/Developer/Xcode/Archives') : null, rebuild: 'Archives are not automatically reproducible', cost: 'high' },
  { id: 'android.avd', name: 'Android virtual devices', tier: 'caution', category: 'Emulator data', paths: () => platformPath({ darwin: '~/.android/avd', linux: '~/.android/avd', windows: '%UserProfile%/.android/avd' }), rebuild: 'Create the emulator again in Android Studio', cost: 'high' },
  { id: 'huggingface.models', name: 'Hugging Face model cache', tier: 'caution', category: 'AI model', paths: () => process.env.HF_HOME ? expand(path.join(process.env.HF_HOME, 'hub')) : platformPath({ darwin: '~/.cache/huggingface/hub', linux: '~/.cache/huggingface/hub', windows: '%UserProfile%/.cache/huggingface' }), rebuild: 'huggingface-cli download <model>', cost: 'high' },
  { id: 'ollama.models', name: 'Ollama models', tier: 'caution', category: 'AI model', paths: () => platformPath({ darwin: '~/.ollama/models', linux: '~/.ollama/models', windows: '%UserProfile%/.ollama/models', env: 'OLLAMA_MODELS' }), rebuild: 'ollama pull <model>', cost: 'high' },
  { id: 'lmstudio.models', name: 'LM Studio models', tier: 'caution', category: 'AI model', paths: () => expand('~/.lmstudio/models'), rebuild: 'Download the model again in LM Studio', cost: 'high' },
  { id: 'conda.packages', name: 'Conda package cache', tier: 'safe', category: 'Package cache', paths: () => expand('~/miniconda3/pkgs'), rebuild: 'conda install <package>', cost: 'medium' },
  { id: 'jetbrains.cache', name: 'JetBrains IDE cache', tier: 'safe', category: 'IDE cache', paths: () => platformPath({ darwin: '~/Library/Caches/JetBrains', linux: '~/.cache/JetBrains', windows: '%LocalAppData%/JetBrains' }), rebuild: 'Open the IDE again', cost: 'low' },
];

const protectedNames = new Set(['.git', '.ssh', '.gnupg', 'Documents', 'Desktop', 'Pictures', 'Movies', 'Music', 'Library/Keychains']);

function isProtected(p) {
  const normalized = path.normalize(p);
  if (normalized === home()) return true;
  const rel = path.relative(home(), normalized);
  // Explicitly scanned fixture or project roots outside HOME are allowed. System
  // locations are never discovered by the default scan and are rejected below.
  if (rel.startsWith('..')) return normalized === path.parse(normalized).root || normalized.startsWith('/System/');
  if (!rel) return true;
  const parts = rel.split(path.sep);
  if (parts.some((part) => protectedNames.has(part))) return true;
  if (rel.includes(`${path.sep}Library${path.sep}Keychains`)) return true;
  if (parts.some((part) => part.endsWith('.app'))) return true;
  return false;
}

async function statSafe(p) {
  try { return await fs.lstat(p); } catch { return null; }
}

async function sizeOf(p, seen = new Set()) {
  const st = await statSafe(p);
  if (!st || st.isSymbolicLink()) return { bytes: 0, files: 0 };
  if (st.isFile()) {
    const key = `${st.dev}:${st.ino}`;
    if (seen.has(key)) return { bytes: 0, files: 0 };
    seen.add(key);
    return { bytes: st.size, files: 1 };
  }
  if (!st.isDirectory()) return { bytes: 0, files: 0 };
  let bytes = 0; let files = 0;
  let entries = [];
  try { entries = await fs.readdir(p, { withFileTypes: true }); } catch { return { bytes: 0, files: 0, inaccessible: true }; }
  for (const entry of entries) {
    const result = await sizeOf(path.join(p, entry.name), seen);
    bytes += result.bytes; files += result.files;
  }
  return { bytes, files };
}

function formatBytes(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let value = bytes; let i = -1;
  do { value /= 1024; i += 1; } while (value >= 1024 && i < units.length - 1);
  return `${value.toFixed(value >= 100 ? 0 : value >= 10 ? 1 : 2)} ${units[i]}`;
}

function gitSignal(projectPath, targetPath) {
  if (!projectPath) return { ignored: false, tracked: false };
  const ignored = spawnSync('git', ['-C', projectPath, 'check-ignore', '-q', targetPath], { stdio: 'ignore' }).status === 0;
  const tracked = spawnSync('git', ['-C', projectPath, 'ls-files', '--error-unmatch', targetPath], { stdio: 'ignore' }).status === 0;
  return { ignored, tracked };
}

function classify(rule, targetPath, projectPath, hasLockfile = false) {
  let tier = rule.tier;
  const signals = [];
  if (isProtected(targetPath)) { tier = 'protected'; signals.push('Protected user or application path'); }
  if (projectPath) {
    const git = gitSignal(projectPath, targetPath);
    if (git.tracked) { tier = 'protected'; signals.push('Git tracks files in this path'); }
    else if (git.ignored) signals.push('Git ignores this path');
    if (rule.lockfiles && !hasLockfile) { tier = 'caution'; signals.push('No lockfile was found'); }
    else if (hasLockfile) signals.push('A lockfile can reproduce the exact dependency versions');
  }
  return { tier, signals };
}

function itemFrom(rule, targetPath, projectPath, size, hasLockfile = false) {
  const classification = classify(rule, targetPath, projectPath, hasLockfile);
  let modifiedAt = null;
  try { modifiedAt = new Date(fsSync.statSync(targetPath).mtimeMs).toISOString(); } catch { modifiedAt = null; }
  const why = classification.tier === 'protected'
    ? 'Protected by Shed safety rules and cannot be selected.'
    : `${rule.name} can be recreated with ${rule.rebuild}. ${classification.signals.join('; ') || 'Matched a known developer artifact rule.'}`;
  return {
    id: `${rule.id}:${targetPath}`,
    ruleId: rule.id,
    name: rule.name,
    path: targetPath,
    projectPath: projectPath || null,
    category: rule.category,
    tier: classification.tier,
    bytes: size.bytes,
    files: size.files,
    modifiedAt,
    signals: classification.signals,
    explanation: why,
    rebuild: rule.rebuild,
    cost: rule.cost,
    clean: classification.tier === 'protected' ? 'blocked' : 'quarantine',
  };
}

async function findProjects(root, options = {}) {
  const results = [];
  const maxProjects = options.maxProjects || 5000;
  const seen = options.seen || new Set();
  async function visit(dir) {
    if (results.length >= maxProjects) return;
    let entries;
    try { entries = await fs.readdir(dir, { withFileTypes: true }); } catch { return; }
    const names = new Set(entries.map((entry) => entry.name));
    for (const rule of projectRules) {
      const marker = rule.markers.find((m) => m.startsWith('*')
        ? entries.some((entry) => entry.name.endsWith(m.slice(1)))
        : names.has(m));
      if (!marker) continue;
      const hasLockfile = Boolean(rule.lockfiles?.some((lock) => names.has(lock)));
      for (const target of rule.targets) {
        const targetPath = path.join(dir, target);
        const st = await statSafe(targetPath);
        if (!st || st.isSymbolicLink() || (!st.isDirectory() && !st.isFile())) continue;
        const size = await sizeOf(targetPath, seen);
        if (size.bytes > 0) results.push(itemFrom(rule, targetPath, dir, size, hasLockfile));
      }
    }
    for (const entry of entries) {
      if (!entry.isDirectory() || entry.isSymbolicLink()) continue;
      if (['.git', 'node_modules', 'target', '.next', '.venv', 'venv', 'Library', 'Applications'].includes(entry.name)) continue;
      await visit(path.join(dir, entry.name));
      if (results.length >= maxProjects) return;
    }
  }
  await visit(root);
  return results;
}

export async function scan(scanPaths = [home()], options = {}) {
  const roots = scanPaths.length ? scanPaths.map(expand) : [home()];
  const items = [];
  const seen = new Set();
  const seenGlobal = new Set();
  const includeGlobal = roots.some((root) => root === home() || root.startsWith(`${home()}${path.sep}`));
  if (includeGlobal) {
    for (const rule of globalRules) {
      const targetPath = rule.paths();
      if (!targetPath || seenGlobal.has(targetPath)) continue;
      seenGlobal.add(targetPath);
      const st = await statSafe(targetPath);
      if (!st || st.isSymbolicLink()) continue;
      const size = await sizeOf(targetPath, seen);
      if (size.bytes > 0) items.push(itemFrom(rule, targetPath, null, size));
    }
  }
  for (const root of roots) {
    const st = await statSafe(root);
    if (st?.isDirectory()) items.push(...await findProjects(root, { ...options, seen }));
  }
  items.sort((a, b) => b.bytes - a.bytes);
  return { generatedAt: new Date().toISOString(), roots, items };
}

export function summarize(items) {
  const summary = Object.fromEntries(TIERS.map((tier) => [tier, { bytes: 0, count: 0 }]));
  for (const item of items) { summary[item.tier].bytes += item.bytes; summary[item.tier].count += 1; }
  return summary;
}

export { formatBytes, expand, globalRules, projectRules, isProtected };
