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

const views = ["feed", "warnings", "stats", "sessions"];
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

async function loadStats() {
  try {
    const data = await fetchJSON("/api/stats");
    renderStatsSummary(data.summary, data.cost_sources || []);
    renderStatsChart(data.by_day || []);
    renderStatsByModel(data.by_model || []);
  } catch (e) {
    console.error("loadStats", e);
  }
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

function renderStatsChart(byDay) {
  const svg = document.getElementById("stats-chart");
  const W = 600, H = 200, padL = 34, padB = 20, padT = 10, padR = 10;
  if (byDay.length === 0) {
    svg.innerHTML = `<text x="16" y="100">no data yet</text>`;
    return;
  }
  const maxCount = Math.max(1, ...byDay.map((d) => d.RequestCount));
  const plotW = W - padL - padR;
  const plotH = H - padT - padB;
  const step = byDay.length > 1 ? plotW / (byDay.length - 1) : 0;

  const points = byDay.map((d, i) => {
    const x = padL + i * step;
    const y = padT + plotH - (d.RequestCount / maxCount) * plotH;
    return [x, y];
  });

  const linePath = points.map((p, i) => (i === 0 ? "M" : "L") + p[0].toFixed(1) + "," + p[1].toFixed(1)).join(" ");
  const dots = points.map((p) => `<circle class="point" cx="${p[0].toFixed(1)}" cy="${p[1].toFixed(1)}" r="2.5"></circle>`).join("");

  const everyN = Math.ceil(byDay.length / 6) || 1;
  const labels = byDay.map((d, i) => (i % everyN === 0
    ? `<text x="${points[i][0].toFixed(1)}" y="${H - 4}" text-anchor="middle">${escapeHtml(d.Day.slice(5))}</text>`
    : "")).join("");

  svg.setAttribute("viewBox", `0 0 ${W} ${H}`);
  svg.innerHTML = `
    <line class="axis" x1="${padL}" y1="${padT}" x2="${padL}" y2="${padT + plotH}"></line>
    <line class="axis" x1="${padL}" y1="${padT + plotH}" x2="${W - padR}" y2="${padT + plotH}"></line>
    <text x="4" y="${padT + 8}">${maxCount}</text>
    <text x="4" y="${padT + plotH}">0</text>
    <path class="line" d="${linePath}"></path>
    ${dots}
    ${labels}
  `;
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
