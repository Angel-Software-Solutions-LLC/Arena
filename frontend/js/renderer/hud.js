/**
 * HUD overlay rendering - kill feed, round info, player roster, connection status.
 * Kill feed and player list render into tab panels beside the arena.
 * Round info stays as the on-canvas broadcast card.
 * @module renderer/hud
 */

export class HudRenderer {
  /**
   * @param {HTMLElement} roundEl
   * @param {HTMLElement} killfeedEl
   * @param {HTMLElement} playersEl
   * @param {HTMLElement} lobbyEl
   * @param {HTMLElement} statusEl
   */
  constructor(roundEl, killfeedEl, playersEl, lobbyEl, statusEl) {
    this.roundEl = roundEl;
    this.killfeedEl = killfeedEl;
    this.playersEl = playersEl;
    this.lobbyEl = lobbyEl;
    this.statusEl = statusEl;
    this._seenKeys = new Set();
    this._activeEntries = [];
    // Bot taunt feed: event ids already shown, plus a small pending queue
    // filled unthrottled from app.js and drained on the throttled update.
    this._seenTauntIds = new Set();
    this._pendingTaunts = [];
    /** @type {Map<string, {el: HTMLElement, refs: Object, sig: string}>} */
    this._playerRows = new Map();
    this._playerOrder = '';
    this._lastLobbyHtml = '';
    this._lastRoundState = null;
    this._lastLobbyState = null;
    this._roundMode = '';
    this._roundRefs = {};
    // Phones boot into the compact pill: at 375px the expanded card plus the
    // fixed header and controls cover ~40% of the screen. One tap expands.
    this.roundCollapsed = window.matchMedia('(max-width: 768px) and (pointer: coarse)').matches;
    this.selectedBotId = null;
    this.onSelectBot = null;

    this.roundEl.addEventListener('click', (event) => {
      const toggle = event.target.closest('[data-hud-toggle]');
      if (!toggle && !this.roundCollapsed) return;
      this.roundCollapsed = !this.roundCollapsed;
      if (this._lastRoundState) {
        this._updateRoundInfo(this._lastRoundState);
      } else if (this._lastLobbyState) {
        this._updateLobbyInfo(this._lastLobbyState);
      }
    });

    this.playersEl.addEventListener('click', (event) => {
      const row = event.target.closest('.player-entry[data-bot-id]');
      if (!row || !this.onSelectBot) return;
      this.onSelectBot(row.dataset.botId);
    });
  }

  updateState(state) {
    if (!state) return;
    if (state.type === 'lobby_state') {
      this._updateLobbyInfo(state);
      return;
    }
    this._updateRoundInfo(state);
    this._updateKillFeed(state.kill_feed || []);
    this._updateTaunts(state.bots || [], state.tick);
    this._updatePlayers(state.bots || []);
    this._updateWaitingBots(state.waiting_bots || []);
  }

  _updateLobbyInfo(state) {
    this._lastLobbyState = state;
    this.resetKillFeed();
    const players = state.players || [];
    const count = state.bots_connected || 0;
    const countdown = state.countdown ? `${state.countdown}s` : 'Waiting';
    const needed = Math.max(0, (state.bots_needed || 2) - count);
    const compactLabel = state.countdown ? `Lobby ${countdown}` : 'Lobby Waiting';

    if (this.roundCollapsed) {
      this._ensureCompactHud();
      this._roundRefs.compactTitle.textContent = compactLabel;
    } else {
      this._ensureLobbyHud();
      this._roundRefs.title.textContent = 'Lobby Flow';
      this._roundRefs.phase.textContent = 'Queue';
      this._roundRefs.metric1.textContent = String(count);
      this._roundRefs.metric2.textContent = countdown;
      this._roundRefs.metric3.textContent = String(needed);
      this._roundRefs.metric4.textContent = state.countdown ? 'Seeding' : 'Staging';
    }

    const html = players.map((p) => this._playerCard({
      name: p.name,
      botId: '',
      avatarColor: p.avatar_color,
      weapon: p.weapon,
      status: 'Ready',
      selectable: false,
      pills: [p.weapon],
    })).join('');

    if (html !== this._lastLobbyHtml) {
      this.lobbyEl.innerHTML = html;
      this._lastLobbyHtml = html;
    }
  }

  _updateRoundInfo(state) {
    this._lastRoundState = state;
    const botsAlive = (state.bots || []).filter((b) => b.is_alive).length;
    const totalBots = (state.bots || []).length;
    // Maps roll a random shape every round — show which one this round is
    // playing on so two spectators comparing views can tell rounds apart.
    const shape = state.map_shape && state.map_shape !== 'square' ? ` · ${state.map_shape}` : '';
    const roundLabel = (state.round_number ? `Round ${state.round_number}` : 'Live Round') + shape;
    const zoneRadius = Math.round(state.safe_zone?.radius || 0);
    const modifierLabel = this._modifierLabel(state.round_modifier);
    // Team modes: surface the mode and live team scores in the HUD.
    const modeLabel = this._modeLabel(state.game_mode);
    const scoreText = this._teamScoreText(state.team_scores);
    const teamInfo = modeLabel && scoreText ? `${modeLabel} ${scoreText}` : modeLabel;
    let phase = teamInfo || (botsAlive > 0 ? modifierLabel : 'Syncing');
    let compactLabel = modifierLabel === 'Normal'
      ? `${this._esc(roundLabel)} - ${botsAlive}/${totalBots}`
      : `${this._esc(roundLabel)} - ${modifierLabel} - ${botsAlive}/${totalBots}`;
    if (teamInfo) compactLabel = `${this._esc(roundLabel)} - ${teamInfo}`;

    // Sudden-death overtime: override the phase line and flag the element
    // with class hooks (styling lands in CSS separately).
    const suddenDeath = !!state.sudden_death;
    const suddenStall = suddenDeath && !!state.sudden_death_stall;
    if (suddenDeath) {
      const mult = state.sudden_death_mult || 2;
      phase = suddenStall ? 'RAPID DAMAGE — FIGHT!' : `SUDDEN DEATH — ${mult}x DMG`;
      compactLabel = `☠ ${compactLabel}`;
    }

    if (this.roundCollapsed) {
      this._ensureCompactHud();
      this._roundRefs.compactTitle.textContent = compactLabel;
      return;
    }

    this._ensureRoundHud();
    this._roundRefs.title.textContent = roundLabel;
    this._roundRefs.phase.textContent = phase;
    this._roundRefs.phase.classList.toggle('is-sudden-death', suddenDeath);
    this._roundRefs.phase.classList.toggle('is-stall', suddenStall);
    this._roundRefs.metric1.textContent = String(state.round_tick || 0);
    this._roundRefs.metric2.textContent = `${botsAlive} / ${totalBots}`;
    this._roundRefs.metric3.textContent = String(zoneRadius);
    this._roundRefs.metric4.textContent = String((state.pickups || []).length);
  }

  /** @private Human label for team game modes ('' for FFA/unknown). */
  _modeLabel(mode) {
    switch (mode) {
      case 'team_battle': return 'Team Battle';
      case 'ctf': return 'CTF';
      default: return '';
    }
  }

  /** @private "Blue 3 : 1 Red"-style score line from the team_scores map. */
  _teamScoreText(scores) {
    if (!scores) return '';
    const names = ['Blue', 'Red', 'Green', 'Gold'];
    const teams = Object.keys(scores).sort((a, b) => Number(a) - Number(b));
    if (teams.length < 2) return '';
    return teams
      .map((t) => `${names[Number(t) - 1] || `T${t}`} ${scores[t]}`)
      .join(' : ');
  }

  _modifierLabel(modifier) {
    switch (modifier) {
      case 'fast_zone': return 'Fast Zone';
      case 'pickup_surge': return 'Pickup Surge';
      case 'double_bounty': return 'Double Bounty';
      case 'teleport_surge': return 'Teleport Surge';
      case 'hazard_storm': return 'Hazard Storm';
      default: return 'Normal';
    }
  }

  _updatePlayers(bots) {
    const alive = bots.filter((b) => b.is_alive);
    const dead = bots.filter((b) => !b.is_alive);
    const sorted = [...alive, ...dead];
    const rows = this._playerRows;

    // Drop rows for bots that left the arena.
    const ids = new Set(sorted.map((b) => b.bot_id));
    for (const [id, row] of rows) {
      if (!ids.has(id)) {
        row.el.remove();
        rows.delete(id);
      }
    }

    for (const b of sorted) {
      const isDead = !b.is_alive;
      const selected = this.selectedBotId === b.bot_id;
      const status = b.is_alive ? `${Math.round(b.hp)} HP` : 'Eliminated';
      const pills = [
        b.weapon,
        `${Math.round(b.round_kills || 0)} Kills`,
        ...(b.kill_streak > 0 ? [`Streak ${Math.round(b.kill_streak)}`] : []),
      ].map((pill) => (pill == null ? '???' : String(pill)));

      let row = rows.get(b.bot_id);
      if (!row) {
        row = this._buildPlayerRow({
          botId: b.bot_id,
          name: b.name,
          avatarColor: b.avatar_color,
          weapon: b.weapon,
          status,
          selectable: true,
          dead: isDead,
          selected,
          pills,
        });
        rows.set(b.bot_id, row);
      }

      // Signature covers every patchable field; skip untouched rows so the
      // 200ms combat updates never disturb hover/click on stable rows.
      const sig = [
        b.name, b.avatar_color, status,
        isDead ? 1 : 0, selected ? 1 : 0, pills.join('\u0001'),
      ].join('\u0002');
      if (sig === row.sig) continue;
      row.sig = sig;

      row.el.classList.toggle('dead', isDead);
      row.el.classList.toggle('selected', selected);
      row.refs.dot.style.color = b.avatar_color || '#fff';
      row.refs.name.textContent = b.name == null ? '???' : String(b.name);
      row.refs.status.textContent = status;
      this._patchPills(row.refs.meta, pills);
    }

    // Reorder (appendChild moves attached nodes) only when the alive/dead
    // sort order actually changed. New rows attach here too: a new bot id
    // always changes the order key.
    const order = sorted.map((b) => b.bot_id).join('\u0001');
    if (order !== this._playerOrder) {
      this._playerOrder = order;
      for (const b of sorted) this.playersEl.appendChild(rows.get(b.bot_id).el);
    }
  }

  /** @private Build one roster row element via a template and capture patch refs. */
  _buildPlayerRow(opts) {
    _ROW_TEMPLATE.innerHTML = this._playerCard(opts);
    const el = _ROW_TEMPLATE.content.firstElementChild;
    el.remove();
    return {
      el,
      refs: {
        dot: el.querySelector('.player-entry-title > span:first-child'),
        name: el.querySelector('.player-entry-name'),
        status: el.querySelector('.player-entry-status'),
        meta: el.querySelector('.player-entry-meta'),
      },
      sig: '',
    };
  }

  /** @private Reuse pill spans in place; add/remove spans only when the count changes. */
  _patchPills(metaEl, pills) {
    while (metaEl.children.length > pills.length) metaEl.lastElementChild.remove();
    for (let i = 0; i < pills.length; i++) {
      let span = metaEl.children[i];
      if (!span) {
        span = document.createElement('span');
        span.className = 'player-pill';
        metaEl.appendChild(span);
      }
      if (span.textContent !== pills[i]) span.textContent = pills[i];
    }
  }

  _updateKillFeed(kills) {
    if (!kills || kills.length === 0) return;

    const newKills = [];
    for (const kill of kills) {
      const key = `${kill.killer}-${kill.victim}-${kill.tick}`;
      if (this._seenKeys.has(key)) continue;
      this._seenKeys.add(key);
      newKills.push(kill);
    }

    for (let i = newKills.length - 1; i >= 0; i--) {
      const kill = newKills[i];
      const el = this._createKillEntry(kill);
      this.killfeedEl.prepend(el);
      this._activeEntries.unshift({ key: `${kill.killer}-${kill.victim}-${kill.tick}`, el });
    }

    // Bound the feed: long rounds otherwise grow the DOM one node per kill
    // until the between-round lobby reset. Keys stay in _seenKeys so entries
    // still inside the server's rolling kill_feed window can't re-add
    // (the set is cleared on every lobby reset).
    while (this._activeEntries.length > 30) {
      this._activeEntries.pop().el.remove();
    }
  }

  _createKillEntry(kill) {
    const el = document.createElement('div');
    el.className = 'killfeed-entry killfeed-new';

    const left = document.createElement('div');

    const killer = document.createElement('span');
    killer.className = 'killer';
    killer.textContent = kill.killer || '???';

    const sep = document.createElement('span');
    sep.style.color = 'var(--text-muted)';
    sep.textContent = ` ${this._weaponIcon(kill.weapon)} `;

    const victim = document.createElement('span');
    victim.className = 'victim';
    victim.textContent = kill.victim || '???';

    left.append(killer, sep, victim);

    const weapon = document.createElement('span');
    weapon.className = 'weapon';
    weapon.textContent = (kill.weapon || 'unknown').replace('_', ' ');

    el.append(left, weapon);
    return el;
  }

  /**
   * Collects taunt events from every broadcast (called unthrottled from
   * app.js so single-broadcast events survive the 200ms HUD throttle).
   */
  queueTaunts(events) {
    for (const ev of events) {
      if (!ev || ev.type !== 'taunt' || !ev.id || !ev.text) continue;
      if (this._seenTauntIds.has(ev.id)) continue;
      this._seenTauntIds.add(ev.id);
      if (this._seenTauntIds.size > 256) {
        const first = this._seenTauntIds.values().next();
        if (!first.done) this._seenTauntIds.delete(first.value);
      }
      this._pendingTaunts.push(ev);
    }
    // A taunt-heavy lobby must not grow the queue while the tab is hidden.
    while (this._pendingTaunts.length > 30) this._pendingTaunts.shift();
  }

  _updateTaunts(bots, currentTick) {
    if (this._pendingTaunts.length === 0) return;
    const names = new Map();
    for (const bot of bots) {
      if (bot && bot.bot_id) names.set(bot.bot_id, bot.name);
    }
    const pending = this._pendingTaunts;
    this._pendingTaunts = [];
    // Reverse iteration matches the kill feed convention (oldest-at-top
    // within one drained batch, both entry kinds read the same way).
    for (let i = pending.length - 1; i >= 0; i--) {
      const ev = pending[i];
      // Age gate: a tab hidden for minutes accumulates stale banter; only
      // taunts from the last ~6 seconds of ticks (10 Hz) are worth showing.
      // ev.tick > currentTick means the round rolled over; drop those too.
      if (typeof currentTick === 'number' && typeof ev.tick === 'number' &&
          (ev.tick > currentTick || currentTick - ev.tick > 60)) {
        continue;
      }
      const el = this._createTauntEntry(names.get(ev.owner_id) || '???', ev.text);
      this.killfeedEl.prepend(el);
      this._activeEntries.unshift({ key: 'taunt-' + ev.id, el });
    }
    while (this._activeEntries.length > 30) {
      this._activeEntries.pop().el.remove();
    }
  }

  _createTauntEntry(name, text) {
    const el = document.createElement('div');
    el.className = 'killfeed-entry killfeed-new killfeed-taunt';

    const left = document.createElement('div');
    const who = document.createElement('span');
    who.className = 'killer';
    who.textContent = name;
    const sep = document.createElement('span');
    sep.style.color = 'var(--text-muted)';
    sep.textContent = ' says ';
    const body = document.createElement('span');
    body.className = 'taunt-text';
    body.textContent = text;
    left.append(who, sep, body);

    el.append(left);
    return el;
  }

  _updateWaitingBots(waitingBots) {
    if (!waitingBots || waitingBots.length === 0) {
      if (this._lastLobbyHtml !== '') {
        this.lobbyEl.innerHTML = '<div style="color:var(--text-muted);padding:8px">No bots waiting</div>';
        this._lastLobbyHtml = '';
      }
      return;
    }

    const intro = `<div style="color:var(--text-muted);padding:4px 0;font-size:0.8rem">Joining next round (${waitingBots.length})</div>`;
    const html = intro + waitingBots.map((p) => this._playerCard({
      name: p.name,
      botId: '',
      avatarColor: p.avatar_color,
      weapon: p.weapon,
      status: 'Queued',
      selectable: false,
      pills: [p.weapon],
    })).join('');

    if (html !== this._lastLobbyHtml) {
      this.lobbyEl.innerHTML = html;
      this._lastLobbyHtml = html;
    }
  }

  resetKillFeed() {
    this._seenKeys.clear();
    this._activeEntries = [];
    this._seenTauntIds.clear();
    this._pendingTaunts = [];
    this.killfeedEl.innerHTML = '';
  }

  setStatus(status) {
    const connected = status === 'connected';
    this.statusEl.className = `ws-status${connected ? ' connected' : ''}`;
    this.statusEl.innerHTML = `<span class="dot"></span> ${connected ? 'Live' : this._esc(status)}`;

    const sitePill = document.getElementById('site-live-pill');
    if (sitePill) {
      sitePill.className = `site-live-pill${connected ? ' connected' : ''}`;
      sitePill.innerHTML = `<span class="dot"></span> ${connected ? 'Spectator live' : `Spectator ${this._esc(status)}`}`;
    }
  }

  setSelectedBot(botID) {
    this.selectedBotId = botID || null;
    // Force-refresh the highlight in place: toggle classes now, and clear
    // each row's signature so the next state update re-patches consistently.
    for (const [id, row] of this._playerRows) {
      row.el.classList.toggle('selected', this.selectedBotId === id);
      row.sig = '';
    }
  }

  _compactHudMarkup(label) {
    return `
      <button class="hud-compact-toggle" type="button" data-hud-toggle>
        <span class="hud-overline">Spectator HUD</span>
        <span class="hud-compact-title">${label}</span>
      </button>
    `;
  }

  _ensureCompactHud() {
    if (this._roundMode === 'compact') return;
    this._roundMode = 'compact';
    this.roundEl.classList.add('is-collapsed');
    this.roundEl.innerHTML = this._compactHudMarkup('Live Round');
    this._roundRefs = {
      compactTitle: this.roundEl.querySelector('.hud-compact-title'),
    };
  }

  _ensureLobbyHud() {
    if (this._roundMode === 'lobby') return;
    this._roundMode = 'lobby';
    this.roundEl.classList.remove('is-collapsed');
    this.roundEl.innerHTML = `
      <div class="hud-topline">
        <div class="hud-overline">Live Arena</div>
        <button class="hud-collapse-btn" type="button" data-hud-toggle>Hide</button>
      </div>
      <div class="hud-title-row">
        <div class="hud-title"></div>
        <div class="hud-phase"></div>
      </div>
      <div class="hud-metric-grid">
        <div class="hud-metric"><span class="hud-metric-label">Connected</span><span class="hud-metric-value" data-hud-m1></span></div>
        <div class="hud-metric"><span class="hud-metric-label">Countdown</span><span class="hud-metric-value" data-hud-m2></span></div>
        <div class="hud-metric"><span class="hud-metric-label">Needed</span><span class="hud-metric-value" data-hud-m3></span></div>
        <div class="hud-metric"><span class="hud-metric-label">State</span><span class="hud-metric-value" data-hud-m4></span></div>
      </div>
    `;
    this._captureRoundRefs();
  }

  _ensureRoundHud() {
    if (this._roundMode === 'round') return;
    this._roundMode = 'round';
    this.roundEl.classList.remove('is-collapsed');
    this.roundEl.innerHTML = `
      <div class="hud-topline">
        <div class="hud-overline">Spectator HUD</div>
        <button class="hud-collapse-btn" type="button" data-hud-toggle>Hide</button>
      </div>
      <div class="hud-title-row">
        <div class="hud-title"></div>
        <div class="hud-phase"></div>
      </div>
      <div class="hud-metric-grid">
        <div class="hud-metric"><span class="hud-metric-label">Tick</span><span class="hud-metric-value" data-hud-m1></span></div>
        <div class="hud-metric"><span class="hud-metric-label">Alive</span><span class="hud-metric-value" data-hud-m2></span></div>
        <div class="hud-metric"><span class="hud-metric-label">Zone Radius</span><span class="hud-metric-value" data-hud-m3></span></div>
        <div class="hud-metric"><span class="hud-metric-label">Pickups</span><span class="hud-metric-value" data-hud-m4></span></div>
      </div>
    `;
    this._captureRoundRefs();
  }

  _captureRoundRefs() {
    this._roundRefs = {
      title: this.roundEl.querySelector('.hud-title'),
      phase: this.roundEl.querySelector('.hud-phase'),
      metric1: this.roundEl.querySelector('[data-hud-m1]'),
      metric2: this.roundEl.querySelector('[data-hud-m2]'),
      metric3: this.roundEl.querySelector('[data-hud-m3]'),
      metric4: this.roundEl.querySelector('[data-hud-m4]'),
    };
  }

  _playerCard({ botId, name, avatarColor, weapon, status, selectable, dead, selected, pills }) {
    const classes = [
      'player-entry',
      selectable ? 'selectable' : '',
      dead ? 'dead' : '',
      selected ? 'selected' : '',
    ].filter(Boolean).join(' ');

    const attrs = botId ? ` data-bot-id="${this._esc(botId)}"` : '';
    const pillMarkup = (pills || []).map((pill) => `<span class="player-pill">${this._esc(pill)}</span>`).join('');

    return `
      <div class="${classes}"${attrs}>
        <div class="player-entry-main">
          <span class="player-entry-title">
            <span style="color:${this._esc(avatarColor || '#fff')}">\u25CF</span>
            <span class="player-entry-name">${this._esc(name)}</span>
          </span>
          <span class="player-entry-status">${this._esc(status)}</span>
        </div>
        <div class="player-entry-meta">${pillMarkup}</div>
      </div>
    `;
  }

  _weaponIcon(weapon) {
    const icons = {
      sword: '\u2694',
      bow: '\uD83C\uDFF9',
      daggers: '\uD83D\uDDE1',
      shield: '\uD83D\uDEE1',
      spear: '\uD83D\uDD31',
      staff: '\uD83E\uDE84',
      grapple: '\u26D3',
    };
    return icons[weapon] || '\u2620';
  }

  _esc(str) {
    const s = str == null ? '???' : String(str);
    return s.replace(/[&<>"']/g, (ch) => _ESC_MAP[ch]);
  }
}

const _ESC_MAP = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

// Shared scratch template for building one roster row at a time from
// _playerCard markup (parsed, never attached to the document itself).
const _ROW_TEMPLATE = document.createElement('template');
