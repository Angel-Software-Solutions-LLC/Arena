import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';

const canonicalRepo = 'https://github.com/Angel-Software-Solutions-LLC/Arena';
const legacyRepo = ['github.com', 'ablac', 'Arena'].join('/');
const homepage = readFileSync(new URL('../frontend/index.html', import.meta.url), 'utf8');
const app = readFileSync(new URL('../frontend/js/app.js', import.meta.url), 'utf8');

assert.ok(homepage.includes(canonicalRepo), 'homepage source link must use the canonical repository');
assert.ok(app.includes(canonicalRepo), 'commit fallback link must use the canonical repository');

try {
  const staleLinks = execFileSync(
    'git',
    ['grep', '-n', '-F', legacyRepo, '--', ':!frontend/vendor/**'],
    { encoding: 'utf8' },
  );
  assert.fail(`stale repository links remain:\n${staleLinks}`);
} catch (error) {
  if (error?.status !== 1) throw error;
}

console.log('public and generated repository links use the canonical organization URL');
