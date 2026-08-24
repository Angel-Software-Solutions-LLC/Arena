# Mobile Dialog Accessibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task with strict TDD and independent review.

**Goal:** Give the mobile Chat, Dashboard, and Shop overlays complete modal keyboard and focus behavior.

**Architecture:** Extend the existing `setupMobileOverlay` controller with shared inert-background ownership and focus lifecycle helpers. Exercise the real mobile document with one focused Playwright spec, then publish a fresh query-string identity for the directly served module.

**Tech Stack:** Vanilla browser JavaScript, HTML, Node.js static checks, Playwright 1.62.1.

**Spec:** `docs/superpowers/specs/2026-08-24-mobile-dialog-accessibility-design.md`

## Global constraints

- Preserve the current overlay DOM, styles, lazy iframe behavior, and public `window.ArenaOpen*` / `window.ArenaCloseOverlay` hooks.
- Preserve pre-existing inert state when restoring background elements.
- Never leave more than one `.mobile-overlay.open`.
- Do not change dependencies or lockfiles.
- Do not touch the concurrent Accounts/OIDC session's files or contracts.
- Follow strict TDD and record the focused browser failure before production edits.

---

### Task 1: Implement and publish accessible mobile modal behavior

**Files:**

- Create: `tests/browser/specs/mobile-overlay-accessibility.spec.mjs`
- Modify: `frontend/m/mobile.js:90-195`
- Modify: `scripts/test-cosmetics-launch-ui.mjs:29-31`
- Modify: `frontend/m/index.html:18`

**Interfaces:**

- Consumes: mobile overlay/FAB pairs, click and keyboard events, and the existing programmatic open/close hooks.
- Produces: the same class and ARIA state transitions plus modal inertness, focus placement/trapping, and opener restoration.

- [ ] Add a focused Playwright fixture that intercepts external fonts and the mobile API/WebSocket calls, enables Chat, and serves a minimal Dashboard iframe document.

- [ ] Assert that all three overlays begin inert. Open Chat from its FAB and assert that Chat becomes non-inert, its close button receives focus, every direct non-overlay body child is inert, and the FAB reports `aria-expanded="true"`.

- [ ] Enable Chat's input/send controls inside the test fixture. Assert Tab wraps from Send to Close and Shift+Tab wraps from Close to Send.

- [ ] While Chat is open, invoke `window.ArenaOpenDashboard()`. Assert Chat closes and becomes inert, Dashboard opens and receives focus, and the background stays inert. Press Escape and assert Dashboard closes, the prior background inert states return, and focus lands on the Dashboard FAB.

- [ ] Run only the new spec and record a behavior-level failure against the current implementation:

  ```bash
  npm test --prefix tests/browser -- specs/mobile-overlay-accessibility.spec.mjs
  ```

  Expected: fail because closed overlays/background are not inert and focus remains on the trigger.

- [ ] In `frontend/m/mobile.js`, add a focusable selector/helper matching visible enabled controls and shared functions that capture, apply, and restore inert state for direct body children outside `.mobile-overlay`.

- [ ] Update `setupMobileOverlay` so closed overlays initialize inert, opening captures a valid return target, closes another overlay without releasing the shared background or restoring its focus, applies modal state, and focuses the close/first control on the next animation frame.

- [ ] Route all close paths through an option-aware close transition that makes the overlay inert, restores the shared background only after the final overlay closes, and restores the captured opener when appropriate. Add a Tab/Shift+Tab trap for the active panel.

- [ ] Rerun the focused browser spec and confirm every configured viewport passes.

- [ ] Change the existing static delivery assertion to require `mobile.js?v=20260824a` and run it before changing HTML:

  ```bash
  node scripts/test-cosmetics-launch-ui.mjs
  ```

  Expected: fail only because `frontend/m/index.html` still publishes the old tag.

- [ ] Update `frontend/m/index.html` to publish `mobile.js?v=20260824a`, then rerun the static check and focused browser spec.

- [ ] Run affected syntax and patch checks:

  ```bash
  node --check frontend/m/mobile.js
  node --check tests/browser/specs/mobile-overlay-accessibility.spec.mjs
  node scripts/test-cosmetics-launch-ui.mjs
  git diff --check
  ```

- [ ] Commit the implementation and regression together:

  ```bash
  git add frontend/m/mobile.js frontend/m/index.html scripts/test-cosmetics-launch-ui.mjs tests/browser/specs/mobile-overlay-accessibility.spec.mjs
  git commit -m "fix: make mobile overlays keyboard accessible"
  ```

### Task 2: Verify and review the completed change

**Files:**

- Review the immutable Task 1 commit against the design and plan.
- Record ignored SDD evidence under `.superpowers/sdd/2026-08-24-mobile-dialog-accessibility/`.

- [ ] Run the focused browser spec, existing static delivery check, and syntax checks from Task 1 against the final tree.

- [ ] Run the repository gate. If host Go is unavailable, use the official Go 1.25 container with the repository Go module mounted read-only:

  ```bash
  docker run --rm -v "$PWD/go-arena:/src:ro" -w /src golang:1.25 go test ./...
  git diff --check
  ```

- [ ] Give the immutable commit and exact acceptance contract to an independent reviewer. Address validated findings with a new regression and fix commit, then repeat review as needed.

- [ ] Rebase onto the latest `origin/develop`, compare the rebased patch to the reviewed patch, rerun every gate, and search again for overlapping pull requests before publishing.

- [ ] Push the task branch and open one normal pull request against `develop`, including motivation, risks, coordination boundary, and exact validation evidence. Do not merge it.
