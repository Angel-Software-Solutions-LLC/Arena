import asyncio
import json

import pytest

from arena_sdk.bot import ArenaBot


class FakeWebSocket:
    def __init__(self, messages):
        self.messages = list(messages)
        self.sent = []
        self.recv_count = 0

    async def recv(self):
        if self.messages:
            self.recv_count += 1
            return json.dumps(self.messages.pop(0))
        await asyncio.Future()

    async def send(self, payload):
        self.sent.append(json.loads(payload))


class TrackingBot(ArenaBot):
    def __init__(self):
        super().__init__("test-key")
        self.on_tick_calls = 0

    async def on_tick(self, state, nearby, safe_zone):
        self.on_tick_calls += 1
        return self.idle()


@pytest.mark.asyncio
async def test_dead_ticks_do_not_invoke_agent_logic_or_submit_actions():
    bot = TrackingBot()
    socket = FakeWebSocket([
        {
            "type": "tick",
            "tick": 10,
            "your_state": {"is_alive": False, "position": [1, 1]},
            "nearby_entities": [],
        },
        {"type": "kick", "reason": "test complete"},
    ])
    bot._ws = socket

    await bot._game_loop()

    assert bot.on_tick_calls == 0
    assert socket.sent == []


@pytest.mark.asyncio
async def test_slow_tick_handler_coalesces_stale_queued_ticks_without_reordering_lifecycle_messages():
    first_tick_started = asyncio.Event()
    release_first_tick = asyncio.Event()

    class SlowBot(ArenaBot):
        def __init__(self):
            super().__init__("test-key")
            self.handled_ticks = []
            self.round_ended_after_ticks = None

        async def on_tick(self, state, nearby, safe_zone):
            self.handled_ticks.append(self._tick_number)
            if len(self.handled_ticks) == 1:
                first_tick_started.set()
                await release_first_tick.wait()
            return self.idle()

        async def on_round_end(self, round_info):
            self.round_ended_after_ticks = list(self.handled_ticks)

    bot = SlowBot()
    socket = FakeWebSocket([
        {
            "type": "tick",
            "tick": tick,
            "your_state": {"is_alive": True, "position": [1, 1]},
            "nearby_entities": [],
        }
        for tick in range(1, 31)
    ] + [
        {"type": "round_end", "round_number": 1},
        {"type": "kick", "reason": "test complete"},
    ])
    bot._ws = socket

    game_loop = asyncio.create_task(bot._game_loop())
    await first_tick_started.wait()
    await asyncio.sleep(0)
    release_first_tick.set()
    await game_loop

    assert 1 <= len(bot.handled_ticks) <= 2
    assert bot.handled_ticks[-1] == 30
    assert [message["tick"] for message in socket.sent] == bot.handled_ticks
    assert bot.round_ended_after_ticks == bot.handled_ticks


@pytest.mark.asyncio
async def test_slow_tick_handler_applies_backpressure_to_non_coalescible_messages():
    first_tick_started = asyncio.Event()
    release_first_tick = asyncio.Event()

    class SlowBot(ArenaBot):
        async def on_tick(self, state, nearby, safe_zone):
            if not first_tick_started.is_set():
                first_tick_started.set()
                await release_first_tick.wait()
            return self.idle()

    messages = [{
        "type": "tick",
        "tick": 1,
        "your_state": {"is_alive": True, "position": [1, 1]},
        "nearby_entities": [],
    }]
    for tick in range(2, 202):
        messages.extend([
            {"type": "round_end", "round_number": tick},
            {
                "type": "tick",
                "tick": tick,
                "your_state": {"is_alive": True, "position": [1, 1]},
                "nearby_entities": [],
            },
        ])
    messages.append({"type": "kick", "reason": "test complete"})

    bot = SlowBot("test-key")
    socket = FakeWebSocket(messages)
    bot._ws = socket

    game_loop = asyncio.create_task(bot._game_loop())
    await asyncio.wait_for(first_tick_started.wait(), timeout=1)
    await asyncio.sleep(0)

    # One message can be in the receiver while 64 are queued and the first
    # tick callback is running. The rest must remain behind socket backpressure.
    assert socket.recv_count <= 66
    assert socket.recv_count < len(messages)

    release_first_tick.set()
    await asyncio.wait_for(game_loop, timeout=2)


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
