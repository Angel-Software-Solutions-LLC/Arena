import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const source = readFileSync(new URL('../frontend/js/renderer/environment.js', import.meta.url), 'utf8');
const engineSource = readFileSync(new URL('../frontend/js/renderer/engine.js', import.meta.url), 'utf8');
const segmentMatch = source.match(/const ZONE_RING_SEGMENTS = (\d+);/);

assert.ok(segmentMatch, 'safe-zone tessellation must have one named budget');
const segments = Number(segmentMatch[1]);
assert.ok(segments >= 48 && segments <= 64,
  `safe-zone tessellation must stay within the spectator-quality 48-64 budget (got ${segments})`);

const zoneBuilder = source.match(/CreateTorus\('zoneRing',[\s\S]*?\}, this\.scene\)/)?.[0] || '';
const targetBuilder = source.match(/CreateTorus\('targetRing',[\s\S]*?\}, this\.scene\)/)?.[0] || '';
assert.match(zoneBuilder, /tessellation: ZONE_RING_SEGMENTS/,
  'the current-zone torus must use the shared geometry budget');
assert.match(targetBuilder, /tessellation: ZONE_RING_SEGMENTS/,
  'the target-zone torus must use the shared geometry budget');

// Babylon's v9.14.0 torus builder emits a (segments + 1)^2 vertex grid and six
// indices per grid vertex. Keep both functional rings within a predictable
// combined geometry budget so a later visual tweak cannot restore 256 segments.
const verticesPerRing = (segments + 1) ** 2;
const indicesPerRing = verticesPerRing * 6;
assert.ok(verticesPerRing <= 4225, `safe-zone ring vertex budget exceeded: ${verticesPerRing}`);
assert.ok(indicesPerRing <= 25350, `safe-zone ring index budget exceeded: ${indicesPerRing}`);
assert.ok(verticesPerRing * 2 <= 8450, 'both safe-zone rings must stay under 8,450 vertices');
assert.ok(indicesPerRing * 2 <= 50700, 'both safe-zone rings must stay under 50,700 indices');
assert.match(engineSource, /environment\.js\?v=20260903a/,
  'the renderer entrypoint must invalidate cached pre-budget environment geometry');

console.log(`safe-zone geometry budget: ${segments} segments, <=${verticesPerRing} vertices and <=${indicesPerRing} indices per ring`);
