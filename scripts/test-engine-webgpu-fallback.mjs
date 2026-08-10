import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const source = readFileSync(new URL('../frontend/js/renderer/engine.js', import.meta.url), 'utf8');
const isolatedSource = source.replace(/import[\s\S]*?from '[^']+';\r?\n/g, '');
const moduleURL = `data:text/javascript;base64,${Buffer.from(isolatedSource).toString('base64')}`;
const { webGPUAvailableWithin } = await import(moduleURL);

assert.match(
  source,
  /new B\.Engine\(this\.canvas, false, \{[\s\S]{0,420}stencil:\s*true/,
  'the WebGL fallback must enable stencil for the pickup HighlightLayer',
);

assert.equal(typeof webGPUAvailableWithin, 'function',
  'the renderer must expose one bounded WebGPU capability probe');

const neverSettles = new Promise(() => {});
const startedAt = performance.now();
const timedOut = await webGPUAvailableWithin({
  WebGPUEngine: { IsSupportedAsync: neverSettles },
}, 10);
const elapsed = performance.now() - startedAt;

assert.equal(timedOut, false, 'an unresolved WebGPU probe must fall back to WebGL');
assert.ok(elapsed < 250, `WebGPU fallback exceeded its bounded test window (${elapsed.toFixed(1)}ms)`);
assert.equal(await webGPUAvailableWithin({
  WebGPUEngine: { IsSupportedAsync: Promise.resolve(true) },
}, 100), true);

console.log('WebGPU capability probing is bounded and falls back to WebGL when the browser stalls');

// Regression gate for the blank-arena fix (#228): on a GPU-capable browser the default
// route must NEVER construct a WebGPUEngine, and ?webgpu=1 must be the sole opt-in.
// The Playwright specs delete navigator.gpu, so they pass with or without this selector
// and cannot cover it; this asserts the real predicate from the shipped source instead.
const gateLine = source.match(/const wantWebGPU = .*;/);
assert.ok(gateLine, 'engine.js must gate WebGPU behind an explicit wantWebGPU predicate');
assert.doesNotMatch(
  gateLine[0],
  /\.has\(/,
  'the WebGPU opt-in must not match on presence alone: ?webgpu=0 and ?webgpu=false would enable it',
);

// Evaluate the SHIPPED predicate against real query strings with a stubbed location.
const evalGate = (search) => {
  const fn = new Function('location', `${gateLine[0]} return wantWebGPU;`);
  return fn({ search });
};
assert.equal(evalGate('?webgpu=1'), true, '?webgpu=1 must opt in to WebGPU');
for (const search of ['', '?', '?webgpu', '?webgpu=0', '?webgpu=false', '?webgpu=true', '?webgpu=yes', '?other=1']) {
  assert.equal(evalGate(search), false, `WebGPU must stay off for ${search || '(no query)'}`);
}

// The only WebGPUEngine construction must sit behind that gate.
assert.match(
  source,
  /const webGPUSupported = wantWebGPU && await webGPUAvailableWithin\(B\)[\s\S]{0,200}new B\.WebGPUEngine\(/,
  'WebGPUEngine may only be constructed after the wantWebGPU gate passes',
);
assert.equal(
  (source.match(/new B\.WebGPUEngine\(/g) || []).length, 1,
  'there must be exactly one WebGPUEngine construction site, so the gate cannot be bypassed',
);

console.log('WebGPU is off by default and opt-in only via an exact ?webgpu=1');
