# Contributing To AI Battle Arena

Thanks for helping improve Arena. This project welcomes bug reports, docs fixes, gameplay ideas, SDK improvements, and self-hosting polish.

## Workflow

1. Open or pick an issue when the change is larger than a small typo.
2. Create a branch from current `develop`.
3. Make focused changes.
4. Run the checks that match what you touched.
5. Open a pull request into `develop`.

`develop` is the integration channel for all changes. `main` is the production
channel and only accepts release pull requests whose source branch is
`develop`. Direct pushes to both branches are blocked. Required CI validates
every pull request; human review is welcome but is not required.

Branch names:

- `feature/<short-description>`
- `fix/<short-description>`
- `docs/<short-description>`
- `refactor/<short-description>`
- `security/<short-description>`

Commit messages should be short and conventional when practical:

```text
feat: add bow charge telemetry
fix: reject invalid loadout stats
docs: update bot setup example
refactor: split spectator renderer helper
```

## Local Setup

```bash
cp .env.example .env
docker compose up -d --build
```

The server listens on `http://localhost:8700`.

Backend tests:

```bash
cd go-arena
go test ./...
```

Frontend syntax checks:

```bash
node --check frontend/js/app.js
```

Python SDK checks:

```bash
python -m compileall sdk/python/arena_sdk sdk/python/examples bots
```

Node SDK setup:

```bash
cd sdk/nodejs
npm install
```

## Project Structure

```text
go-arena/           Go server
frontend/           Browser spectator UI and toolkit
sdk/python/         Python bot SDK
sdk/nodejs/         Node.js bot SDK
bots/               Example and local test bots
docs/               Architecture, deployment, and feature notes
examples/           API usage examples
```

## Public-Repo Safety

Do not commit:

- `.env`
- API keys or admin tokens
- database passwords
- private hostnames, SSH ports, private IPs, or private server paths
- binary assets without source and license/provenance notes
- generated reports that are not useful to future contributors

## Bot And API Changes

If you change bot-facing behavior, update at least one of:

- [BOT-GUIDE.md](BOT-GUIDE.md)
- `GET /api/v1/bot-setup` in `go-arena/internal/api/botsetup.go`
- SDK examples in `sdk/`
- [frontend/llms.txt](frontend/llms.txt)

## Security

Do not open public issues for undisclosed vulnerabilities. Follow [SECURITY.md](SECURITY.md).
