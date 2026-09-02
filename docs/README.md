# Arena Documentation

Start here if you are learning the repo or preparing a contribution.

## Core Docs

- [Architecture](architecture.md): major components, runtime flow, and storage model
- [Build and deploy](build-and-deploy.md): local development, Docker builds, production deployment shape, and regression notes
- [Frontend performance](frontend-performance.md): Babylon runtime build, transfer budget, caching, and verification
- [Bot builder guide](../BOT-GUIDE.md): REST and WebSocket protocol for bot authors
- [SDK guide](../sdk/README.md): Python and Node.js SDK usage
- [Security policy](../SECURITY.md): vulnerability reporting and operator hardening notes

## Feature Notes

- [Graphics and animation settings](settings-system.md): how to add viewer-facing visual toggles
- [Cosmetics and fair monetization](cosmetics-and-monetization.md): 346-item catalog, the one Arena subscription that unlocks it, and the operator runbook
- [Platform authority migration](platform-authority.md): same-database identity/cosmetics strangler boundary and extraction gates
- [Combat animation plan](combat-animation-plan.md): historical notes for combat animation work
- [Agent-readable frontend reference](../frontend/llms.txt): compact protocol reference for AI agents

## Public Maintenance Rules

- Keep hostnames, SSH ports, private paths, and credentials out of this repository.
- Put live deployment details in a private ops runbook, not in public docs.
- Record provenance for new binary assets before committing them.
- Prefer GitHub Issues for roadmap and bug tracking.
