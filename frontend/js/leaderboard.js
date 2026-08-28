'use strict';

/**
 * Leaderboard, bounty-board, and weapon-stats fetching, rendering, and tab switching.
 * @module leaderboard
 */

import { isEnabled } from './settings.js';
import { apiPath } from './paths.js?v=20260710a';

const SORT_OPTIONS = {
  elo: 'ELO Rating',
  kills: 'Most Kills',
  kd_ratio: 'K/D Ratio',
  streak: 'Best Streak',
};

const PERIOD_OPTIONS = {
  '1h': 'Last Hour',
  '24h': '24 Hours',
  '7d': '7 Days',
  '30d': '30 Days',
  all_time: 'All Time',
};

export async function fetchLeaderboard(sort = 'elo', limit = 50, period = 'all_time') {
  const resp = await fetch(`${apiPath('/leaderboard')}?sort=${sort}&limit=${limit}&period=${period}`);
  if (!resp.ok) throw new Error(`Leaderboard fetch failed: ${resp.status}`);
  return resp.json();
}

export async function fetchBountyBoard() {
  const resp = await fetch(apiPath('/bounties'));
  if (!resp.ok) throw new Error(`Bounty board fetch failed: ${resp.status}`);
  return resp.json();
}

export async function fetchWeaponStats() {
  const resp = await fetch(apiPath('/weapon-stats'));
  if (!resp.ok) throw new Error(`Weapon stats fetch failed: ${resp.status}`);
  return resp.json();
}

export function initLeaderboardWidget({
  root,
  modeTabsContainer,
  sortTabsContainer,
  podiumEl,
  leaderboardBody,
  bountyBody,
  weaponPodiumEl,
  weaponBody,
  weaponUpdatedEl,
  limit = 50,
}) {
  const state = {
    sort: 'elo',
    period: 'all_time',
    limit,
    refreshTimer: null,
    // Last-rendered payload signature per board: refreshes that return
    // identical data skip the DOM rebuild entirely.
    lastSig: { leaderboard: '', bounty: '', weapons: '' },
    // Set when a periodic refresh was skipped while the standings overlay
    // was closed (or the tab hidden), so reopening can refresh immediately.
    dirty: false,
    // The standings widget lives inside #standings-overlay; when the element
    // is missing (other embeds), treat the boards as always visible.
    overlayEl: document.getElementById('standings-overlay'),
  };

  buildSortTabs(state, sortTabsContainer, podiumEl, leaderboardBody);

  modeTabsContainer.querySelectorAll('button[data-board]').forEach((btn) => {
    btn.addEventListener('click', () => switchBoard(root, btn.dataset.board, modeTabsContainer));
  });

  // Force an immediate refresh when the overlay opens with stale data
  // (app.js toggles the 'open' class; we just watch for it).
  if (state.overlayEl) {
    new MutationObserver(() => {
      if (state.dirty && boardsVisible(state)) {
        state.dirty = false;
        refreshData(state, podiumEl, leaderboardBody, bountyBody, weaponPodiumEl, weaponBody, weaponUpdatedEl);
      }
    }).observe(state.overlayEl, { attributes: true, attributeFilter: ['class'] });
  }

  // Flush a dirty refresh when the tab becomes visible again with the
  // overlay already open (the MutationObserver only covers overlay reopen).
  document.addEventListener('visibilitychange', () => {
    if (state.dirty && boardsVisible(state)) {
      state.dirty = false;
      refreshData(state, podiumEl, leaderboardBody, bountyBody, weaponPodiumEl, weaponBody, weaponUpdatedEl);
    }
  });

  refreshData(state, podiumEl, leaderboardBody, bountyBody, weaponPodiumEl, weaponBody, weaponUpdatedEl);
  startAutoRefresh(state, podiumEl, leaderboardBody, bountyBody, weaponPodiumEl, weaponBody, weaponUpdatedEl);
}

/** True when refreshing the boards can produce something the user can see. */
function boardsVisible(state) {
  if (document.hidden) return false;
  return !state.overlayEl || state.overlayEl.classList.contains('open');
}

function buildSortTabs(state, sortTabsContainer, podiumEl, leaderboardBody) {
  sortTabsContainer.innerHTML = '';

  Object.entries(SORT_OPTIONS).forEach(([key, label]) => {
    const btn = document.createElement('button');
    btn.textContent = label;
    btn.dataset.sort = key;
    if (key === state.sort) btn.classList.add('active');
    btn.addEventListener('click', () => switchSort(state, key, sortTabsContainer, podiumEl, leaderboardBody));
    sortTabsContainer.appendChild(btn);
  });

  const sep = document.createElement('span');
  sep.textContent = '|';
  sep.style.cssText = 'color:var(--text-muted);margin:0 6px;opacity:0.3;align-self:center';
  sortTabsContainer.appendChild(sep);

  Object.entries(PERIOD_OPTIONS).forEach(([key, label]) => {
    const btn = document.createElement('button');
    btn.textContent = label;
    btn.dataset.period = key;
    if (key === state.period) btn.classList.add('active');
    btn.style.fontSize = '0.75rem';
    btn.addEventListener('click', () => switchPeriod(state, key, sortTabsContainer, podiumEl, leaderboardBody));
    sortTabsContainer.appendChild(btn);
  });
}

function switchSort(state, sort, tabsContainer, podiumEl, tbody) {
  state.sort = sort;
  tabsContainer.querySelectorAll('button[data-sort]').forEach((button) => {
    button.classList.toggle('active', button.dataset.sort === sort);
  });
  refreshLeaderboardOnly(state, podiumEl, tbody);
}

function switchPeriod(state, period, tabsContainer, podiumEl, tbody) {
  state.period = period;
  tabsContainer.querySelectorAll('button[data-period]').forEach((button) => {
    button.classList.toggle('active', button.dataset.period === period);
  });
  refreshLeaderboardOnly(state, podiumEl, tbody);
}

function switchBoard(root, board, modeTabsContainer) {
  modeTabsContainer.querySelectorAll('button[data-board]').forEach((button) => {
    button.classList.toggle('active', button.dataset.board === board);
  });

  root.querySelectorAll('.leaderboard-panel').forEach((panel) => {
    panel.classList.toggle('active', panel.dataset.board === board);
  });
}

async function refreshLeaderboardOnly(state, podiumEl, tbody) {
  try {
    const data = await fetchLeaderboard(state.sort, state.limit, state.period);
    renderLeaderboardIfChanged(data, podiumEl, tbody, state);
  } catch (err) {
    console.error('[Leaderboard] Fetch error:', err);
  }
}

async function refreshData(state, podiumEl, leaderboardBody, bountyBody, weaponPodiumEl, weaponBody, weaponUpdatedEl) {
  try {
    const [leaderboardData, bountyData, weaponStatsData] = await Promise.all([
      fetchLeaderboard(state.sort, state.limit, state.period),
      fetchBountyBoard(),
      fetchWeaponStats(),
    ]);

    renderLeaderboardIfChanged(leaderboardData, podiumEl, leaderboardBody, state);

    const bountySig = JSON.stringify(bountyData);
    if (bountySig !== state.lastSig.bounty) {
      state.lastSig.bounty = bountySig;
      renderBountyBoard(bountyData, bountyBody);
    }

    const weaponSig = JSON.stringify(weaponStatsData);
    if (weaponSig !== state.lastSig.weapons) {
      state.lastSig.weapons = weaponSig;
      renderWeaponStatsBoard(weaponStatsData, weaponPodiumEl, weaponBody, weaponUpdatedEl);
    }
  } catch (err) {
    console.error('[Leaderboard] Refresh error:', err);
  }
}

/** Rebuild the leaderboard table/podium only when the payload (or view) changed. */
function renderLeaderboardIfChanged(data, podiumEl, tbody, state) {
  const sig = `${state.sort}|${state.period}|${JSON.stringify(data)}`;
  if (sig === state.lastSig.leaderboard) return;
  state.lastSig.leaderboard = sig;
  renderLeaderboard(data, podiumEl, tbody, state);
}

function startAutoRefresh(state, podiumEl, leaderboardBody, bountyBody, weaponPodiumEl, weaponBody, weaponUpdatedEl) {
  if (state.refreshTimer) clearInterval(state.refreshTimer);
  state.refreshTimer = setInterval(() => {
    // No point fetching while the standings overlay is closed or the tab is
    // hidden; mark dirty so opening the overlay refreshes right away.
    if (!boardsVisible(state)) {
      state.dirty = true;
      return;
    }
    state.dirty = false;
    refreshData(
      state,
      podiumEl,
      leaderboardBody,
      bountyBody,
      weaponPodiumEl,
      weaponBody,
      weaponUpdatedEl,
    );
  }, 15000);
}

// Rank-change memory for standings motion: previous rank per bot, keyed to
// the sort/period the ranks came from so a tab switch never produces a wall
// of false flashes. Rebuilt each render (departed bots drop out naturally).
const _prevRanks = new Map();
let _prevRanksKey = '';

function renderLeaderboard(data, podiumEl, tbody, state) {
  tbody.innerHTML = '';
  const entries = data.entries || data.leaderboard || [];
  renderPodium(entries, podiumEl, state.sort);

  if (entries.length === 0) {
    const msg = state.period === 'all_time'
      ? 'No bots qualified yet - get your first kill!'
      : 'No data for this time period yet. Rounds need to complete to populate.';
    tbody.innerHTML = `<tr><td colspan="5" style="text-align:center;color:var(--text-muted);padding:24px">${msg}</td></tr>`;
    return;
  }

  const ranksKey = `${state.sort}|${state.period}`;
  const compare = ranksKey === _prevRanksKey;
  const nextRanks = new Map();

  entries.forEach((entry, index) => {
    const rank = entry.rank || index + 1;
    const statValue = getStatValue(entry, state.sort);
    const tr = document.createElement('tr');
    tr.innerHTML = `<td${rank <= 3 ? ` class="rank-${rank}"` : ''}>${getRankDisplay(rank)}</td>
      <td>${escapeHtml(entry.name)}</td>
      <td>${escapeHtml(statValue)}</td>
      <td>${escapeHtml(entry.deaths > 0 ? (entry.kills / entry.deaths).toFixed(1) : `${entry.kills}.0`)}</td>
      <td>${escapeHtml(entry.elo)}</td>`;
    if (rank <= 3) tr.className = `rank-${rank}`;
    // Motion classes only when the row's rank ACTUALLY changed since the
    // last refresh of the same view, so steady-state refreshes stay still.
    // classList.add appends (the top-3 className assignment above stays).
    if (compare && isEnabled('siteMotion', 'rankChangeFlash')) {
      const prev = _prevRanks.get(entry.name);
      if (prev === undefined && _prevRanks.size > 0) tr.classList.add('rank-new');
      else if (prev !== undefined && rank < prev) tr.classList.add('rank-up');
      else if (prev !== undefined && rank > prev) tr.classList.add('rank-down');
    }
    nextRanks.set(entry.name, rank);
    tbody.appendChild(tr);
  });

  _prevRanks.clear();
  for (const [k, v] of nextRanks) _prevRanks.set(k, v);
  _prevRanksKey = ranksKey;
}

function renderBountyBoard(data, tbody) {
  tbody.innerHTML = '';
  const entries = data.entries || [];
  if (entries.length === 0) {
    tbody.innerHTML = '<tr><td colspan="5" style="text-align:center;color:var(--text-muted);padding:24px">No active bounties yet - win consecutive rounds to light the board up.</td></tr>';
    return;
  }

  entries.forEach((entry, index) => {
    const rank = entry.rank || index + 1;
    const tr = document.createElement('tr');
    tr.innerHTML = `<td${rank <= 3 ? ` class="rank-${rank}"` : ''}>${getRankDisplay(rank)}</td>
      <td><span class="bounty-bot"><span class="bounty-dot" style="background:${escapeHtml(entry.avatar_color || '#fff')}"></span>${escapeHtml(entry.name)}</span></td>
      <td><span class="bounty-chip">${escapeHtml(entry.bounty_points)} pts</span></td>
      <td>${escapeHtml(entry.win_streak)}</td>
      <td>${entry.is_target ? '<span class="bounty-status live">LIVE TARGET</span>' : '<span class="bounty-status dormant">TRACKED</span>'}</td>`;
    if (rank <= 3) tr.className = `rank-${rank}`;
    tbody.appendChild(tr);
  });
}

function renderWeaponStatsBoard(data, podiumEl, tbody, updatedEl) {
  tbody.innerHTML = '';
  const entries = data.entries || [];
  renderWeaponPodium(entries, podiumEl);

  if (updatedEl) {
    updatedEl.textContent = `Updated ${formatUpdatedAt(data.updated_at)}`;
  }

  if (entries.length === 0) {
    tbody.innerHTML = '<tr><td colspan="10" style="text-align:center;color:var(--text-muted);padding:24px">Weapon telemetry is warming up. Complete a few rounds to populate the board.</td></tr>';
    return;
  }

  entries.forEach((entry) => {
    const tr = document.createElement('tr');
    tr.innerHTML = `<td><span class="weapon-tier tier-${escapeHtml(entry.tier)}">${escapeHtml(entry.tier)}</span></td>
      <td>
        <div class="weapon-name-cell">
          <strong>${escapeHtml(toTitleCase(entry.weapon))}</strong>
          <span>${escapeHtml(entry.special)} - ${escapeHtml(entry.rounds_tracked)} rounds tracked</span>
        </div>
      </td>
      <td>${formatScore(entry.meta_score)}</td>
      <td>${formatScore(entry.recent_form)}</td>
      <td>${renderBalanceSignal(entry.balance_direction, entry.recent_diff_pct, entry.recent_rounds, entry.history || [], entry.weapon)}</td>
      <td>${escapeHtml(entry.kills)}</td>
      <td>${escapeHtml(entry.kills_24h)}</td>
      <td>${renderWeaponStatValue(entry.damage_exact.toFixed(2), entry.last_damage_move || entry.damage_trend, entry.damage_shift_pct, 'damage', entry.history || [], entry.weapon, entry.base_damage, `base ${entry.base_damage}`)}</td>
      <td>${renderWeaponStatValue(`${entry.cooldown.toFixed(2)}s`, entry.last_cooldown_move || entry.cooldown_trend, entry.cooldown_shift_pct, 'cooldown', entry.history || [], entry.weapon, entry.base_cooldown, `base ${entry.base_cooldown.toFixed(2)}s`)}</td>
      <td>${escapeHtml(entry.grid_range)} tiles</td>`;
    tbody.appendChild(tr);
  });
}

function renderPodium(entries, container, sort) {
  if (!container) return;
  const top = entries.slice(0, 3);
  if (top.length === 0) {
    container.innerHTML = '';
    return;
  }

  container.innerHTML = top.map((entry, index) => {
    const rank = entry.rank || index + 1;
    const statValue = getStatValue(entry, sort);
    const kd = entry.deaths > 0 ? (entry.kills / entry.deaths).toFixed(1) : `${entry.kills}.0`;
    const accent = rank === 1 ? 'var(--accent-gold)' : rank === 2 ? '#d7dde6' : '#ffb57a';

    return `<article class="podium-card rank-${rank}" style="--podium-accent:${accent}">
      <div class="podium-rank">${getRankDisplay(rank)}</div>
      <div class="podium-name-row">
        <span class="podium-dot" style="background:${escapeHtml(entry.avatar_color || '#ffffff')}"></span>
        <strong>${escapeHtml(entry.name)}</strong>
      </div>
      <div class="podium-metric">
        <span class="podium-metric-label">${escapeHtml(SORT_OPTIONS[sort] || 'ELO Rating')}</span>
        <span class="podium-metric-value">${escapeHtml(statValue)}</span>
      </div>
      <div class="podium-substats">
        <span>K/D ${escapeHtml(kd)}</span>
        <span>ELO ${escapeHtml(entry.elo)}</span>
      </div>
    </article>`;
  }).join('');
}

function renderWeaponPodium(entries, container) {
  if (!container) return;
  const top = entries.slice(0, 3);
  if (top.length === 0) {
    container.innerHTML = '';
    return;
  }

  container.innerHTML = top.map((entry, index) => {
    const rank = entry.rank || index + 1;
    const accent = rank === 1 ? 'var(--accent-gold)' : rank === 2 ? '#d7dde6' : '#ffb57a';

    return `<article class="podium-card weapon-podium-card rank-${rank}" style="--podium-accent:${accent}">
      <div class="podium-rank">${escapeHtml(entry.tier)} Tier - ${getRankDisplay(rank)}</div>
      <div class="podium-name-row">
        <strong>${escapeHtml(toTitleCase(entry.weapon))}</strong>
      </div>
      <div class="podium-metric">
        <span class="podium-metric-label">Live Score</span>
        <span class="podium-metric-value">${formatScore(entry.meta_score)}</span>
      </div>
      <div class="podium-substats">
        <span>${escapeHtml(entry.kills)} kills</span>
        <span>${escapeHtml(entry.kills_24h)} in 24h</span>
        <span>${escapeHtml(entry.damage)} dmg</span>
      </div>
    </article>`;
  }).join('');
}

function getStatValue(entry, sort) {
  switch (sort) {
    case 'kills':
      return entry.kills;
    case 'kd_ratio': {
      const kd = entry.deaths > 0 ? entry.kills / entry.deaths : entry.kills;
      return kd.toFixed(2);
    }
    case 'streak':
      return entry.best_streak ?? entry.kills ?? 0;
    default:
      return entry.elo;
  }
}

function getRankDisplay(rank) {
  if (rank === 1) return '1ST';
  if (rank === 2) return '2ND';
  if (rank === 3) return '3RD';
  return `#${rank}`;
}

function formatUpdatedAt(value) {
  if (!value) return 'just now';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return 'just now';
  return date.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
}

function formatScore(value) {
  return Number(value || 0).toFixed(1);
}

function renderWeaponStatValue(value, trend, shiftPct, type, history = [], weapon = '', baseValue = null, baseLabel = '') {
  const trendClass = ` ${trendTone(trend, type)}`;
  const arrow = trendArrow(trend, type);
  const delta = Number(shiftPct || 0);
  const deltaLabel = Math.abs(delta) < 0.01 ? 'base' : `${delta > 0 ? '+' : ''}${delta.toFixed(1)}%`;
  const tooltipSide = type === 'damage' || type === 'cooldown' ? 'left' : 'right';
  return `<span class="weapon-stat-value${trendClass} tooltip-${tooltipSide}">
    <span class="weapon-stat-primary">
      <span class="weapon-stat-main">${value}</span>
      <span class="weapon-stat-trend">${arrow}</span>
      <span class="weapon-stat-delta">${deltaLabel}</span>
    </span>
    ${baseLabel ? `<span class="weapon-stat-base">${baseLabel}</span>` : ''}
    ${renderHistoryTooltip(history, type, weapon, baseValue)}
  </span>`;
}

function renderBalanceSignal(direction, diffPct, rounds, history = [], weapon = '') {
  const tone = direction === 'buffing' ? 'buffed' : direction === 'nerfing' ? 'nerfed' : 'flat';
  const label = direction === 'buffing' ? 'Buffing' : direction === 'nerfing' ? 'Nerfing' : 'Steady';
  const delta = Number(diffPct || 0);
  const deltaLabel = Math.abs(delta) < 0.01 ? 'vs mean' : `${delta > 0 ? '+' : ''}${delta.toFixed(1)}%`;
  const roundsLabel = rounds > 0 ? `${rounds}r` : 'cold';
  return `<span class="weapon-balance-signal ${tone} tooltip-right">
    <span class="weapon-balance-label">${label}</span>
    <span class="weapon-balance-delta">${deltaLabel}</span>
    <span class="weapon-balance-rounds">${roundsLabel}</span>
    ${renderHistoryTooltip(history, 'balance', weapon, 0)}
  </span>`;
}

function renderHistoryTooltip(history, metric, weapon, baselineValue = null) {
  if (!Array.isArray(history) || history.length < 1) return '';
  const title = metric === 'damage'
    ? `Damage across ${history.length} balance rounds`
    : metric === 'cooldown'
      ? `Cooldown across ${history.length} balance rounds`
      : `Balance vs mean across ${history.length} balance rounds`;
  const series = history.map((point) => {
    if (metric === 'damage') return Number(point.damage_exact || 0);
    if (metric === 'cooldown') return Number(point.cooldown || 0);
    return Number(point.diff_pct || 0);
  });
  const values = series.map((value) => Number.isFinite(value) ? value : 0);
  if (values.length === 1) values.push(values[0]);
  const hasBaseline = Number.isFinite(Number(baselineValue));
  const baseline = hasBaseline ? Number(baselineValue) : null;
  const extentValues = hasBaseline ? [...values, baseline] : values;
  const min = Math.min(...extentValues);
  const max = Math.max(...extentValues);
  const width = 216;
  const height = 72;
  const pad = 8;
  const rawSpan = max - min;
  const minSpan = metric === 'balance' ? 4 : metric === 'cooldown' ? 0.04 : 0.08;
  const span = Math.max(rawSpan, minSpan);
  let chartMin;
  let chartMax;
  if (hasBaseline) {
    const above = Math.max(0, max - baseline);
    const below = Math.max(0, baseline - min);
    const halfSpan = Math.max(minSpan / 2, above, below) * 1.15;
    chartMin = baseline - halfSpan;
    chartMax = baseline + halfSpan;
  } else {
    const center = (min + max) / 2;
    chartMin = center - span * 0.62;
    chartMax = center + span * 0.62;
  }
  const chartSpan = Math.max(minSpan, chartMax - chartMin);
  const step = values.length > 1 ? (width - pad * 2) / (values.length - 1) : 0;
  const points = values.map((value, index) => {
    const x = pad + index * step;
    const y = height - pad - ((value - chartMin) / chartSpan) * (height - pad * 2);
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(' ');
  const baselineYValue = hasBaseline ? baseline : 0;
  const baselineY = height - pad - ((baselineYValue - chartMin) / chartSpan) * (height - pad * 2);
  const latest = values[values.length - 1];
  const firstX = pad;
  const lastX = pad + step * (values.length - 1);
  const areaPoints = `${firstX.toFixed(1)},${(height - pad).toFixed(1)} ${points} ${lastX.toFixed(1)},${(height - pad).toFixed(1)}`;
  const latestX = pad + step * (values.length - 1);
  const latestY = height - pad - ((latest - chartMin) / chartSpan) * (height - pad * 2);
  const starting = values[0];
  return `<span class="weapon-history-tooltip" role="tooltip">
    <span class="weapon-history-title">${escapeHtml(toTitleCase(weapon))} · ${title}</span>
    <svg class="weapon-history-chart" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" aria-hidden="true">
      <line class="weapon-history-baseline" x1="${pad}" y1="${baselineY.toFixed(1)}" x2="${(width - pad)}" y2="${baselineY.toFixed(1)}" />
      <polygon class="weapon-history-area" points="${areaPoints}" />
      <polyline points="${points}" />
      <circle class="weapon-history-point" cx="${latestX.toFixed(1)}" cy="${latestY.toFixed(1)}" r="3.4" />
    </svg>
    <span class="weapon-history-meta">
      <span>${history.length} rounds</span>
      <span>${formatTooltipValue(metric, starting)} -> ${formatTooltipValue(metric, latest)}</span>
      ${hasBaseline ? `<span>Base ${formatTooltipValue(metric, baseline)}</span>` : ''}
      <span>Now ${formatTooltipValue(metric, latest)}</span>
    </span>
  </span>`;
}

function formatTooltipValue(metric, value) {
  if (metric === 'cooldown') return `${Number(value || 0).toFixed(2)}s`;
  if (metric === 'balance') return `${Number(value || 0).toFixed(2)}%`;
  return Number(value || 0).toFixed(2);
}

function trendArrow(trend, type) {
  if (type === 'cooldown') {
    if (trend === 'down') return '&darr;';
    if (trend === 'up') return '&uarr;';
    return '&rarr;';
  }
  if (trend === 'up') return '&uarr;';
  if (trend === 'down') return '&darr;';
  return '&rarr;';
}

function trendTone(trend, type) {
  if (trend === 'flat') return 'flat';
  if (type === 'cooldown') {
    return trend === 'down' ? 'buffed' : 'nerfed';
  }
  return trend === 'up' ? 'buffed' : 'nerfed';
}

function toTitleCase(value) {
  return String(value)
    .split(/[_\s-]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ');
}

const _ESC_MAP = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

function escapeHtml(str) {
  return String(str ?? '').replace(/[&<>"']/g, (ch) => _ESC_MAP[ch]);
}
