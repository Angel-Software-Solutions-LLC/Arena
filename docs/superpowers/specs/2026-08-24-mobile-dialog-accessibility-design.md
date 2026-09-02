# Mobile Dialog Accessibility Design

## Goal

Make the mobile spectator's Chat, Dashboard, and Shop sheets behave like real
modal dialogs for keyboard and assistive-technology users without changing
their visual design, lazy-loading behavior, or public integration hooks.

## Current failure

The mobile overlays update `aria-hidden` and their trigger's `aria-expanded`
state, but they do not manage keyboard focus. Controls inside closed overlays
remain in the document's focus model, opening a sheet leaves focus on the
background trigger, Tab can escape into the arena controls, and closing a
sheet does not return focus to the control that opened it. The background also
remains interactive despite each overlay declaring `aria-modal="true"`.

## Interaction contract

- Every closed `.mobile-overlay` is inert, including at initial page load.
- Opening an overlay closes any other mobile overlay without briefly releasing
  the modal background state.
- The open overlay is not inert. Every ordinary direct body child outside the
  mobile overlay set, including one appended later, is inert for the lifetime
  of the modal interaction.
- Background elements restore the inert state they had before the first mobile
  overlay opened; the controller must not blindly make previously inert
  elements interactive.
- A direct-body native `dialog` or
  `[role="alertdialog"][aria-modal="true"]` is identified as a modal root and
  never owned as mobile background, including before it opens or while it is
  hidden. When the native dialog is open or the alertdialog is visible, it is
  the higher-priority active modal. Tab is contained by that priority root,
  and a capture-phase snapshot makes the bubble-phase mobile Escape handler
  yield even when the priority modal dismisses itself earlier in the same
  dispatch. After it closes, the mobile overlay resumes its own focus trap and
  lifecycle.
- On open, focus moves on the next animation frame to the overlay's close
  button, falling back to the first visible focusable control in its panel.
- Tab and Shift+Tab wrap between the first and last visible enabled controls in
  the active panel. If focus is found outside the active panel, the next Tab
  moves it back to the first control.
- Escape, the close button, the backdrop, and the existing
  `window.ArenaCloseOverlay` hook all close through the same state transition.
- Closing the final overlay restores focus to the element that opened it when
  that element remains connected. A programmatic open from inside another
  overlay falls back to the new overlay's FAB trigger.
- Existing `aria-hidden`, `aria-expanded`, `active` class, chat observation,
  Dashboard/Shop lazy iframe loading, and `window.ArenaOpen*` contracts remain
  unchanged.

## Implementation shape

Keep the behavior in `frontend/m/mobile.js`. Add a small visible-focusable
query, shared background-inert state with direct-child observation,
higher-priority modal detection, and option-aware internal close handling to
`setupMobileOverlay`. Do not introduce a dependency or duplicate this
mobile-only controller into the desktop shell.

Because the frontend is served directly, publish the changed module as
`mobile.js?v=20260824b` from `frontend/m/index.html` and update the existing
static delivery assertion.

## Verification

A focused Playwright regression must load the real `/m/` shell and prove the
initial inert state, open focus placement, background isolation, forward and
reverse focus wrapping, late-child ownership, priority-modal interaction,
overlay switching, Escape close, state restoration, and focus restoration.
Record its failure against the current controller
before editing production code. Then run the focused browser spec across the
configured viewports, the existing cosmetics/static-delivery check, JavaScript
syntax checks, the Go repository gate, and `git diff --check`.

## Coordination boundary

This change owns only `frontend/m/mobile.js`, its importer tag in
`frontend/m/index.html`, the existing assertion for that tag, the new focused
browser spec, and its design/plan documents. It does not touch Accounts/OIDC,
customer sessions, profiles, Dashboard internals, router/configuration, or
account documentation owned by the concurrent Arena session.
