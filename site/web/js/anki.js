// Anki tab

// ---------- Anki tab ----------
// Talks to Anki through the AnkiConnect add-on, via the server (/api/anki, a fixed list of actions).
// Anki has to be open. Reviewing happens in Anki itself; Study opens the deck there.
let ankiLoaded = false, ankiData = null, ankiEdit = null, ankiRetry = null;
const anki = async (action, params = {}) => (await post("/api/anki", { action, params })).result;
// Field HTML -> plain text for display (DOMParser doesn't run scripts or load images).
const ankiText = html => new DOMParser().parseFromString(String(html).replace(/<img[^>]*>/gi, " 🖼 ").replace(/<br\s*\/?>/gi, " "), "text/html").body.textContent.trim();
const toArea = html => String(html).replace(/<br\s*\/?>/gi, "\n");
const fromArea = text => text.replace(/\r?\n/g, "<br>");

function ankiSetupHtml(st) {
  const open = st.installed ? `<button class="sm primary" data-anki-open>Open Anki</button>` : "";
  return `<div class="mcp-setup"><b>Connect Anki</b><ol>
    ${st.installed ? (st.running ? "" : `<li>Open Anki. ${open}</li>`) : `<li>Install <a class="link" href="https://apps.ankiweb.net" target="_blank" rel="noopener">Anki</a>.</li>`}
    <li>In Anki, go to <b>Tools → Add-ons → Get Add-ons…</b>, enter <code>${esc(st.addon || "2055492159")}</code> (AnkiConnect) and press OK.</li>
    <li>Restart Anki, then keep it open while you use this (it's how the dashboard and the Anki MCP reach your cards).</li></ol></div>`;
}
async function openAnki() {
  try { const r = await post("/api/anki/open", {}); toast(r.started ? "Opening Anki…" : "Anki is already open"); setTimeout(loadAnki, 4000); }
  catch (e) { toast(e.message, true); }
}
$("#anki-open").onclick = openAnki;
$("#anki-sync").onclick = async e => {
  e.target.disabled = true; e.target.textContent = "Syncing…";
  try { await anki("sync"); toast("Anki synced"); await loadAnki(); } catch (err) { toast("Anki: " + err.message, true); }
  e.target.disabled = false; e.target.textContent = "↻ Sync";
};

async function loadAnki() {
  clearTimeout(ankiRetry);
  const st = await (await fetch("/api/anki/status")).json().catch(() => ({ connected: false }));
  $("#anki-status").className = "chat-status" + (st.connected ? "" : " off");
  $("#anki-status").innerHTML = `<i></i>${st.connected ? "Connected" : st.running ? "AnkiConnect isn't answering" : "Anki isn't open"}`;
  $("#anki-sync").disabled = !st.connected;
  $("#anki-setup").hidden = st.connected; $("#anki-main").hidden = !st.connected;
  if (!st.connected) {
    $("#anki-setup").innerHTML = ankiSetupHtml(st);
    $("#anki-setup").querySelectorAll("[data-anki-open]").forEach(b => b.onclick = openAnki);
    if (!$("#tab-anki").hidden) ankiRetry = setTimeout(loadAnki, 8000);  // pick it up once Anki opens
    return;
  }
  try {
    const r = await fetch("/api/anki/overview"), d = await r.json();
    if (!r.ok) throw new Error(d.error);
    ankiData = d;
  } catch (e) { $("#anki-decks").innerHTML = `<p class="err">${esc(e.message)}</p>`; return; }
  renderAnkiDecks();
  if (!ankiLoaded) { await fillAnkiForm(); runAnkiSearch(); }
  ankiLoaded = true;
}

function renderAnkiDecks() {
  const { decks, reviewed_today, by_day } = ankiData;
  // Parent decks include their subdecks' counts, so total the top-level ones only.
  const top = decks.filter(d => !d.name.includes("::"));
  const due = top.reduce((n, d) => n + d.new + d.learn + d.review, 0);
  // AnkiConnect lists only days with reviews ([["2026-10-20", 31], ...]); chart the last 30 calendar days.
  const counts = new Map(by_day), days = [];
  for (let i = 29; i >= 0; i--) { const d = new Date(now); d.setDate(d.getDate() - i); days.push([dayKey(d), counts.get(dayKey(d)) || 0]); }
  const max = Math.max(1, ...days.map(d => d[1]));
  const num = (n, cls) => `<span class="${n ? cls : "z"}">${n}</span>`;
  $("#anki-decks").innerHTML = `<div class="card-head"><h2>Decks</h2><span class="small muted">${due} due today</span></div>
    <div class="anki-stats"><div><div class="big">${reviewed_today}</div><div class="lbl">reviewed today</div></div>
      <div class="anki-chart" title="Reviews, last 30 days">${days.map(([d, n]) => `<div class="${n ? "" : "zero"}" style="height:${Math.max(4, (n / max) * 100)}%" title="${esc(d)}: ${n}"></div>`).join("")}</div></div>
    ${decks.length ? `<div class="deck-head"><span>New</span><span>Learn</span><span>Due</span></div>` : ""}
    ${decks.map(d => { const depth = d.name.split("::").length - 1, short = d.name.split("::").pop();
      // A deck's own total leaves out its subdecks; count them in.
      const cards = decks.reduce((n, x) => n + (x.name === d.name || x.name.startsWith(d.name + "::") ? x.total : 0), 0);
      return `<div class="deck" style="padding-left:${depth * 16}px"><span class="name">${esc(short)}<small>${cards.toLocaleString()} cards</small></span>
        <span class="counts">${num(d.new, "c-new")}${num(d.learn, "c-learn")}${num(d.review, "c-due")}</span>
        <button class="sm" data-study="${esc(d.name)}" title="Open this deck in Anki">Study</button></div>`; }).join("") || `<p class="muted small">No decks yet. Add a card to create one.</p>`}`;
  $("#anki-decks").querySelectorAll("[data-study]").forEach(b => b.onclick = async () => {
    try { await anki("guiDeckOverview", { name: b.dataset.study }); toast("Opened in Anki"); } catch (e) { toast("Anki: " + e.message, true); }
  });
}

// ----- add card -----
async function fillAnkiForm() {
  const remember = k => { try { return localStorage.getItem(k); } catch { return null; } };
  const deck = remember("anki-deck"), model = remember("anki-model");
  $("#anki-deck").innerHTML = ankiData.decks.map(d => `<option${d.name === deck ? " selected" : ""}>${esc(d.name)}</option>`).join("") + `<option value="__new">+ New deck…</option>`;
  const models = ankiData.models;
  const pick = models.includes(model) ? model : models.find(m => m === "Basic") || models[0];
  $("#anki-model").innerHTML = models.map(m => `<option${m === pick ? " selected" : ""}>${esc(m)}</option>`).join("");
  await drawAnkiFields();
}
async function drawAnkiFields() {
  const names = await anki("modelFieldNames", { modelName: $("#anki-model").value });
  $("#anki-fields").innerHTML = names.map((n, i) => `<label>${esc(n)}<textarea class="txt" data-field="${esc(n)}" rows="${i ? 3 : 2}"></textarea></label>`).join("");
  $("#anki-fields textarea")?.focus();
}
$("#anki-model").onchange = () => { try { localStorage.setItem("anki-model", $("#anki-model").value); } catch {} drawAnkiFields(); };
$("#anki-deck").onchange = async e => {
  if (e.target.value === "__new") {
    const name = prompt("New deck name (use :: for a subdeck, e.g. Japanese::Verbs)")?.trim();
    if (name) { try { await anki("createDeck", { deck: name }); ankiData.decks.push({ name, new: 0, learn: 0, review: 0, total: 0 }); ankiData.decks.sort((a, b) => a.name.localeCompare(b.name)); } catch (err) { toast(err.message, true); } }
    await fillAnkiForm(); if (name) $("#anki-deck").value = name;
  }
  try { localStorage.setItem("anki-deck", $("#anki-deck").value); } catch {}
};
async function addAnkiCard() {
  const fields = Object.fromEntries([...document.querySelectorAll("#anki-fields [data-field]")].map(t => [t.dataset.field, fromArea(t.value.trim())]));
  if (!Object.values(fields)[0]) return toast("Fill in the first field", true);
  const btn = $("#anki-add-btn"); btn.disabled = true;
  try {
    await anki("addNote", { note: { deckName: $("#anki-deck").value, modelName: $("#anki-model").value, fields,
      tags: $("#anki-tags").value.split(/\s+/).filter(Boolean), options: { allowDuplicate: false } } });
    toast(`Added to ${$("#anki-deck").value}`);
    document.querySelectorAll("#anki-fields textarea").forEach(t => t.value = "");
    $("#anki-fields textarea")?.focus();
    loadAnki(); if ($("#anki-q").value.trim() === "added:7") runAnkiSearch();
  } catch (e) { toast(/duplicate/i.test(e.message) ? "That card already exists" : "Anki: " + e.message, true); }
  btn.disabled = false;
}
$("#anki-add-btn").onclick = addAnkiCard;
$("#anki-add").addEventListener("keydown", e => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); addAnkiCard(); } });

// ----- browse -----
let ankiNotes = [];
async function runAnkiSearch() {
  const q = $("#anki-q").value.trim() || "deck:*", out = $("#anki-results");
  out.innerHTML = `<p class="muted small">Searching…</p>`;
  try {
    const ids = (await anki("findNotes", { query: q })).sort((a, b) => b - a);  // note ids are creation times: newest first
    $("#anki-count").textContent = ids.length ? `${ids.length} note${ids.length === 1 ? "" : "s"}${ids.length > 60 ? " · newest 60 shown" : ""}` : "";
    const notes = ids.length ? await anki("notesInfo", { notes: ids.slice(0, 60) }) : [];
    const cards = notes.flatMap(n => n.cards);
    const info = cards.length ? await anki("cardsInfo", { cards }) : [];
    const byCard = new Map(info.map(c => [c.cardId, c]));
    ankiNotes = notes.map(n => { const cs = n.cards.map(id => byCard.get(id)).filter(Boolean);
      return { ...n, deck: cs[0]?.deckName || "", suspended: cs.length > 0 && cs.every(c => c.queue === -1), due: cs.some(c => c.queue === 2 && c.due <= 0) }; });
    renderAnkiNotes();
  } catch (e) { out.innerHTML = `<p class="err">${esc(e.message)}</p>`; }
}
function renderAnkiNotes() {
  const out = $("#anki-results");
  if (!ankiNotes.length) { out.innerHTML = `<p class="muted small">No notes match.</p>`; return; }
  out.innerHTML = ankiNotes.map(n => {
    const f = Object.entries(n.fields).sort((a, b) => a[1].order - b[1].order);
    if (ankiEdit === n.noteId) return `<div class="anki-note-edit" data-note="${n.noteId}">
      ${f.map(([name, v]) => `<label>${esc(name)}<textarea class="txt" data-field="${esc(name)}">${esc(toArea(v.value))}</textarea></label>`).join("")}
      <label>Tags<input class="txt" data-tags value="${esc(n.tags.join(" "))}"></label>
      <div class="foot"><button class="sm" data-cancel>Cancel</button><button class="sm primary" data-save>Save</button></div></div>`;
    return `<div class="note${n.suspended ? " suspended" : ""}" data-note="${n.noteId}"><div class="qa">
        <div class="q">${esc(ankiText(f[0]?.[1].value || "")) || "<span class='muted'>(empty)</span>"}</div>
        ${f[1] ? `<div class="a">${esc(ankiText(f[1][1].value)).slice(0, 220)}</div>` : ""}
        <div class="meta"><span class="badge">${esc(n.deck)}</span>${n.modelName !== "Basic" ? `<span class="badge">${esc(n.modelName)}</span>` : ""}
          ${n.suspended ? `<span class="badge warn">suspended</span>` : ""}${n.tags.map(t => `<span class="badge">#${esc(t)}</span>`).join("")}</div></div>
      <span class="acts"><button class="icon" data-edit title="Edit">✎</button>
        <button class="icon" data-susp title="${n.suspended ? "Unsuspend" : "Suspend (hide from reviews)"}">${n.suspended ? "▶" : "⏸"}</button>
        <button class="icon" data-browse title="Open in Anki's browser">↗</button>
        <button class="icon del" data-del title="Delete">✕</button></span></div>`;
  }).join("");
  out.querySelectorAll("[data-note]").forEach(row => {
    const n = ankiNotes.find(x => x.noteId === +row.dataset.note), q = sel => row.querySelector(sel);
    if (q("[data-edit]")) q("[data-edit]").onclick = () => { ankiEdit = n.noteId; renderAnkiNotes(); out.querySelector(".anki-note-edit textarea")?.focus(); };
    if (q("[data-cancel]")) q("[data-cancel]").onclick = () => { ankiEdit = null; renderAnkiNotes(); };
    if (q("[data-save]")) q("[data-save]").onclick = async () => {
      const fields = Object.fromEntries([...row.querySelectorAll("[data-field]")].map(t => [t.dataset.field, fromArea(t.value)]));
      q("[data-save]").disabled = true;
      try {
        await anki("updateNoteFields", { note: { id: n.noteId, fields } });
        await anki("updateNoteTags", { note: n.noteId, tags: q("[data-tags]").value.split(/\s+/).filter(Boolean) });
        toast("Saved"); ankiEdit = null; runAnkiSearch();
      } catch (e) { toast("Anki: " + e.message + (/browser/i.test(e.message) ? "" : " (close the note in Anki's browser if it's open)"), true); q("[data-save]").disabled = false; }
    };
    if (q("[data-susp]")) q("[data-susp]").onclick = async () => {
      try { await anki(n.suspended ? "unsuspend" : "suspend", { cards: n.cards }); toast(n.suspended ? "Unsuspended" : "Suspended"); runAnkiSearch(); loadAnki(); }
      catch (e) { toast("Anki: " + e.message, true); }
    };
    if (q("[data-browse]")) q("[data-browse]").onclick = () => anki("guiBrowse", { query: `nid:${n.noteId}` }).then(() => toast("Opened in Anki")).catch(e => toast(e.message, true));
    if (q("[data-del]")) q("[data-del]").onclick = async () => {
      if (!confirm(`Delete "${ankiText(Object.values(n.fields)[0]?.value || "").slice(0, 60)}" and its cards from Anki?`)) return;
      try { await anki("deleteNotes", { notes: [n.noteId] }); toast("Deleted"); runAnkiSearch(); loadAnki(); } catch (e) { toast("Anki: " + e.message, true); }
    };
  });
}
$("#anki-search").onsubmit = e => { e.preventDefault(); ankiEdit = null; runAnkiSearch(); };
