// Behavioural test for the round-transition latch.
//
// The transition is entered from a round_end and released from arena_state.
// The server's SpectatorState (go-arena/internal/game/views.go) carries NO
// round number, so a release rule that needs one can never fire and the map
// teardown becomes permanent. This drives the real prototype methods with the
// real payload shape and asserts the transition actually lifts.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const src = readFileSync(new URL('../frontend/js/renderer/engine.js', import.meta.url), 'utf8');

// Load the two methods without pulling in Babylon: evaluate the class body's
// methods against a stub receiver.
const grab = (name) => {
  const i = src.indexOf(`  ${name}(state) {`);
  assert.ok(i > 0, `${name} not found`);
  let depth = 0, j = src.indexOf('{', i);
  for (let k = j; k < src.length; k++) {
    if (src[k] === '{') depth++;
    else if (src[k] === '}') { depth--; if (depth === 0) { j = k; break; } }
  }
  return src.slice(i + `  ${name}`.length, j + 1);
};

const helper = src.slice(src.indexOf('export function roundStateReleasesTransition'));
const helperBody = helper.slice(0, helper.indexOf('\n}') + 2).replace('export ', '');

const begin = new Function('roundStateReleasesTransition', `return function ${grab('_beginRoundTransition')}`);
const end   = new Function('roundStateReleasesTransition', `return function ${grab('_maybeEndRoundTransition')}`);
const rel   = new Function(`${helperBody}; return roundStateReleasesTransition;`)();

// A spectator payload exactly as the Go server builds it: no round number.
const arenaState = (roundTick) => ({
  type: 'arena_state', tick: 1000 + roundTick, round_tick: roundTick,
  round_modifier: '', bots: [], safe_zone: {}, pickups: [], kill_feed: [],
  obstacles: [], mask_rects: [], waiting_bots: [],
});

let torn = 0, restored = 0;
const engine = {
  state: arenaState(2870),
  gameplayRenderer: { beginRoundTransition: () => torn++, endRoundTransition: () => restored++ },
  _beginRoundTransition: begin(rel),
  _maybeEndRoundTransition: end(rel),
};

// Round ends. The map is torn down.
engine._beginRoundTransition({ type: 'round_end', round_number: 30 });
assert.equal(torn, 1, 'round_end must tear the map down');
assert.equal(engine._roundTransitionActive, true, 'transition must latch on round_end');

// Tail of the ending round keeps streaming. Still held, correctly.
for (const t of [2871, 2872, 2873]) engine._maybeEndRoundTransition(arenaState(t));
assert.equal(restored, 0, 'the ending round must not release its own transition');

// The next round starts: round_tick restarts. This MUST release.
engine._maybeEndRoundTransition(arenaState(1));
assert.equal(engine._roundTransitionActive, false,
  'a round_tick reset must release the transition, since arena_state carries no round number');
assert.equal(restored, 1, 'the map must be restored when the next round begins');

// And it stays released as the new round runs.
for (const t of [2, 3, 4]) engine._maybeEndRoundTransition(arenaState(t));
assert.equal(restored, 1, 'release must happen exactly once');

console.log('round transition releases on a real arena_state payload (no round number present)');
