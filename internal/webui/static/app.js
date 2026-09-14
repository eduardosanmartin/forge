"use strict";

// Minimal JSON-RPC 2.0 client over the daemon's existing WebSocket transport
// (see internal/daemon/rpc.go for the wire format and method names). No
// framework, no build step: this file is served as-is by internal/webui.

const RPC = (() => {
  let ws = null;
  let nextId = 1;
  const pending = new Map(); // id -> {resolve, reject}
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

const state = {
  sessions: [],
  selectedId: null,
  messages: [],
  lastModelUsed: {}, // sessionId -> model string returned by the last session.execute_turn
};

const el = {
  status: document.getElementById("status"),
  sessionList: document.getElementById("session-list"),
  newSessionBtn: document.getElementById("new-session-btn"),
  main: document.getElementById("main"),
  loginOverlay: document.getElementById("login-overlay"),
  loginForm: document.getElementById("login-form"),
  loginPassword: document.getElementById("login-password"),
  loginError: document.getElementById("login-error"),
};

function shortId(id) {
  return id && id.length > 8 ? id.slice(0, 8) : id;
}

function fmtTime(unixSeconds) {
  if (!unixSeconds) return "";
  return new Date(unixSeconds * 1000).toLocaleString();
}

function setStatus(connected) {
  el.status.textContent = connected ? "connected" : "reconnecting…";
  el.status.className = "status " + (connected ? "connected" : "disconnected");
  if (connected) {
    hideLogin();
    refreshSessions();
  } else {
    // The WS handshake 401s on every attempt if the session cookie expired
    // (or was never set) while auth is required — re-showing the login form
    // beats "reconnecting…" spinning forever. RPC's own retry loop keeps
    // running underneath; it succeeds on its own once login sets a fresh
    // cookie, no extra wiring needed here.
    checkAuthStatus().then((required) => {
      if (required) showLogin();
    });
  }
}

// checkAuthStatus queries the one endpoint that never requires credentials
// (RF-7.4) so the GUI knows, before ever touching /ws, whether it needs to
// show the login form at all.
async function checkAuthStatus() {
  try {
    const res = await fetch("/auth/status");
    const data = await res.json();
    return !!data.required;
  } catch {
    return false; // fail open on the *check* — the WS/API calls behind auth still enforce it
  }
}

function showLogin() {
  // Avoid re-stealing focus/cursor position on every ~2s reconnect retry
  // while the overlay is already up and the user is mid-typing.
  if (!el.loginOverlay.hidden) return;
  el.loginOverlay.hidden = false;
  el.loginPassword.focus();
}

function hideLogin() {
  el.loginOverlay.hidden = true;
  el.loginError.hidden = true;
  el.loginPassword.value = "";
}

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

async function refreshSessions() {
  try {
    const res = await RPC.call("session.list", { limit: 200 });
    state.sessions = res.sessions || [];
    renderSidebar();
  } catch (e) {
    console.error("session.list failed", e);
  }
}

function renderSidebar() {
  el.sessionList.innerHTML = "";
  if (state.sessions.length === 0) {
    const d = document.createElement("div");
    d.className = "empty";
    d.textContent = "No sessions yet.";
    el.sessionList.appendChild(d);
    return;
  }
  const sorted = [...state.sessions].sort((a, b) => b.updated_at - a.updated_at);
  for (const s of sorted) {
    const btn = document.createElement("button");
    btn.className = "session-item" + (s.id === state.selectedId ? " active" : "");
    btn.onclick = () => selectSession(s.id);

    const idEl = document.createElement("div");
    idEl.className = "id";
    idEl.textContent = shortId(s.id);
    btn.appendChild(idEl);

    const meta = document.createElement("div");
    meta.className = "meta";
    meta.textContent = `${s.message_count || 0} msgs · ${fmtTime(s.updated_at)}`;
    btn.appendChild(meta);

    const parent = s.metadata && s.metadata.branch_parent;
    if (parent) {
      const lineage = document.createElement("div");
      lineage.className = "lineage";
      lineage.textContent = `⮑ branched from ${shortId(parent)}`;
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
    console.error("session.create failed", e);
    alert("Could not create a session: " + e.message);
  } finally {
    el.newSessionBtn.disabled = false;
  }
}

async function selectSession(id) {
  state.selectedId = id;
  renderSidebar();
  await loadMessages();
  renderMain();
}

async function loadMessages() {
  if (!state.selectedId) return;
  try {
    const res = await RPC.call("session.get_messages", { session_id: state.selectedId });
    state.messages = res.messages || [];
  } catch (e) {
    console.error("session.get_messages failed", e);
    state.messages = [];
  }
}

function renderMain() {
  const session = state.sessions.find((s) => s.id === state.selectedId);
  el.main.innerHTML = "";
  if (!session) {
    const d = document.createElement("div");
    d.className = "empty-state";
    d.textContent = "Select a session on the left.";
    el.main.appendChild(d);
    return;
  }

  const header = document.createElement("div");
  header.id = "detail-header";

  const idEl = document.createElement("span");
  idEl.className = "id";
  idEl.textContent = session.id;
  header.appendChild(idEl);

  const select = document.createElement("select");
  const noneOpt = document.createElement("option");
  noneOpt.value = "";
  noneOpt.textContent = "Compare with…";
  select.appendChild(noneOpt);
  for (const s of state.sessions) {
    if (s.id === session.id) continue;
    const opt = document.createElement("option");
    opt.value = s.id;
    opt.textContent = shortId(s.id);
    select.appendChild(opt);
  }
  header.appendChild(select);

  const compareBtn = document.createElement("button");
  compareBtn.textContent = "Compare";
  compareBtn.onclick = () => runCompare(session.id, select.value);
  header.appendChild(compareBtn);

  el.main.appendChild(header);

  const messagesEl = document.createElement("div");
  messagesEl.id = "messages";
  renderMessages(messagesEl);
  el.main.appendChild(messagesEl);

  el.main.appendChild(buildCompose(session.id));

  const compareEl = document.createElement("div");
  compareEl.id = "compare-panel";
  compareEl.hidden = true;
  el.main.appendChild(compareEl);

  el.main.appendChild(buildFooter(session));
}

function buildCompose(sessionId) {
  const compose = document.createElement("div");
  compose.id = "compose";

  const textarea = document.createElement("textarea");
  textarea.id = "compose-input";
  textarea.placeholder = "Message this session… (Ctrl+Enter to send)";
  textarea.rows = 2;
  compose.appendChild(textarea);

  const sendBtn = document.createElement("button");
  sendBtn.id = "compose-send";
  sendBtn.textContent = "Send";
  compose.appendChild(sendBtn);

  const errEl = document.createElement("div");
  errEl.id = "compose-error";
  errEl.className = "compose-error";
  errEl.hidden = true;
  compose.appendChild(errEl);

  const submit = () => sendMessage(sessionId, textarea, sendBtn, errEl);
  sendBtn.onclick = submit;
  textarea.addEventListener("keydown", (e) => {
    if ((e.key === "Enter" && (e.ctrlKey || e.metaKey))) {
      e.preventDefault();
      submit();
    }
  });

  return compose;
}

async function sendMessage(sessionId, textarea, sendBtn, errEl) {
  const text = textarea.value.trim();
  if (!text) return;

  textarea.disabled = true;
  sendBtn.disabled = true;
  sendBtn.textContent = "Sending…";
  errEl.hidden = true;

  try {
    const result = await RPC.call("session.execute_turn", {
      session_id: sessionId,
      user_message: text,
    });
    if (result && result.model) state.lastModelUsed[sessionId] = result.model;
    textarea.value = "";
    await loadMessages();
    const container = document.getElementById("messages");
    if (container) renderMessages(container);
    const footer = document.getElementById("session-footer");
    const session = state.sessions.find((s) => s.id === sessionId);
    if (footer && session) footer.replaceWith(buildFooter(session));
  } catch (e) {
    errEl.textContent = "Send failed: " + e.message;
    errEl.hidden = false;
  } finally {
    textarea.disabled = false;
    sendBtn.disabled = false;
    sendBtn.textContent = "Send";
    textarea.focus();
  }
}

// buildFooter shows which model this session last used (session.switch_model
// records its choice in this session's metadata, but — see
// internal/daemon/session_mgr.go SwitchModel — it actually hot-swaps the
// *daemon's* default model via the LLM registry, not a per-session setting.
// The UI is upfront about that scope instead of implying isolation session
// switch_model doesn't have.
function buildFooter(session) {
  const footer = document.createElement("div");
  footer.id = "session-footer";

  const current =
    (session.metadata && session.metadata.model) ||
    state.lastModelUsed[session.id] ||
    "";

  const info = document.createElement("span");
  info.className = "footer-info";
  info.textContent = current
    ? `Model: ${current}`
    : "Model: (daemon default — no explicit switch recorded yet)";
  footer.appendChild(info);

  const input = document.createElement("input");
  input.type = "text";
  input.placeholder = "model name (within the current provider)…";
  input.value = current;
  footer.appendChild(input);

  const btn = document.createElement("button");
  btn.textContent = "Switch";
  btn.title = "Hot-swaps the daemon's default model for every session, not just this one";
  btn.onclick = () => switchModel(session.id, input.value.trim());
  footer.appendChild(btn);

  const hint = document.createElement("span");
  hint.className = "footer-hint";
  hint.textContent = "(daemon-wide default — affects every session, not only this one)";
  footer.appendChild(hint);

  return footer;
}

async function switchModel(sessionId, value) {
  if (!value) return;
  try {
    await RPC.call("session.switch_model", { session_id: sessionId, model: value });
    await refreshSessions();
    const footer = document.getElementById("session-footer");
    const session = state.sessions.find((s) => s.id === sessionId);
    if (footer && session) footer.replaceWith(buildFooter(session));
  } catch (e) {
    alert("Could not switch model: " + e.message);
  }
}

function renderMessages(container) {
  container.innerHTML = "";
  if (state.messages.length === 0) {
    const d = document.createElement("div");
    d.className = "empty";
    d.textContent = "No messages yet.";
    container.appendChild(d);
    return;
  }
  for (const m of state.messages) {
    const msgEl = document.createElement("div");
    msgEl.className = `msg role-${m.role}`;

    const role = document.createElement("div");
    role.className = "role";
    role.textContent = `${m.role} · #${m.seq}`;
    msgEl.appendChild(role);

    if (m.content) {
      const content = document.createElement("div");
      content.className = "content";
      content.textContent = m.content;
      msgEl.appendChild(content);
    }

    for (const tc of m.tool_calls || []) {
      const tcEl = document.createElement("div");
      tcEl.className = "tool-call";
      const fn = tc.function || {};
      tcEl.innerHTML = `<span class="name">${escapeHtml(fn.name || "")}</span>(${escapeHtml(fn.arguments || "")})`;
      msgEl.appendChild(tcEl);
    }

    container.appendChild(msgEl);
  }
}

function escapeHtml(s) {
  const d = document.createElement("div");
  d.textContent = s;
  return d.innerHTML;
}

async function runCompare(a, b) {
  const panel = document.getElementById("compare-panel");
  if (!b) {
    panel.hidden = true;
    return;
  }
  panel.hidden = false;
  panel.innerHTML = "<h3>Comparing…</h3>";
  try {
    const cmp = await RPC.call("session.compare", { session_a: a, session_b: b });
    renderCompare(panel, cmp);
  } catch (e) {
    panel.innerHTML = `<h3>Compare failed</h3><div class="empty">${escapeHtml(e.message)}</div>`;
  }
}

function renderCompare(panel, cmp) {
  panel.innerHTML = "";
  const h = document.createElement("h3");
  h.textContent = `Divergence: ${cmp.divergent_count_a} vs ${cmp.divergent_count_b} messages since branch (seq ${cmp.branch_at_seq_a})`;
  panel.appendChild(h);

  const cols = document.createElement("div");
  cols.className = "compare-columns";

  for (const [label, list] of [
    [shortId(cmp.session_a.id), cmp.divergent_a],
    [shortId(cmp.session_b.id), cmp.divergent_b],
  ]) {
    const col = document.createElement("div");
    col.className = "col";
    const h4 = document.createElement("h4");
    h4.textContent = label;
    col.appendChild(h4);
    for (const m of list || []) {
      const line = document.createElement("div");
      line.className = "diff-line";
      line.textContent = `[${m.role} #${m.seq}] ${m.content || "(tool call)"}`;
      col.appendChild(line);
    }
    if (!list || list.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty";
      empty.textContent = "No divergent messages.";
      col.appendChild(empty);
    }
    cols.appendChild(col);
  }

  panel.appendChild(cols);
}

el.newSessionBtn.onclick = createNewSession;

RPC.onStatusChange(setStatus);
RPC.onNotify((method, params) => {
  if (method === "session.event") {
    refreshSessions();
    return;
  }
  if (
    (method === "message.event" || method === "tool.call.event") &&
    params &&
    params.session_id === state.selectedId
  ) {
    loadMessages().then(() => {
      const container = document.getElementById("messages");
      if (container) renderMessages(container);
    });
  }
});

RPC.connect();
