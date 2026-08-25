'use strict';

/**
 * Developer lobby chat panel.
 * Connects to /ws/chat, renders the shared lobby for bot developers, and
 * lets signed-in customers post. Self-initializing: hides itself when the
 * server reports chat disabled.
 *
 * On pages with no pre-built #chat-overlay markup (the desktop site), this
 * module builds its own floating bubble + panel and mounts it to <body>.
 * Pages that already provide #chat-overlay (the mobile site's full-screen
 * sheet) keep their own layout; this module only fills in its contents.
 * @module chat-panel
 */

import { apiPath, wsURL } from './paths.js?v=20260710a';
import { openProfilePopup } from './profile-popup.js?v=20260714a';
import { startSignIn, watchSignInState } from './sign-in.js?v=20260825a';

const OVERLAY_ID = 'chat-overlay';
// Discord-style grouping: consecutive messages from the same sender within
// this window share one header instead of repeating handle + time per line.
const GROUP_WINDOW_MS = 5 * 60 * 1000;

const CHAT_ICON_SVG = '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"></path></svg>';

/** Mirror of SpectatorSocket's reconnect discipline for the chat stream. */
class ChatSocket {
  constructor(url, onMessage, onStatus) {
    this.url = url;
    this.onMessage = onMessage;
    this.onStatus = onStatus;
    this.ws = null;
    this.reconnectDelay = 1000;
    this.maxReconnectDelay = 30000;
    this.shouldConnect = false;
    this._pingInterval = null;
    this._staleTimer = null;
    this._staleTimeout = 45000;
    this._reconnectTimer = null;
    this._backoffResetTimer = null;
  }

  connect() {
    this.shouldConnect = true;
    this._doConnect();
  }

  disconnect() {
    this.shouldConnect = false;
    this._stopPing();
    this._clearTimer('_staleTimer');
    this._clearTimer('_reconnectTimer');
    this._clearTimer('_backoffResetTimer');
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
  }

  _clearTimer(name) {
    if (this[name] !== null) {
      clearTimeout(this[name]);
      this[name] = null;
    }
  }

  _doConnect() {
    if (!this.shouldConnect) return;
    this._clearTimer('_reconnectTimer');
    try {
      this.ws = new WebSocket(this.url);
    } catch (err) {
      this._scheduleReconnect();
      return;
    }

    this.ws.onopen = () => {
      this.onStatus('connected');
      this._startPing();
      this._resetStaleTimer();
      // Reset backoff only after the connection survives a beat, so an
      // accept-then-drop server does not loop at 1s.
      this._clearTimer('_backoffResetTimer');
      this._backoffResetTimer = setTimeout(() => {
        this.reconnectDelay = 1000;
      }, 5000);
    };

    this.ws.onmessage = (event) => {
      this._resetStaleTimer();
      let msg;
      try {
        msg = JSON.parse(event.data);
      } catch (err) {
        return;
      }
      if (msg.type === 'heartbeat') return;
      this.onMessage(msg);
    };

    this.ws.onclose = () => {
      this._stopPing();
      this._clearTimer('_staleTimer');
      this._clearTimer('_backoffResetTimer');
      this.ws = null;
      if (this.shouldConnect) {
        this.onStatus('reconnecting');
        this._scheduleReconnect();
      }
    };

    this.ws.onerror = () => {
      this.onStatus('error');
    };
  }

  _scheduleReconnect() {
    if (!this.shouldConnect || this._reconnectTimer !== null) return;
    const delay = this.reconnectDelay / 2 + Math.random() * (this.reconnectDelay / 2);
    this.reconnectDelay = Math.min(this.reconnectDelay * 2, this.maxReconnectDelay);
    this._reconnectTimer = setTimeout(() => {
      this._reconnectTimer = null;
      this._doConnect();
    }, delay);
  }

  _startPing() {
    this._stopPing();
    this._pingInterval = setInterval(() => {
      if (this.ws && this.ws.readyState === WebSocket.OPEN) {
        this.ws.send('ping');
      }
    }, 15000);
  }

  _stopPing() {
    if (this._pingInterval !== null) {
      clearInterval(this._pingInterval);
      this._pingInterval = null;
    }
  }

  _resetStaleTimer() {
    this._clearTimer('_staleTimer');
    this._staleTimer = setTimeout(() => {
      if (this.ws) this.ws.close();
    }, this._staleTimeout);
  }

  send(obj) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(obj));
      return true;
    }
    return false;
  }
}

function formatTime(tsMillis) {
  const d = new Date(tsMillis);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

// Wire handles are "Name#xxxxxxxx" (see chatHandle in the Go ws package) --
// the discriminator disambiguates two people who both picked the same name,
// but is noise in the message log itself. Kept as a title/tooltip instead.
function shortHandle(handle) {
  const raw = handle || 'dev';
  const idx = raw.lastIndexOf('#');
  return idx > 0 ? raw.slice(0, idx) : raw;
}

async function fetchChatConfig() {
  try {
    const resp = await fetch(apiPath('/chat/config'), { cache: 'no-store' });
    if (!resp.ok) return null;
    return await resp.json();
  } catch (err) {
    return null;
  }
}

/**
 * Resolves the DOM chat-panel.js drives, either by reusing pre-built markup
 * (mobile's full-screen sheet) or, if none exists, building a floating
 * bubble + panel from scratch (desktop). Either way the returned ids/classes
 * inside are the same, so the rest of this module never needs to know which
 * path was taken.
 */
function ensureChatDOM() {
  const existing = document.getElementById(OVERLAY_ID);
  if (existing) {
    return {
      overlay: existing,
      launcherEl: document.getElementById('fab-chat'),
      listEl: document.getElementById('chat-messages'),
      formEl: document.getElementById('chat-form'),
      inputEl: document.getElementById('chat-input'),
      sendBtn: document.getElementById('chat-send'),
      statusEl: document.getElementById('chat-status-line'),
    };
  }

  const bubble = document.createElement('button');
  bubble.type = 'button';
  bubble.id = 'chat-bubble';
  bubble.className = 'chat-bubble';
  bubble.setAttribute('aria-label', 'Open chat');
  bubble.setAttribute('aria-expanded', 'false');
  bubble.setAttribute('aria-controls', OVERLAY_ID);
  bubble.innerHTML = CHAT_ICON_SVG;

  const overlay = document.createElement('div');
  overlay.id = OVERLAY_ID;
  overlay.className = 'chat-floating-panel';
  overlay.setAttribute('aria-hidden', 'true');
  overlay.setAttribute('role', 'dialog');
  overlay.setAttribute('aria-label', 'Developer lobby chat');
  overlay.innerHTML = `
    <div class="chat-floating-header">
      <span>Developer Lobby</span>
      <button type="button" class="chat-floating-close" aria-label="Close chat">&times;</button>
    </div>
    <div class="chat-panel">
      <div class="chat-status-line" id="chat-status-line" data-tone="info" aria-live="polite">Open to connect</div>
      <div class="chat-messages" id="chat-messages" aria-label="Chat messages"></div>
      <form class="chat-form" id="chat-form" autocomplete="off">
        <input type="text" id="chat-input" name="chat-input" placeholder="Message the lobby" aria-label="Chat message" disabled>
        <button type="submit" id="chat-send" disabled>Send</button>
      </form>
    </div>
  `;

  document.body.appendChild(bubble);
  document.body.appendChild(overlay);

  const setOpen = (open) => {
    overlay.classList.toggle('open', open);
    overlay.setAttribute('aria-hidden', String(!open));
    bubble.classList.toggle('is-active', open);
    bubble.setAttribute('aria-expanded', String(open));
  };
  bubble.addEventListener('click', () => setOpen(!overlay.classList.contains('open')));
  overlay.querySelector('.chat-floating-close').addEventListener('click', () => setOpen(false));
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && overlay.classList.contains('open')) setOpen(false);
  });

  return {
    overlay,
    launcherEl: bubble,
    listEl: overlay.querySelector('#chat-messages'),
    formEl: overlay.querySelector('#chat-form'),
    inputEl: overlay.querySelector('#chat-input'),
    sendBtn: overlay.querySelector('#chat-send'),
    statusEl: overlay.querySelector('#chat-status-line'),
  };
}

export function setChatAvailability({ enabled, overlay, launcherEl, watermark }) {
  if (!enabled) {
    overlay._mobileOverlayClose?.();
    overlay.classList.remove('open');
    overlay.setAttribute('aria-hidden', 'true');
    watermark.hidden = true;
    launcherEl?.classList.remove('is-active', 'active');
    launcherEl?.setAttribute('aria-expanded', 'false');
  }

  if (!launcherEl) return;
  launcherEl.disabled = !enabled;
  launcherEl.classList.toggle('is-disabled', !enabled);
  launcherEl.setAttribute('aria-label', enabled ? 'Open chat' : 'Chat disabled');
  launcherEl.title = enabled ? 'Open chat' : 'Chat disabled';
}

/** Wraps listEl in a positioning shell and adds the read-only watermark over it. */
function buildWatermark(listEl) {
  const shell = document.createElement('div');
  shell.className = 'chat-messages-shell';
  listEl.parentElement.insertBefore(shell, listEl);
  shell.appendChild(listEl);

  const watermark = document.createElement('div');
  watermark.className = 'chat-watermark';
  watermark.hidden = true;
  watermark.innerHTML = '<button type="button" class="chat-watermark-btn">Sign in to chat</button>';
  shell.appendChild(watermark);

  // The press is wired up by initChatPanel, which owns the status line this
  // needs to report into.
  return watermark;
}

function initChatPanel(cfg) {
  const dom = ensureChatDOM();
  const { overlay, launcherEl, listEl, formEl, inputEl, sendBtn, statusEl } = dom;
  if (!overlay || !listEl || !formEl || !inputEl || !sendBtn || !statusEl) return;

  const watermark = buildWatermark(listEl);

  document.body.classList.add('chat-enabled');
  setChatAvailability({ enabled: !!cfg.enabled, overlay, launcherEl, watermark });
  const bodyLimit = cfg.max_body_len > 0 ? cfg.max_body_len : 280;
  // maxlength counts UTF-16 code units, but the server counts code points, so
  // an emoji-heavy message could be truncated below the real limit. Use a
  // loose 2x backstop here and enforce the true code-point limit on submit.
  inputEl.maxLength = bodyLimit * 2;
  // The server only ever retains/backfills this many messages (see
  // ChatHistorySize), so rendering more than that client-side is pointless.
  const renderCap = cfg.history_size > 0 ? cfg.history_size : 50;

  let canPost = false;
  let connected = false;
  let started = false;
  let runtimeEnabled = !!cfg.enabled;
  let requiresSignIn = false;
  const lineIndex = new Map(); // message id -> its .chat-body element
  let lastGroup = null; // { key, el, ts }

  function setStatus(text, tone) {
    statusEl.textContent = text;
    statusEl.dataset.tone = tone || 'info';
  }

  function updateComposer() {
    const ready = connected && canPost && runtimeEnabled;
    inputEl.disabled = !ready;
    sendBtn.disabled = !ready;
  }

  function nearBottom() {
    return listEl.scrollHeight - listEl.scrollTop - listEl.clientHeight < 60;
  }

  function evictOldest() {
    while (lineIndex.size > renderCap) {
      const oldestId = lineIndex.keys().next().value;
      removeMessage(oldestId);
    }
  }

  // Server strings only ever land in the DOM through textContent.
  function appendMessage(msg) {
    if (lineIndex.has(msg.id)) return;
    const stick = nearBottom();
    const key = msg.handle || 'dev';

    let groupEl;
    if (lastGroup && lastGroup.key === key && msg.ts - lastGroup.ts < GROUP_WINDOW_MS) {
      groupEl = lastGroup.el;
    } else {
      groupEl = document.createElement('div');
      groupEl.className = 'chat-group';

      const meta = document.createElement('div');
      meta.className = 'chat-row-meta';
      const handleEl = document.createElement('span');
      handleEl.className = 'chat-handle';
      handleEl.textContent = shortHandle(key);
      handleEl.title = key;
      if (msg.account_id) {
        handleEl.classList.add('chat-handle-clickable');
        handleEl.setAttribute('role', 'button');
        handleEl.setAttribute('tabindex', '0');
        const openThisProfile = () => openProfilePopup(msg.account_id);
        handleEl.addEventListener('click', openThisProfile);
        handleEl.addEventListener('keydown', (event) => {
          if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            openThisProfile();
          }
        });
      }
      const timeEl = document.createElement('span');
      timeEl.className = 'chat-time';
      timeEl.textContent = formatTime(msg.ts);
      meta.appendChild(handleEl);
      meta.appendChild(timeEl);
      groupEl.appendChild(meta);

      listEl.appendChild(groupEl);
    }

    const bodyEl = document.createElement('div');
    bodyEl.className = 'chat-body';
    bodyEl.textContent = msg.body || '';
    bodyEl.title = formatTime(msg.ts);
    groupEl.appendChild(bodyEl);

    lineIndex.set(msg.id, bodyEl);
    lastGroup = { key, el: groupEl, ts: msg.ts };

    evictOldest();
    if (stick) listEl.scrollTop = listEl.scrollHeight;
  }

  function appendNotice(text) {
    lastGroup = null; // a notice always starts a fresh group after it
    const row = document.createElement('div');
    row.className = 'chat-row-notice';
    row.textContent = text;
    listEl.appendChild(row);
    if (nearBottom()) listEl.scrollTop = listEl.scrollHeight;
  }

  function removeMessage(id) {
    const bodyEl = lineIndex.get(id);
    if (!bodyEl) return;
    const groupEl = bodyEl.parentElement;
    bodyEl.remove();
    lineIndex.delete(id);
    if (!groupEl) return;

    const remainingFirst = groupEl.querySelector('.chat-body');
    if (!remainingFirst) {
      if (lastGroup && lastGroup.el === groupEl) lastGroup = null;
      groupEl.remove();
      return;
    }
    // The removed line may have been the group's first message, whose time
    // the header displays (the handle does not need updating: every message
    // in a group is from the same sender). Keep it in sync with whichever
    // message is now first -- this matters here because eviction always
    // removes the single oldest surviving line, which is always a group's
    // first message.
    const timeEl = groupEl.querySelector('.chat-time');
    if (timeEl) timeEl.textContent = remainingFirst.title;
  }

  const socket = new ChatSocket(
    wsURL('/chat'),
    (msg) => {
      switch (msg.type) {
        case 'chat_status':
          // A successful connection is itself proof chat is currently
          // enabled (the server 404s the upgrade otherwise), so this always
          // clears any earlier chat_settings-disabled state on (re)connect.
          canPost = !!msg.can_post;
          runtimeEnabled = true;
          requiresSignIn = !canPost && msg.reason === 'sign_in_required';
          setChatAvailability({ enabled: true, overlay, launcherEl, watermark });
          if (canPost) {
            setStatus('Chatting as ' + shortHandle(msg.handle), 'ok');
            watermark.hidden = true;
          } else if (requiresSignIn) {
            setStatus('Read-only', 'info');
            watermark.hidden = false;
          } else {
            setStatus('Read-only', 'info');
            watermark.hidden = true;
          }
          updateComposer();
          break;
        case 'chat_settings':
          runtimeEnabled = !!msg.enabled;
          setStatus(runtimeEnabled ? 'Chat re-enabled' : 'Chat disabled by an admin', runtimeEnabled ? 'ok' : 'warn');
          setChatAvailability({ enabled: runtimeEnabled, overlay, launcherEl, watermark });
          if (runtimeEnabled) watermark.hidden = !requiresSignIn;
          updateComposer();
          break;
        case 'chat_history':
          listEl.textContent = '';
          lineIndex.clear();
          lastGroup = null;
          (msg.messages || []).forEach(appendMessage);
          listEl.scrollTop = listEl.scrollHeight;
          break;
        case 'chat_message':
          if (msg.message) appendMessage(msg.message);
          break;
        case 'chat_message_hidden':
          removeMessage(msg.id);
          break;
        case 'chat_error':
          if (msg.code === 'BOT_ALIVE_LOCK') {
            appendNotice('Chat is locked while your bot is alive in the round.');
          } else if (msg.code === 'BLOCKED_KEYWORD') {
            appendNotice('Message blocked: it contains a restricted word.');
          } else if (msg.code === 'CHAT_DISABLED') {
            appendNotice('Chat is temporarily disabled by an admin.');
          } else {
            appendNotice(msg.message || 'Message rejected.');
          }
          break;
        default:
          break;
      }
    },
    (state) => {
      connected = state === 'connected';
      if (!connected) {
        setStatus(state === 'reconnecting' ? 'Reconnecting...' : 'Connecting...', 'warn');
      }
      updateComposer();
    },
  );

  formEl.addEventListener('submit', (event) => {
    event.preventDefault();
    const body = inputEl.value.trim();
    if (!body) return;
    // Count code points, matching the server's rune limit.
    if ([...body].length > bodyLimit) {
      appendNotice('Message is too long (max ' + bodyLimit + ' characters).');
      return;
    }
    if (socket.send({ type: 'chat_post', body })) {
      inputEl.value = '';
    }
  });

  // Escape with a draft clears the draft; Escape on an empty input falls
  // through to the panel's own close handler.
  inputEl.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && inputEl.value !== '') {
      inputEl.value = '';
      event.stopPropagation();
    }
  });

  // Connect lazily on first open; afterwards the socket stays up so the
  // unread state survives the panel closing.
  const observer = new MutationObserver(() => {
    if (!started && overlay.classList.contains('open')) {
      started = true;
      observer.disconnect();
      setStatus('Connecting...', 'warn');
      socket.connect();
    }
  });
  observer.observe(overlay, { attributes: true, attributeFilter: ['class'] });

  /*
   * "Sign in to chat" signs you in to chat.
   *
   * It used to press the Dashboard control on whichever page this panel was
   * mounted to, which opened a drawer, which asked which way you wanted to
   * sign in -- three screens to reach the one thing the button named. Now it
   * opens the Accounts window itself, and the panel behind it stays exactly
   * where it was, mid-conversation.
   *
   * The one press that cannot open a window is a press on an Arena with no
   * customer OIDC configured. That says so on the status line, which is where
   * this panel's reader is already looking, rather than opening nothing.
   */
  watermark.querySelector('.chat-watermark-btn').addEventListener('click', async () => {
    const result = await startSignIn();
    if (result.status === 'unconfigured') setStatus(result.message, 'warn');
  });

  // Both the dashboard (in its own iframe) and this panel post to the same
  // customer session cookie, so a sign-in on either side shows up on the
  // other via the shared session watch -- reconnecting an already-open socket
  // picks up the new identity, since the server resolves it once at WS
  // upgrade. Sharing sign-in.js's watch rather than starting a second one
  // also means the sign-in press above already knows the answer it needs.
  watchSignInState(() => {
    if (started) {
      socket.disconnect();
      setStatus('Connecting...', 'warn');
      socket.connect();
    }
  });
}

document.addEventListener('DOMContentLoaded', async () => {
  const cfg = await fetchChatConfig();
  if (!cfg) return;
  initChatPanel(cfg);
});
