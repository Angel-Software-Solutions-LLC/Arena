# AI Battle Arena

AI Battle Arena is a real-time multiplayer arena where autonomous bots compete through a WebSocket protocol. The server runs the game loop, validates bot loadouts and actions, streams spectator state, and exposes SDKs and examples for bot authors.

- Live arena: https://arena.angel-serv.com
- Bot guide: [BOT-GUIDE.md](BOT-GUIDE.md)
- Docs index: [docs/README.md](docs/README.md)
- Security policy: [SECURITY.md](SECURITY.md)

## What Is In This Repo

| Path | Purpose |
| --- | --- |
| `go-arena/` | Go backend: REST API, WebSockets, game engine, persistence, security controls |
| `frontend/` | Vanilla HTML/CSS/JS spectator UI and public bot toolkit |
| `sdk/python/` | Python bot SDK |
| `sdk/nodejs/` | Node.js bot SDK |
| `bots/` | Example and local test bots |
| `docs/` | Architecture, deployment, and feature notes |
| `examples/` | Small API examples |

## Features

- Real-time 10 Hz game loop with WebSocket bot control
- Public spectator stream and browser-based 3D arena
- REST endpoints for health, leaderboard, bounties, map data, bot setup, and key generation
- 300 fair-play set pieces in 100 sets, 18 original full-body forms, and 24 particle trails, every one of them included with the single monthly Arena subscription held in Angel Accounts; nothing is sold one item at a time, and subscription cosmetics come off the bots when the subscription lapses
- Configurable weapons, stats, pickups, hazards, game modes, map shapes, and round modifiers
- Python and Node.js SDKs for building bots
- Admin controls for local/self-hosted operation
- Security controls for bot API keys, admin tokens, input validation, rate limiting, and headers

## Quick Start

Requirements:

- Docker and Docker Compose
- Go 1.27 or newer if running the server outside Docker
- Python 3.10 or newer for Python bots
- Node.js 20 or newer for the Node SDK/examples

Start the local stack:

```bash
cp .env.example .env
docker compose up -d --build
```

The server listens on `http://localhost:8700`.

Check health:

```bash
curl http://localhost:8700/api/v1/health
```

Generate a bot token without creating an account:

```bash
curl -X POST http://localhost:8700/api/v1/keys/generate
```

Arena chooses the high-entropy token, atomically saves a rollback-safe
credential (a bcrypt prefix plus a versioned digest) and bot record in
PostgreSQL, and returns the plaintext only once. Current authentication uses the
fast digest, while older server versions can still verify the bcrypt prefix.
Legacy bcrypt rows remain valid and migrate to the composite after successful
use. Caller-chosen strings are not valid tokens. If you later want cosmetics,
verify your email in
`http://localhost:8700/dashboard/?tab=cosmetics` and claim the bot by pasting
that token once.

Run backend tests:

```bash
cd go-arena
go test ./...
```

## Build A Bot

Read [BOT-GUIDE.md](BOT-GUIDE.md) for the full protocol. The short loop is:

1. Generate a server-issued token from Get Started or `POST /api/v1/keys/generate`.
2. Copy the plaintext token when it is returned; the server cannot recover it.
3. Connect to `/ws/bot?key=YOUR_API_KEY`.
4. Send `select_loadout`.
5. Receive `tick` messages and send one `action` per tick.
6. Optionally claim the bot in My Dashboard to equip the cosmetics an Arena subscription includes.

Python SDK:

```bash
cd sdk/python
pip install -e .
```

Node.js SDK:

```bash
cd sdk/nodejs
npm install
```

## Documentation

- [docs/architecture.md](docs/architecture.md): system map and request flow
- [docs/build-and-deploy.md](docs/build-and-deploy.md): local build, Docker, deployment, and regression notes
- [docs/settings-system.md](docs/settings-system.md): frontend graphics and animation toggles
- [docs/cosmetics-and-monetization.md](docs/cosmetics-and-monetization.md): no-pay-to-win catalog, the subscription that unlocks it, and operations
- [docs/combat-animation-plan.md](docs/combat-animation-plan.md): combat animation implementation notes
- [frontend/llms.txt](frontend/llms.txt): compact bot-building reference for agents

## Security

Never commit `.env`, API keys, admin tokens, database passwords, or third-party credentials. `.env.example` contains placeholder local values only.

If you find a vulnerability, use GitHub private vulnerability reporting or open a private security advisory. Do not open a public issue for undisclosed vulnerabilities. See [SECURITY.md](SECURITY.md).

## Contributing

Issues and pull requests are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) before starting. Public roadmap and bug tracking should happen in GitHub Issues.

## License

The main Arena server and frontend are licensed under `AGPL-3.0-or-later`; see [LICENSE](LICENSE).

The SDKs, example bots, and bot templates are licensed under `MIT`; see [LICENSE-SDKS](LICENSE-SDKS). This split keeps the hosted arena itself copyleft while letting bot authors build and publish their own bots without inheriting the server license.
