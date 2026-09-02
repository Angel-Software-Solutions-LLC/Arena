# SDK Resilience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Correct Python fog-radius delivery and make Node.js reconnect backoff survive short accepted sessions.

**Architecture:** Preserve both SDK public APIs and fix behavior at their existing message-loop and reconnect-loop seams. Add behavior-level regressions to the current connection test files, then make the smallest implementation changes needed to align both SDKs.

**Tech Stack:** Python 3.10+ with asyncio/pytest; Node.js 20+ with `node:test` and `ws`.

**Spec:** `docs/superpowers/specs/2026-08-24-sdk-resilience-design.md`

## Global Constraints

- Python reads `fog_radius` from the top-level tick message and falls back to `0` when absent.
- Node.js resets reconnect backoff only after a ready connection lasts at least `30_000` milliseconds.
- Reconnect delay starts at `1_000` milliseconds, doubles after unstable attempts, and caps at `30_000` milliseconds.
- Maintenance retry time remains a lower bound on the reconnect delay.
- Preserve every public SDK constructor, callback, action, and wire-message shape.
- Do not change dependencies or lockfiles.
- Do not touch Accounts/OIDC, customer session/profile, Dashboard, router, config, or account-documentation files.
- Follow strict TDD: record the focused failing result before changing production code.

---

### Task 1: Deliver the server fog radius to Python bots

**Files:**
- Modify: `sdk/python/tests/test_connection.py`
- Modify: `sdk/python/arena_sdk/bot.py:483-505`

**Interfaces:**
- Consumes: bot tick objects with top-level `fog_radius` and nested `your_state`.
- Produces: the existing `ArenaBot.on_tick(state, nearby, safe_zone)` callback with `safe_zone["fog_radius"]` populated from the message envelope.

- [ ] **Step 1: Write the failing protocol regression**

Append this test to `sdk/python/tests/test_connection.py`:

```python
@pytest.mark.asyncio
async def test_tick_passes_top_level_fog_radius_to_agent():
    class FogAwareBot(ArenaBot):
        def __init__(self):
            super().__init__("test-key")
            self.safe_zone = None

        async def on_tick(self, state, nearby, safe_zone):
            self.safe_zone = safe_zone
            return self.idle()

    bot = FogAwareBot()
    socket = FakeWebSocket([
        {
            "type": "tick",
            "tick": 10,
            "fog_radius": 7,
            "your_state": {
                "is_alive": True,
                "position": [1, 1],
                "fog_radius": 99,
            },
            "nearby_entities": [],
        },
        {"type": "kick", "reason": "test complete"},
    ])
    bot._ws = socket

    await bot._game_loop()

    assert bot.safe_zone["fog_radius"] == 7
```

- [ ] **Step 2: Verify the test fails for the protocol mismatch**

Run:

```bash
cd sdk/python
.venv/bin/python -m pytest tests/test_connection.py::test_tick_passes_top_level_fog_radius_to_agent -q
```

Expected: FAIL with `assert 99 == 7`, proving the SDK read the nested decoy.

- [ ] **Step 3: Read the value from the message envelope**

In the `safe_zone` mapping inside `_game_loop`, replace the fog-radius entry with:

```python
"fog_radius": msg.get("fog_radius", 0),
```

- [ ] **Step 4: Verify focused and Python SDK behavior**

Run:

```bash
cd sdk/python
.venv/bin/python -m pytest tests/test_connection.py::test_tick_passes_top_level_fog_radius_to_agent -q
.venv/bin/python -m pytest tests -q
.venv/bin/python -m compileall arena_sdk examples
```

Expected: the focused regression and all Python SDK tests pass; compileall exits 0.

- [ ] **Step 5: Commit the Python contract fix**

```bash
git add sdk/python/arena_sdk/bot.py sdk/python/tests/test_connection.py
git commit -m "fix: deliver fog radius to Python bots"
```

### Task 2: Preserve Node.js backoff across short sessions

**Files:**
- Modify: `sdk/nodejs/test/connection.test.js`
- Modify: `sdk/nodejs/src/ArenaBot.js:4-5,560-576`

**Interfaces:**
- Consumes: elapsed wall-clock time between a completed `connect()` and `_waitForDisconnect()` returning.
- Produces: the existing `ArenaBot.run()` behavior with a 30-second stability threshold; no new public methods or constructor options.

- [ ] **Step 1: Write failing short-session and recovery regressions**

Append a local helper and these tests to `sdk/nodejs/test/connection.test.js`. The helper temporarily replaces `Date.now` and `globalThis.setTimeout`, restores both in `finally`, and records the real delay requested by `run()`:

```javascript
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
```

- [ ] **Step 2: Verify both tests fail because successful connects reset too early**

Run:

```bash
cd sdk/nodejs
node --test --test-name-pattern='run (escalates|resets)' test/connection.test.js
```

Expected: FAIL because current output is `[1_000, 1_000, 1_000]`; neither test may fail from leaked timers or test setup.

- [ ] **Step 3: Add the stability threshold to `run()`**

Add beside the handshake timeout:

```javascript
const RECONNECT_BACKOFF_RESET_MS = 30_000;
```

In `run()`, set `connectedAt` to `null` before each attempt, assign
`Date.now()` immediately after `connect()` resolves, remove the unconditional
delay reset, and before calculating `maintenanceDelay` reset to `1_000` only
when:

```javascript
connectedAt !== null && Date.now() - connectedAt >= RECONNECT_BACKOFF_RESET_MS
```

Keep the maintenance lower-bound calculation and the `30_000` cap unchanged.

- [ ] **Step 4: Verify focused and Node.js SDK behavior**

Run:

```bash
cd sdk/nodejs
node --test --test-name-pattern='run (escalates|resets)' test/connection.test.js
node --test test/*.test.js
node --check src/ArenaBot.js
```

Expected: both reconnect regressions and all Node.js SDK tests pass; syntax check exits 0 with no warnings.

- [ ] **Step 5: Verify the complete SDK change and commit**

Run from the repository root:

```bash
sdk/python/.venv/bin/python -m pytest sdk/python/tests -q
node --test sdk/nodejs/test/*.test.js
git diff --check
```

Expected: all SDK tests pass and `git diff --check` exits 0.

Then commit:

```bash
git add sdk/nodejs/src/ArenaBot.js sdk/nodejs/test/connection.test.js
git commit -m "fix: retain SDK reconnect backoff"
```
