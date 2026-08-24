import WebSocket from 'ws';
import { distance, directionToward, directionAway } from './helpers.js';

const CONNECT_HANDSHAKE_TIMEOUT_MS = 15_000;
const RECONNECT_BACKOFF_RESET_MS = 30_000;

function serverConnectionError(message) {
  const error = new Error(message?.message || 'Arena connection rejected');
  if (message?.code) error.code = message.code;
  const retryAfter = Number(message?.details?.retry_after || 0);
  if (Number.isFinite(retryAfter) && retryAfter > 0) error.retryAfter = retryAfter;
  return error;
}

function serviceStatusFingerprint(status) {
  const canonicalize = (value) => {
    if (Array.isArray(value)) return value.map(canonicalize);
    if (value && typeof value === 'object') {
      return Object.fromEntries(
        Object.keys(value).sort().map((key) => [key, canonicalize(value[key])]),
      );
    }
    return value;
  };
  const semantic = { ...(status || {}) };
  delete semantic.revision;
  delete semantic.server_time;
  return JSON.stringify(canonicalize(semantic));
}

/**
 * Base class for AI Battle Arena bots.
 * Subclass and override {@link onTick} (required), plus optionally
 * {@link onMapInit}, {@link onDeath}, {@link onRespawn}, and {@link onRoundEnd}.
 */
export default class ArenaBot {
  /** @param {string} apiKey  @param {string} [serverUrl] */
  constructor(apiKey, serverUrl = 'wss://arena.angel-serv.com/ws/bot') {
    this.apiKey = apiKey;
    this.serverUrl = serverUrl;
    /** @type {WebSocket|null} */ this.ws = null;
    /** @type {string|null} */ this.botId = null;
    /** @type {object|null} */ this.serverInfo = null;
    this._weapon = 'sword';
    this._stats = { hp: 5, speed: 5, attack: 5, defense: 5 };
    this._fallback = 'aggressive';
    this._running = false;
    this._lastPos = [0, 0];
    this.serviceStatus = null;
    this._serviceStatusRevision = -1;
    this._serviceStatusFingerprint = null;
    this._maintenanceRetryUntil = 0;

    // Terrain cache (populated by map_init)
    /** @type {string[][]|null} */ this._terrain = null;
    /** @type {number} */ this._mapWidth = 0;
    /** @type {number} */ this._mapHeight = 0;
    /** @type {number} */ this._cellSize = 1;
    /** @type {object|null} */ this.mapInfo = null;
  }

  /**
   * Configure the loadout sent on connect.
   * @param {string} weapon  @param {object} stats  @param {string} [fallback]
   */
  setLoadout(weapon, stats, fallback = 'aggressive') {
    this._weapon = weapon;
    this._stats = stats;
    this._fallback = fallback;
  }

  // ── Override these ─────────────────────────────────────────────

  /** Called every tick. Must return an action object. */
  async onTick(state, nearby, safeZone) { throw new Error('Implement onTick()'); }
  /**
   * Called once at the start of each round with map data.
   * Default implementation stores the terrain grid.
   * @param {string[][]} terrain  row-major terrain grid
   * @param {number} width   map width in tiles
   * @param {number} height  map height in tiles
   */
  async onMapInit(terrain, width, height) {}
  /** @param {object} deathInfo */
  async onDeath(deathInfo) {}
  /** @param {object} respawnInfo */
  async onRespawn(respawnInfo) {}
  /** @param {object} roundInfo */
  async onRoundEnd(roundInfo) {}
  /** Called when a site broadcast or maintenance status changes. */
  async onServiceStatus(status) {}

  // ── Action helpers ─────────────────────────────────────────────

  /** Move toward a target position (returns grid direction with -1/0/1 components). */
  moveToward(myPos, targetPos) {
    return { action: 'move', direction: directionToward(myPos, targetPos) };
  }
  /** Move away from a threat position (returns grid direction with -1/0/1 components). */
  moveAway(myPos, threatPos) {
    return { action: 'move', direction: directionAway(myPos, threatPos) };
  }
  /** Move to an exact grid position [col, row]. */
  moveTo(targetPos) {
    return { action: 'move_to', target_position: [targetPos[0], targetPos[1]] };
  }
  /**
   * Attack a target by ID. The server accepts exactly one aim mode per
   * attack: for staff weapons, pass targetPosition [col, row] instead of
   * targetId (equivalent to staffAttack()) - targetId is ignored when
   * targetPosition is given.
   */
  attack(targetId, targetPosition) {
    if (targetPosition) return { action: 'attack', target_position: [targetPosition[0], targetPosition[1]] };
    return { action: 'attack', target: targetId };
  }
  /** Staff area attack at a position [col, row]. */
  staffAttack(targetPosition) {
    return { action: 'attack', target_position: [targetPosition[0], targetPosition[1]] };
  }
  /** Shove a target — knocks them back far with a short stun, no damage. */
  shove(targetId) { return { action: 'shove', target: targetId }; }
  /** Dodge in a direction. */
  dodge(direction) { return { action: 'dodge', direction }; }
  /** Use an item by ID. */
  useItem(itemId) { return { action: 'use_item', item_id: itemId }; }
  /** Place a landmine at current position (max 3 per bot, arms after delay). */
  placeMine() { return { action: 'place_mine' }; }
  /** Deploy a gravity well at a target position [col, row]. Requires a charge from pickup. */
  useGravityWell(targetPosition) {
    return { action: 'use_gravity_well', target_position: [targetPosition[0], targetPosition[1]] };
  }
  /** Grapple a target bot (universal ability: 2 charges/round, 12-tile range). */
  grapple(targetId) { return { action: 'grapple', target: targetId }; }
  /** Do nothing this tick. */
  idle() { return { action: 'idle' }; }

  // ── Query helpers ──────────────────────────────────────────────

  /** Find the closest enemy bot in the nearby list. */
  closestEnemy(nearby) {
    const bots = (nearby || []).filter((e) => e.type === 'bot' && (e.id || e.bot_id) !== this.botId);
    if (!bots.length) return null;
    const myPos = this._lastPos;
    return bots.reduce((best, b) =>
      distance(myPos, b.position) < distance(myPos, best.position) ? b : best);
  }

  /** Find the enemy bot with the lowest HP. */
  lowestHpEnemy(nearby) {
    const bots = (nearby || []).filter((e) => e.type === 'bot' && (e.id || e.bot_id) !== this.botId);
    if (!bots.length) return null;
    return bots.reduce((best, b) =>
      (b.hp ?? Infinity) < (best.hp ?? Infinity) ? b : best);
  }

  /** Return nearby pickups sorted by distance (closest first). */
  nearbyPickups(nearby) {
    const myPos = this._lastPos;
    return (nearby || [])
      .filter((e) => e.type === 'pickup')
      .sort((a, b) => distance(myPos, a.position) - distance(myPos, b.position));
  }

  // ── Map / pathfinding helpers ──────────────────────────────────

  /**
   * Build an ASCII view of the local area around the bot.
   * Merges cached terrain with entity positions.
   * Legend: '@' = self, 'B' = bot, 'P' = pickup, '.' = ground, '#' = wall
   * @param {object} state      your_state from tick
   * @param {object[]} nearby   nearby_entities from tick
   * @param {number} [radius=5] view radius in tiles
   * @returns {string[]}        array of strings (one per row)
   */
  getLocalMap(state, nearby, radius = 5) {
    const [myCol, myRow] = state.position;
    const size = radius * 2 + 1;
    const rows = [];

    for (let dr = -radius; dr <= radius; dr++) {
      let row = '';
      for (let dc = -radius; dc <= radius; dc++) {
        const c = myCol + dc;
        const r = myRow + dr;
        if (dc === 0 && dr === 0) {
          row += '@';
        } else if (this._terrain && r >= 0 && r < this._mapHeight && c >= 0 && c < this._mapWidth) {
          row += this._terrain[r][c];
        } else {
          row += ' ';
        }
      }
      rows.push(row);
    }

    // Overlay entities
    for (const ent of (nearby || [])) {
      if (!ent.position) continue;
      const [ec, er] = ent.position;
      const dc = ec - myCol + radius;
      const dr = er - myRow + radius;
      if (dc < 0 || dc >= size || dr < 0 || dr >= size) continue;
      if (dc === radius && dr === radius) continue; // skip self position
      const ch = ent.type === 'bot' ? 'B' : ent.type === 'pickup' ? 'P' : '?';
      const line = rows[dr];
      rows[dr] = line.substring(0, dc) + ch + line.substring(dc + 1);
    }

    return rows;
  }

  /**
   * A* pathfinding on the cached terrain grid.
   * Walls ('#') and void ('V') are impassable.
   * @param {number[]} start  [col, row]
   * @param {number[]} goal   [col, row]
   * @returns {number[][]}    array of [col, row] waypoints (excluding start, including goal), or [] if no path
   */
  findPath(start, goal) {
    if (!this._terrain) return [];
    const [sc, sr] = start;
    const [gc, gr] = goal;
    if (sc === gc && sr === gr) return [];

    const w = this._mapWidth;
    const h = this._mapHeight;

    const isPassable = (c, r) => {
      if (c < 0 || c >= w || r < 0 || r >= h) return false;
      const cell = this._terrain[r][c];
      return cell !== '#' && cell !== 'V';
    };

    if (!isPassable(gc, gr)) return [];

    // Chebyshev heuristic
    const heuristic = (c, r) => Math.max(Math.abs(c - gc), Math.abs(r - gr));

    const key = (c, r) => r * w + c;
    const gScore = new Map();
    const cameFrom = new Map();
    gScore.set(key(sc, sr), 0);

    // Simple priority queue using sorted array
    const open = [{ c: sc, r: sr, f: heuristic(sc, sr) }];
    const closed = new Set();

    const DIRS = [
      [-1, -1], [0, -1], [1, -1],
      [-1,  0],          [1,  0],
      [-1,  1], [0,  1], [1,  1],
    ];

    while (open.length > 0) {
      // Find node with lowest f
      let bestIdx = 0;
      for (let i = 1; i < open.length; i++) {
        if (open[i].f < open[bestIdx].f) bestIdx = i;
      }
      const current = open[bestIdx];
      open.splice(bestIdx, 1);

      const ck = key(current.c, current.r);
      if (closed.has(ck)) continue;
      closed.add(ck);

      if (current.c === gc && current.r === gr) {
        // Reconstruct path
        const path = [];
        let k = ck;
        while (cameFrom.has(k)) {
          const r = Math.floor(k / w);
          const c = k % w;
          path.push([c, r]);
          k = cameFrom.get(k);
        }
        path.reverse();
        return path;
      }

      const currentG = gScore.get(ck);

      for (const [dc, dr] of DIRS) {
        const nc = current.c + dc;
        const nr = current.r + dr;
        if (!isPassable(nc, nr)) continue;
        const nk = key(nc, nr);
        if (closed.has(nk)) continue;

        const tentG = currentG + 1;
        const prevG = gScore.get(nk);
        if (prevG !== undefined && tentG >= prevG) continue;

        gScore.set(nk, tentG);
        cameFrom.set(nk, ck);
        open.push({ c: nc, r: nr, f: tentG + heuristic(nc, nr) });
      }
    }

    return []; // no path found
  }

  // ── Connection ─────────────────────────────────────────────────

  /** Open the WebSocket, send loadout, and wait for confirmation. */
  async connect() {
    const url = `${this.serverUrl}?key=${this.apiKey}`;
    return new Promise((resolve, reject) => {
      let ready = false;
      let settled = false;
      const finish = (callback, value) => {
        if (settled) return;
        settled = true;
        clearTimeout(handshakeTimer);
        callback(value);
      };
      const readyConnection = () => {
        ready = true;
        finish(resolve);
      };
      const rejectConnection = (error) => finish(reject, error);
      const pendingMessages = [];
      let processingMessages = false;
      let closed = false;
      let handshakeTimer;

      const handleMessage = async (msg) => {
        if (closed || this.ws !== socket || (settled && !ready)) return;
        if (!ready && msg.type === 'error') {
          const error = serverConnectionError(msg);
          if (error.retryAfter) {
            this._maintenanceRetryUntil = Math.max(
              this._maintenanceRetryUntil,
              Date.now() + error.retryAfter * 1000,
            );
          }
          rejectConnection(error);
          socket.close(1000, 'handshake rejected');
          return;
        }
        try {
          await this._handleMessage(msg, readyConnection, socket);
        } catch (err) {
          console.error('[ArenaBot] Handler error:', err.message);
          if (!ready) rejectConnection(err);
        }
      };

      const drainMessages = async () => {
        if (processingMessages) return;
        processingMessages = true;
        try {
          while (!closed && this.ws === socket && pendingMessages.length > 0) {
            const msg = pendingMessages.shift();
            await handleMessage(msg);
          }
        } finally {
          processingMessages = false;
        }
      };

      const enqueueMessage = (msg) => {
        if (closed || this.ws !== socket) return;
        // An action for an older tick has no value after a newer tick arrives.
        // Keep lifecycle messages ordered, but collapse adjacent queued ticks so
        // a slow agent callback cannot later flush a punitive action burst.
        const last = pendingMessages[pendingMessages.length - 1];
        if (msg.type === 'tick' && last?.type === 'tick') {
          pendingMessages[pendingMessages.length - 1] = msg;
        } else {
          pendingMessages.push(msg);
        }
        void drainMessages();
      };

      const socket = new WebSocket(url);
      this.ws = socket;
      handshakeTimer = setTimeout(() => {
        const error = new Error('Arena connection timed out before loadout confirmation');
        error.code = 'HANDSHAKE_TIMEOUT';
        rejectConnection(error);
        socket.close(1000, 'handshake timeout');
      }, CONNECT_HANDSHAKE_TIMEOUT_MS);
      socket.on('open', () => console.log('[ArenaBot] Connected'));
      socket.on('message', async (raw) => {
        let msg;
        try { msg = JSON.parse(raw); } catch { return; }
        enqueueMessage(msg);
      });
      socket.on('error', (err) => {
        console.error('[ArenaBot] WebSocket error:', err.message);
        rejectConnection(err);
      });
      socket.on('close', (code, reason) => {
        closed = true;
        pendingMessages.length = 0;
        const closeReason = reason?.length ? `, reason=${reason.toString()}` : '';
        console.log(`[ArenaBot] Disconnected (code=${code}${closeReason})`);
        if (!ready) {
          const suffix = reason?.length ? `: ${reason.toString()}` : '';
          const error = new Error(`Arena connection closed before loadout confirmation (code=${code})${suffix}`);
          error.code = 'CONNECTION_CLOSED';
          rejectConnection(error);
        }
      });
    });
  }

  /** @private Wait for the current ready connection to close. */
  async _waitForDisconnect() {
    const socket = this.ws;
    if (!socket || socket.readyState !== WebSocket.OPEN) return;
    await new Promise((resolve) => socket.once('close', resolve));
  }

  /** Fetch the current or pre-generated arena map over REST. */
  async fetchMap() {
    const base = this.serverUrl
      .replace(/^wss:/, 'https:')
      .replace(/^ws:/, 'http:')
      .split('/ws/')[0];
    try {
      const response = await fetch(`${base}/api/v1/arena/map`, {
        signal: AbortSignal.timeout(CONNECT_HANDSHAKE_TIMEOUT_MS / 3),
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json();
      if (data.status !== 'ok' || !Array.isArray(data.terrain)) return false;

      const terrain = data.terrain.map((row) => (
        typeof row === 'string' ? row.split('') : row
      ));
      this._terrain = terrain;
      this._mapWidth = Number(data.width || 0);
      this._mapHeight = Number(data.height || 0);
      this._cellSize = Number(data.cell_size || 1);
      this.mapInfo = { ...data, terrain };
      await this.onMapInit(terrain, this._mapWidth, this._mapHeight);
      return true;
    } catch (error) {
      console.warn(`[ArenaBot] Map fetch failed: ${error.message}`);
      return false;
    }
  }

  /** @private */
  async _handleMessage(msg, onReady, socket = this.ws) {
    switch (msg.type) {
      case 'connected':
        this.botId = msg.bot_id;
        this.serverInfo = msg;
        console.log(`[ArenaBot] Bot ID: ${this.botId}`);
        this._send({
          type: 'select_loadout', weapon: this._weapon,
          stats: this._stats, fallback_behavior: this._fallback,
        }, socket);
        if (msg.service_status) await this._handleServiceStatus(msg.service_status);
        break;
      case 'loadout_confirmed':
        console.log(`[ArenaBot] Loadout confirmed: ${msg.weapon}`);
        await this.fetchMap();
        if (onReady && this.ws === socket && socket?.readyState === WebSocket.OPEN) onReady();
        break;
      case 'map_init':
        // Normalise compact row-string format to 2D char array
        if (msg.terrain && msg.terrain.length > 0 && typeof msg.terrain[0] === 'string') {
          this._terrain = msg.terrain.map(row => row.split(''));
        } else {
          this._terrain = msg.terrain;
        }
        this._mapWidth = msg.width;
        this._mapHeight = msg.height;
        this._cellSize = msg.cell_size || 1;
        console.log(`[ArenaBot] Map loaded: ${msg.width}x${msg.height} (cell_size=${this._cellSize})`);
        await this.onMapInit(this._terrain, msg.width, msg.height);
        break;
      case 'round_start':
        // Intermission exposes the next terrain early; fetch again once the
        // round starts to receive pads, hazards, objectives, and game mode.
        await this.fetchMap();
        break;
      case 'tick': {
        if (msg.service_status) await this._handleServiceStatus(msg.service_status);
        const st = msg.your_state || {};
        if (st.position) this._lastPos = st.position;
        this.lastActionResult = st.last_action_result || null;
        // Team number in team-based game modes (0 = no team / FFA).
        this.team = st.team || 0;
        const safeZone = {
          center: st.zone_center || [0, 0],
          radius: st.zone_radius ?? 100,
          in_safe_zone: st.in_safe_zone ?? true,
          distance_to_edge: st.distance_to_zone_edge || 0,
        };
        if (msg.fog_radius !== undefined) {
          safeZone.fog_radius = msg.fog_radius;
        }
        // The server keeps sending state snapshots after death so clients can
        // observe the round. Do not run agent logic or submit actions until a
        // later round explicitly marks the bot alive again.
        if (st.is_alive !== true) break;
        const action = await this.onTick(st, msg.nearby_entities, safeZone);
        if (action && this.ws === socket) {
          this._send({ type: 'action', tick: msg.tick_number, ...action }, socket);
        }
        break;
      }
      case 'death':
        console.log(`[ArenaBot] Died — killed by ${msg.killed_by}`);
        await this.onDeath(msg);
        break;
      case 'respawn':
        console.log('[ArenaBot] Respawned');
        await this.onRespawn(msg);
        break;
      case 'round_end':
        console.log(`[ArenaBot] Round ${msg.round_number} ended`);
        await this.onRoundEnd(msg);
        break;
      case 'service_status':
        await this._handleServiceStatus(msg);
        break;
      case 'error':
        console.error(`[ArenaBot] Server error: ${msg.message}`);
        break;
      case 'kick':
        console.error(`[ArenaBot] Kicked: ${msg.reason}`);
        socket?.close();
        break;
    }
  }

  /** @private */
  _send(data, socket = this.ws) {
    if (socket?.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify(data));
    }
  }

  /** @private */
  async _handleServiceStatus(status) {
    const revision = Number(status?.revision || 0);
    if (revision < this._serviceStatusRevision) return;
    const fingerprint = serviceStatusFingerprint(status);
    if (revision === this._serviceStatusRevision && fingerprint === this._serviceStatusFingerprint) return;
    this._serviceStatusRevision = revision;
    this._serviceStatusFingerprint = fingerprint;
    this.serviceStatus = status;
    const maintenance = status?.maintenance || null;
    const retryAfter = Number(maintenance?.retry_after_seconds || 0);
    this._maintenanceRetryUntil = retryAfter > 0 ? Date.now() + retryAfter * 1000 : 0;
    if (maintenance) console.warn(`[ArenaBot] Maintenance: ${maintenance.message || 'server restarting'}`);
    try {
      await this.onServiceStatus(status);
    } catch (error) {
      console.error(`[ArenaBot] onServiceStatus error: ${error.message}`);
    }
  }

  /** Run the bot with automatic reconnection (exponential backoff). */
  async run() {
    this._running = true;
    let delay = 1000;
    while (this._running) {
      let connectedAt = null;
      try {
        await this.connect();
        connectedAt = Date.now();
        await this._waitForDisconnect();
      } catch { /* connection failed */ }
      if (!this._running) break;
      if (connectedAt !== null && Date.now() - connectedAt >= RECONNECT_BACKOFF_RESET_MS) {
        delay = 1000;
      }
      const maintenanceDelay = Math.max(0, this._maintenanceRetryUntil - Date.now());
      const waitFor = Math.max(delay, maintenanceDelay);
      console.log(`[ArenaBot] Reconnecting in ${Math.ceil(waitFor / 1000)}s...`);
      await new Promise((r) => setTimeout(r, waitFor));
      delay = Math.min(delay * 2, 30000);
    }
  }
}
