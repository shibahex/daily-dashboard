// Today tab: training, checklist, weight, goals, week, reminders

// ---------- today ----------
function renderToday() {
  $("#today").textContent = now.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", year: "numeric" });
  renderSession(); renderChecklist(); renderWeight(); renderGoals(); renderWeek(); renderNotes();
}

function renderSession() {
  const el = $("#card-session");
  const key = plan.schedule[DAYS[now.getDay()]];
  const s = plan.sessions[key] || { title: "Nothing scheduled" };
  el.innerHTML = cardHead("Today's training", "session") + `<div id="session-body"></div>`;
  wireEditBtn(el);
  const body = $("#session-body");
  if (!editing.has("session")) {
    body.innerHTML = `<h3>${esc(s.title)}</h3>` +
      (s.exercises?.length ? `<table class="ex">${s.exercises.map(([n, r]) => `<tr><td>${esc(n)}</td><td>${esc(r)}</td></tr>`).join("")}</table>` : "") +
      (s.note ? `<p class="muted" style="margin:8px 0 0">${esc(s.note)}</p>` : "");
    return;
  }
  body.innerHTML = `<div class="editor"><label class="lbl">Title</label><input class="txt" id="s-title" style="width:100%" value="${esc(s.title)}">
    <label class="lbl">Note</label><input class="txt" id="s-note" style="width:100%" value="${esc(s.note || "")}">
    <label class="lbl">Exercises</label></div><div id="s-ex"></div>
    <p class="small muted">Editing “${esc(key)}”. It's used every ${esc(DAYS.filter(d => plan.schedule[d] === key).join(", "))}.</p>`;
  rowEditor($("#s-ex"), s.exercises || [], [{ placeholder: "Exercise" }, { placeholder: "Sets × reps", narrow: true }], rows => {
    if (rows) {
      plan.sessions[key] = { ...s, title: $("#s-title").value.trim() || s.title, note: $("#s-note").value.trim() || undefined, exercises: rows.length ? rows : undefined };
      savePlan("Workout saved");
    }
    editing.delete("session"); renderToday();
  }, "Add exercise");
}

// Stored values: "done", "skip", or { state: "skip", note }.
const cellState = v => typeof v === "object" && v ? v.state : v;
const cellNote = v => typeof v === "object" && v ? v.note || "" : "";
function checkState(item) { return cellState((checklist[todayKey] || {})[item]); }
function checkNote(item) { return cellNote((checklist[todayKey] || {})[item]); }
let noteOpen = null;   // checklist item whose skip-reason box is open
async function setCheck(item, state, note = "") {
  const st = { ...(checklist[todayKey] || {}) };
  if (!state) delete st[item];
  else st[item] = state === "skip" && note.trim() ? { state: "skip", note: note.trim() } : state;
  checklist[todayKey] = st;
  renderChecklist(); renderStreak(); renderHistory(); renderActivity();
  try { await post("/api/checklist", { date: todayKey, items: st }); }
  catch (e) { toast("Couldn't save: " + e.message, true); }
}

// A day counts as complete when every item is done or skipped.
function dayRatio(key) {
  const st = checklist[key] || {}, items = plan.checklist;
  if (!items.length) return 0;
  return items.filter(i => st[i]).length / items.length;
}

function renderChecklist() {
  const el = $("#card-checklist");
  el.innerHTML = cardHead("Daily checklist", "checklist") + `<div id="cl-body"></div>`;
  wireEditBtn(el);
  const body = $("#cl-body");
  if (editing.has("checklist")) {
    rowEditor(body, plan.checklist, [{ placeholder: "Checklist item" }], rows => {
      if (rows) { plan.checklist = rows.map(r => r[0]); savePlan("Checklist saved"); }
      editing.delete("checklist"); renderToday(); renderStreak();
    }, "Add item");
    return;
  }
  const items = plan.checklist;
  const done = items.filter(i => checkState(i) === "done").length;
  const skipped = items.filter(i => checkState(i) === "skip").length;
  const pct = n => items.length ? (n / items.length) * 100 : 0;
  const history = [];
  for (let i = 13; i >= 0; i--) {
    const d = new Date(now); d.setDate(d.getDate() - i);
    const k = dayKey(d), r = dayRatio(k);
    history.push(`<i class="${r >= 1 ? "full" : r > 0 ? "part" : ""}${i === 0 ? " today" : ""}" title="${d.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" })}: ${Math.round(r * 100)}%"></i>`);
  }
  body.innerHTML =
    `<div class="small muted">${done + skipped} of ${items.length} done${skipped ? ` (${skipped} skipped)` : ""}${done + skipped === items.length && items.length ? " · day complete ✓" : ""}</div>` +
    `<div class="progress"><div class="d" style="width:${pct(done)}%"></div><div class="s" style="width:${pct(skipped)}%"></div></div>` +
    items.map((item, i) => {
      const s = checkState(item), note = checkNote(item);
      const noteLine = s !== "skip" ? "" : noteOpen === item
        ? `<div class="note-edit"><input class="txt" data-note="${i}" maxlength="300" placeholder="Why are you skipping? (optional)" value="${esc(note)}">
             <button class="sm primary" data-note-save="${i}">Save</button></div>`
        : `<button class="note-line" data-note-open="${i}" title="Edit reason">${note ? "↳ " + esc(note) : "+ add a reason"}</button>`;
      return `<div class="check-wrap"><div class="check${s ? " " + s : ""}">
        <label><input type="checkbox" data-i="${i}"${s === "done" ? " checked" : ""}><span>${esc(item)}</span></label>
        <button class="skip-btn" data-skip="${i}" title="${s === "skip" ? "Un-skip" : "Skip today. Still counts toward the day."}">${s === "skip" ? "skipped" : "skip"}</button></div>${noteLine}</div>`;
    }).join("") +
    `<div class="history" title="Last 14 days">${history.join("")}</div>`;
  body.querySelectorAll("input[data-i]").forEach(cb => cb.onchange = () => setCheck(items[cb.dataset.i], cb.checked ? "done" : null));
  body.querySelectorAll("[data-skip]").forEach(b => b.onclick = () => {
    const it = items[b.dataset.skip];
    if (checkState(it) === "skip") { noteOpen = null; setCheck(it, null); }
    else { noteOpen = it; setCheck(it, "skip"); }
  });
  body.querySelectorAll("[data-note-open]").forEach(b => b.onclick = () => { noteOpen = items[b.dataset.noteOpen]; renderChecklist(); });
  const saveNote = i => { const it = items[i]; noteOpen = null; setCheck(it, "skip", body.querySelector(`[data-note="${i}"]`).value); toast("Reason saved"); };
  body.querySelectorAll("[data-note-save]").forEach(b => b.onclick = () => saveNote(b.dataset.noteSave));
  body.querySelectorAll("input[data-note]").forEach(inp => {
    inp.onkeydown = e => { if (e.key === "Enter") saveNote(inp.dataset.note); if (e.key === "Escape") { noteOpen = null; renderChecklist(); } };
    if (noteOpen === items[inp.dataset.note]) setTimeout(() => inp.focus(), 0);
  });
}

function renderStreak() {
  let streak = 0;
  const d = new Date(now);
  if (dayRatio(dayKey(d)) < 1) d.setDate(d.getDate() - 1);  // today still in progress
  while (dayRatio(dayKey(d)) >= 1) { streak++; d.setDate(d.getDate() - 1); }
  const el = $("#streak");
  el.hidden = streak === 0;
  el.textContent = `🔥 ${streak}-day streak`;
}

function renderWeight() {
  const el = $("#card-weight"), w = plan.weight;
  el.innerHTML = cardHead("Weight", "weight") + `<div id="w-body"></div>`;
  wireEditBtn(el);
  const body = $("#w-body");
  if (editing.has("weight")) {
    body.innerHTML = `<div class="editor">
      <label class="lbl">Starting weight (lb)</label><input class="txt" id="w-start" value="${esc(w.start)}">
      <label class="lbl">Start date</label><input class="txt" id="w-date" type="date" value="${esc(w.startDate)}">
      <label class="lbl">Target</label><input class="txt" id="w-target" value="${esc(w.target)}">
      <label class="lbl">Lowest ever (lb)</label><input class="txt" id="w-best" value="${esc(w.best)}">
      <div class="foot"><span></span><span><button class="sm" id="w-cancel">Cancel</button> <button class="sm primary" id="w-save">Save</button></span></div></div>`;
    $("#w-cancel").onclick = () => { editing.delete("weight"); renderWeight(); };
    $("#w-save").onclick = () => {
      plan.weight = { start: parseFloat($("#w-start").value) || w.start, startDate: $("#w-date").value || w.startDate, target: $("#w-target").value.trim(), best: parseFloat($("#w-best").value) || w.best };
      savePlan("Weight settings saved"); editing.delete("weight"); renderWeight();
    };
    return;
  }
  const last = weights.at(-1);
  const todayEntry = weights.find(x => x.date === todayKey);
  const cur = last ? last.lb : w.start;
  const delta = +(cur - w.start).toFixed(1);
  const day = k => new Date(k + "T12:00:00").toLocaleDateString(undefined, { month: "short", day: "numeric" });
  body.innerHTML = `
    <div class="w-now"><span class="big">${esc(cur)} lb</span>${last ? `<span class="small muted">weighed ${last.date === todayKey ? "today" : esc(day(last.date))}</span>` : ""}</div>
    <div class="w-stats">
      <div class="goal"><b class="${delta < 0 ? "delta-down" : delta > 0 ? "delta-up" : ""}">${delta === 0 ? "±0" : (delta < 0 ? "▼ " : "▲ ") + Math.abs(delta)}</b><span>since ${esc(day(w.startDate))}</span></div>
      <div class="goal"><b>${esc(w.target)}</b><span>target</span></div>
      <div class="goal"><b>${esc(w.best)}</b><span>lowest</span></div>
    </div>
    ${spark()}
    <div class="weight-row w-log">
      <input class="txt" id="w-in" type="number" step="0.1" placeholder="Today" value="${todayEntry ? todayEntry.lb : ""}"><span class="muted">lb</span>
      <button class="sm primary" id="w-log">${todayEntry ? "Update" : "Log"}</button>
      ${todayEntry ? `<button class="sm ghost" id="w-del">Remove</button>` : ""}
    </div>`;
  const log = async () => {
    const lb = parseFloat($("#w-in").value);
    if (!(lb > 50 && lb < 700)) return toast("Enter a weight in lb", true);
    try {
      $("#w-log").disabled = true;
      const r = await post("/api/weights", { date: todayKey, lb });
      weights = weights.filter(x => x.date !== todayKey).concat({ date: todayKey, lb: Math.round(lb * 10) / 10 }).sort((a, b) => a.date.localeCompare(b.date));
      r.warning ? toast(r.warning, true) : toast("Weight logged · sent to MyFitnessPal");
      const weighIn = plan.checklist.find(i => /weigh/i.test(i));
      if (weighIn && !checkState(weighIn)) setCheck(weighIn, "done");
      renderWeight();
    } catch (e) { toast(e.message, true); $("#w-log").disabled = false; }
  };
  $("#w-log").onclick = log;
  $("#w-in").onkeydown = e => { if (e.key === "Enter") log(); };
  const del = $("#w-del");
  if (del) del.onclick = async () => {
    try {
      await post("/api/weights", { date: todayKey, lb: null }); weights = weights.filter(x => x.date !== todayKey); renderWeight();
      toast("Removed here. Remove it in MyFitnessPal too, or the next sync brings it back.");
    }
    catch (e) { toast(e.message, true); }
  };
}

function spark() {
  const from = new Date(now); from.setDate(from.getDate() - 90);
  const pts = weights.filter(x => x.date >= dayKey(from));
  if (pts.length < 2) return `<p class="small muted" style="margin:8px 0 0">Log a few weigh-ins to see your trend.</p>`;
  const lbs = pts.map(p => p.lb), min = Math.min(...lbs) - 1, max = Math.max(...lbs) + 1;
  const W = 300, H = 60, x = i => (i / (pts.length - 1)) * W, y = v => H - ((v - min) / (max - min)) * H;
  const d = pts.map((p, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(p.lb).toFixed(1)}`).join(" ");
  return `<svg class="spark" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none"><path d="${d}" fill="none" stroke="var(--accent)" stroke-width="2" vector-effect="non-scaling-stroke"/></svg>
    <div class="small muted" style="display:flex;justify-content:space-between">${[pts[0], pts.at(-1)].map(p => `<span>${esc(new Date(p.date + "T12:00:00").toLocaleDateString(undefined, { month: "short", day: "numeric" }))}</span>`).join("")}</div>`;
}

function renderGoals() {
  if (foodForm?.isConnected) return;  // a food form is open; redraw after it closes
  if (mfp?.data && !editing.has("goals")) return renderNutrition();
  const el = $("#card-goals");
  el.innerHTML = cardHead("Daily goals", "goals") + `<div id="g-body"></div>`;
  wireEditBtn(el);
  const body = $("#g-body");
  if (editing.has("goals")) {
    rowEditor(body, plan.goals, [{ key: "label", placeholder: "Nutrient" }, { key: "value", placeholder: "Value", narrow: true }, { key: "unit", placeholder: "Unit", narrow: true }], rows => {
      if (rows) { plan.goals = rows.map(([label, value, unit]) => ({ label, value, unit })); savePlan("Goals saved"); }
      editing.delete("goals"); renderGoals();
    }, "Add goal");
    return;
  }
  body.innerHTML = `<div class="goals">${plan.goals.map(g => `<div class="goal"><b>${esc(g.value)}</b><span>${esc(g.label)}${g.unit ? " · " + esc(g.unit) : ""}</span></div>`).join("")}</div>
    <p class="small muted" style="margin:10px 0 0">${mfp?.error ? `<span class="err">MyFitnessPal: ${esc(mfp.error)}</span> <a class="link" href="#mcp">Set up</a>` : mfp?.syncing ? "Connecting to MyFitnessPal…" : "Change the calorie and macro goals in MyFitnessPal too."}</p>`;
}
