'use strict';

/**
 * Browser error reporting.
 *
 * The spectator had no error reporting of any kind, so a total rendering
 * outage (the vendored-Babylon side-effect regression) ran for four weeks
 * with nothing but console output on machines nobody was watching. This
 * module forwards uncaught errors to the server, where they land in the
 * bounded in-memory error aggregator and show up in the admin Errors tab.
 *
 * Deliberately conservative: it never throws, never retries, caps how much it
 * will ever send, and de-duplicates repeats. A broken page must not become a
 * broken page that also floods the network.
 *
 * @module client-errors
 */

import { apiPath } from './paths.js?v=20260710a';

// Hard ceiling per page load. A render loop failing every frame would
// otherwise generate thousands of identical reports.
const MAX_REPORTS_PER_SESSION = 12;
// Minimum gap between any two sends, independent of dedup.
const MIN_SEND_INTERVAL_MS = 2000;

let sent = 0;
let lastSendAt = 0;
const seen = new Set();
let installed = false;

function buildTag() {
  // The module URL carries the deploy's cache-bust tag, which is the fastest
  // way to tell which frontend revision produced a report.
  try {
    const match = /[?&]v=([^&]+)/.exec(import.meta.url);
    return match ? match[1] : 'untagged';
  } catch {
    return 'unknown';
  }
}

function post(body) {
  try {
    const payload = JSON.stringify(body);
    const url = apiPath('/client-errors');
    // sendBeacon survives pagehide/unload, which is exactly when a fatal
    // error is most likely to be reported.
    if (navigator.sendBeacon) {
      const blob = new Blob([payload], { type: 'application/json' });
      if (navigator.sendBeacon(url, blob)) return;
    }
    fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: payload,
      keepalive: true,
    }).catch(() => { /* reporting must never surface its own failure */ });
  } catch {
    // Never let the reporter throw inside an error handler.
  }
}

/**
 * Report a client-side error. Safe to call from anywhere; all limiting,
 * de-duplication and failure handling is internal.
 * @param {string} kind short category, e.g. 'uncaught' or 'render-loop'
 * @param {*} error the thrown value
 * @param {Object} [extra] optional {source}
 */
export function reportClientError(kind, error, extra = {}) {
  try {
    if (sent >= MAX_REPORTS_PER_SESSION) return;
    const message = String(
      (error && (error.message || error.reason || error)) || 'unknown error',
    ).slice(0, 500);
    // Dedup on kind+message so a repeating fault reports once.
    const key = `${kind}:${message}`;
    if (seen.has(key)) return;
    const now = Date.now();
    if (now - lastSendAt < MIN_SEND_INTERVAL_MS) return;
    seen.add(key);
    sent += 1;
    lastSendAt = now;
    post({
      kind,
      message,
      stack: String((error && error.stack) || '').slice(0, 2000),
      source: String(extra.source || '').slice(0, 200),
      page: String(location.pathname + location.search).slice(0, 200),
      build: buildTag(),
    });
  } catch {
    // Swallow: reporting is best-effort by design.
  }
}

/** Install global handlers. Idempotent. */
export function installClientErrorReporting() {
  if (installed || typeof window === 'undefined') return;
  installed = true;
  window.addEventListener('error', (event) => {
    // Resource load failures (img/script/css) surface here with no `error`
    // object; they are still worth knowing about, hence the fallback.
    if (event.error) {
      reportClientError('uncaught', event.error, { source: event.filename || '' });
    } else if (event.target && event.target !== window && event.target.src) {
      reportClientError('resource', new Error(`failed to load ${event.target.src}`), {
        source: event.target.tagName || '',
      });
    }
  }, true);
  window.addEventListener('unhandledrejection', (event) => {
    reportClientError('unhandled-rejection', event.reason);
  });
}
