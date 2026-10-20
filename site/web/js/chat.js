// MCPs tab: chat with an AI through the MCP servers

// ---------- MCPs tab ----------
// Chat with an AI agent (Claude Code, Codex or Gemini CLI) that has the MyFitnessPal MCP and can
// read the docs. The server runs the agent per message and streams the answer as NDJSON; the
// conversation is kept on the server (data/chat.json).
const md = t => window.marked ? marked.parse(esc(t)) : `<p>${esc(t)}</p>`;
// Example questions for the empty chat, by MCP server.
const SUGGESTIONS = {
  myfitnesspal: [["🍽️", "What should I eat for the rest of today?"], ["🍳", "Log 2 eggs and a slice of toast for breakfast"],
    ["🧂", "Why is my sodium so high today?"], ["💪", "How am I doing on protein this week?"]],
  anki: [["🗂️", "How many Anki cards are due today?"], ["✍️", "Make 5 flashcards from my nutrition plan"]],
};
const ICONS = {
  leaf: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 20A7 7 0 0 1 9.8 6.1C15.5 5 17 4.48 19 2c1 2 2 4.18 2 8 0 5.5-4.78 10-10 10Z"/><path d="M2 21c0-3 1.85-5.36 5.08-6C9.5 14.52 12 13 13 12"/></svg>`,
  send: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V5M5 12l7-7 7 7"/></svg>`,
  stop: `<svg viewBox="0 0 24 24" fill="currentColor"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>`,
  cards: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="7" width="13" height="14" rx="2"/><path d="M8 3h11a2 2 0 0 1 2 2v12"/></svg>`,
};
document.querySelectorAll("[data-icon-cards]").forEach(el => el.innerHTML = ICONS.cards);
document.querySelectorAll("[data-icon]").forEach(el => el.innerHTML = ICONS.leaf);
$("#chat-send").innerHTML = ICONS.send; $("#chat-stop").innerHTML = ICONS.stop;
const TYPING = `<span class="typing"><i></i><i></i><i></i></span>`;
let chatBusy = false, chatState = null;
const toolChips = tools => tools?.length ? `<div class="tools">${tools.map(t => `<span class="${t.error ? "bad" : ""}">${esc(t.label)}${t.detail ? " · " + esc(t.detail) : ""}</span>`).join("")}</div>` : "";
function chatMsgHtml(m) {
  if (m.role === "user") return `<div class="msg user">${esc(m.text)}</div>`;
  const by = m.model ? modelLabel(m.provider, m.model) : "";
  return `<div class="msg bot"><div class="av">${ICONS.leaf}</div><div class="body">${toolChips(m.tools)}<div class="bubble md">${m.text ? md(m.text) : ""}</div>${m.error ? `<div class="err">${esc(m.error)}</div>` : ""}${by ? `<div class="by">${esc(by)}</div>` : ""}</div></div>`;
}
function renderChat(messages) {
  const log = $("#chat-log");
  log.innerHTML = messages.length ? messages.map(chatMsgHtml).join("")
    : (() => {
        const on = (chatState?.mcps || []).filter(m => m.enabled).map(m => m.id);
        const sugg = on.length === 2 ? [...SUGGESTIONS.myfitnesspal.slice(0, 2), ...SUGGESTIONS.anki]
          : on.length ? SUGGESTIONS[on[0]].slice(0, 4) : [];
        const title = on.length === 1 && on[0] === "anki" ? "What do you want to study?" : "What did you eat today?";
        const sub = { myfitnesspal: "tell it what you ate and it'll log it", anki: "have it make or find Anki cards" };
        return `<div class="chat-empty"><div class="chat-badge">${ICONS.leaf}</div><h3>${title}</h3>
          <p>Ask about your plan${on.map(m => ", or " + sub[m]).join("")}.</p>
          <div class="sugg">${sugg.map(([i, q]) => `<button data-sugg="${esc(q)}"><span>${i}</span>${esc(q)}</button>`).join("")}</div></div>`;
      })();
  log.querySelectorAll("[data-sugg]").forEach(b => b.onclick = () => { $("#chat-in").value = b.dataset.sugg; sendChat(); });
  log.scrollTop = log.scrollHeight;
}
async function loadChat() {
  try {
    const c = chatState = await (await fetch("/api/chat")).json();
    $("#chat-ai").innerHTML = c.providers.map(p => `<option value="${p.id}"${p.id === c.provider ? " selected" : ""}${p.available ? "" : " disabled"}>${esc(p.label)}${p.installed ? "" : p.available ? " (via Nix)" : " (not installed)"}</option>`).join("");
    renderMcpSetup(c.mcps);
    renderModelPicker(c);
    const ai = c.providers.find(p => p.id === c.provider), on = c.mcps.filter(m => m.enabled);
    const ok = on.every(m => m.ready), ankiOff = on.some(m => m.id === "anki" && !m.ready);
    $("#chat-status").className = "chat-status" + (ok ? "" : " off");
    $("#chat-status").innerHTML = `<i></i>${esc(ai?.label || "")} · ${!on.length ? "docs only" : ok ? "ready"
      : ankiOff ? `<a class="link" href="#anki">Anki isn't connected</a>` : "needs setup"}`;
    $("#mcp-toggles").innerHTML = c.mcps.map(m => `<button class="pill mcp-toggle${m.enabled ? " on" : ""}${m.ready ? " ready" : ""}" data-mcp="${m.id}"
      title="${m.enabled ? "Turn off" : "Turn on"} the ${esc(m.label)} MCP${m.ready ? "" : m.id === "anki" ? " (not connected: see the Anki tab)" : " (needs setup)"}"><i></i>${esc(m.label)}</button>`).join("");
    $("#mcp-toggles").querySelectorAll("[data-mcp]").forEach(b => b.onclick = async () => {
      const mcps = c.mcps.filter(m => m.id === b.dataset.mcp ? !m.enabled : m.enabled).map(m => m.id);
      try { await post("/api/chat/mcps", { mcps }); await loadChat(); } catch (e) { toast(e.message, true); }
    });
    if (!chatBusy) renderChat(c.messages || []);
  } catch (e) { $("#chat-log").innerHTML = `<p class="err">${esc(e.message)}</p>`; }
}
// Setup steps for a turned-on MyFitnessPal MCP that isn't connected. Anki's steps live in the
// Anki tab; here its toggle and the status line point there.
function renderMcpSetup(mcps) {
  const el = $("#mcp-setup"), todo = mcps.filter(m => m.enabled && !m.ready && m.id === "myfitnesspal");
  el.hidden = !todo.length;
  // Chatting needs at least one usable MCP, unless they're all off (then it only reads the docs).
  const on = mcps.filter(m => m.enabled);
  $("#chat-in").disabled = $("#chat-send").disabled = on.length > 0 && on.every(m => !m.ready);
  el.innerHTML = todo.map(m => m.id === "myfitnesspal" ? `<div class="mcp-setup"><b>Set up the MyFitnessPal MCP</b><ol>
    <li>Download the <a class="link" href="${esc(m.setup.repo)}" target="_blank" rel="noopener">MyFitnessPal MCP</a> (or skip this: the dashboard can run the released package with <code>uv</code>).</li>
    <li>Connect your account: <code>uvx --from 'mfp-mcp[autorefresh]' mfp-mcp auth</code> and paste your MyFitnessPal session cookie, as the MCP's README shows.</li>
    <li>Reload this page.</li></ol></div>` : ankiSetupHtml(m.setup)).join("");
  el.querySelectorAll("[data-anki-open]").forEach(b => b.onclick = openAnki);
}
// Model menu for the current AI: its default, the known models, a typed-in one, or Custom…
function renderModelPicker(c) {
  const ai = c.providers.find(p => p.id === c.provider), cur = c.models?.[c.provider] || "";
  const known = ai?.models || [];
  const opts = [["", "Default model"], ...known, ...(cur && !known.some(([id]) => id === cur) ? [[cur, cur]] : []), ["__custom", "Custom…"]];
  $("#chat-model").innerHTML = opts.map(([id, label, desc]) => `<option value="${esc(id)}"${id === cur ? " selected" : ""}${desc ? ` title="${esc(desc)}"` : ""}>${esc(label)}</option>`).join("");
}
const modelLabel = (pid, id) => chatState?.providers.find(p => p.id === pid)?.models.find(([m]) => m === id)?.[1] || id;
$("#chat-model").onchange = async e => {
  let model = e.target.value;
  if (model === "__custom") {
    model = prompt("Model name, as the AI's CLI takes it (e.g. claude-opus-5-5, gpt-5.5, gemini-2.5-pro)")?.trim();
    if (!model) return renderModelPicker(chatState);
  }
  try { await post("/api/chat/model", { model }); await loadChat(); toast(model ? `Using ${modelLabel(chatState.provider, model)}` : "Using the default model"); }
  catch (err) { toast(err.message, true); renderModelPicker(chatState); }
};
$("#chat-ai").onchange = async e => {
  const p = chatState.providers.find(x => x.id === e.target.value);
  if (chatState.messages?.length && !confirm(`Start a new chat with ${p.label}?`)) { e.target.value = chatState.provider; return; }
  try { await post("/api/chat/new", { provider: p.id }); await loadChat(); if (!p.installed) toast(`${p.label} downloads through Nix on first use, so the first answer is slower`); }
  catch (err) { toast(err.message, true); e.target.value = chatState.provider; }
};
function setChatBusy(b) {
  chatBusy = b; $("#chat-send").hidden = b; $("#chat-stop").hidden = !b; $("#chat-new").disabled = $("#chat-ai").disabled = $("#chat-model").disabled = b;
}
async function sendChat() {
  const input = $("#chat-in"), message = input.value.trim();
  if (!message || chatBusy) return;
  const log = $("#chat-log");
  log.querySelector(".chat-empty")?.remove();
  log.insertAdjacentHTML("beforeend", chatMsgHtml({ role: "user", text: message }));
  const bot = document.createElement("div");
  bot.className = "msg bot";
  bot.innerHTML = `<div class="av">${ICONS.leaf}</div><div class="body"><div class="tools"></div><div class="bubble md">${TYPING}</div></div>`;
  log.append(bot); log.scrollTop = log.scrollHeight;
  input.value = ""; input.style.height = "auto"; setChatBusy(true);
  const tools = [];
  let text = "", gap = false;
  const nearBottom = () => log.scrollHeight - log.scrollTop - log.clientHeight < 80;
  const draw = () => { const stick = nearBottom(); bot.querySelector(".tools").outerHTML = toolChips(tools) || `<div class="tools"></div>`;
    bot.querySelector(".md").innerHTML = text ? md(text) : TYPING; if (stick) log.scrollTop = log.scrollHeight; };
  try {
    const r = await fetch("/api/chat", { method: "POST", headers: { "Content-Type": "application/json", "X-Dashboard": "1" }, body: JSON.stringify({ message }) });
    if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
    const reader = r.body.getReader(), dec = new TextDecoder();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      const lines = buf.split("\n"); buf = lines.pop();
      for (const line of lines) {
        if (!line.trim()) continue;
        const ev = JSON.parse(line);
        if (ev.type === "text") { text += (gap && text ? "\n\n" : "") + ev.text; gap = false; }
        else if (ev.type === "break") gap = true;
        else if (ev.type === "tool") tools.push({ label: ev.label, detail: ev.detail });
        else if (ev.type === "tool_error") { const t = tools.find(x => x.label === ev.label && x.detail === ev.detail && !x.error); if (t) t.error = true; }
        else if (ev.type === "done") {
          if (ev.error) bot.querySelector(".body").insertAdjacentHTML("beforeend", `<div class="err">${esc(ev.error)}</div>`);
          if (ev.changed) { loadMfp(); toast("MyFitnessPal updated"); }
          if (ev.anki) { ankiLoaded = false; if (!$("#tab-anki").hidden) loadAnki(); }
        }
        draw();
      }
    }
  } catch (e) { bot.querySelector(".body").insertAdjacentHTML("beforeend", `<div class="err">${esc(e.message)}</div>`); }
  if (!text) bot.querySelector(".md").innerHTML = "";
  setChatBusy(false); input.focus();
  if (chatState) chatState.messages = [...(chatState.messages || []), { role: "user", text: message }];
}
$("#chat-form").onsubmit = e => { e.preventDefault(); sendChat(); };
$("#chat-in").onkeydown = e => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); sendChat(); } };
// The message box grows with what you type, up to its max height.
$("#chat-in").oninput = e => { e.target.style.height = "auto"; e.target.style.height = e.target.scrollHeight + "px"; };
$("#chat-stop").onclick = () => post("/api/chat/stop", {}).catch(() => {});
$("#chat-new").onclick = async () => {
  try { await post("/api/chat/new", {}); chatState.messages = []; renderChat([]); $("#chat-in").focus(); } catch (e) { toast(e.message, true); }
};
