"use strict";

const $ = (s) => document.querySelector(s);
const ROW_H = 26;

// ─── state ──────────────────────────────────────────────────────────────────

const S = {
  columns: [], rows: [], cells: [],
  curRow: 0, curCol: 0,
  detail: false,
  searching: false, searchQuery: "", searchMatches: [], searchIdx: -1,
  loading: false,
  history: JSON.parse(localStorage.getItem("jacques-history") || "[]"),
};

// ─── boot ───────────────────────────────────────────────────────────────────

document.addEventListener("DOMContentLoaded", async () => {
  await loadConnections();
  $("#grid-container").addEventListener("keydown", onGridKey);
  $("#grid-scroll").addEventListener("scroll", renderViewport);
  $("#run-btn").addEventListener("click", runQuery);
  $("#detail-close").addEventListener("click", closeDetail);
  $("#history-btn").addEventListener("click", openHistory);
  $("#history-overlay").addEventListener("click", (e) => {
    if (e.target === $("#history-overlay")) closeHistory();
  });
  $("#history-filter").addEventListener("input", renderHistory);
  $("#query-input").addEventListener("keydown", (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter") { e.preventDefault(); runQuery(); }
    if (e.key === "Tab") {
      e.preventDefault();
      const ta = e.target, s = ta.selectionStart;
      ta.value = ta.value.slice(0, s) + "  " + ta.value.slice(ta.selectionEnd);
      ta.selectionStart = ta.selectionEnd = s + 2;
    }
  });
  window.addEventListener("resize", () => { computeColWidths(); renderAll(); });
  $("#grid-container").focus();
});

async function loadConnections() {
  try {
    const conns = await (await fetch("/api/connections")).json();
    const sel = $("#conn-select");
    sel.innerHTML = "";
    for (const c of conns) {
      const o = document.createElement("option");
      o.value = c.name;
      o.textContent = `${c.name} (${c.type})`;
      if (c.current) o.selected = true;
      sel.appendChild(o);
    }
  } catch (e) { setStatus("failed to load connections: " + e.message, "error"); }
}

// ─── query execution ────────────────────────────────────────────────────────

async function runQuery() {
  const query = $("#query-input").value.trim();
  if (!query || S.loading) return;

  S.loading = true;
  $("#run-btn").disabled = true;
  setStatus("querying…");

  try {
    const conn = $("#conn-select").value;
    const res = await fetch("/api/query", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ connection: conn, query }),
    });
    const data = await res.json();
    if (data.error) { setStatus(data.error, "error"); return; }

    S.columns = data.columns || [];
    S.rows = data.rows || [];
    S.cells = data.cells || [];
    S.curRow = 0; S.curCol = 0; S.detail = false;
    S.searchQuery = ""; S.searchMatches = []; S.searchIdx = -1;
    closeDetail();

    addHistory(conn, query, S.rows.length, data.elapsed);
    computeColWidths();
    renderAll();
    setStatus(`${S.rows.length} rows in ${data.elapsed}`, "success");
    $("#grid-container").focus();
  } catch (e) { setStatus("error: " + e.message, "error"); }
  finally { S.loading = false; $("#run-btn").disabled = false; }
}

// ─── column widths ──────────────────────────────────────────────────────────

let colWidths = [];

function computeColWidths() {
  if (!S.columns.length) { colWidths = []; return; }
  const ch = 7.8; // approximate monospace char width at 13px
  colWidths = S.columns.map((col, ci) => {
    let max = col.Name.length;
    const sample = Math.min(S.cells.length, 200);
    for (let r = 0; r < sample; r++) {
      const len = (S.cells[r]?.[ci] ?? "").length;
      if (len > max) max = len;
    }
    const px = Math.max(60, Math.min(400, max * ch + 20));
    return Math.round(px);
  });
}

function totalWidth() { return colWidths.reduce((a, b) => a + b, 0); }

// ─── virtual scroll rendering ───────────────────────────────────────────────

function renderAll() {
  renderHeader();
  const scroll = $("#grid-scroll");
  const totalH = S.cells.length * ROW_H;
  $("#grid-spacer").style.height = totalH + "px";
  renderViewport();
  scrollCursorIntoView();
}

function renderHeader() {
  const hdr = $("#grid-header");
  hdr.innerHTML = "";
  hdr.style.width = totalWidth() + "px";
  for (let c = 0; c < S.columns.length; c++) {
    const div = document.createElement("div");
    div.className = "hcell";
    div.style.width = colWidths[c] + "px";
    div.textContent = S.columns[c].Name;
    div.dataset.col = c;
    if (isColSearchMatch(c)) div.classList.add("col-match");
    hdr.appendChild(div);
  }
}

function renderViewport() {
  const scroll = $("#grid-scroll");
  const vp = $("#grid-viewport");
  const scrollTop = scroll.scrollTop;
  const viewH = scroll.clientHeight;

  const firstRow = Math.max(0, Math.floor(scrollTop / ROW_H) - 5);
  const lastRow = Math.min(S.cells.length - 1, Math.ceil((scrollTop + viewH) / ROW_H) + 5);
  if (lastRow < 0) { vp.innerHTML = ""; return; }

  vp.style.transform = `translateY(${firstRow * ROW_H}px)`;
  vp.style.width = totalWidth() + "px";

  // recycle or rebuild
  const frag = document.createDocumentFragment();
  for (let r = firstRow; r <= lastRow; r++) {
    const row = document.createElement("div");
    row.className = "vrow";
    if (r % 2 === 1) row.classList.add("alt");
    if (r === S.curRow) row.classList.add("cursor-row");

    for (let c = 0; c < S.columns.length; c++) {
      const cell = document.createElement("div");
      cell.className = "vcell";
      cell.style.width = colWidths[c] + "px";

      const text = S.cells[r]?.[c] ?? "";
      const isCur = r === S.curRow && c === S.curCol;
      const isMatch = isCellSearchMatch(r, c);

      if (isCur) cell.classList.add("cursor-cell");
      if (isMatch) {
        cell.classList.add("cell-match");
        cell.innerHTML = highlightText(text);
      } else {
        cell.textContent = text;
      }
      row.appendChild(cell);
    }
    frag.appendChild(row);
  }
  vp.innerHTML = "";
  vp.appendChild(frag);
}

function scrollCursorIntoView() {
  const scroll = $("#grid-scroll");
  const top = S.curRow * ROW_H;
  const viewH = scroll.clientHeight;
  if (top < scroll.scrollTop) scroll.scrollTop = top;
  else if (top + ROW_H > scroll.scrollTop + viewH) scroll.scrollTop = top + ROW_H - viewH;

  // horizontal: scroll to cursor column
  const left = colWidths.slice(0, S.curCol).reduce((a, b) => a + b, 0);
  const right = left + colWidths[S.curCol];
  const viewW = scroll.clientWidth;
  if (left < scroll.scrollLeft) scroll.scrollLeft = left;
  else if (right > scroll.scrollLeft + viewW) scroll.scrollLeft = right - viewW;
}

// ─── keyboard ───────────────────────────────────────────────────────────────

function onGridKey(e) {
  if (S.searching) { handleSearchKey(e); return; }
  if (document.activeElement === $("#query-input")) return;

  const nr = S.cells.length, nc = S.columns.length;
  if (nr === 0 && e.key !== "/" && e.key !== "Escape") return;

  const pageRows = Math.floor($("#grid-scroll").clientHeight / ROW_H);
  let handled = true;

  switch (e.key) {
    case "j": case "ArrowDown":  move(1, 0); break;
    case "k": case "ArrowUp":    move(-1, 0); break;
    case "h": case "ArrowLeft":  move(0, -1); break;
    case "l": case "ArrowRight": move(0, 1); break;
    case "g": S.curRow = 0; break;
    case "G": S.curRow = nr - 1; break;
    case "0": S.curCol = 0; break;
    case "$": S.curCol = nc - 1; break;
    case "d": if (e.ctrlKey) { S.curRow = Math.min(S.curRow + Math.floor(pageRows / 2), nr - 1); } else handled = false; break;
    case "u": if (e.ctrlKey) { S.curRow = Math.max(S.curRow - Math.floor(pageRows / 2), 0); } else handled = false; break;
    case "PageDown": S.curRow = Math.min(S.curRow + pageRows, nr - 1); break;
    case "PageUp":   S.curRow = Math.max(S.curRow - pageRows, 0); break;
    case " ": case "Enter": toggleDetail(); break;
    case "Escape":
      if (S.detail) closeDetail();
      else if (S.searchQuery) { S.searchQuery = ""; S.searchMatches = []; }
      break;
    case "y": yankCell(); break;
    case "Y": yankRow(); break;
    case "/": e.preventDefault(); openSearch(); return;
    case "n": nextMatch(1); break;
    case "N": nextMatch(-1); break;
    default: handled = false;
  }
  if (handled) { e.preventDefault(); renderViewport(); scrollCursorIntoView(); updateStatusBar(); }
}

function move(dr, dc) {
  const nr = S.cells.length, nc = S.columns.length;
  S.curRow = Math.max(0, Math.min(nr - 1, S.curRow + dr));
  S.curCol = Math.max(0, Math.min(nc - 1, S.curCol + dc));
  if (S.detail) updateDetail();
}

// ─── search ─────────────────────────────────────────────────────────────────

function parseSearchQuery(raw) {
  const o = { pattern: "", columnOnly: false, caseSensitive: false };
  let p = "";
  for (let i = 0; i < raw.length; i++) {
    if (raw[i] === "\\" && i + 1 < raw.length) {
      const n = raw[++i];
      if (n === "j") o.columnOnly = true;
      else if (n === "c") o.caseSensitive = false;
      else if (n === "C") o.caseSensitive = true;
      else if (n === "\\") p += "\\";
      else p += "\\" + n;
    } else p += raw[i];
  }
  o.pattern = p;
  return o;
}

function openSearch() {
  S.searching = true;
  const bar = $("#search-bar"); bar.classList.add("open");
  const inp = $("#search-input"); inp.value = S.searchQuery; inp.focus(); inp.select();
}

function handleSearchKey(e) {
  if (e.key === "Enter") {
    e.preventDefault();
    S.searchQuery = $("#search-input").value;
    S.searching = false;
    $("#search-bar").classList.remove("open");
    runSearch();
    if (S.searchMatches.length > 0) { S.searchIdx = 0; jumpToMatch(); }
    renderViewport(); renderHeader(); updateStatusBar();
    $("#grid-container").focus();
  } else if (e.key === "Escape") {
    e.preventDefault();
    S.searching = false;
    $("#search-bar").classList.remove("open");
    $("#grid-container").focus();
  }
}

function runSearch() {
  S.searchMatches = [];
  if (!S.searchQuery) return;
  const opts = parseSearchQuery(S.searchQuery);
  if (!opts.pattern) return;

  const match = opts.caseSensitive
    ? (h, n) => h.includes(n)
    : (h, n) => h.toLowerCase().includes(n.toLowerCase());

  if (opts.columnOnly) {
    for (let c = 0; c < S.columns.length; c++)
      if (match(S.columns[c].Name, opts.pattern))
        S.searchMatches.push({ row: -1, col: c });
  } else {
    for (let r = 0; r < S.cells.length; r++)
      for (let c = 0; c < (S.cells[r]?.length ?? 0); c++)
        if (match(S.cells[r][c] ?? "", opts.pattern))
          S.searchMatches.push({ row: r, col: c });
  }
}

function nextMatch(dir) {
  if (!S.searchMatches.length) return;
  S.searchIdx = (S.searchIdx + dir + S.searchMatches.length) % S.searchMatches.length;
  jumpToMatch();
  renderViewport(); updateStatusBar();
}

function jumpToMatch() {
  const m = S.searchMatches[S.searchIdx];
  if (!m) return;
  if (m.row >= 0) S.curRow = m.row;
  S.curCol = m.col;
  scrollCursorIntoView();
}

function isColSearchMatch(c) {
  return S.searchMatches.some(m => m.row === -1 && m.col === c);
}
function isCellSearchMatch(r, c) {
  return S.searchQuery && S.searchMatches.some(m => m.row === r && m.col === c);
}

function highlightText(text) {
  if (!S.searchQuery) return esc(text);
  const opts = parseSearchQuery(S.searchQuery);
  if (!opts.pattern) return esc(text);
  const needle = opts.caseSensitive ? opts.pattern : opts.pattern.toLowerCase();
  const haystack = opts.caseSensitive ? text : text.toLowerCase();
  let out = "", pos = 0, idx = haystack.indexOf(needle, pos);
  while (idx >= 0) {
    out += esc(text.slice(pos, idx));
    out += `<span class="search-hl">${esc(text.slice(idx, idx + needle.length))}</span>`;
    pos = idx + needle.length;
    idx = haystack.indexOf(needle, pos);
  }
  return out + esc(text.slice(pos));
}

function esc(s) { return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }

// ─── detail panel ───────────────────────────────────────────────────────────

function toggleDetail() {
  if (S.detail) closeDetail();
  else { S.detail = true; updateDetail(); $("#detail-panel").classList.add("open"); }
}

function updateDetail() {
  const r = S.curRow, c = S.curCol;
  if (r >= S.rows.length) return;
  const raw = S.rows[r]?.[c];
  const colName = S.columns[c]?.Name ?? `col ${c}`;
  $("#detail-title").textContent = `${colName} — row ${r + 1}`;
  $("#detail-content").textContent = prettyValue(raw);
}

function closeDetail() {
  S.detail = false;
  $("#detail-panel").classList.remove("open");
}

function prettyValue(v) {
  if (v == null) return "";
  if (typeof v === "object") return JSON.stringify(v, null, 2);
  if (typeof v === "string") {
    try { const p = JSON.parse(v); if (typeof p === "object") return JSON.stringify(p, null, 2); } catch {}
    return v;
  }
  return String(v);
}

// ─── yank ───────────────────────────────────────────────────────────────────

function yankCell() {
  const text = S.detail ? prettyValue(S.rows[S.curRow]?.[S.curCol]) : (S.cells[S.curRow]?.[S.curCol] ?? "");
  navigator.clipboard.writeText(text).catch(() => {});
  setStatus(`yanked: ${text.length > 60 ? text.slice(0, 59) + "…" : text}`);
}

function yankRow() {
  const text = (S.cells[S.curRow] ?? []).join("\t");
  navigator.clipboard.writeText(text).catch(() => {});
  setStatus("yanked row");
}

// ─── query history ──────────────────────────────────────────────────────────

function addHistory(conn, query, rows, elapsed) {
  S.history.unshift({ conn, query, rows, elapsed, time: new Date().toISOString() });
  if (S.history.length > 100) S.history.length = 100;
  localStorage.setItem("jacques-history", JSON.stringify(S.history));
}

function openHistory() {
  $("#history-overlay").classList.add("open");
  $("#history-filter").value = "";
  renderHistory();
  $("#history-filter").focus();
}

function closeHistory() { $("#history-overlay").classList.remove("open"); }

function renderHistory() {
  const filter = ($("#history-filter").value || "").toLowerCase();
  const ul = $("#history-list");
  ul.innerHTML = "";
  for (const h of S.history) {
    if (filter && !h.query.toLowerCase().includes(filter) && !h.conn.toLowerCase().includes(filter)) continue;
    const li = document.createElement("li");
    const time = new Date(h.time);
    const ago = formatAgo(time);
    li.innerHTML = `<span class="hist-time">${ago} · ${h.rows} rows · ${h.elapsed}</span>`
      + `<span class="hist-conn">${esc(h.conn)}</span>`
      + `<span class="hist-query">${esc(h.query)}</span>`;
    li.addEventListener("click", () => {
      $("#query-input").value = h.query;
      $("#conn-select").value = h.conn;
      closeHistory();
      $("#query-input").focus();
    });
    ul.appendChild(li);
  }
}

function formatAgo(d) {
  const s = Math.floor((Date.now() - d.getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}

// ─── status ─────────────────────────────────────────────────────────────────

function setStatus(msg, cls) {
  const el = $("#status");
  el.textContent = msg;
  el.className = cls || "";
}

function updateStatusBar() {
  if (!S.cells.length) { $("#pos").textContent = ""; $("#search-status").textContent = ""; return; }
  const cn = S.columns[S.curCol]?.Name ?? "";
  $("#pos").textContent = `row ${S.curRow + 1}/${S.cells.length}  col ${S.curCol + 1}/${S.columns.length} [${cn}]`;
  if (S.searchMatches.length > 0)
    $("#search-status").textContent = `/${S.searchQuery} [${S.searchIdx + 1}/${S.searchMatches.length}]`;
  else if (S.searchQuery)
    $("#search-status").textContent = `/${S.searchQuery} [no matches]`;
  else
    $("#search-status").textContent = "";
}
