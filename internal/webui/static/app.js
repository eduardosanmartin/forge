"use strict";

// forge session GUI (RF-7.2/7.3). No framework, no build step: this file is
// served as-is by internal/webui. Talks to the daemon exclusively over the
// same JSON-RPC-over-WebSocket API the CLI/TUI use (internal/daemon/rpc.go).

// ------------------------------------------------------------------ RPC ---

const RPC = (() => {
  let ws = null;
  let nextId = 1;
  const pending = new Map();
  const notifyHandlers = new Set();
  let onStatus = () => {};

  function connect() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    ws = new WebSocket(`${proto}//${location.host}/ws`);

    ws.onopen = () => onStatus(true);
    ws.onclose = () => {
      onStatus(false);
      setTimeout(connect, 2000);
    };
    ws.onerror = () => ws.close();

    ws.onmessage = (evt) => {
      let msg;
      try {
        msg = JSON.parse(evt.data);
      } catch {
        return;
      }
      if (msg.id !== undefined && msg.id !== null) {
        const p = pending.get(msg.id);
        if (!p) return;
        pending.delete(msg.id);
        if (msg.error) p.reject(new Error(msg.error.message || "rpc error"));
        else p.resolve(msg.result);
        return;
      }
      if (msg.method) {
        for (const h of notifyHandlers) h(msg.method, msg.params);
      }
    };
  }

  function call(method, params) {
    return new Promise((resolve, reject) => {
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        reject(new Error("not connected"));
        return;
      }
      const id = nextId++;
      pending.set(id, { resolve, reject });
      ws.send(JSON.stringify({ jsonrpc: "2.0", id, method, params: params || {} }));
    });
  }

  return {
    connect,
    call,
    onNotify: (fn) => notifyHandlers.add(fn),
    onStatusChange: (fn) => { onStatus = fn; },
  };
})();

// ------------------------------------------------------------ ui prefs ---

const UI_KEYS = { rail: "forge.ui.railCollapsed", res: "forge.ui.resourcesOpen", tab: "forge.ui.resourcesTab" };

function loadPref(key, fallback) {
  try {
    const v = localStorage.getItem(key);
    return v === null ? fallback : v;
  } catch {
    return fallback;
  }
}
function savePref(key, value) {
  try { localStorage.setItem(key, value); } catch { /* private mode etc: UI prefs just don't persist */ }
}

// --------------------------------------------------------------- state ---

const state = {
  sessions: [],
  selectedId: null,
  messages: [],
  lastModelUsed: {},
  railCollapsed: loadPref(UI_KEYS.rail, "0") === "1",
  resourcesOpen: loadPref(UI_KEYS.res, "0") === "1",
  resourcesTab: loadPref(UI_KEYS.tab, "compare"),
  compareTarget: null,
};

const el = {};
[
  "app", "topbar", "railToggle", "sessionTitle", "statusDot", "statusText", "haltBtn", "resourcesToggle",
  "body", "rail", "railTop", "sessionSearch", "newSessionBtn", "sessionList",
  "main", "threadScroll", "thread", "composerWrap", "composeInput", "composeSend", "composeError",
  "footerModel", "footerTokens",
  "resources", "resTabs", "resClose", "resBody",
  "loginOverlay", "loginForm", "loginPassword", "loginError",
].forEach((k) => { /* placeholder, filled in init() */ });

function bindEls() {
  el.railToggle = document.getElementById("rail-toggle");
  el.sessionTitle = document.getElementById("session-title");
  el.statusDot = document.getElementById("status-dot");
  el.statusText = document.getElementById("status-text");
  el.haltBtn = document.getElementById("halt-btn");
  el.resourcesToggle = document.getElementById("resources-toggle");
  el.rail = document.getElementById("rail");
  el.sessionSearch = document.getElementById("session-search");
  el.newSessionBtn = document.getElementById("new-session-btn");
  el.sessionList = document.getElementById("session-list");
  el.threadScroll = document.getElementById("thread-scroll");
  el.thread = document.getElementById("thread");
  el.composerWrap = document.getElementById("composer-wrap");
  el.composeInput = document.getElementById("compose-input");
  el.composeSend = document.getElementById("compose-send");
  el.composeError = document.getElementById("compose-error");
  el.footerModel = document.getElementById("footer-model");
  el.footerTokens = document.getElementById("footer-tokens");
  el.resources = document.getElementById("resources");
  el.resTabs = document.getElementById("res-tabs");
  el.resClose = document.getElementById("res-close");
  el.resBody = document.getElementById("res-body");
  el.loginOverlay = document.getElementById("login-overlay");
  el.loginForm = document.getElementById("login-form");
  el.loginPassword = document.getElementById("login-password");
  el.loginError = document.getElementById("login-error");
}

// --------------------------------------------------------------- utils ---

function shortId(id) { return id && id.length > 8 ? id.slice(0, 8) : id; }
// All created_at/updated_at fields from the daemon are unix milliseconds
// (store.nowMs()/time.UnixMilli()) — NOT seconds.
function fmtTime(unixMs) {
  if (!unixMs) return "";
  return new Date(unixMs).toLocaleString();
}
function fmtRelative(unixMs) {
  if (!unixMs) return "";
  const diffMs = Date.now() - unixMs;
  const m = Math.round(diffMs / 60000);
  if (m < 1) return "now";
  if (m < 60) return `${m}m`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h`;
  return `${Math.round(h / 24)}d`;
}
function pad2(n) { return String(n).padStart(2, "0"); }
// "[DD-MM-YYYY:HH:mm]" prefix shown next to each session in the rail.
function fmtDatePrefix(unixMs) {
  if (!unixMs) return "";
  const d = new Date(unixMs);
  return `[${pad2(d.getDate())}-${pad2(d.getMonth() + 1)}-${d.getFullYear()}:${pad2(d.getHours())}:${pad2(d.getMinutes())}]`;
}
// Elapsed time as "2h45m35s", dropping leading zero units ("35s", "45m35s").
function fmtDurationHMS(ms) {
  if (!ms || ms < 0) return "";
  const totalSec = Math.round(ms / 1000);
  const h = Math.floor(totalSec / 3600);
  const m = Math.floor((totalSec % 3600) / 60);
  const s = totalSec % 60;
  if (h > 0) return `${h}h${pad2(m)}m${pad2(s)}s`;
  if (m > 0) return `${m}m${pad2(s)}s`;
  return `${s}s`;
}
function escapeHtml(s) {
  const d = document.createElement("div");
  d.textContent = s == null ? "" : String(s);
  return d.innerHTML;
}
function fmtUSD(n) { return "$" + Number(n || 0).toFixed(4); }

// --------------------------------------------------------- panel toggles ---

function applyRailState() {
  el.rail.classList.toggle("collapsed", state.railCollapsed);
  el.railToggle.classList.toggle("on", !state.railCollapsed);
}
function toggleRail() {
  state.railCollapsed = !state.railCollapsed;
  savePref(UI_KEYS.rail, state.railCollapsed ? "1" : "0");
  applyRailState();
}

function applyResourcesState() {
  el.resources.classList.toggle("collapsed", !state.resourcesOpen);
  el.resourcesToggle.classList.toggle("on", state.resourcesOpen);
}
function openResources(tab) {
  state.resourcesOpen = true;
  savePref(UI_KEYS.res, "1");
  if (tab) selectResourceTab(tab);
  applyResourcesState();
}
function toggleResources() {
  state.resourcesOpen = !state.resourcesOpen;
  savePref(UI_KEYS.res, state.resourcesOpen ? "1" : "0");
  applyResourcesState();
  if (state.resourcesOpen) renderResourceTab();
}

const RESOURCE_RENDERERS = {}; // tab name -> async render function, filled below

function selectResourceTab(tab) {
  state.resourcesTab = tab;
  savePref(UI_KEYS.tab, tab);
  for (const btn of el.resTabs.querySelectorAll(".res-tab")) {
    btn.classList.toggle("on", btn.dataset.tab === tab);
  }
  renderResourceTab();
}
function renderResourceTab() {
  if (!state.resourcesOpen) return;
  const fn = RESOURCE_RENDERERS[state.resourcesTab];
  el.resBody.innerHTML = "";
  if (fn) fn(el.resBody);
}

// ------------------------------------------------------------- sessions ---

async function refreshSessions() {
  try {
    const res = await RPC.call("session.list", { limit: 200 });
    state.sessions = res.sessions || [];
    renderSidebar();
    if (state.selectedId) renderTopbarTitle();
  } catch (e) {
    console.error("session.list failed", e);
  }
}

function renderSidebar() {
  const q = (el.sessionSearch.value || "").trim().toLowerCase();
  el.sessionList.innerHTML = "";
  const sorted = [...state.sessions].sort((a, b) => b.updated_at - a.updated_at);
  const filtered = q ? sorted.filter((s) => s.id.toLowerCase().includes(q)) : sorted;
  if (filtered.length === 0) {
    const d = document.createElement("div");
    d.className = "empty";
    d.textContent = state.sessions.length === 0 ? "No sessions yet." : "No sessions match.";
    el.sessionList.appendChild(d);
    return;
  }
  for (const s of filtered) {
    const btn = document.createElement("button");
    btn.className = "srow" + (s.id === state.selectedId ? " active" : "");
    btn.type = "button";
    btn.onclick = () => selectSession(s.id);

    const title = document.createElement("div");
    title.className = "srow-title";
    title.textContent = `${fmtDatePrefix(s.created_at)}-${(s.metadata && s.metadata.label) || shortId(s.id)}`;
    btn.appendChild(title);

    const meta = document.createElement("div");
    meta.className = "srow-meta";
    meta.innerHTML = `<span>${s.message_count || 0} msgs</span><span>&middot;</span><span>${fmtRelative(s.updated_at)}</span>`;
    btn.appendChild(meta);

    const parent = s.metadata && s.metadata.branch_parent;
    if (parent) {
      const lineage = document.createElement("div");
      lineage.className = "srow-lineage";
      lineage.textContent = `⮡ branched from ${shortId(parent)}`;
      btn.appendChild(lineage);
    }
    el.sessionList.appendChild(btn);
  }
}

async function createNewSession() {
  el.newSessionBtn.disabled = true;
  try {
    const created = await RPC.call("session.create", {});
    await refreshSessions();
    await selectSession(created.id);
  } catch (e) {
    alert("Could not create a session: " + e.message);
  } finally {
    el.newSessionBtn.disabled = false;
  }
}

async function selectSession(id) {
  state.selectedId = id;
  state.compareTarget = null;
  renderSidebar();
  renderTopbarTitle();
  el.composerWrap.hidden = false;
  await loadMessages();
  renderThread();
  if (state.resourcesOpen && state.resourcesTab === "compare") renderResourceTab();
  if (state.resourcesOpen && state.resourcesTab === "cost") renderResourceTab();
}

function currentSession() {
  return state.sessions.find((s) => s.id === state.selectedId) || null;
}

function renderTopbarTitle() {
  const s = currentSession();
  el.sessionTitle.textContent = s ? ((s.metadata && s.metadata.label) || shortId(s.id)) : "Select a session";
  const model = (s && s.metadata && s.metadata.model) || state.lastModelUsed[state.selectedId] || "";
  el.footerModel.textContent = model ? model : "daemon default";
}

// -------------------------------------------------------------- thread ---

async function loadMessages() {
  if (!state.selectedId) return;
  try {
    const res = await RPC.call("session.get_messages", { session_id: state.selectedId, limit: 1000 });
    state.messages = res.messages || [];
  } catch (e) {
    console.error("session.get_messages failed", e);
    state.messages = [];
  }
}

function renderThread() {
  el.thread.innerHTML = "";
  if (!state.selectedId) {
    const d = document.createElement("div");
    d.className = "empty-state";
    d.textContent = "Select a session on the left, or start a new one.";
    el.thread.appendChild(d);
    return;
  }
  if (state.messages.length === 0) {
    const d = document.createElement("div");
    d.className = "empty-state";
    d.textContent = "No messages yet — say something below.";
    el.thread.appendChild(d);
    return;
  }
  for (const m of state.messages) {
    const turn = document.createElement("div");
    turn.className = "turn";

    if (m.role === "user") {
      const row = document.createElement("div");
      row.className = "u-row";
      const bubble = document.createElement("div");
      bubble.className = "u-bubble";
      bubble.textContent = m.content || "";
      row.appendChild(bubble);
      turn.appendChild(row);
    } else if (m.role === "tool") {
      const block = document.createElement("div");
      block.className = "a-block";
      block.innerHTML = `<div class="a-mark tool">t</div>`;
      const chip = document.createElement("div");
      chip.className = "tool-chip";
      chip.style.flex = "1";
      chip.innerHTML = `<span class="name">${escapeHtml(m.name || "tool")}</span><span class="args">${escapeHtml(truncate(m.content, 200))}</span>`;
      block.appendChild(chip);
      turn.appendChild(block);
    } else {
      const block = document.createElement("div");
      block.className = "a-block";
      const mark = document.createElement("div");
      mark.className = "a-mark";
      mark.textContent = "f";
      block.appendChild(mark);

      const bodyWrap = document.createElement("div");
      bodyWrap.style.flex = "1";

      const { reasoning, answer } = splitReasoning(m.content);
      if (reasoning) {
        const toggle = document.createElement("button");
        toggle.type = "button";
        toggle.className = "reasoning-toggle";
        toggle.textContent = "razonamiento";
        const reasoningBody = document.createElement("div");
        reasoningBody.className = "a-body reasoning-body";
        reasoningBody.textContent = reasoning;
        reasoningBody.hidden = true;
        toggle.onclick = () => {
          reasoningBody.hidden = !reasoningBody.hidden;
          toggle.classList.toggle("open", !reasoningBody.hidden);
        };
        bodyWrap.appendChild(toggle);
        bodyWrap.appendChild(reasoningBody);
      }

      const body = document.createElement("div");
      body.className = "a-body";
      body.textContent = answer;
      bodyWrap.appendChild(body);
      block.appendChild(bodyWrap);

      if (m.tool_calls && m.tool_calls.length) {
        const tools = document.createElement("div");
        tools.className = "tools";
        for (const tc of m.tool_calls) {
          const fn = tc.function || {};
          const row = document.createElement("div");
          row.className = "tool-chip";
          row.innerHTML = `<span class="name">${escapeHtml(fn.name || "")}</span><span class="args">${escapeHtml(truncate(fn.arguments || "", 160))}</span>`;
          tools.appendChild(row);
        }
        block.appendChild(tools);
      }
      turn.appendChild(block);
      const metaParts = [];
      if (m.usage && m.usage.total_tokens) metaParts.push(`${m.usage.total_tokens} tokens`);
      if (m.model) metaParts.push(m.model);
      if (m.duration_ms) metaParts.push(fmtDurationHMS(m.duration_ms));
      if (metaParts.length) {
        const meta = document.createElement("div");
        meta.className = "meta-line";
        meta.textContent = metaParts.join(" · ");
        turn.appendChild(meta);
      }
    }
    el.thread.appendChild(turn);
  }
  el.threadScroll.scrollTop = el.threadScroll.scrollHeight;
}

function truncate(s, n) {
  s = s || "";
  return s.length > n ? s.slice(0, n) + "…" : s;
}

// Some models (e.g. minimax-m3) prefix their answer with a <think>...</think>
// reasoning block. Split it out so the thread can show it behind a toggle
// instead of dumping raw internal monologue into the visible answer.
function splitReasoning(content) {
  content = content || "";
  const m = /^\s*<think>([\s\S]*?)<\/think>\s*/.exec(content);
  if (!m) return { reasoning: null, answer: content };
  return { reasoning: m[1].trim(), answer: content.slice(m[0].length) };
}

async function sendMessage() {
  const text = el.composeInput.value.trim();
  if (!text || !state.selectedId) return;
  const sessionId = state.selectedId;

  el.composeInput.disabled = true;
  el.composeSend.disabled = true;
  el.composeError.hidden = true;
  el.composeInput.value = "";
  autoGrow();

  // Show the user's message and a "thinking" placeholder immediately —
  // execute_turn blocks until the model call finishes, which can take a
  // while, and a silent UI in that window is indistinguishable from a
  // broken send button.
  if (state.messages.length === 0) el.thread.innerHTML = "";
  const optimisticRow = document.createElement("div");
  optimisticRow.className = "turn";
  optimisticRow.innerHTML = `<div class="u-row"><div class="u-bubble"></div></div>`;
  optimisticRow.querySelector(".u-bubble").textContent = text;
  el.thread.appendChild(optimisticRow);
  const thinking = document.createElement("div");
  thinking.className = "turn thinking-row";
  thinking.innerHTML = `<div class="a-block"><div class="a-mark">f</div><div class="a-body thinking-dots">thinking</div></div>`;
  el.thread.appendChild(thinking);
  el.threadScroll.scrollTop = el.threadScroll.scrollHeight;

  try {
    const result = await RPC.call("session.execute_turn", { session_id: sessionId, user_message: text });
    if (result && result.model) state.lastModelUsed[sessionId] = result.model;
    await loadMessages();
    if (state.selectedId === sessionId) renderThread();
    renderTopbarTitle();
    if (result && result.usage) {
      const parts = [`${result.usage.total_tokens} tokens this turn`];
      if (result.model) parts.push(result.model);
      const lastAssistant = (result.messages || []).filter((m) => m.role === "assistant").pop();
      if (lastAssistant && lastAssistant.duration_ms) parts.push(fmtDurationHMS(lastAssistant.duration_ms));
      el.footerTokens.textContent = parts.join(" · ");
    }
  } catch (e) {
    thinking.remove();
    el.composeError.textContent = "Send failed: " + e.message;
    el.composeError.hidden = false;
  } finally {
    el.composeInput.disabled = false;
    el.composeSend.disabled = false;
    el.composeInput.focus();
  }
}

function autoGrow() {
  el.composeInput.style.height = "auto";
  el.composeInput.style.height = Math.min(el.composeInput.scrollHeight, 160) + "px";
}

// ---------------------------------------------------------- resources: compare ---

RESOURCE_RENDERERS.compare = (root) => {
  const s = currentSession();
  if (!s) { root.innerHTML = `<div class="res-empty">Select a session first.</div>`; return; }

  const wrap = document.createElement("div");
  wrap.style.display = "flex";
  wrap.style.flexDirection = "column";
  wrap.style.gap = "12px";

  const h = document.createElement("div");
  h.className = "res-h";
  h.innerHTML = `<svg class="icon"><use href="#i-compare"/></svg>Compare ${escapeHtml(shortId(s.id))} with&#8230;`;
  wrap.appendChild(h);

  const selectRow = document.createElement("div");
  selectRow.className = "compare-select";
  const select = document.createElement("select");
  const none = document.createElement("option");
  none.value = ""; none.textContent = "Choose a session…";
  select.appendChild(none);
  for (const other of state.sessions) {
    if (other.id === s.id) continue;
    const opt = document.createElement("option");
    opt.value = other.id;
    opt.textContent = (other.metadata && other.metadata.label) || shortId(other.id);
    if (other.id === state.compareTarget) opt.selected = true;
    select.appendChild(opt);
  }
  selectRow.appendChild(select);
  const goBtn = document.createElement("button");
  goBtn.className = "btn";
  goBtn.textContent = "Compare";
  goBtn.onclick = () => { state.compareTarget = select.value; renderResourceTab(); };
  selectRow.appendChild(goBtn);
  wrap.appendChild(selectRow);

  root.appendChild(wrap);

  if (!state.compareTarget) return;
  const result = document.createElement("div");
  result.className = "res-empty";
  result.textContent = "Loading…";
  root.appendChild(result);

  RPC.call("session.compare", { session_a: s.id, session_b: state.compareTarget }).then((cmp) => {
    result.remove();
    const summary = document.createElement("div");
    summary.className = "res-h";
    summary.style.marginTop = "4px";
    summary.textContent = `Diverged: ${cmp.divergent_count_a} vs ${cmp.divergent_count_b} messages (seq ${cmp.branch_at_seq_a})`;
    root.appendChild(summary);

    for (const [label, list] of [[shortId(cmp.session_a.id), cmp.divergent_a], [shortId(cmp.session_b.id), cmp.divergent_b]]) {
      const label_el = document.createElement("div");
      label_el.className = "res-h";
      label_el.style.marginTop = "8px";
      label_el.textContent = label;
      root.appendChild(label_el);
      if (!list || list.length === 0) {
        const e = document.createElement("div");
        e.className = "res-empty";
        e.textContent = "No divergent messages.";
        root.appendChild(e);
        continue;
      }
      for (const m of list) {
        const row = document.createElement("div");
        row.className = "diff-row " + (m.role === "user" ? "ctx" : "add");
        row.textContent = `[${m.role} #${m.seq}] ${truncate(m.content || "(tool call)", 240)}`;
        root.appendChild(row);
      }
    }
  }).catch((e) => {
    result.textContent = "Compare failed: " + e.message;
    result.className = "res-error";
  });
};

// ---------------------------------------------------------- resources: cost ---

RESOURCE_RENDERERS.cost = (root) => {
  const s = currentSession();
  const wrap = document.createElement("div");
  wrap.innerHTML = `<div class="res-h"><svg class="icon"><use href="#i-coin"/></svg>This session</div>`;
  root.appendChild(wrap);

  if (!s) {
    root.innerHTML += `<div class="res-empty">Select a session first.</div>`;
  } else {
    const box = document.createElement("div");
    box.className = "res-empty";
    box.textContent = "Loading…";
    root.appendChild(box);
    RPC.call("session.cost", { session_id: s.id }).then((c) => {
      box.remove();
      const big = document.createElement("div");
      big.className = "cost-big";
      big.textContent = c.priced ? fmtUSD(c.estimated_usd) : "—";
      root.appendChild(big);
      const sub = document.createElement("div");
      sub.className = "cost-sub";
      sub.textContent = c.priced ? "estimated, this session" : "not priced (no pricing configured for this provider)";
      root.appendChild(sub);

      const table = document.createElement("div");
      table.className = "cost-table";
      table.style.marginTop = "10px";
      table.innerHTML = `
        <div class="cost-row"><span class="l">provider</span><span class="v accent">${escapeHtml(c.provider || "—")}</span></div>
        <div class="cost-row"><span class="l">model</span><span class="v">${escapeHtml(c.model || "daemon default")}</span></div>
        <div class="cost-row"><span class="l">prompt tokens</span><span class="v">${c.prompt_tokens || 0}</span></div>
        <div class="cost-row"><span class="l">completion tokens</span><span class="v">${c.completion_tokens || 0}</span></div>
        <div class="cost-row"><span class="l">total</span><span class="v">${c.total_tokens || 0}</span></div>`;
      root.appendChild(table);
    }).catch((e) => {
      box.textContent = "Failed: " + e.message;
      box.className = "res-error";
    });
  }

  const h2 = document.createElement("div");
  h2.className = "res-h";
  h2.style.marginTop = "6px";
  h2.textContent = "All sessions, by provider";
  root.appendChild(h2);
  const box2 = document.createElement("div");
  box2.className = "res-empty";
  box2.textContent = "Loading…";
  root.appendChild(box2);
  RPC.call("cost.summary", {}).then((res) => {
    box2.remove();
    const providers = res.providers || [];
    if (providers.length === 0) {
      const e = document.createElement("div");
      e.className = "res-empty";
      e.textContent = "No sessions yet.";
      root.appendChild(e);
      return;
    }
    for (const pc of providers) {
      const row = document.createElement("div");
      row.className = "cost-row";
      row.innerHTML = `<span class="l">${escapeHtml(pc.provider)} &middot; ${pc.sessions} sessions</span><span class="v ${pc.priced ? "accent" : "muted"}">${pc.priced ? fmtUSD(pc.estimated_usd) : "not priced"}</span>`;
      root.appendChild(row);
    }
  }).catch((e) => {
    box2.textContent = "Failed: " + e.message;
    box2.className = "res-error";
  });
};

// -------------------------------------------------------- resources: memory ---

RESOURCE_RENDERERS.memory = (root) => {
  const s = currentSession();
  const h = document.createElement("div");
  h.className = "res-h";
  h.textContent = s ? `Anchors for ${shortId(s.id)}` : "All anchors";
  root.appendChild(h);

  const form = document.createElement("div");
  form.className = "anchor-form";
  const ta = document.createElement("textarea");
  ta.placeholder = "Anchor a fact or decision…";
  form.appendChild(ta);
  const addBtn = document.createElement("button");
  addBtn.className = "btn";
  addBtn.textContent = "Add anchor";
  addBtn.onclick = async () => {
    const content = ta.value.trim();
    if (!content) return;
    addBtn.disabled = true;
    try {
      await RPC.call("memory.create", { content, session_id: s ? s.id : undefined, source: "user" });
      ta.value = "";
      renderResourceTab();
    } catch (e) {
      alert("Could not create anchor: " + e.message);
    } finally {
      addBtn.disabled = false;
    }
  };
  form.appendChild(addBtn);
  root.appendChild(form);

  const list = document.createElement("div");
  list.style.display = "flex";
  list.style.flexDirection = "column";
  list.style.gap = "8px";
  list.style.marginTop = "6px";
  const loading = document.createElement("div");
  loading.className = "res-empty";
  loading.textContent = "Loading…";
  root.appendChild(loading);

  RPC.call("memory.list", s ? { session_id: s.id } : {}).then((res) => {
    loading.remove();
    const anchors = res.anchors || [];
    if (anchors.length === 0) {
      const e = document.createElement("div");
      e.className = "res-empty";
      e.textContent = "No anchors yet.";
      root.appendChild(e);
      return;
    }
    for (const a of anchors) {
      const card = document.createElement("div");
      card.className = "anchor-card";
      const content = document.createElement("div");
      content.className = "content";
      content.textContent = a.content;
      card.appendChild(content);
      if (a.tags && a.tags.length) {
        const tags = document.createElement("div");
        tags.className = "tags";
        tags.innerHTML = a.tags.map((t) => `<span class="tag-chip">${escapeHtml(t)}</span>`).join("");
        card.appendChild(tags);
      }
      const foot = document.createElement("div");
      foot.className = "foot";
      foot.innerHTML = `<span class="meta">#${a.id} &middot; ${escapeHtml(a.source)} &middot; ${fmtRelative(a.created_at)}</span>`;
      const del = document.createElement("button");
      del.className = "iconbtn small danger";
      del.innerHTML = `<svg class="icon"><use href="#i-trash"/></svg>`;
      del.title = "Delete anchor";
      del.onclick = async () => {
        del.disabled = true;
        try {
          await RPC.call("memory.delete", { id: a.id });
          renderResourceTab();
        } catch (e) {
          alert("Could not delete anchor (" + e.message + ") — anchoring_delete may be denied by the daemon's permission policy.");
          del.disabled = false;
        }
      };
      foot.appendChild(del);
      card.appendChild(foot);
      list.appendChild(card);
    }
    root.appendChild(list);
  }).catch((e) => {
    loading.textContent = "Failed: " + e.message + (e.message && e.message.includes("denied") ? "" : " (memory may not be configured for this daemon)");
    loading.className = "res-error";
  });
};

// ------------------------------------------------------- resources: plugins/skills ---

function renderToggleableList(root, { list, kind, listMethod, enableMethod, disableMethod, reloadMethod }) {
  const h = document.createElement("div");
  h.className = "res-h";
  h.innerHTML = `${kind}<button class="iconbtn small" style="margin-left:auto" title="Reload"><svg class="icon"><use href="#i-refresh"/></svg></button>`;
  h.querySelector("button").onclick = async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    try {
      await RPC.call(reloadMethod, {});
      renderResourceTab();
    } catch (e) {
      alert("Reload failed: " + e.message);
      btn.disabled = false;
    }
  };
  root.appendChild(h);

  const loading = document.createElement("div");
  loading.className = "res-empty";
  loading.textContent = "Loading…";
  root.appendChild(loading);

  RPC.call(listMethod, {}).then((res) => {
    loading.remove();
    if (!list(res) || list(res).length === 0) {
      const e = document.createElement("div");
      e.className = "res-empty";
      e.textContent = `No ${kind.toLowerCase()} loaded.`;
      root.appendChild(e);
      return;
    }
    for (const item of list(res)) {
      const row = document.createElement("div");
      row.className = "row";
      const main = document.createElement("div");
      main.className = "main-col";
      main.innerHTML = `<span class="name">${escapeHtml(item.name)}</span><span class="sub">${escapeHtml(item.description || item.source || "")}${item.tool_count != null ? ` &middot; ${item.tool_count} tools` : ""}</span>`;
      row.appendChild(main);
      const pill = document.createElement("span");
      pill.className = "pill " + (item.enabled ? "on" : "off");
      pill.textContent = item.enabled ? "enabled" : "disabled";
      row.appendChild(pill);
      const toggle = document.createElement("button");
      toggle.className = "iconbtn small";
      toggle.title = item.enabled ? "Disable" : "Enable";
      toggle.innerHTML = `<svg class="icon"><use href="#i-power"/></svg>`;
      toggle.onclick = async () => {
        toggle.disabled = true;
        try {
          await RPC.call(item.enabled ? disableMethod : enableMethod, { name: item.name });
          renderResourceTab();
        } catch (e) {
          alert("Could not toggle: " + e.message);
          toggle.disabled = false;
        }
      };
      row.appendChild(toggle);
      root.appendChild(row);
    }
  }).catch((e) => {
    loading.textContent = "Failed: " + e.message;
    loading.className = "res-error";
  });
}

RESOURCE_RENDERERS.plugins = (root) => renderToggleableList(root, {
  list: (res) => res.plugins, kind: "Plugins",
  listMethod: "plugin.list", enableMethod: "plugin.enable", disableMethod: "plugin.disable", reloadMethod: "plugin.reload",
});
RESOURCE_RENDERERS.skills = (root) => renderToggleableList(root, {
  list: (res) => res.skills, kind: "Skills",
  listMethod: "skill.list", enableMethod: "skill.enable", disableMethod: "skill.disable", reloadMethod: "skill.reload",
});

// ------------------------------------------------------------ resources: jobs ---

RESOURCE_RENDERERS.jobs = (root) => {
  const h = document.createElement("div");
  h.className = "res-h";
  h.textContent = "Background jobs";
  root.appendChild(h);

  const loading = document.createElement("div");
  loading.className = "res-empty";
  loading.textContent = "Loading…";
  root.appendChild(loading);

  RPC.call("job.list", {}).then((res) => {
    loading.remove();
    const jobs = res.jobs || [];
    if (jobs.length === 0) {
      const e = document.createElement("div");
      e.className = "res-empty";
      e.textContent = "No jobs.";
      root.appendChild(e);
      return;
    }
    for (const j of jobs) {
      const row = document.createElement("div");
      row.className = "row job-row";
      const main = document.createElement("div");
      main.className = "main-col";
      main.innerHTML = `<span class="name">${escapeHtml(shortId(j.session_id))} &middot; seq ${j.seq}</span><span class="sub">#${escapeHtml(j.id)} &middot; ${fmtRelative(j.updated_at)}</span>`;
      row.appendChild(main);
      const pill = document.createElement("span");
      pill.className = "status-pill " + (j.status || "").toLowerCase();
      pill.textContent = j.status;
      row.appendChild(pill);
      if (j.status === "running") {
        const cancel = document.createElement("button");
        cancel.className = "iconbtn small danger";
        cancel.title = "Cancel job";
        cancel.innerHTML = `<svg class="icon"><use href="#i-stop"/></svg>`;
        cancel.onclick = async () => {
          cancel.disabled = true;
          try {
            await RPC.call("job.cancel", { job_id: j.id });
            renderResourceTab();
          } catch (e) {
            alert("Could not cancel: " + e.message);
            cancel.disabled = false;
          }
        };
        row.appendChild(cancel);
      }
      root.appendChild(row);
    }
  }).catch((e) => {
    loading.textContent = "Failed: " + e.message;
    loading.className = "res-error";
  });
};

// ---------------------------------------------------------- resources: daemon ---

RESOURCE_RENDERERS.daemon = (root) => {
  const h = document.createElement("div");
  h.className = "res-h";
  h.innerHTML = `<svg class="icon"><use href="#i-lock"/></svg>Daemon`;
  root.appendChild(h);

  const loading = document.createElement("div");
  loading.className = "res-empty";
  loading.textContent = "Loading…";
  root.appendChild(loading);

  RPC.call("daemon.status", {}).then((s) => {
    loading.remove();
    const table = document.createElement("div");
    table.className = "cost-table";
    table.innerHTML = `
      <div class="cost-row"><span class="l">running</span><span class="v">${s.running ? "yes" : "no"}</span></div>
      <div class="cost-row"><span class="l">sessions</span><span class="v">${s.sessions}</span></div>
      <div class="cost-row"><span class="l">version</span><span class="v">${escapeHtml(s.version)}</span></div>`;
    root.appendChild(table);
  }).catch((e) => {
    loading.textContent = "Failed: " + e.message;
    loading.className = "res-error";
  });

  const h2 = document.createElement("div");
  h2.className = "res-h";
  h2.style.marginTop = "14px";
  h2.textContent = "Emergency";
  root.appendChild(h2);

  const warn = document.createElement("div");
  warn.className = "warn-box";
  warn.textContent = "Halts every running turn on every session immediately. Use if a turn is stuck or misbehaving.";
  root.appendChild(warn);

  const btn = document.createElement("button");
  btn.className = "btn";
  btn.style.marginTop = "8px";
  btn.textContent = "Halt all sessions";
  btn.onclick = () => haltAll(btn);
  root.appendChild(btn);
};

async function haltAll(triggerBtn) {
  if (!confirm("Halt every running session immediately?")) return;
  if (triggerBtn) triggerBtn.disabled = true;
  try {
    await RPC.call("emergency.halt_all", {});
  } catch (e) {
    alert("Halt failed: " + e.message);
  } finally {
    if (triggerBtn) triggerBtn.disabled = false;
  }
}

// -------------------------------------------------------------- auth/login ---

function setStatus(connected) {
  el.statusDot.className = "live-dot " + (connected ? "on" : "off");
  el.statusText.textContent = connected ? "connected" : "reconnecting…";
  if (connected) {
    hideLogin();
    refreshSessions();
  } else {
    checkAuthStatus().then((required) => { if (required) showLogin(); });
  }
}

async function checkAuthStatus() {
  try {
    const res = await fetch("/auth/status");
    const data = await res.json();
    return !!data.required;
  } catch {
    return false;
  }
}

function showLogin() {
  if (!el.loginOverlay.hidden) return;
  el.loginOverlay.hidden = false;
  el.loginPassword.focus();
}
function hideLogin() {
  el.loginOverlay.hidden = true;
  el.loginError.hidden = true;
  el.loginPassword.value = "";
}

function wireLogin() {
  el.loginForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    const submitBtn = el.loginForm.querySelector("button");
    submitBtn.disabled = true;
    el.loginError.hidden = true;
    try {
      const res = await fetch("/auth/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token: el.loginPassword.value }),
      });
      if (!res.ok) {
        el.loginError.textContent = res.status === 401 ? "Wrong password." : "Login failed.";
        el.loginError.hidden = false;
        return;
      }
      hideLogin();
      RPC.connect();
    } catch {
      el.loginError.textContent = "Could not reach the daemon.";
      el.loginError.hidden = false;
    } finally {
      submitBtn.disabled = false;
    }
  });
}

// ------------------------------------------------------------------ init ---

function wireEvents() {
  el.railToggle.onclick = toggleRail;
  el.resourcesToggle.onclick = toggleResources;
  el.resClose.onclick = () => { state.resourcesOpen = false; savePref(UI_KEYS.res, "0"); applyResourcesState(); };
  el.haltBtn.onclick = () => haltAll(el.haltBtn);
  el.newSessionBtn.onclick = createNewSession;
  el.sessionSearch.oninput = renderSidebar;

  for (const btn of el.resTabs.querySelectorAll(".res-tab")) {
    btn.onclick = () => selectResourceTab(btn.dataset.tab);
  }

  el.composeSend.onclick = sendMessage;
  el.composeInput.addEventListener("input", autoGrow);
  el.composeInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      sendMessage();
    }
  });

  document.addEventListener("keydown", (e) => {
    const mod = e.ctrlKey || e.metaKey;
    if (!mod) return;
    if (e.key.toLowerCase() === "b") { e.preventDefault(); toggleRail(); }
    else if (e.key.toLowerCase() === "r") { e.preventDefault(); toggleResources(); }
  });

  RPC.onStatusChange(setStatus);
  RPC.onNotify((method, params) => {
    if (method === "session.event") {
      refreshSessions();
      return;
    }
    if ((method === "message.event" || method === "tool.call.event" || method === "message.delta.event") &&
        params && params.session_id === state.selectedId) {
      loadMessages().then(renderThread);
    }
  });
}

function init() {
  bindEls();
  for (const btn of el.resTabs.querySelectorAll(".res-tab")) {
    if (btn.dataset.tab === state.resourcesTab) btn.classList.add("on");
  }
  applyRailState();
  applyResourcesState();
  wireLogin();
  wireEvents();
  RPC.connect();
}

document.addEventListener("DOMContentLoaded", init);
