"use strict";

// ---- small formatting helpers -------------------------------------------

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function fmtTokens(n) {
  n = n || 0;
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
  return String(n);
}

function fmtCost(v) {
  if (v === null || v === undefined) return "-";
  return "$" + v.toFixed(4);
}

function fmtBytes(n) {
  n = n || 0;
  if (n >= 1e9) return (n / 1e9).toFixed(1) + " GB";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + " MB";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + " KB";
  return n + " B";
}

// isUnpriced is the one place "this call has no cost" is decided — a null
// CostUSD, not a zero one, since a free/zero-cost call is still priced.
function isUnpriced(costUSD) {
  return costUSD === null || costUSD === undefined;
}

// costBadge is the "?" an unpriced call carries, with the reason in the
// tooltip. Meaning is never colour-only: the glyph is a literal "?" and the
// title names the source, so the badge still reads in monochrome and to a
// screen reader. It reuses .badge.warn rather than adding a class, since both
// badges mean the same thing to the reader — this row needs attention.
function costBadge(source) {
  if (!source || source === "configured") return "";
  const reason = `cost ${escapeHtml(source)} — no configured price for this call (lens prices --set)`;
  return ` <span class="badge warn" title="${reason}" aria-label="${reason}">?</span>`;
}

// warnBadge is the "⚠" a row carries when an analyzer raised something on it,
// with the count when the caller has one. Same rule as costBadge: the glyph
// alone is decoration, so the title says what it counts and where to read the
// detail — on hover and to a screen reader.
function warnBadge(count, hint) {
  const what = count ? `${count} warning${count === 1 ? "" : "s"}` : "Warnings";
  const label = `${what} — ${hint}`;
  return `<span class="badge warn" title="${escapeHtml(label)}" aria-label="${escapeHtml(label)}">&#9888;${count ? " " + count : ""}</span>`;
}

function costCell(costUSD, source) {
  return fmtCost(costUSD) + costBadge(source);
}

// fmtCostTotal is the mixed-pricing rule: a sum that covers only some of the
// calls says so. Presenting a partial total as the whole bill is the one
// failure mode this feature exists to prevent.
function fmtCostTotal(v, unpriced) {
  if (unpriced > 0) return fmtCost(v) + " + " + unpriced + " unpriced";
  return fmtCost(v);
}

function fmtDurationNs(ns) {
  if (!ns) return "0ms";
  const ms = ns / 1e6;
  if (ms < 1000) return ms.toFixed(0) + "ms";
  return (ms / 1000).toFixed(2) + "s";
}

// fmtSpanMs formats a span of wall-clock time (a session's length), which
// runs to minutes and hours where a single call's duration runs to
// milliseconds — hence its own helper rather than fmtDurationNs.
function fmtSpanMs(ms) {
  if (!ms || ms < 0) return "0ms";
  if (ms < 1000) return ms.toFixed(0) + "ms";
  if (ms < 60000) return (ms / 1000).toFixed(1) + "s";
  if (ms < 3600000) return Math.floor(ms / 60000) + "m";
  return (ms / 3600000).toFixed(1) + "h";
}

function fmtTime(iso) {
  if (!iso) return "-";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleTimeString();
}

function relTime(iso) {
  if (!iso) return "-";
  const d = new Date(iso).getTime();
  if (isNaN(d)) return iso;
  const s = Math.max(0, Math.floor((Date.now() - d) / 1000));
  if (s < 60) return s + "s ago";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}

function sevBadge(sev) {
  const s = (sev || "info").toLowerCase();
  const cls = s.indexOf("err") >= 0 ? "sev-error" : s.indexOf("warn") >= 0 ? "sev-warn" : "sev-info";
  return `<span class="sev ${cls}">${escapeHtml(sev || "info")}</span>`;
}

// fetchRaw is the one place the response is checked, so the paged and the
// unpaged paths cannot drift into two different ideas of what a failed request
// looks like.
async function fetchRaw(url) {
  const res = await fetch(url);
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error || msg; } catch (e) { /* ignore */ }
    throw new Error(`${url}: ${res.status} ${msg}`);
  }
  return res;
}

async function fetchJSON(url) {
  return (await fetchRaw(url)).json();
}

// fetchPage reads a list endpoint's pagination metadata off the response
// headers. The body stays a bare array — the headers are what carry the total
// and the applied window — so this returns both rather than the body becoming
// an envelope. items is defaulted because the store encodes an empty result
// as null, which would otherwise blow up every caller's .map.
async function fetchPage(url) {
  const res = await fetchRaw(url);
  const items = await res.json();
  const hdr = (name) => {
    const n = parseInt(res.headers.get(name), 10);
    return Number.isFinite(n) ? n : 0;
  };
  return {
    items: items || [],
    total: hdr("X-Total-Count"),
    limit: hdr("X-Limit"),
    offset: hdr("X-Offset"),
  };
}

// ---- app state ------------------------------------------------------------

const state = {
  paused: false,
  totals: { calls: 0, tokens: 0, cost: 0, unpriced: 0 },
  warnedIds: new Set(),
  // warningsDebounceTimer coalesces a burst of SSE warning events into one
  // summary refetch; warningsFetchSeq is the last-issued-wins guard over
  // loadWarnings. See both at their use sites.
  warningsDebounceTimer: null,
  warningsFetchSeq: 0,
  // warningDetailPage is the warnings drill-down's window, and
  // warningDetailFetchSeq its last-issued-wins guard. Same shape as the
  // sessionsPage/sessionsFetchSeq pair below; the limit starts at a PAGE_SIZES
  // option for the same reason.
  warningDetailPage: { limit: 50, offset: 0 },
  warningDetailFetchSeq: 0,
  feedRowLimit: 200,
  // sessionsPage is the sessions table's window. The limit starts at one of
  // PAGE_SIZES so the select's initial value and the first request's ?limit=
  // are the same number by construction — they cannot diverge.
  sessionsPage: { limit: 50, offset: 0 },
  // sessionsFetchSeq is the last-issued-wins guard over loadSessions; see the
  // comment there.
  sessionsFetchSeq: 0,
  // replayEnabled mirrors /api/health's replay_enabled. The detail view offers
  // the replay editor only when it is true: the endpoint answers 403 when
  // replay is off, so the button is inert rather than offered and broken.
  replayEnabled: false,
  // settingsPrices is GET/POST /api/prices's last-known-good response body —
  // the source both the table render and an edit's "whole row" POST read
  // from, so a rejected edit has something to revert to that is not the
  // value that was just rejected. settingsPriceDirty is the one flag R7
  // asks for: true from the moment a cell is opened for editing until the
  // save that follows it resolves successfully.
  settingsPrices: null,
  settingsPriceDirty: false,
  // settingsRetention is GET /api/retention's last-known response — the
  // confirm steps read their numbers from here rather than re-fetching, so
  // "the numbers on the confirm step are the preview's" (br-GI-17-10) holds
  // even if the preview is a moment old.
  settingsRetention: null,
  // statsMetric / statsByPeriod / statsGranularity are the Stats tab's
  // retained view. The metric toggle re-renders the chart from the rows
  // already fetched — all three metrics share one request — and loadStats()
  // is re-entered on every visit to the tab and after a purge, neither of
  // which should reset the user's chosen metric.
  statsMetric: "count",
  statsByPeriod: [],
  statsGranularity: "day",
};

function bumpTotals(req) {
  state.totals.calls += 1;
  state.totals.tokens += (req.InputTokens || 0) + (req.OutputTokens || 0);
  addCost(req);
  renderTotals();
}

// addCost folds one request into the running total. An unpriced call is
// counted, not added as zero: `cost += req.CostUSD || 0` is how a header
// total quietly claims to cover calls it never priced.
function addCost(req) {
  if (isUnpriced(req.CostUSD)) {
    state.totals.unpriced += 1;
  } else {
    state.totals.cost += req.CostUSD;
  }
}

function renderTotals() {
  document.getElementById("total-calls").textContent = state.totals.calls;
  document.getElementById("total-tokens").textContent = fmtTokens(state.totals.tokens);
  document.getElementById("total-cost").textContent = fmtCostTotal(state.totals.cost, state.totals.unpriced);
}

// ---- tab navigation ---------------------------------------------------

const views = ["feed", "warnings", "stats", "sessions", "settings"];
function showView(name) {
  for (const v of views) {
    document.getElementById("view-" + v).hidden = v !== name;
  }
  for (const btn of document.querySelectorAll(".tab")) {
    btn.classList.toggle("active", btn.dataset.view === name);
  }
  if (name === "warnings") {
    // A still-pending debounce timer would otherwise fire a second
    // loadWarnings immediately after this one. This does NOT make the fetch
    // single-flight — clearTimeout is a no-op on a timer that already fired,
    // so a fetch in flight can still overlap this one. The generation counter
    // inside loadWarnings is what covers that.
    clearTimeout(state.warningsDebounceTimer);
    state.warningsDebounceTimer = null;
    loadWarnings();
  }
  if (name === "stats") loadStats();
  if (name === "sessions") loadSessions();
  if (name === "settings") loadSettings();
}

// reloadDataViews re-fetches the feed, Stats and Sessions data after a
// purge (R5) — those three describe rows that may no longer exist, and the
// cheapest correct thing is to re-ask for what could be on screen next,
// rather than inventing a new SSE event type to announce a delete.
function reloadDataViews() {
  loadInitialFeed();
  loadStats();
  loadSessions();
}

document.getElementById("tabs").addEventListener("click", (e) => {
  const btn = e.target.closest(".tab");
  if (btn) showView(btn.dataset.view);
});

// ---- live feed ----------------------------------------------------------

function feedRowHTML(req) {
  const warned = state.warnedIds.has(req.ID);
  const model = req.ModelResolved || req.ModelRequested || "-";
  return `<tr data-id="${req.ID}">
    <td>${fmtTime(req.StartedAt)}</td>
    <td>${fmtDurationNs(req.Duration)}</td>
    <td>${escapeHtml(model)}</td>
    <td>${fmtTokens(req.InputTokens)}/${fmtTokens(req.OutputTokens)}</td>
    <td>${costCell(req.CostUSD, req.CostSource)}</td>
    <td>${req.Status}</td>
    <td>${warned ? warnBadge(0, "click the call for detail") : ""}</td>
  </tr>`;
}

function prependFeedRow(req) {
  const body = document.getElementById("feed-body");
  body.insertAdjacentHTML("afterbegin", feedRowHTML(req));
  while (body.rows.length > state.feedRowLimit) {
    const last = body.rows[body.rows.length - 1];
    state.warnedIds.delete(Number(last.dataset.id));
    body.deleteRow(body.rows.length - 1);
  }
}

function markFeedRowWarned(id) {
  state.warnedIds.add(id);
  const row = document.querySelector(`#feed-body tr[data-id="${id}"]`);
  if (row) {
    const cell = row.cells[row.cells.length - 1];
    cell.innerHTML = warnBadge(0, "click the call for detail");
  }
}

document.getElementById("feed-body").addEventListener("click", (e) => {
  const row = e.target.closest("tr[data-id]");
  if (row) openDetail(Number(row.dataset.id));
});

document.getElementById("pause-btn").addEventListener("click", () => {
  state.paused = !state.paused;
  const btn = document.getElementById("pause-btn");
  const status = document.getElementById("feed-status");
  btn.textContent = state.paused ? "Resume" : "Pause";
  status.textContent = state.paused ? "paused" : "live";
  status.classList.toggle("paused", state.paused);
});

async function loadInitialFeed() {
  try {
    const reqs = await fetchJSON("/api/requests?limit=50");
    const body = document.getElementById("feed-body");
    body.innerHTML = reqs.map(feedRowHTML).join("");
    state.totals = { calls: 0, tokens: 0, cost: 0, unpriced: 0 };
    for (const r of reqs) {
      state.totals.calls += 1;
      state.totals.tokens += (r.InputTokens || 0) + (r.OutputTokens || 0);
      addCost(r);
    }
    renderTotals();
  } catch (e) {
    console.error("loadInitialFeed", e);
  }
}

// ---- warnings inbox -------------------------------------------------------

// The counts come from the server (store.WarningSummary, via
// /api/warnings/summary) rather than from grouping a page of rows here.
// Grouping and pagination do not compose: a page of N rows cannot yield a
// correct global per-kind count, so the old client-side version undercounted
// every kind that fired more than the fetch cap.
async function loadWarnings() {
  // Last-issued-wins. A debounce fire and a tab switch can each have a fetch
  // in flight, and the earlier one can land last.
  const seq = ++state.warningsFetchSeq;
  try {
    const groups = await fetchJSON("/api/warnings/summary");
    if (seq !== state.warningsFetchSeq) return;
    renderWarningGroups(groups || []);
  } catch (e) {
    console.error("loadWarnings", e);
  }
}

// WarningGroup carries no json tags — the repo's convention for types
// crossing the API/SSE broker — so the served keys are the Go field names.
// Reading the lowercase names here still compiles and still runs; it just
// renders every cell undefined.
function renderWarningGroups(groups) {
  const body = document.getElementById("warnings-groups");
  body.innerHTML = groups.map((g) => `<tr data-kind="${escapeHtml(g.Kind)}" data-severity="${escapeHtml(g.Severity)}">
    <td>${escapeHtml(g.Kind)}</td>
    <td>${sevBadge(g.Severity)}</td>
    <td>${g.Count}</td>
    <td>${relTime(g.LastSeen)}</td>
  </tr>`).join("") || `<tr><td colspan="4" class="hint">No warnings yet.</td></tr>`;

  body.querySelectorAll("tr[data-kind]").forEach((row) => {
    // true: switching group starts at the first page. Keeping a deep offset
    // here would show an empty table for any group smaller than it — a page
    // that does not exist for that group.
    row.addEventListener("click", () => showWarningDetail(row.dataset.kind, row.dataset.severity, true));
  });
}

// The rows have to come from the server: the summary that names the group
// holds counts, not the warnings themselves. showWarningDetail doubles as the
// pager's page-change handler, so both entry points share one offset-reset
// rule — see the comment on resetOffset.
async function showWarningDetail(kind, severity, resetOffset) {
  // Last-issued-wins, and for a second reason the counter covers that a
  // "disable while loading" flag would not: clicking group B while group A's
  // fetch is still in flight must not let A's rows land in B's table.
  const seq = ++state.warningDetailFetchSeq;
  if (resetOffset) state.warningDetailPage.offset = 0;

  const panel = document.getElementById("warnings-detail");
  const title = document.getElementById("warnings-detail-title");
  const body = document.getElementById("warnings-detail-body");
  const { limit, offset } = state.warningDetailPage;
  const q = `kind=${encodeURIComponent(kind)}&severity=${encodeURIComponent(severity)}`;
  try {
    const page = await fetchPage(`/api/warnings?${q}&limit=${limit}&offset=${offset}`);
    if (seq !== state.warningDetailFetchSeq) return;
    state.warningDetailPage = { limit: page.limit || limit, offset: page.offset };

    // X-Total-Count is the group's true global count, which is the point:
    // the old title counted rows in a capped client-side cache, so it
    // disagreed with the summary table directly above it. It is also the
    // pager's Z, so the two numbers cannot drift apart.
    title.textContent = `${kind} (${severity}) — ${page.total} occurrence(s)`;
    body.innerHTML = page.items.map((w) => `<tr data-id="${w.RequestID}">
      <td>${w.RequestID}</td>
      <td>${sevBadge(w.Severity)}</td>
      <td>${relTime(w.CreatedAt)}</td>
      <td>${w.Path ? `<code>${escapeHtml(w.Path)}</code>` : ""}</td>
      <td>${escapeHtml(w.Detail)}</td>
    </tr>`).join("") || `<tr><td colspan="5" class="hint">Nothing on this page.</td></tr>`;
    body.querySelectorAll("tr[data-id]").forEach((row) => {
      row.addEventListener("click", () => openDetail(Number(row.dataset.id)));
    });
    renderPager(document.getElementById("warnings-detail-pager"), page, (next) => {
      state.warningDetailPage = next;
      showWarningDetail(kind, severity, false);
    });
    panel.hidden = false;
  } catch (e) {
    console.error("showWarningDetail", e);
  }
}

// ---- stats ----------------------------------------------------------------

// STATS_METRICS maps a metric toggle button's data-metric to the row field it
// plots and the formatter for the value it writes on the axis. One
// parameterized chart, not three near-duplicate renderers that differ only in
// the field they read.
const STATS_METRICS = {
  count: {
    label: "Calls",
    value: (d) => d.RequestCount,
    axis: (v) => String(v),
  },
  tokens: {
    label: "Tokens",
    value: (d) => (d.InputTokens || 0) + (d.OutputTokens || 0),
    axis: fmtTokens,
  },
  cost: {
    label: "Cost",
    value: (d) => d.CostUSDTotal,
    // fmtCost, not fmtCostTotal: the axis marker is a bare Math.max() scalar
    // with no link back to the bucket that produced it, so there is no
    // UnpricedCount to pass. The history table below is where fmtCostTotal
    // belongs, per row, where the count is available.
    axis: fmtCost,
  },
};

// FROM_LOOKBACK_MS bounds an unset From when the granularity changes: hour
// granularity over a long-retained install would otherwise render years of
// buckets. month is 0 because all-time is already readable at that bucket
// size. This is a UI default only — the API never rejects a large window.
const FROM_LOOKBACK_MS = { hour: 24 * 3600e3, day: 30 * 86400e3, week: 90 * 86400e3, month: 0 };

// utcDayBound turns an <input type="date"> value (YYYY-MM-DD) into the RFC3339
// UTC-midnight bound the API's since/until expect. The explicit T00:00:00Z
// suffix pins UTC midnight regardless of the browser's local zone. The To
// bound is the start of the NEXT day, because until is exclusive-upper:
// [start, next-start) covers the whole picked day with no double-counted
// boundary row — so "To: March 5" reads as "through the end of March 5 UTC",
// not "up to March 5 00:00".
function utcDayBound(dateStr, endExclusive) {
  if (!dateStr) return "";
  const d = new Date(dateStr + "T00:00:00Z");
  if (isNaN(d.getTime())) return "";
  if (endExclusive) d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().replace(/\.\d{3}Z$/, "Z");
}

function statsValue(id) {
  const el = document.getElementById(id);
  return el ? el.value : "";
}

async function loadStats() {
  try {
    // An empty From/To sends no since/until at all, which is the unbounded
    // all-time window this tab has always shown — the two-bound form is a
    // superset of the old one-bound behavior, not a change to it.
    const granularity = statsValue("stats-granularity") || "day";
    const from = utcDayBound(statsValue("stats-from"), false);
    const to = utcDayBound(statsValue("stats-to"), true);
    const params = new URLSearchParams({ granularity });
    if (from) params.set("since", from);
    if (to) params.set("until", to);

    const data = await fetchJSON("/api/stats?" + params.toString());
    renderStatsSummary(data.summary, data.cost_sources || []);
    const byPeriod = data.by_period || [];
    // One fetched row set, two views: the chart plots it and the table reads
    // it out exactly. Both are retained so the metric toggle can re-render
    // without a second request.
    state.statsByPeriod = byPeriod;
    state.statsGranularity = granularity;
    renderStatsChart(byPeriod, granularity);
    renderStatsHistoryTable(byPeriod);
    renderStatsByModel(data.by_model || []);
    renderStatsHeading(granularity);
  } catch (e) {
    console.error("loadStats", e);
  }
}

// renderStatsHeading keeps the chart's visible title and its aria-label
// describing what is actually plotted. Both track the metric toggle, which
// re-renders without refetching.
function renderStatsHeading(granularity) {
  const metric = STATS_METRICS[state.statsMetric] || STATS_METRICS.count;
  const title = `${metric.label} per ${granularity}`;
  const h = document.getElementById("stats-chart-title");
  if (h) h.textContent = title;
  const svg = document.getElementById("stats-chart");
  if (svg) svg.setAttribute("aria-label", title + " line chart");
}

// statsXLabel extracts the readable part of a bucket label. An hour bucket
// ("2026-09-17T14:00") yields its time half; day, week and month labels all
// drop their leading year and dash ("03-01", "W08", "03"). A uniform
// .slice(5) would print 11 characters per hour tick, unreadable at density.
function statsXLabel(period, granularity) {
  const s = String(period);
  if (granularity === "hour") {
    const parts = s.split("T");
    return parts.length > 1 ? parts[1] : s;
  }
  return s.slice(5);
}

function renderStatsSummary(s, costSources) {
  const body = document.querySelector("#stats-summary tbody");
  if (!s) { body.innerHTML = ""; return; }
  const rows = [
    ["Calls", s.RequestCount],
    ["Errors", s.ErrorCount],
    ["Warnings", s.WarningCount],
    ["Input tokens", fmtTokens(s.InputTokens)],
    ["Output tokens", fmtTokens(s.OutputTokens)],
    ["Cost", fmtCostTotal(s.CostUSDTotal, s.UnpricedCount || 0)],
    ["Duration p50", s.DurationP50Ms.toFixed(0) + "ms"],
    ["Duration p95", s.DurationP95Ms.toFixed(0) + "ms"],
  ];
  // The cost-source breakdown is what says *why* a total is partial: a
  // configured/priced window and one full of unpriced calls can report the
  // same number.
  if (costSources.length) {
    rows.push(["Cost by source", costSources
      .map((c) => `${escapeHtml(c.Source)} ${c.RequestCount}`)
      .join(" · ")]);
  }
  body.innerHTML = rows.map(([k, v]) => `<tr><td>${k}</td><td>${v}</td></tr>`).join("");
}

function renderStatsChart(byPeriod, granularity) {
  const svg = document.getElementById("stats-chart");
  const W = 600, H = 200, padL = 34, padB = 20, padT = 10, padR = 10;
  const metric = STATS_METRICS[state.statsMetric] || STATS_METRICS.count;
  if (byPeriod.length === 0) {
    svg.innerHTML = `<text x="16" y="100">no data yet</text>`;
    return;
  }
  const maxValue = Math.max(1, ...byPeriod.map((d) => metric.value(d)));
  const plotW = W - padL - padR;
  const plotH = H - padT - padB;
  const step = byPeriod.length > 1 ? plotW / (byPeriod.length - 1) : 0;

  const points = byPeriod.map((d, i) => {
    const x = padL + i * step;
    const y = padT + plotH - (metric.value(d) / maxValue) * plotH;
    return [x, y];
  });

  const linePath = points.map((p, i) => (i === 0 ? "M" : "L") + p[0].toFixed(1) + "," + p[1].toFixed(1)).join(" ");
  const dots = points.map((p) => `<circle class="point" cx="${p[0].toFixed(1)}" cy="${p[1].toFixed(1)}" r="2.5"></circle>`).join("");

  const everyN = Math.ceil(byPeriod.length / 6) || 1;
  const labels = byPeriod.map((d, i) => (i % everyN === 0
    ? `<text x="${points[i][0].toFixed(1)}" y="${H - 4}" text-anchor="middle">${escapeHtml(statsXLabel(d.Period, granularity))}</text>`
    : "")).join("");

  svg.setAttribute("viewBox", `0 0 ${W} ${H}`);
  svg.innerHTML = `
    <line class="axis" x1="${padL}" y1="${padT}" x2="${padL}" y2="${padT + plotH}"></line>
    <line class="axis" x1="${padL}" y1="${padT + plotH}" x2="${W - padR}" y2="${padT + plotH}"></line>
    <text x="4" y="${padT + 8}">${escapeHtml(metric.axis(maxValue))}</text>
    <text x="4" y="${padT + plotH}">0</text>
    <path class="line" d="${linePath}"></path>
    ${dots}
    ${labels}
  `;
}

// renderStatsHistoryTable reads out the same buckets the chart plots, exactly
// rather than by eye — a line can be eyeballed, a period's tokens cannot.
// Deliberately uncapped, no pager: this is a single-user local tool, and the
// row count is bounded in practice by FROM_LOOKBACK_MS. Add the .pager pattern
// if a wide hand-picked window ever proves tedious.
function renderStatsHistoryTable(byPeriod) {
  const body = document.getElementById("stats-history");
  if (!body) return;
  body.innerHTML = byPeriod.map((p) => `<tr>
    <td>${escapeHtml(p.Period)}</td>
    <td>${p.RequestCount}</td>
    <td>${fmtTokens((p.InputTokens || 0) + (p.OutputTokens || 0))}</td>
    <td>${fmtCostTotal(p.CostUSDTotal, p.UnpricedCount || 0)}</td>
  </tr>`).join("") || `<tr><td colspan="4" class="hint">No data yet.</td></tr>`;
}

function renderStatsByModel(byModel) {
  const body = document.getElementById("stats-by-model");
  const max = Math.max(1, ...byModel.map((m) => m.RequestCount));
  body.innerHTML = byModel.map((m) => `<tr>
    <td>${escapeHtml(m.Model || "(unknown)")}</td>
    <td><div class="model-bar-row"><span>${m.RequestCount}</span>
      <span class="model-bar" style="width:${Math.round((m.RequestCount / max) * 120)}px"></span></div></td>
    <td>${fmtTokens(m.InputTokens)}/${fmtTokens(m.OutputTokens)}</td>
    <td>${fmtCostTotal(m.CostUSDTotal, m.UnpricedCount || 0)}</td>
  </tr>`).join("") || `<tr><td colspan="4" class="hint">No data yet.</td></tr>`;
}

// The metric toggle re-renders from the retained row set: switching metric is
// a view change over rows already fetched, not a reason to re-ask the server.
document.getElementById("stats-metric").addEventListener("click", (e) => {
  const btn = e.target.closest(".metric");
  if (!btn) return;
  const metric = btn.dataset.metric;
  state.statsMetric = STATS_METRICS[metric] ? metric : "count";
  for (const b of e.currentTarget.querySelectorAll(".metric")) {
    b.classList.toggle("active", b === btn);
  }
  renderStatsChart(state.statsByPeriod, state.statsGranularity);
  renderStatsHeading(state.statsGranularity);
});

document.getElementById("stats-granularity").addEventListener("change", () => {
  // An hour view over an empty From would render years of buckets, so default
  // From to a sane lookback the first time — but never overwrite a date the
  // user typed, which is why this only fills an empty field.
  const fromEl = document.getElementById("stats-from");
  const lookback = FROM_LOOKBACK_MS[statsValue("stats-granularity")] || 0;
  if (!fromEl.value && lookback > 0) {
    fromEl.value = new Date(Date.now() - lookback).toISOString().slice(0, 10);
  }
  loadStats();
});

for (const id of ["stats-from", "stats-to"]) {
  document.getElementById(id).addEventListener("change", loadStats);
}

// ---- pager ------------------------------------------------------------

// PAGE_SIZES is the page-size select's options. Both pager call sites seed
// their state with one of these, so the server's applied X-Limit is always a
// value the select can display.
const PAGE_SIZES = [25, 50, 100, 200];

// renderPager draws page controls into container and calls onPageChange with
// the new {limit, offset} on interaction. Shared by the sessions table and the
// warnings drill-down: the only thing the two share is this class, which is
// what keeps the markup from having to be a template.
//
// The select's value comes from the APPLIED limit the server reported, not
// from a private constant. X-Limit is the size of the page actually fetched,
// so deriving it from anywhere else would let the shown value, the count
// label, and the Next/Prev arithmetic disagree with the rows on screen.
function renderPager(container, page, onPageChange) {
  const { total, limit, offset } = page;
  // first is clamped to total as well, not just last. A page past the end is
  // reachable (delay a Next click past a list that shrank, or the accepted
  // X-Total-Count staleness), and an unclamped first reads "201-120 of 120" —
  // a backwards range sitting directly above a "nothing on this page" row.
  const first = total === 0 ? 0 : Math.min(offset + 1, total);
  const last = Math.min(offset + limit, total);
  container.innerHTML = `
    <label>Rows per page
      <select class="pager-size">${PAGE_SIZES.map(
        (n) => `<option value="${n}"${n === limit ? " selected" : ""}>${n}</option>`).join("")}</select>
    </label>
    <button class="pager-prev" type="button"${offset > 0 ? "" : " disabled"}>&laquo; Prev</button>
    <button class="pager-next" type="button"${offset + limit < total ? "" : " disabled"}>Next &raquo;</button>
    <span class="pager-count">${first}&ndash;${last} of ${total}</span>`;

  const select = container.querySelector(".pager-size");
  select.addEventListener("change", () => {
    // Resizing always returns to the top. Keeping the offset would land a user
    // on page 3 of 100-row pages at item 200 *of 25-row pages* — a page they
    // never chose, reading as missing data.
    onPageChange({ limit: parseInt(select.value, 10), offset: 0 });
  });
  container.querySelector(".pager-prev").addEventListener("click", () => {
    onPageChange({ limit, offset: Math.max(0, offset - limit) });
  });
  container.querySelector(".pager-next").addEventListener("click", () => {
    onPageChange({ limit, offset: offset + limit });
  });
}

// ---- sessions ---------------------------------------------------------

async function loadSessions() {
  // Last-issued-wins. The pager is exactly the surface that invites rapid
  // repeated triggers — double-clicking Next, or changing the page size right
  // after a Next click — and two fetches can be in flight with the slower,
  // EARLIER one resolving last and overwriting the page the user asked for.
  const seq = ++state.sessionsFetchSeq;
  const { limit, offset } = state.sessionsPage;
  try {
    document.getElementById("session-detail").hidden = true; // a reload invalidates any open drill-down
    const page = await fetchPage(`/api/sessions?limit=${limit}&offset=${offset}`);
    if (seq !== state.sessionsFetchSeq) return;

    // Take the applied window from the response so the next Next/Prev moves
    // from the page that is actually on screen.
    state.sessionsPage = { limit: page.limit || limit, offset: page.offset };

    // "Empty" has two causes now, and they must not read the same. A valid
    // page past the end is reachable through the accepted X-Total-Count
    // staleness, and telling a user who asked for page 9 that no sessions
    // were recorded turns a staleness blip into the appearance of data loss.
    const emptyHint = page.offset > 0
      ? "No sessions on this page — the list has shrunk since it was loaded."
      : "No sessions recorded yet.";

    const body = document.getElementById("sessions-body");
    body.innerHTML = page.items.map((s) => `<tr data-id="${escapeHtml(s.ID)}">
      <td>${escapeHtml(s.ID)}</td>
      <td>${relTime(s.FirstSeen)}</td>
      <td>${fmtSpanMs(new Date(s.LastSeen) - new Date(s.FirstSeen))}</td>
      <td>${s.RequestCount}</td>
      <td>${fmtTokens((s.TotalInputTokens || 0) + (s.TotalOutputTokens || 0))}</td>
      <td>${fmtCostTotal(s.TotalCostUSD, s.UnpricedCount || 0)}</td>
      <td>${s.WarningCount ? warnBadge(s.WarningCount, "click the session to list them") : ""}</td>
    </tr>`).join("") || `<tr><td colspan="7" class="hint">${emptyHint}</td></tr>`;
    body.querySelectorAll("tr[data-id]").forEach((row) => {
      row.addEventListener("click", () => openSession(row.dataset.id));
    });
    renderPager(document.getElementById("sessions-pager"), page, (next) => {
      state.sessionsPage = next;
      loadSessions();
    });
  } catch (e) {
    console.error("loadSessions", e);
  }
}

// groupSessionWarnings collapses a session's warnings to one line per
// (kind, site). This is the view the whole feature exists for: a
// cache_control_ignored firing on all forty turns of one run is one story
// with a count on it, not forty rows to scroll past. Grouping by kind and
// site rather than by detail is deliberate — the detail of a dropped
// parameter varies per turn, the site it was dropped at does not.
function groupSessionWarnings(warnings) {
  const groups = new Map();
  for (const w of warnings) {
    const key = w.Kind + "|" + w.Path;
    let g = groups.get(key);
    if (!g) {
      g = { kind: w.Kind, severity: w.Severity, path: w.Path, detail: w.Detail, count: 0 };
      groups.set(key, g);
    }
    g.count++;
  }
  return [...groups.values()].sort((a, b) => b.count - a.count);
}

// peakSummary renders a session's peak rollup: how many of its calls were
// priced under peak hours, and what share of the session's cost they were.
// It answers the one question the sessions table cannot — was this session
// expensive because of the work, or because of the clock.
//
// The share is omitted, never rendered as NaN%, when there is no priced total
// to divide by: an all-unpriced session has calls and TotalCostUSD = 0, so
// the division is 0/0 and the count alone is the honest answer.
function peakSummary(peak, total) {
  const calls = (peak && peak.calls) || 0;
  if (!calls) return "none";
  const cost = (peak && peak.cost_usd) || 0;
  const share = total > 0 ? ` · ${Math.round((cost / total) * 100)}% of cost` : "";
  return `${calls} call${calls === 1 ? "" : "s"} · ${fmtCost(cost)}${share}`;
}

async function openSession(id) {
  try {
    const s = await fetchJSON(`/api/sessions/${encodeURIComponent(id)}`);
    const calls = s.calls || [];
    const warnings = s.warnings || [];
    const groups = groupSessionWarnings(warnings);

    const perCall = new Map();
    for (const w of warnings) perCall.set(w.RequestID, (perCall.get(w.RequestID) || 0) + 1);

    document.getElementById("session-detail-title").textContent =
      `${s.ID} — ${s.RequestCount} turn(s)`;
    document.getElementById("session-detail-summary").innerHTML = [
      ["Started", escapeHtml(s.FirstSeen)],
      ["Last active", escapeHtml(s.LastSeen)],
      ["Duration", fmtSpanMs(new Date(s.LastSeen) - new Date(s.FirstSeen))],
      ["Turns", s.RequestCount],
      ["Tokens", `in ${fmtTokens(s.TotalInputTokens)} / out ${fmtTokens(s.TotalOutputTokens)}`],
      ["Cost", fmtCostTotal(s.TotalCostUSD, s.UnpricedCount || 0)],
      ["Peak-priced", peakSummary(s.peak, s.TotalCostUSD)],
      ["Models", escapeHtml(s.ModelSet || "-")],
      ["Prefix hash", escapeHtml(s.PrefixHash || "(none)")],
    ].map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join("");

    document.getElementById("session-warnings").innerHTML = groups.map((g) => `<tr>
      <td>${escapeHtml(g.kind)}</td>
      <td>${sevBadge(g.severity)}</td>
      <td>${g.count}</td>
      <td>${g.path ? `<code>${escapeHtml(g.path)}</code>` : ""}</td>
      <td>${escapeHtml(g.detail)}</td>
    </tr>`).join("") || `<tr><td colspan="5" class="hint">No warnings in this session.</td></tr>`;

    // The running total is the point of the chronological order: it answers
    // "what had this run cost by turn 12". Unpriced calls are counted and
    // named, never folded in as zero.
    let runTokens = 0, runCost = 0, runUnpriced = 0;
    const callRows = calls.map((c, i) => {
      runTokens += (c.InputTokens || 0) + (c.OutputTokens || 0);
      if (isUnpriced(c.CostUSD)) runUnpriced += 1;
      else runCost += c.CostUSD;
      const n = perCall.get(c.ID) || 0;
      return `<tr data-id="${c.ID}">
        <td>${i + 1}</td>
        <td>${fmtTime(c.StartedAt)}</td>
        <td>${fmtDurationNs(c.Duration)}</td>
        <td>${escapeHtml(c.ModelResolved || c.ModelRequested || "-")}</td>
        <td>${fmtTokens(c.InputTokens)}/${fmtTokens(c.OutputTokens)}</td>
        <td>${costCell(c.CostUSD, c.CostSource)}</td>
        <td>${fmtTokens(runTokens)} · ${fmtCostTotal(runCost, runUnpriced)}</td>
        <td>${n ? warnBadge(n, "click the turn for detail") : ""}</td>
      </tr>`;
    }).join("");
    const callsBody = document.getElementById("session-calls");
    callsBody.innerHTML = callRows || `<tr><td colspan="8" class="hint">No calls in this session.</td></tr>`;
    callsBody.querySelectorAll("tr[data-id]").forEach((row) => {
      row.addEventListener("click", () => openDetail(Number(row.dataset.id)));
    });

    // The panel lives below the sessions table, which is routinely taller than
    // the viewport — unhiding it alone is invisible from the row that was
    // clicked, so the drill-down looked like a dead click.
    const panel = document.getElementById("session-detail");
    panel.hidden = false;
    panel.scrollIntoView({ block: "start" });
  } catch (e) {
    console.error("openSession", e);
  }
}

document.getElementById("session-detail-close").addEventListener("click", () => {
  document.getElementById("session-detail").hidden = true;
});

// ---- settings: pricing (br-GI-17-09) -----------------------------------

// RATE_FIELDS pairs each POST /api/prices "rates" key with its column
// header — the four rate fields the route takes as one whole-row PATCH-like
// write (D3), never a per-field endpoint.
const RATE_FIELDS = [
  ["input", "Input"],
  ["output", "Output"],
  ["cache_read", "Cache read"],
  ["cache_write", "Cache write"],
];

// loadSettings is the Settings tab's showView load hook: it fans out to the
// two independent sections (pricing, retention/purge) rather than one fetch
// carrying both, since GET /api/prices and GET /api/retention are two
// routes with two unwired-503 states that must not depend on each other.
function loadSettings() {
  loadSettingsPrices();
  loadSettingsRetention();
}

async function loadSettingsPrices() {
  const errEl = document.getElementById("settings-price-error");
  const wrap = document.getElementById("settings-price-wrap");
  try {
    const res = await fetch("/api/prices");
    if (res.status === 503) {
      // Unwired (no SetPricing in serve.go) is a plain message, not an
      // empty table — an empty table would read as "no models configured"
      // rather than "pricing isn't available at all".
      wrap.hidden = true;
      errEl.textContent = "Pricing is unavailable on this server.";
      errEl.hidden = false;
      return;
    }
    if (!res.ok) {
      let msg = res.statusText;
      try { msg = (await res.json()).error || msg; } catch (e) { /* ignore */ }
      throw new Error(msg);
    }
    const data = await res.json();
    errEl.hidden = true;
    wrap.hidden = false;
    state.settingsPrices = data;
    renderSettingsPrices(data);
  } catch (e) {
    console.error("loadSettingsPrices", e);
    wrap.hidden = true;
    errEl.textContent = "Failed to load pricing: " + (e.message || e);
    errEl.hidden = false;
  }
}

// priceSourceBadge follows costBadge's pattern: the glyph — here the source
// word itself — never carries meaning alone, so title and aria-label always
// say the same thing a sighted user reads from the badge's colour.
function priceSourceBadge(source) {
  const label = `source: ${source}`;
  const cls = source === "configured" ? "badge" : "badge warn";
  return `<span class="${cls}" title="${escapeHtml(label)}" aria-label="${escapeHtml(label)}">${escapeHtml(source)}</span>`;
}

// rateCellHTML renders null as a genuinely empty cell (D3's convention: "no
// rate configured" and "this model is free" are different statements) and 0
// as the digit "0" — the two must never collapse into the same rendering.
function rateCellHTML(model, key) {
  const v = model[key];
  const display = v === null || v === undefined ? "" : String(v);
  return `<td class="price-cell" data-field="${key}">${escapeHtml(display)}</td>`;
}

// calendarSummary echoes the calendar the server is pricing against as the
// user's own config text, verbatim — a range stays a range, because
// re-serialising the parsed days would show them something they did not
// write. Both empty means no calendar is installed, which is the
// window-and-weekend rule with no holidays.
function calendarSummary(data) {
  const off = data.off_peak_dates || "";
  const work = data.work_dates || "";
  if (!off && !work) return "No holiday calendar configured: peak hours follow the weekday window alone.";
  return `Off-peak dates: ${off || "(none)"} · Work dates: ${work || "(none)"}`;
}

function renderSettingsPrices(data) {
  document.getElementById("settings-price-path").textContent = data.path;
  document.getElementById("settings-peak-multiplier").textContent = data.peak_multiplier;
  document.getElementById("settings-calendar").textContent = calendarSummary(data);
  const body = document.getElementById("settings-price-body");
  body.innerHTML = (data.models || []).map((m) => `<tr data-model="${escapeHtml(m.model)}">
    <td>${escapeHtml(m.model)}</td>
    ${RATE_FIELDS.map(([key]) => rateCellHTML(m, key)).join("")}
    <td>${priceSourceBadge(m.source)}</td>
  </tr>`).join("") || `<tr><td colspan="6" class="hint">No models configured.</td></tr>`;
}

function settingsPriceMessage(text) {
  const el = document.getElementById("settings-price-msg");
  if (!text) { el.hidden = true; return; }
  el.textContent = text;
  el.hidden = false;
}

// Click-to-edit is delegated on the tbody rather than wired per cell, so a
// re-render (after every save) never has to re-attach listeners.
document.getElementById("settings-price-body").addEventListener("click", (e) => {
  const cell = e.target.closest("td.price-cell");
  if (!cell || cell.querySelector("input")) return;
  startEditingPriceCell(cell);
});

function startEditingPriceCell(cell) {
  const orig = cell.textContent;
  cell.dataset.orig = orig;
  cell.innerHTML = `<input type="text" inputmode="decimal" value="${escapeHtml(orig)}">`;
  const input = cell.querySelector("input");
  input.focus();
  input.select();
  state.settingsPriceDirty = true;

  let finished = false;
  const finish = (commit) => {
    if (finished) return; // Enter then blur on the same input would otherwise fire twice
    finished = true;
    if (commit) savePriceCell(cell, input.value);
    else cell.textContent = cell.dataset.orig;
  };
  input.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") { ev.preventDefault(); finish(true); }
    else if (ev.key === "Escape") { ev.preventDefault(); finish(false); }
  });
  input.addEventListener("blur", () => finish(true));
}

// savePriceCell sends the edited field's whole row (D3): the other three
// rates come from the last-known-good response, not from whatever is
// currently on screen, so a save can never resend a value the server has
// not actually confirmed.
async function savePriceCell(cell, rawValue) {
  const row = cell.closest("tr");
  const model = row.dataset.model;
  const field = cell.dataset.field;
  const trimmed = rawValue.trim();

  let value = null;
  if (trimmed !== "") {
    value = Number(trimmed);
    if (!Number.isFinite(value)) {
      cell.textContent = cell.dataset.orig;
      settingsPriceMessage(`"${trimmed}" is not a number — left unset.`);
      return;
    }
  }

  const known = ((state.settingsPrices || {}).models || []).find((m) => m.model === model) || {};
  const rates = {
    input: known.input, output: known.output,
    cache_read: known.cache_read, cache_write: known.cache_write,
  };
  rates[field] = value;

  cell.textContent = trimmed === "" ? "" : String(value);
  try {
    const res = await fetch("/api/prices", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model, rates }),
    });
    let body = {};
    try { body = await res.json(); } catch (e) { /* keep the default below */ }
    if (!res.ok) throw new Error(body.error || res.statusText);

    // Re-render from the response, never from local state (R1): this is
    // what turns a concurrent `lens prices --set` into "the table now shows
    // the value that won" instead of a UI that silently disagrees with the
    // file on disk.
    state.settingsPrices = body;
    state.settingsPriceDirty = false;
    settingsPriceMessage("");
    renderSettingsPrices(body);
  } catch (e) {
    // Rejected: leave the table on the server's last-known state, not the
    // value that was just refused, and say why.
    state.settingsPriceDirty = true;
    settingsPriceMessage(`Save failed: ${e.message || e}`);
    if (state.settingsPrices) renderSettingsPrices(state.settingsPrices);
  }
}

// ---- settings: retention and purge (br-GI-17-10) -----------------------
//
// This is the only place in the product a click deletes captured data
// irreversibly, which is why every action here is preview-then-confirm and
// why VACUUM is never offered (R3): it takes an exclusive lock that blocks
// capture for as long as it runs, which is the wrong shape for a browser
// button with no progress feedback. `lens purge --vacuum` is where it
// belongs.

async function loadSettingsRetention() {
  const errEl = document.getElementById("settings-retention-error");
  const wrap = document.getElementById("settings-retention-wrap");
  try {
    const res = await fetch("/api/retention");
    if (res.status === 503) {
      wrap.hidden = true;
      errEl.textContent = "Retention and purge are unavailable on this server.";
      errEl.hidden = false;
      return;
    }
    if (!res.ok) {
      let msg = res.statusText;
      try { msg = (await res.json()).error || msg; } catch (e) { /* ignore */ }
      throw new Error(msg);
    }
    const data = await res.json();
    errEl.hidden = true;
    wrap.hidden = false;
    state.settingsRetention = data;
    renderSettingsRetention(data);
  } catch (e) {
    console.error("loadSettingsRetention", e);
    wrap.hidden = true;
    errEl.textContent = "Failed to load retention: " + (e.message || e);
    errEl.hidden = false;
  }
}

// renderSettingsRetention draws the two-state summary and rebuilds both
// confirm panels' copy from the fresh preview. days<=0 ("keep forever") and
// days>0 are the two readings of an unconfigured threshold, and only one is
// true — the older-than action must be ABSENT (hidden), not merely
// disabled, in the former: a greyed button still reads as "there is
// something to purge here".
function renderSettingsRetention(data) {
  const summary = document.getElementById("settings-retention-summary");
  const olderThanAction = document.getElementById("settings-older-than-action");

  if (data.days <= 0) {
    summary.textContent = "Retention is off (keep forever). Set --retention-days, " +
      "LENS_RETENTION_DAYS, or retention_days in config.toml to enable it.";
    olderThanAction.hidden = true;
    document.getElementById("settings-older-than-confirm").hidden = true;
  } else {
    const eligible = data.eligible_requests > 0
      ? `${data.eligible_requests} request(s), ~${fmtBytes(data.eligible_bytes)}` +
        ` (${data.oldest} to ${data.newest})`
      : "nothing to purge";
    summary.textContent = `Retention is configured to ${data.days} day(s). ` +
      `Cutoff: ${data.cutoff}. Eligible now: ${eligible}.`;
    olderThanAction.hidden = false;
    document.getElementById("settings-older-than-confirm-text").textContent =
      data.eligible_requests > 0
        ? `This deletes ${data.eligible_requests} request(s) older than ${data.cutoff} ` +
          `(~${fmtBytes(data.eligible_bytes)}). This cannot be undone.`
        : `Nothing is older than ${data.cutoff} right now — there is nothing to delete.`;
  }

  // The unpriced action is independent of days (D10/D11): it stays
  // available under "keep forever", and its confirm copy always names the
  // figure as the positive-token unpriced count — a strict SUBSET of the
  // Stats tab's larger "unpriced N" (which has no token condition), so the
  // two on-screen figures differ by design and the label is what keeps that
  // reading as intended rather than as a contradiction.
  document.getElementById("settings-unpriced-confirm-text").textContent =
    `This deletes ${data.unpriced_requests} unpriced request(s) with at least one token ` +
    `(~${fmtBytes(data.unpriced_bytes)}) — a subset of the Stats tab's larger "unpriced" count, ` +
    `which also includes zero-token rows. This cannot be undone.`;
}

document.getElementById("settings-older-than-btn").addEventListener("click", () => {
  const panel = document.getElementById("settings-older-than-confirm");
  panel.hidden = false;
  panel.scrollIntoView({ block: "nearest" });
});
document.getElementById("settings-older-than-cancel-btn").addEventListener("click", () => {
  document.getElementById("settings-older-than-confirm").hidden = true;
});
document.getElementById("settings-older-than-confirm-btn").addEventListener("click", (e) => {
  const days = (state.settingsRetention || {}).days;
  if (!days || days <= 0) return; // the panel that offers this is hidden in this state; belt
  runPurgeAction("older_than", days, e.currentTarget, "settings-older-than-result");
});

document.getElementById("settings-unpriced-btn").addEventListener("click", () => {
  const panel = document.getElementById("settings-unpriced-confirm");
  panel.hidden = false;
  panel.scrollIntoView({ block: "nearest" });
});
document.getElementById("settings-unpriced-cancel-btn").addEventListener("click", () => {
  document.getElementById("settings-unpriced-confirm").hidden = true;
});
document.getElementById("settings-unpriced-confirm-btn").addEventListener("click", (e) => {
  runPurgeAction("unpriced", 0, e.currentTarget, "settings-unpriced-result");
});

// runPurgeAction issues the one POST /api/purge the confirm click commits
// to. The button is disabled for the duration so a second click (or a
// double-click on the confirm) cannot fire the action twice, and a failed
// POST surfaces its message in place rather than silently reloading
// anything — reloadDataViews only runs after a confirmed success.
async function runPurgeAction(mode, days, button, resultElId) {
  const result = document.getElementById(resultElId);
  button.disabled = true;
  result.textContent = "purging…";
  try {
    const body = mode === "older_than" ? { mode, days } : { mode };
    const res = await fetch("/api/purge", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    let data = {};
    try { data = await res.json(); } catch (e) { /* keep the default below */ }
    if (!res.ok) throw new Error(data.error || res.statusText);

    result.textContent = `Deleted ${data.deleted} request(s), reconciled ${data.sessions_reconciled} session(s).`;
    button.closest(".detail-panel").hidden = true;
    reloadDataViews();
    loadSettingsRetention();
  } catch (e) {
    result.textContent = `Purge failed: ${e.message || e}`;
  } finally {
    button.disabled = false;
  }
}

// ---- request detail modal ----------------------------------------------

function decodeBody(b64) {
  if (!b64) return "";
  try {
    const bin = atob(b64);
    const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
    const text = new TextDecoder("utf-8", { fatal: false }).decode(bytes);
    try { return JSON.stringify(JSON.parse(text), null, 2); } catch (e) { return text; }
  } catch (e) {
    return "(binary or undecodable body)";
  }
}

function prettyHeaders(json) {
  if (!json) return "(none)";
  try { return JSON.stringify(JSON.parse(json), null, 2); } catch (e) { return json; }
}

function bodySection(label, b64) {
  const text = decodeBody(b64);
  return `<details class="collapsible"><summary>${label}</summary>
    <pre class="body-block">${escapeHtml(text || "(empty)")}</pre></details>`;
}

async function openDetail(id) {
  try {
    const r = await fetchJSON(`/api/requests/${id}`);
    const warnings = r.warnings || [];
    const model = r.ModelResolved || r.ModelRequested || "-";

    const warningsHTML = warnings.length
      ? `<div class="detail-panel"><h3>Warnings (${warnings.length})</h3>` +
        warnings.map((w) => `<div>${sevBadge(w.Severity)} <strong>${escapeHtml(w.Kind)}</strong>${w.Path ? ` <code>${escapeHtml(w.Path)}</code>` : ""}: ${escapeHtml(w.Detail)}</div>`).join("") +
        `</div>`
      : "";

    document.getElementById("detail-body").innerHTML = `
      <h2>Request #${r.ID}</h2>
      <dl class="detail-grid">
        <dt>Started</dt><dd>${escapeHtml(r.StartedAt)}</dd>
        <dt>TTFB / Duration</dt><dd>${fmtDurationNs(r.TTFB)} / ${fmtDurationNs(r.Duration)}</dd>
        <dt>Method / Path</dt><dd>${escapeHtml(r.Method)} ${escapeHtml(r.Path)}</dd>
        <dt>Status</dt><dd>${r.Status}</dd>
        <dt>Model requested</dt><dd>${escapeHtml(r.ModelRequested || "-")}</dd>
        <dt>Model resolved</dt><dd>${escapeHtml(model)}</dd>
        <dt>Tokens</dt><dd>in=${r.InputTokens} out=${r.OutputTokens} cache_creation=${r.CacheCreationTokens} cache_read=${r.CacheReadTokens}</dd>
        <dt>Cost</dt><dd>${fmtCost(r.CostUSD)}${r.CostSource ? " (" + escapeHtml(r.CostSource) + ")" : ""}</dd>
        <dt>Stop reason</dt><dd>${escapeHtml(r.StopReason || "-")}</dd>
        ${r.ErrorText ? `<dt>Error</dt><dd>${escapeHtml(r.ErrorText)}</dd>` : ""}
      </dl>
      ${warningsHTML}
      <details class="collapsible" open><summary>Request headers (redacted)</summary>
        <pre class="body-block">${escapeHtml(prettyHeaders(r.ReqHeaders))}</pre></details>
      ${bodySection("Request body", r.ReqBody)}
      <details class="collapsible"><summary>Response headers (redacted)</summary>
        <pre class="body-block">${escapeHtml(prettyHeaders(r.RespHeaders))}</pre></details>
      ${bodySection("Response body", r.RespBody)}
      ${replayPanelHTML(r)}
    `;
    wireReplayPanel(r);
    document.getElementById("detail-modal").hidden = false;
  } catch (e) {
    console.error("openDetail", e);
  }
}

// ---- replay -------------------------------------------------------------

// replayPanelHTML is the request detail's replay editor. When replay is
// disabled the panel says so and offers nothing — the guarded endpoint would
// answer 403, and a button that cannot work is worse than no button.
function replayPanelHTML(r) {
  if (!state.replayEnabled) {
    return `<div class="detail-panel"><h3>Replay</h3>
      <p class="hint">Replay is disabled. Start <code>lens serve --replay</code> to enable it.</p>
    </div>`;
  }
  const cost = isUnpriced(r.CostUSD)
    ? "unknown — this call has no configured price"
    : fmtCost(r.CostUSD);
  return `<div class="detail-panel">
    <h3>Replay</h3>
    <p class="hint">Replay re-sends this request to the LLM API. It never executes anything,
      never touches your repository, and never re-runs tools: a captured body may describe tool
      calls the original client ran, and replay re-sends that description and stores the reply.</p>
    <p class="hint"><strong>This is a billable API call.</strong> The original cost ${escapeHtml(cost)};
      the replay will be similar.</p>
    <label for="replay-edits">Edits — one <code>path=value</code> per line, e.g.
      <code>temperature=0.7</code> (optional)</label>
    <textarea id="replay-edits" rows="3" spellcheck="false"
      placeholder="temperature=0.7&#10;messages.0.content=&quot;hi&quot;"></textarea>
    <label class="replay-confirm"><input type="checkbox" id="replay-confirm">
      I understand this makes a billable API call</label>
    <button id="replay-send" type="button" disabled>Send replay</button>
    <div id="replay-result" aria-live="polite"></div>
  </div>`;
}

// wireReplayPanel arms the editor: the send button stays disabled until the
// cost confirmation is ticked, so a stray click cannot spend anything.
function wireReplayPanel(original) {
  const send = document.getElementById("replay-send");
  if (!send) return;
  const confirm = document.getElementById("replay-confirm");
  confirm.addEventListener("change", () => { send.disabled = !confirm.checked; });
  send.addEventListener("click", () => sendReplay(original, send));
}

async function sendReplay(original, button) {
  const out = document.getElementById("replay-result");
  const sets = document.getElementById("replay-edits").value
    .split("\n").map((line) => line.trim()).filter((line) => line !== "");

  const query = new URLSearchParams();
  for (const s of sets) query.append("set", s);

  button.disabled = true;
  out.textContent = "sending…";
  try {
    // Same-origin, so the endpoint's Origin/Host guard passes — that guard is
    // exactly why this page can call it and another page cannot.
    const res = await fetch(`/api/requests/${original.ID}/replay?${query.toString()}`, { method: "POST" });
    let body = {};
    try { body = await res.json(); } catch (e) { /* keep the default below */ }
    if (!res.ok) throw new Error(body.error || res.statusText);

    if (!body.captured) {
      out.innerHTML = `<p class="hint">Sent, not recorded. Upstream status ${body.status}.</p>`;
      return;
    }
    const replayed = await fetchJSON(`/api/requests/${body.id}`);
    out.innerHTML = replayComparisonHTML(original, replayed);
  } catch (e) {
    out.innerHTML = `<p><span class="sev sev-error">replay failed</span> ${escapeHtml(String(e.message || e))}</p>`;
  } finally {
    button.disabled = false;
  }
}

// replayComparisonHTML puts the original and the replay side by side. The "≠"
// marker carries the difference, so it still reads on a monochrome screen and
// to a screen reader instead of living only in a colour.
function replayComparisonHTML(original, replayed) {
  const model = (r) => r.ModelResolved || r.ModelRequested || "-";
  const tokens = (r) => `${fmtTokens(r.InputTokens)}/${fmtTokens(r.OutputTokens)}`;
  const warns = (r) => String((r.warnings || []).length);
  const row = (field, a, b) =>
    `<tr><td>${a === b ? "" : "≠"}</td><td>${field}</td><td>${a}</td><td>${b}</td></tr>`;

  return `<h4>Replay #${replayed.ID} compared with #${original.ID}</h4>
    <div class="table-wrap"><table>
      <thead><tr><th></th><th>Field</th><th>#${original.ID}</th><th>#${replayed.ID}</th></tr></thead>
      <tbody>
        ${row("status", String(original.Status), String(replayed.Status))}
        ${row("model", escapeHtml(model(original)), escapeHtml(model(replayed)))}
        ${row("tokens", tokens(original), tokens(replayed))}
        ${row("cost", fmtCost(original.CostUSD), fmtCost(replayed.CostUSD))}
        ${row("duration", fmtDurationNs(original.Duration), fmtDurationNs(replayed.Duration))}
        ${row("warnings", warns(original), warns(replayed))}
      </tbody>
    </table></div>`;
}

document.getElementById("detail-close").addEventListener("click", () => {
  document.getElementById("detail-modal").hidden = true;
});
document.getElementById("detail-modal").addEventListener("click", (e) => {
  if (e.target.id === "detail-modal") document.getElementById("detail-modal").hidden = true;
});

// ---- SSE live push ----------------------------------------------------

function connectStream() {
  const es = new EventSource("/api/stream");
  // Events are handled one at a time, in arrival order: onmessage fires
  // synchronously per event, but its body is async, so without this chain
  // two events' fetchJSON calls could resolve out of order and prepend feed
  // rows in the wrong sequence.
  let queue = Promise.resolve();
  es.onmessage = (ev) => {
    queue = queue.then(() => handleStreamEvent(ev));
  };
  es.onerror = () => {
    // EventSource retries on its own; nothing else to do here.
  };
}

async function handleStreamEvent(ev) {
  let evt;
  try { evt = JSON.parse(ev.data); } catch (e) { return; }

  if (evt.type === "request") {
    try {
      const req = await fetchJSON(`/api/requests/${evt.id}`);
      bumpTotals(req);
      if (!state.paused) prependFeedRow(req);
    } catch (e) { console.error("stream request fetch", e); }
  } else if (evt.type === "warnings") {
    markFeedRowWarned(evt.id);
    // Debounced to one refetch per burst. A multi-turn warning streak fires N
    // events back to back, and N summary fetches make the table visibly
    // flicker through N intermediate states on the way to the same answer.
    if (!document.getElementById("view-warnings").hidden) {
      clearTimeout(state.warningsDebounceTimer);
      state.warningsDebounceTimer = setTimeout(loadWarnings, 250);
    }
  }
}

// ---- boot ---------------------------------------------------------------

// loadHealth reads the one piece of server state the dashboard needs to render
// correctly: whether the replay endpoint is enabled. Everything else about the
// page is derived from the data endpoints.
async function loadHealth() {
  try {
    const h = await fetchJSON("/api/health");
    state.replayEnabled = !!h.replay_enabled;
  } catch (e) {
    console.error("loadHealth", e);
  }
}

loadHealth();
loadInitialFeed();
connectStream();
