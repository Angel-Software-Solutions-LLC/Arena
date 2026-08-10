import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const { GameplayRenderer } = await import('../frontend/js/renderer/gameplay.js');
const { ArenaEngine, roundStateReleasesTransition } = await import('../frontend/js/renderer/engine.js');
const engineSource = readFileSync(new URL('../frontend/js/renderer/engine.js', import.meta.url), 'utf8');

const gameplay = Object.create(GameplayRenderer.prototype);
gameplay._roundTransitionActive = false;
gameplay.bountyTargetId = 'winner-id';
gameplay.bountyBots = [{ bot_id: 'winner-id', is_alive: true, position: [10, 20] }];
gameplay.bountyGroup = {
  ring: { visibility: 1 },
  sparkle: { emitRate: 18 },
};

gameplay.beginRoundTransition();
assert.equal(gameplay._roundTransitionActive, true, 'round_end must suspend bounty rendering');
assert.equal(gameplay.bountyTargetId, null, 'round_end must release the latched bounty target');
assert.deepEqual(gameplay.bountyBots, [], 'round_end must discard stale fallback bot positions');
assert.equal(gameplay.bountyGroup.ring.visibility, 0, 'round_end must hide the crown immediately');
assert.equal(gameplay.bountyGroup.sparkle.emitRate, 0, 'round_end must stop crown particles immediately');

gameplay.endRoundTransition();
assert.equal(gameplay._roundTransitionActive, false, 'the next round must restore bounty rendering');

assert.equal(roundStateReleasesTransition(8, 7), true, 'the next sequential round releases the hold');
assert.equal(roundStateReleasesTransition(7, 7), false, 'stale same-round snapshots stay frozen');
assert.equal(roundStateReleasesTransition(0, 7), true,
  'a reconnect after server restart releases the old high-round hold');
assert.equal(roundStateReleasesTransition(undefined, 7), false,
  'an unnumbered snapshot cannot release transition ownership');

globalThis.document = { hidden: false };
const stalePresentationCalls = [];
const engine = Object.create(ArenaEngine.prototype);
Object.assign(engine, {
  ready: true,
  state: { round_number: 7 },
  _roundTransitionActive: true,
  _roundTransitionRound: 7,
  _canvasVisible: true,
  _grading: {
    setPhase: () => stalePresentationCalls.push('phase'),
    setSuddenDeath: () => stalePresentationCalls.push('sudden-death'),
  },
  intermissionDirector: {
    handleArenaState: () => {},
    holdsWorld: () => true,
    holdsBots: () => true,
  },
  envRenderer: {
    setMapShape: () => stalePresentationCalls.push('map-theme'),
    update: () => stalePresentationCalls.push('environment'),
  },
  obstacleRenderer: { update: () => stalePresentationCalls.push('obstacles') },
  botRenderer: { update: () => stalePresentationCalls.push('bots') },
  pickupRenderer: { update: () => stalePresentationCalls.push('pickups') },
  effectRenderer: { update: () => stalePresentationCalls.push('death-damage-effects') },
  gameplayRenderer: {
    update: () => stalePresentationCalls.push('gameplay'),
    endRoundTransition: () => stalePresentationCalls.push('transition-ended'),
  },
  camera: {
    followId: null,
    updateBotPositions: () => stalePresentationCalls.push('camera'),
  },
  _playArenaEvents: () => stalePresentationCalls.push('combat-events'),
});
engine.setState({
  type: 'arena_state',
  round_number: 7,
  bots: [],
  events: [{ id: 'mine:1', type: 'mine_detonated', position: [10, 20] }],
  pickups: [],
});
assert.equal(engine._roundTransitionActive, true,
  'same-round spectator state must not release transition ownership');
assert.deepEqual(stalePresentationCalls, [],
  'same-round state after round_end must not render invisible combat or other stale presentation');

assert.match(
  engineSource,
  /if \(state\.type === 'round_end'\)[\s\S]*_beginRoundTransition\(state\)/,
  'the typed round_end path must enter renderer transition ownership',
);
assert.match(
  engineSource,
  /if \(self\.botRenderer && !self\._roundTransitionActive\)[\s\S]*self\.botRenderer\.interpolate\(\)/,
  'normal bot interpolation must stop while the round transition owns the winner pose',
);
assert.match(
  engineSource,
  /_maybeEndRoundTransition\(state\)[\s\S]*this\.gameplayRenderer\.update\(state\)/,
  'only a newer authoritative arena state may restore normal gameplay rendering',
);
assert.match(
  engineSource,
  /this\.gameplayRenderer = new GameplayRenderer\(scene\);[\s\S]{0,500}if \(this\._roundTransitionActive\) this\.gameplayRenderer\.beginRoundTransition\(\)/,
  'a scene rebuild during intermission must keep stale bounty visuals suspended',
);

console.log('round transition freezes bots and clears stale bounty visuals');

// Enter and release must read the round the SAME way. Reading only round_number on
// the release path while the enter path accepts `round_number ?? round` makes a
// payload that carries `round` enter the transition and never leave it, so the map
// teardown becomes permanent and the arena renders black with only glow left.
const engineSrc = readFileSync(new URL('../frontend/js/renderer/engine.js', import.meta.url), 'utf8');
const enterReads = /const round = Number\(state && \(state\.round_number \?\? state\.round\)\)/.test(engineSrc);
const releaseReads = /state\.round_number \?\? state\.round[\s\S]{0,200}roundStateReleasesTransition\(releaseRound/.test(engineSrc);
assert.ok(enterReads, 'the transition enter path must accept round_number ?? round');
assert.ok(releaseReads, 'the transition release path must read the round the same way the enter path does');
console.log('round-transition enter and release read the round symmetrically');

// The transition is entered from round_end but only arena_state ever reaches the
// release path, and the server's SpectatorState carries no round number, so the
// round-number rule alone can never lift it. Assert the round_tick reset fallback
// exists, since that is the only signal the releasing payload actually carries.
assert.match(engineSrc, /_roundTransitionEntryTick/,
  'the transition must remember the ending round tick to detect the next round');
assert.match(engineSrc, /tick < entry/,
  'a round_tick below the entry tick must release the transition');
console.log('round transition lifts on a round_tick reset, not only on a round number');
