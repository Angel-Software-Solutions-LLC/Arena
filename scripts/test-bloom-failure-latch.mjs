// Reproduces the production black-arena sequence on WebGPU.
//
// Observed live on arena.angel-serv.com:
//   render loop threw ... createBindGroup ... Required member is undefined
//   [Arena] disabled bloom after a render-loop failure      <- containment worked
//   [Arena] arena size changed to 2166x2166 - rebuilding scene
//   Babylon.js v9.14.0 - WebGPU1 engine                     <- new engine, NEW pipeline
//   Texture "bloomBlur" not found ... (again)
//   [Frame 3..N] No bind group set at group index 1 ... Invalid CommandBuffer
//
// and NO second containment line, because after the pass is dropped the same
// fault resurfaces as an async GPUValidationError that never throws into JS.
// The canvas then stays black while the render loop happily runs at ~50 fps.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const src = readFileSync(new URL('../frontend/js/renderer/engine.js', import.meta.url), 'utf8');

const method = (name) => {
  const head = `  ${name}(err) {`;
  const i = src.indexOf(head);
  assert.ok(i > 0, `${name} not found`);
  let depth = 0, j = src.indexOf('{', i);
  for (let k = j; k < src.length; k++) {
    if (src[k] === '{') depth++;
    else if (src[k] === '}') { depth--; if (depth === 0) { j = k; break; } }
  }
  return new Function(`return function ${src.slice(i + `  ${name}`.length, j + 1)}`)();
};

const onError = method('_onRenderLoopError');
const quiet = { error() {}, warn() {}, log() {} };
const realConsole = globalThis.console;

// A pipeline as init() builds it, honouring the engine's known-broken flag.
const buildPipeline = (engine) => ({ bloomEnabled: !engine._bloomBroken, disposed: false, dispose() { this.disposed = true; } });
// applyPipelineFlags, with the user's Bloom setting ON (the shipped default).
const applySettings = (engine, bloomSetting = true) => {
  if (!engine.pipeline) return;
  engine.pipeline.bloomEnabled = bloomSetting && !engine._bloomBroken;
};

globalThis.console = quiet;
try {
  const engine = { _onRenderLoopError: onError };

  // --- first init ---
  engine.pipeline = buildPipeline(engine);
  applySettings(engine);
  assert.equal(engine.pipeline.bloomEnabled, true, 'bloom starts on for a healthy device');

  // --- frame 1: the WebGPU bind fault throws and is contained ---
  engine._onRenderLoopError(new TypeError('Failed to execute createBindGroup'));
  assert.equal(engine.pipeline.bloomEnabled, false, 'containment must drop the bloom pass');
  assert.equal(engine._bloomBroken, true, 'the failure must latch on the engine instance');

  // --- round boundary: _rebuildForArenaSize does dispose() then init() ---
  // The engine instance survives; the scene and pipeline do not.
  engine.pipeline = buildPipeline(engine);
  applySettings(engine);
  assert.equal(engine.pipeline.bloomEnabled, false,
    'a scene rebuild must NOT bring back a bloom pass that already failed here');

  // --- the user opens settings and Bloom is still ticked ---
  applySettings(engine, true);
  assert.equal(engine.pipeline.bloomEnabled, false,
    'a settings re-apply must not override the containment either');

  // --- and a second rebuild, since rounds keep coming ---
  engine.pipeline = buildPipeline(engine);
  applySettings(engine);
  assert.equal(engine.pipeline.bloomEnabled, false, 'containment holds for the whole session');

  // A device where bloom works is untouched.
  const healthy = { _onRenderLoopError: onError };
  healthy.pipeline = buildPipeline(healthy);
  applySettings(healthy);
  assert.equal(healthy.pipeline.bloomEnabled, true, 'a healthy device keeps its bloom');
} finally {
  globalThis.console = realConsole;
}

console.log('bloom containment survives the between-round scene rebuild and a settings re-apply');

// The two one-line policy sites that the simulation above stands in for.
// Both run again on every _rebuildForArenaSize, so a regression on either
// re-arms the fault at the next round boundary.
assert.match(src, /pipeline\.bloomEnabled = !this\._bloomBroken;/,
  'init() must build the pipeline with bloom off when it already failed here');
assert.match(src, /this\.pipeline\.bloomEnabled = isEnabled\('rendering', 'bloom'\) && !this\._bloomBroken;/,
  'applyPipelineFlags must not resurrect a failed bloom pass');
console.log('init() and applyPipelineFlags both honour the latch');
