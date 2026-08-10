import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const html = readFileSync(new URL('../frontend/index.html', import.meta.url), 'utf8');
const css = readFileSync(new URL('../frontend/css/site-shell.css', import.meta.url), 'utf8');
const sectionsCss = readFileSync(new URL('../frontend/css/sections.css', import.meta.url), 'utf8');
const js = readFileSync(new URL('../frontend/js/site-shell.js', import.meta.url), 'utf8');
const app = readFileSync(new URL('../frontend/js/app.js', import.meta.url), 'utf8');
const llms = readFileSync(new URL('../frontend/llms.txt', import.meta.url), 'utf8');

const matches = (source, pattern) => Array.from(source.matchAll(pattern));
const ids = matches(html, /\sid="([^"]+)"/g).map((match) => match[1]);
const idSet = new Set(ids);

assert.equal(ids.length, idSet.size, 'site shell must not contain duplicate IDs');
for (const id of [
  'arena',
  'arena-shell',
  'arena-canvas',
  'hud-round',
  'hud-killfeed',
  'hud-players',
  'hud-lobby',
  'ws-status',
  'zoom-slider',
  'zoom-value',
  'follow-bot',
  'auto-pan',
  'fullscreen-btn',
  'arena-tabs',
]) {
  assert(idSet.has(id), `existing renderer/app contract #${id} must be preserved`);
}
assert.equal(matches(html, /id="arena-canvas"/g).length, 1, 'the shell must own exactly one persistent arena canvas');

assert.match(html, /css\/site-shell\.css\?v=/, 'responsive shell stylesheet must be loaded');
const serviceStatusImports = matches(`${app}\n${js}`, /['"]\.\/service-status\.js[^'"]*['"]/g).map((match) => match[0]);
assert(serviceStatusImports.length >= 2, 'app.js and site-shell.js must both import the public service-status client');
assert.equal(new Set(serviceStatusImports).size, 1, 'every importer must use the same module URL (cache tag included) so the singleton bridge is shared');
assert.match(html, /js\/site-shell\.js\?v=/, 'responsive shell controller must be loaded');
assert.doesNotMatch(html, /id="footer-stats"|site-round-summary/, 'the header must not repeat bot and round counts already shown in the spectator HUD');
assert.doesNotMatch(app, /fetchArenaStatus|\/arena\/status|footer-stats/, 'the removed header summary must not leave an orphaned arena-status request');
assert.match(
  css,
  /\.site-header-status\s*\{[^}]*margin-inline-start:\s*auto/,
  'Spectator live must claim the remaining desktop header space',
);
assert.match(
  css,
  /\.site-header-status\s*\{[^}]*justify-content:\s*flex-end/,
  'Spectator live must align to the far-right edge of its header space',
);
assert.match(
  css,
  /@media \(max-width: 768px\)[\s\S]*--spectator-touch-target:\s*44px/,
  'touch and responsive layouts must define a 44px spectator target baseline',
);
assert.match(
  css,
  /\.mobile-command-actions :is\(button, a\)[\s\S]{0,700}min-height:\s*var\(--spectator-touch-target\)/,
  'mobile header actions must meet the shared touch-target baseline',
);
assert.match(
  css,
  /\.arena-controls input\[type="range"\][\s\S]{0,700}height:\s*var\(--spectator-touch-target\)/,
  'the zoom slider must expose a 44px effective touch area',
);
assert.match(html, /name="viewport"\s+content="width=device-width, initial-scale=1\.0, viewport-fit=cover"/, 'layout must be viewport and safe-area driven');
assert.doesNotMatch(html, /mobile-suggest|href="m\/"/, 'the main site must not hand phones to a separate page');
assert.match(html, /id="fullscreen-btn"[^>]*aria-pressed="false">Cinema Mode<\/button>/, 'shell action must be presented as Cinema Mode before JS boots');
assert.doesNotMatch(app, /pointer:\s*coarse|location\.href\s*=\s*appPath\('\/m\/'\)/, 'legacy coarse-pointer mobile redirect must be removed');

assert.match(css, /height:\s*100dvh/, 'shell must use the dynamic mobile viewport');
assert.match(css, /safe-area-inset-top/, 'shell must respect top safe area');
assert.match(css, /safe-area-inset-bottom/, 'shell must respect bottom safe area');
assert.match(css, /@media\s*\(max-width:\s*768px\),\s*\(max-height:\s*620px\)/, 'shell must use the mobile layout for narrow phones and short Android landscape screens');
assert.doesNotMatch(css, /max-height:\s*620px\)\s*and\s*\(min-width:\s*769px/, 'short screens must not be forced back into the desktop shell');
assert.doesNotMatch(css, /pointer:\s*(coarse|fine)/, 'mobile layout must not depend on pointer/UA heuristics');
assert.match(css, /\.arena-section[\s\S]*height:\s*100dvh/, 'the live arena must fill the first viewport');
assert.match(css, /\.site-command-dock\.is-open[\s\S]*translateY\(0\)/, 'mobile navigation must open as a bottom sheet');
assert.match(css, /\.arena-shell\.telemetry-open\s+\.arena-sidebar/, 'mobile match telemetry must be secondary and dismissible');
assert.match(css, /body\.site-cinema-mode/, 'cinema mode must hide shell chrome');
assert.match(css, /overflow:\s*hidden/, 'root shell must prevent accidental page overflow');

const mobileMediaStart = css.indexOf('@media (max-width: 768px), (max-height: 620px)');
const mobileMediaEnd = css.indexOf('@media (max-width: 420px)', mobileMediaStart);
assert(mobileMediaStart >= 0 && mobileMediaEnd > mobileMediaStart, 'mobile shell rules must have a bounded source block');
const mobileCss = css.slice(mobileMediaStart, mobileMediaEnd);
assert.match(
  mobileCss,
  /\.arena-sidebar,\s*\.arena-shell\.is-floating \.arena-sidebar\s*\{[^}]*grid-template-rows:\s*auto auto minmax\(0,\s*1fr\)/,
  'mobile telemetry must reserve stable intro, tab, and scrollable-content rows',
);
assert.match(
  mobileCss,
  /\.hud-round\.is-collapsed\s*\{[^}]*top:\s*calc\(var\(--shell-safe-top\) \+ var\(--shell-header-height\) \+ 6px\)[^}]*max-width:\s*calc\(100% - 240px\)/,
  'the compact mobile spectator HUD must align with and reserve room for the live-feed controls',
);
assert.match(
  mobileCss,
  /\.hud-round\.is-collapsed \.hud-compact-title\s*\{[^}]*text-overflow:\s*ellipsis[^}]*white-space:\s*nowrap/,
  'long compact HUD labels must truncate instead of covering live-feed controls',
);
assert.match(
  mobileCss,
  /\.arena-tab-panel\s*\{[^}]*height:\s*100%/,
  'mobile telemetry panels must fill the bounded content track',
);
assert.match(
  mobileCss,
  /\.site-command-dock\.onboarding-rail-stack,\s*\.onboarding-drawer-scroll,\s*\.onboarding-flow-shell-scroll\s*\{[^}]*-webkit-overflow-scrolling:\s*touch[^}]*touch-action:\s*pan-y/,
  'mobile menu and drawer scroll owners must support direct touch panning',
);
assert.match(
  mobileCss,
  /\.overlay-standings \.onboarding-drawer-scroll\s*\{[^}]*overflow-y:\s*auto[^}]*touch-action:\s*pan-y/,
  'mobile Standings must expose one reachable outer vertical scroller',
);
assert.match(
  mobileCss,
  /\.overlay-standings \.onboarding-flow-standings,[\s\S]*?\.overlay-standings \.leaderboard-widget-overlay\s*\{[^}]*height:\s*auto[^}]*min-height:\s*0[^}]*flex:\s*none/,
  'mobile Standings must release the desktop full-height chain',
);
assert.match(
  mobileCss,
  /\.overlay-standings \.leaderboard-scroll-inner\s*\{[^}]*height:\s*auto[^}]*overflow-y:\s*visible[^}]*overflow-x:\s*auto/,
  'mobile Standings tables must expand into the outer vertical scroller while retaining horizontal scroll',
);
assert.match(
  mobileCss,
  /\.overlay-standings \.leaderboard-scroll-inner\s*\{[^}]*overscroll-behavior-x:\s*contain[^}]*overscroll-behavior-y:\s*auto/,
  'wide Standings tables must pass vertical touch gestures to the outer drawer',
);
assert.match(
  sectionsCss,
  /\.arena-tab-content\s*\{[^}]*min-height:\s*0[^}]*overflow:\s*hidden/,
  'telemetry content must stay inside its grid track',
);
assert.match(
  sectionsCss,
  /\.arena-tab-panel\s*\{[^}]*height:\s*100%[^}]*overflow-y:\s*auto/,
  'each telemetry panel must scroll without resizing the tabs',
);

assert.match(js, /stopImmediatePropagation\(\)/, 'cinema control must supersede the legacy mobile redirect');
assert.match(js, /site-cinema-mode/, 'cinema control must toggle shell chrome only');
assert.match(js, /requestArenaResize/, 'layout transitions must resize the existing renderer');
assert.match(
  css,
  /\.arena-sidebar[^{]*\{[^}]*view-transition-name:\s*arena-telemetry-panel/,
  'desktop telemetry must expose a named view-transition surface for a smooth size morph',
);
assert.match(
  css,
  /::view-transition-group\(arena-telemetry-panel\)[^{]*\{[^}]*animation-duration:\s*0\.18s[^}]*animation-timing-function:\s*cubic-bezier\(0\.23,\s*1,\s*0\.32,\s*1\)/,
  'telemetry morph must use the same short responsive motion register as the Spectator HUD',
);
assert.match(
  js,
  /document\.startViewTransition/,
  'telemetry collapse must animate geometry through View Transitions when supported',
);
assert.match(js, /let intendedCollapsed\s*=\s*shell\.classList\.contains/, 'rapid telemetry input must track logical intent synchronously');
assert.match(js, /const generation\s*=\s*\+\+collapseGeneration/, 'telemetry transitions must version deferred state callbacks');
assert.match(js, /if \(generation !== collapseGeneration\) return/, 'stale native View Transition callbacks must not overwrite newer input');
assert.match(js, /setCollapsed\(!intendedCollapsed\)/, 'each rapid click must invert logical intent instead of stale DOM state');
assert.match(js, /nativeTransitionSettling[\s\S]*nativeMorphCooldownUntil\s*=\s*now\s*\+\s*MORPH_DURATION_MS[\s\S]*applyState\(\)/, 'rapid native-transition input must commit synchronously during the compositor settle window');
assert.match(
  js,
  /matchMedia\('\(prefers-reduced-motion:\s*reduce\)'\)\.matches/,
  'telemetry collapse must respect reduced motion',
);
assert.match(
  js,
  /cloneNode\(true\)[\s\S]*ghost\.animate\(/,
  'telemetry collapse must retain a compositor-only FLIP fallback when View Transitions are unavailable',
);
assert.match(js, /cubic-bezier\(0\.23,\s*1,\s*0\.32,\s*1\)/, 'telemetry morph must use the committed responsive ease-out curve');
assert.doesNotMatch(js, /new\s+ArenaEngine|replaceChild|removeChild|location\.(?:href|assign)|\/m\//, 'shell controller must never recreate or redirect the arena');
assert.match(js, /event\.key\s*!==\s*'Escape'/, 'sheets must support Escape');
assert.match(js, /event\.key\s*!==\s*'Tab'/, 'modal drawers must trap keyboard focus');
assert.match(js, /aria-expanded/, 'interactive sheets must publish expanded state');
assert.match(js, /initServiceStatus\(\)/, 'public service status must initialize in the shell');
assert.match(js, /if\s*\(open\)\s*\{[^}]*dock\.scrollTop\s*=\s*0/, 'the mobile command menu must reset to its first item whenever it opens');
assert.match(
  app,
  /const scrollRoot\s*=\s*drawer\.querySelector\('\.onboarding-flow-shell-scroll'\)\s*\|\|\s*drawer\.querySelector\('\.onboarding-drawer-scroll'\)\s*\|\|\s*drawer/,
  'overlays must reset and target their real scroll owner',
);
assert.match(app, /scrollRoot\.scrollTo\(\{ top:\s*0, behavior:\s*'smooth' \}\)/, 'opening an overlay must reset its scroll owner');

assert.match(html, /id="siteBroadcast"[\s\S]*data-service-status-root[\s\S]*data-service-status[\s\S]*data-service-status-dismiss/, 'public broadcast host contract must be present');
assert.match(html, /id="siteBroadcast"[\s\S]*role="status"[\s\S]*aria-live="polite"/, 'public notices must be announced accessibly');
assert.match(html, /delay applies to ordered[\s\S]*arena_state[\s\S]*lobby_state[\s\S]*heartbeat[\s\S]*service_status/, 'spectator docs must distinguish delayed gameplay state from immediate controls');
assert.match(llms, /Range is 12 grid tiles with a 4s cooldown[\s\S]*requires line of sight[\s\S]*Supply exactly one of `target` or `target_position`/, 'AI guidance must match universal grapple enforcement');
assert.match(llms, /Only lit pads with `is_ready: true` activate[\s\S]*locks the pair for 3s[\s\S]*5s cooldown on both linked pads/, 'AI guidance must explain teleporter readiness and both cooldown layers');
assert.match(llms, /Targeted `attack`, `shove`, and `grapple`[\s\S]*current fog-of-war view[\s\S]*public bounty target/, 'AI guidance must explain server-side target visibility enforcement');
assert.match(llms, /features_pending: true[\s\S]*`game_mode` is omitted[\s\S]*round-feature arrays are empty[\s\S]*until `round_start`/, 'AI guidance must distinguish pre-generated terrain from unresolved round features');
assert.match(llms, /Landmines[\s\S]*1-tile blast radius[\s\S]*Gravity Wells[\s\S]*within 3 tiles/, 'AI guidance must match mine and gravity-well radii');
assert.match(llms, /Hazard Zones[\s\S]*3 HP\/tick[\s\S]*Sudden Death[\s\S]*999 damage/, 'AI guidance must match hazard and void damage');
assert.match(html, /balanced speed allocation averages half a cell per tick/, 'movement docs must explain speed-scaled grid pacing');
assert.match(html, /Target visibility:[\s\S]*target-ID[\s\S]*current fog-of-war view[\s\S]*public bounty target/, 'primary action docs must explain target visibility enforcement');
assert.match(html, /grapple[\s\S]*exactly one aim mode[\s\S]*Sending both is rejected/, 'primary grapple docs must explain exclusive aim modes');
assert.match(llms, /These are base values[\s\S]*\/api\/v1\/weapon-stats/, 'AI weapon guidance must distinguish base values from adaptive live values');
assert.match(html, /Safe zone initial radius[\s\S]*Covers the map \(~71 tiles on the square arena\)/, 'site docs must reflect the circumscribed initial zone');

const keith = html.indexOf('>Keith S<');
const andrew = html.indexOf('>Andrew<');
const will = html.indexOf('>Will<');
assert(keith >= 0 && keith < andrew && andrew < will, 'builder credits must list Keith S, Andrew, then Will');
assert.match(html, /href="https:\/\/www\.linkedin\.com\/in\/keith-swoger"/, 'Keith S LinkedIn must be linked');
assert.match(html, /href="https:\/\/www\.linkedin\.com\/in\/andrew-demczuk-4038a217\/"/, 'Andrew LinkedIn must remain linked');
assert.match(html, /<strong>Will<\/strong><small>Details coming soon<\/small>/, 'Will must have a future-details placeholder');
assert.match(html, /href="https:\/\/angel-serv\.com\/"/, 'Contact must link to Angel-Serv.com');
assert.match(html, /href="mailto:Hello@angel-serv\.com"/, 'Contact must link to Hello@angel-serv.com');

for (const match of matches(html, /aria-labelledby="([^"]+)"/g)) {
  for (const labelledBy of match[1].split(/\s+/)) {
    assert(idSet.has(labelledBy), `aria-labelledby must resolve #${labelledBy}`);
  }
}
for (const match of matches(html, /data-overlay-open="([^"]+)"/g)) {
  assert(idSet.has(match[1]), `overlay trigger must resolve #${match[1]}`);
}
for (const match of matches(html, /data-close-overlay="([^"]+)"/g)) {
  assert(idSet.has(match[1]), `overlay close control must resolve #${match[1]}`);
}

const overlays = matches(html, /<div class="[^"]*onboarding-overlay[^"]*" id="([^"]+)"/g).map((match) => match[1]);
for (const overlay of overlays) {
  assert.match(html, new RegExp(`class="drawer-close" data-close-overlay="${overlay}"`), `${overlay} must have a visible close button`);
}

console.log('responsive arena-first site shell checks passed');
