// Typography guards for the arena stylesheets.
//
// Two defects shipped together and both are invisible in review:
//
// 1. letter-spacing is applied after EVERY character, including the space, so
//    negative tracking narrows the word gap as well as the letter fit. Measured
//    on the live site, "Match Telemetry" rendered with a 0.153em word gap and
//    read as one word. Each negative tracking value must be paired with an equal
//    positive word-spacing, which restores the gap and leaves the letterforms
//    exactly as tight as designed.
//
// 2. A line-height below 1 makes the line box shorter than the glyphs. That is
//    a deliberate look for a single-line display heading, but the HUD round
//    title wraps every round the map has a shape ("Round 77 . diamond"), and
//    wrapped lines then sit on top of each other.
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';

const CSS_DIR = new URL('../frontend/css/', import.meta.url);
const files = readdirSync(CSS_DIR).filter((f) => f.endsWith('.css'));

const problems = [];
for (const file of files) {
  const src = readFileSync(new URL(file, CSS_DIR), 'utf8');
  const blocks = src.split(/(?<=\})/);
  for (const block of blocks) {
    const neg = block.match(/letter-spacing:\s*(-0\.\d+)em;/);
    if (neg && !/word-spacing:/.test(block)) {
      const sel = (block.match(/([^{}]+)\{/) || [, '?'])[1].trim().split('\n').pop();
      problems.push(`${file}: "${sel}" has letter-spacing ${neg[1]}em with no compensating word-spacing`);
    }
    const lh = block.match(/line-height:\s*(0\.\d+)\s*;/);
    if (lh) {
      const sel = (block.match(/([^{}]+)\{/) || [, '?'])[1].trim().split('\n').pop();
      problems.push(`${file}: "${sel}" has line-height ${lh[1]}, below 1, so wrapped lines overlap`);
    }
  }
}

assert.deepEqual(problems, [], 'typography guards:\n  ' + problems.join('\n  '));

// The guards must be able to fail, or they are decoration. Prove it on synthetic
// input rather than trusting that the real files happen to be clean.
const probe = (css) => {
  const found = [];
  for (const block of css.split(/(?<=\})/)) {
    if (/letter-spacing:\s*-0\.\d+em;/.test(block) && !/word-spacing:/.test(block)) found.push('tracking');
    if (/line-height:\s*0\.\d+\s*;/.test(block)) found.push('leading');
  }
  return found;
};
assert.deepEqual(probe('.a{letter-spacing:-0.04em;}'), ['tracking'], 'tracking guard must fire');
assert.deepEqual(probe('.a{line-height:0.94;}'), ['leading'], 'leading guard must fire');
assert.deepEqual(probe('.a{letter-spacing:-0.04em;word-spacing:0.04em;}'), [], 'compensated tracking must pass');
assert.deepEqual(probe('.a{line-height:1.12;}'), [], 'leading at or above 1 must pass');

console.log(`typography: ${files.length} stylesheets clean (tracking compensated, no sub-1 leading)`);
