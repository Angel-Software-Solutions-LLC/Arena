import assert from 'node:assert/strict';
import test from 'node:test';

import WebSocket, { WebSocketServer } from 'ws';

import ArenaBot from '../src/ArenaBot.js';

async function withServer(onConnection, run) {
  const server = new WebSocketServer({ host: '127.0.0.1', port: 0 });
  await new Promise((resolve, reject) => {
    server.once('listening', resolve);
    server.once('error', reject);
  });
  server.on('connection', onConnection);

  const address = server.address();
  try {
    await run(`ws://127.0.0.1:${address.port}/ws/bot`);
  } finally {
    for (const client of server.clients) client.terminate();
    await new Promise((resolve) => server.close(resolve));
  }
}

async function settleWithin(promise, timeoutMs = 250) {
  return Promise.race([
    promise.then(
      () => ({ state: 'resolved' }),
      (error) => ({ state: 'rejected', error }),
    ),
    new Promise((resolve) => setTimeout(() => resolve({ state: 'timeout' }), timeoutMs)),
  ]);
}

async function waitFor(predicate, timeoutMs = 1000) {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('timed out waiting for condition');
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}

test('connect rejects a structured server error before loadout confirmation', async () => {
  await withServer((socket) => {
    socket.send(JSON.stringify({
      type: 'error',
      message: 'reconnecting too fast, wait a few seconds',
      code: 'RECONNECT_TOO_FAST',
      details: { retry_after: 5 },
    }));
    socket.close(1008, 'reconnect cooldown');
  }, async (serverUrl) => {
    const bot = new ArenaBot('test-key', serverUrl);
    const outcome = await settleWithin(bot.connect());

    assert.equal(outcome.state, 'rejected');
    assert.equal(outcome.error.code, 'RECONNECT_TOO_FAST');
    assert.equal(outcome.error.retryAfter, 5);
  });
});

test('connect rejects when the socket closes before the bot is ready', async () => {
  await withServer((socket) => {
    socket.close(1013, 'try again later');
  }, async (serverUrl) => {
    const bot = new ArenaBot('test-key', serverUrl);
    const outcome = await settleWithin(bot.connect());

    assert.equal(outcome.state, 'rejected');
    assert.match(outcome.error.message, /closed before loadout confirmation/i);
  });
});

test('tick handlers are serialized and stale queued ticks are coalesced', async () => {
  let activeHandlers = 0;
  let maxActiveHandlers = 0;
  let handledTicks = 0;

  class SlowBot extends ArenaBot {
    async onTick() {
      activeHandlers += 1;
      maxActiveHandlers = Math.max(maxActiveHandlers, activeHandlers);
      handledTicks += 1;
      await new Promise((resolve) => setTimeout(resolve, 25));
      activeHandlers -= 1;
      return this.idle();
    }
  }

  await withServer((socket) => {
    socket.send(JSON.stringify({ type: 'connected', bot_id: 'slow-bot' }));
    socket.on('message', (raw) => {
      const message = JSON.parse(raw);
      if (message.type !== 'select_loadout') return;
      socket.send(JSON.stringify({ type: 'loadout_confirmed', weapon: 'sword' }));
      for (let tick = 1; tick <= 30; tick += 1) {
        socket.send(JSON.stringify({
          type: 'tick',
          tick,
          tick_number: tick,
          your_state: { is_alive: true, position: [1, 1] },
          nearby_entities: [],
        }));
      }
    });
  }, async (serverUrl) => {
    const bot = new SlowBot('test-key', serverUrl);
    await bot.connect();
    await waitFor(() => handledTicks >= 1);
    await waitFor(() => activeHandlers === 0);

    assert.equal(maxActiveHandlers, 1, 'onTick must not overlap for one bot');
    assert.ok(handledTicks <= 3, `processed ${handledTicks} stale ticks instead of coalescing them`);
    bot.ws.close();
  });
});

test('closed connection handlers cannot act on or close a replacement socket', async () => {
  let connectionNumber = 0;
  const replacementMessages = [];
  let releaseOldTick;
  let oldTickStarted;
  let oldTickFinished;
  const oldTickGate = new Promise((resolve) => { releaseOldTick = resolve; });
  const started = new Promise((resolve) => { oldTickStarted = resolve; });
  const finished = new Promise((resolve) => { oldTickFinished = resolve; });

  class ReconnectingBot extends ArenaBot {
    async fetchMap() { return true; }

    async onTick() {
      oldTickStarted();
      await oldTickGate;
      oldTickFinished();
      return this.idle();
    }
  }

  await withServer((socket) => {
    connectionNumber += 1;
    const session = connectionNumber;
    socket.send(JSON.stringify({ type: 'connected', bot_id: 'reconnecting-bot' }));
    socket.on('message', (raw) => {
      const message = JSON.parse(raw);
      if (session === 2) replacementMessages.push(message);
      if (message.type !== 'select_loadout') return;
      socket.send(JSON.stringify({ type: 'loadout_confirmed', weapon: 'sword' }));
      if (session === 1) {
        socket.send(JSON.stringify({
          type: 'tick',
          tick: 10,
          tick_number: 10,
          your_state: { is_alive: true, position: [1, 1] },
          nearby_entities: [],
        }));
        socket.send(JSON.stringify({ type: 'kick', reason: 'old session ended' }));
      }
    });
  }, async (serverUrl) => {
    const bot = new ReconnectingBot('test-key', serverUrl);
    await bot.connect();
    await started;

    const oldSocket = bot.ws;
    const oldClosed = new Promise((resolve) => oldSocket.once('close', resolve));
    oldSocket.close();
    await oldClosed;

    await bot.connect();
    const replacementSocket = bot.ws;
    releaseOldTick();
    await finished;
    await new Promise((resolve) => setTimeout(resolve, 30));

    assert.equal(replacementSocket.readyState, WebSocket.OPEN, 'old kick closed the replacement socket');
    assert.equal(
      replacementMessages.filter((message) => message.type === 'action').length,
      0,
      'old tick submitted an action through the replacement socket',
    );
    replacementSocket.close();
  });
});

test('dead ticks do not invoke agent logic or submit actions', async () => {
  let onTickCalls = 0;
  let actionMessages = 0;

  class DeadBot extends ArenaBot {
    async onTick() {
      onTickCalls += 1;
      return this.idle();
    }
  }

  await withServer((socket) => {
    socket.send(JSON.stringify({ type: 'connected', bot_id: 'dead-bot' }));
    socket.on('message', (raw) => {
      const message = JSON.parse(raw);
      if (message.type === 'action') actionMessages += 1;
      if (message.type !== 'select_loadout') return;
      socket.send(JSON.stringify({ type: 'loadout_confirmed', weapon: 'sword' }));
      socket.send(JSON.stringify({
        type: 'tick',
        tick: 10,
        tick_number: 10,
        your_state: { is_alive: false, position: [1, 1] },
        nearby_entities: [],
      }));
    });
  }, async (serverUrl) => {
    const bot = new DeadBot('test-key', serverUrl);
    await bot.connect();
    await new Promise((resolve) => setTimeout(resolve, 50));

    assert.equal(onTickCalls, 0);
    assert.equal(actionMessages, 0);
    bot.ws.close();
  });
});

test('disconnect wait resolves when the ready socket already closed', async () => {
  const bot = new ArenaBot('test-key');
  bot.ws = {
    readyState: 3,
    once() {
      throw new Error('must not attach a listener to an already closed socket');
    },
  };

  const outcome = await settleWithin(bot._waitForDisconnect(), 50);
  assert.equal(outcome.state, 'resolved');
});

test('SDK fetches terrain on connect and refreshes active features at round start', async () => {
  const originalFetch = globalThis.fetch;
  const requested = [];
  let responseNumber = 0;
  globalThis.fetch = async (url) => {
    requested.push(String(url));
    responseNumber += 1;
    return {
      ok: true,
      async json() {
        return {
          status: 'ok',
          width: 2,
          height: 2,
          cell_size: 20,
          map_shape: 'rooms',
          features_pending: responseNumber === 1,
          terrain: ['..', '.T'],
          teleport_pads: responseNumber === 1 ? [] : [{ id: 'pad-a' }],
        };
      },
    };
  };

  let mapInitCalls = 0;
  class MapBot extends ArenaBot {
    async onMapInit(terrain, width, height) {
      mapInitCalls += 1;
      assert.deepEqual(terrain, [['.', '.'], ['.', 'T']]);
      assert.equal(width, 2);
      assert.equal(height, 2);
    }
  }

  try {
    await withServer((socket) => {
      socket.send(JSON.stringify({ type: 'connected', bot_id: 'map-bot' }));
      socket.on('message', (raw) => {
        const message = JSON.parse(raw);
        if (message.type !== 'select_loadout') return;
        socket.send(JSON.stringify({ type: 'loadout_confirmed', weapon: 'sword' }));
        setTimeout(() => socket.send(JSON.stringify({ type: 'round_start', round_number: 2 })), 10);
      });
    }, async (serverUrl) => {
      const bot = new MapBot('test-key', serverUrl);
      await bot.connect();
      await waitFor(() => mapInitCalls === 2);

      assert.deepEqual(requested, [
        `${serverUrl.replace('ws://', 'http://').split('/ws/')[0]}/api/v1/arena/map`,
        `${serverUrl.replace('ws://', 'http://').split('/ws/')[0]}/api/v1/arena/map`,
      ]);
      assert.deepEqual(bot._terrain, [['.', '.'], ['.', 'T']]);
      bot.ws.close();
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

async function reconnectDelaysForSessionDurations(sessionDurations) {
  const originalNow = Date.now;
  const originalSetTimeout = globalThis.setTimeout;
  const delays = [];
  let now = 0;
  let session = 0;

  class TimedSessionBot extends ArenaBot {
    async connect() {}

    async _waitForDisconnect() {
      now += sessionDurations[session];
      session += 1;
    }
  }

  const bot = new TimedSessionBot('test-key');
  Date.now = () => now;
  globalThis.setTimeout = (resolve, delay) => {
    delays.push(delay);
    if (delays.length === sessionDurations.length) bot._running = false;
    resolve();
    return 0;
  };

  try {
    await bot.run();
  } finally {
    Date.now = originalNow;
    globalThis.setTimeout = originalSetTimeout;
  }
  return delays;
}

test('run escalates backoff across short accepted sessions', async () => {
  assert.deepEqual(
    await reconnectDelaysForSessionDurations([1_000, 1_000, 1_000]),
    [1_000, 2_000, 4_000],
  );
});

test('run resets accumulated backoff after a stable session', async () => {
  assert.deepEqual(
    await reconnectDelaysForSessionDurations([1_000, 1_000, 30_000]),
    [1_000, 2_000, 1_000],
  );
});
