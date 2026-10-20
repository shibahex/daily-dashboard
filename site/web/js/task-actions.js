// Task buttons: editor, done (time taken), done on, catch up, skip reasons

function wireEditor(el) {
  el.querySelectorAll("form[data-editor]").forEach(form => {
    const t = findTask(form.dataset.editor);
    if (!t) return;
    form.querySelector("[data-cancel]").onclick = () => { openEditor = null; renderAllTaskViews(); };
    wireRepeat(form, t);
    form.querySelectorAll("[data-denotate]").forEach(b => b.onclick = async () => {
      const a = t.annotations[+b.dataset.denotate];
      try { await post("/api/tasks/denotate", { uuid: t.uuid, text: a.description }); toast("Note removed", false, true); await refreshTasks(); }
      catch (e) { toast(e.message, true); }
    });
    const addNote = async () => {
      const text = form.note.value.trim(); if (!text) return;
      try { await post("/api/tasks/annotate", { uuid: t.uuid, text }); toast("Note added", false, true); await refreshTasks(); }
      catch (e) { toast(e.message, true); }
    };
    form.querySelector("[data-annotate]").onclick = addNote;
    form.note.onkeydown = e => { if (e.key === "Enter") { e.preventDefault(); addNote(); } };
    form.onsubmit = async e => {
      e.preventDefault();
      // Send only what changed.
      const f = {}, v = n => form[n].value.trim();
      if (v("description") !== t.description) f.description = v("description");
      if (v("project") !== (t.project || "")) f.project = v("project");
      if (v("priority") !== (t.priority || "")) f.priority = v("priority");
      for (const k of ["due", "scheduled", "wait", "end"]) if (form[k] && v(k) !== twInput(t[k])) f[k] = v(k);
      const oldTags = new Set(t.tags || []), newTags = new Set(v("tags").split(/[\s,]+/).map(s => s.replace(/^\+/, "")).filter(Boolean));
      const add = [...newTags].filter(x => !oldTags.has(x)), rem = [...oldTags].filter(x => !newTags.has(x));
      if (add.length) f.tags_add = add;
      if (rem.length) f.tags_remove = rem;
      if (!Object.keys(f).length) { openEditor = null; renderAllTaskViews(); return; }
      try {
        await post("/api/tasks/modify", { uuid: t.uuid, fields: f, scope: form.scope?.value || "one", templates: groupOf(t).map(g => g.uuid) });
        openEditor = null; toast("Saved", false, true); await refreshTasks();
      } catch (err) { toast(err.message, true); }
    };
  });
}

function wire(el) {
  el.querySelectorAll("[data-open]").forEach(x => x.onclick = () => {
    openEditor = openEditor === x.dataset.open ? null : x.dataset.open;
    renderAllTaskViews();
    if (openEditor) el.querySelector(`form[data-editor="${openEditor}"] input[name=description]`)?.focus();
  });
  el.querySelectorAll("button[data-act]").forEach(btn => btn.onclick = async () => {
    const act = btn.dataset.act;
    if (act === "skip" || act === "dismiss") return askReason(btn, act);
    if (act === "done" && !btn.dataset.active) return askDone(btn);
    if (act === "done-on") return askDoneOn(btn);
    if (act === "catchup") return askCatchUp(btn);
    btn.disabled = true;
    try {
      await post("/api/tasks/" + act, { uuid: btn.dataset.uuid });
      toast({ done: "Done ✓", start: "Started", stop: "Stopped", reopen: "Reopened" }[act], false, true);
      await refreshTasks(); loadTime();
    } catch (e) { toast("Taskwarrior: " + e.message, true); btn.disabled = false; }
  });
  wireEditor(el);
}

// Panels that open under a task's buttons (one at a time per task).
function openPanel(btn, cls, html) {
  const actions = btn.closest(".actions");
  actions.parentElement.querySelectorAll(".reason, .panel").forEach(x => x.remove());
  const box = document.createElement("div");
  box.className = "panel " + cls;
  box.innerHTML = html;
  actions.after(box);
  return box;
}
const hhmm = d => `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;  // for inputs
// For display, 12-hour: "7:00 PM". A range shows AM/PM once when both ends share it: "7:00–7:45 PM".
const clock12 = d => `${d.getHours() % 12 || 12}:${String(d.getMinutes()).padStart(2, "0")}`;
const ampm = d => d.getHours() < 12 ? "AM" : "PM";
const timeRange = (s, e) => !e ? `${clock12(s)} ${ampm(s)}–now`
  : ampm(s) === ampm(e) ? `${clock12(s)}–${clock12(e)} ${ampm(e)}` : `${clock12(s)} ${ampm(s)}–${clock12(e)} ${ampm(e)}`;

// How long something took: "30", "45m", "1h", "1.5h", "1h30", "1h 30m", "2 hours", "1:30".
// Returns minutes, null when blank (don't log time), NaN when it can't be read.
function parseMins(s) {
  s = String(s || "").trim().toLowerCase();
  if (!s) return null;
  let m = s.match(/^(\d+):(\d{1,2})$/);
  if (m) return +m[1] * 60 + +m[2];
  m = s.match(/^(?:(\d+(?:\.\d+)?)\s*h(?:ours?|rs?)?)?\s*(?:(\d+)\s*(?:m(?:ins?|inutes?)?)?)?$/);
  if (!m || (!m[1] && !m[2])) return NaN;
  return Math.round((+m[1] || 0) * 60 + (+m[2] || 0));
}
// Reads a "took" box; toasts and returns undefined when the value is unusable.
function readMins(input) {
  const m = parseMins(input.value);
  if (m === null || (m >= 1 && m <= 1440)) return m;
  toast("Time taken: use minutes or hours, like 30, 45m, 1h 30m (up to 24h)", true);
  input.focus();
}
const TOOK_INPUT = `<input class="txt num" data-m value="30" placeholder="30m" title="Minutes or hours: 30, 45m, 1h, 1h 30m. Leave blank to not log time.">`;
const fmtMins = m => m < 60 ? `${m} min` : fmtDur(m * 60000);

// "Done": ask how long it took (default 30 min) and log that in Timewarrior, ending now.
// A task that was started with Start skips this: its timer already logged the real time.
function askDone(btn) {
  const box = openPanel(btn, "done", `
    <label>Took ${TOOK_INPUT}</label>
    <span class="small muted">e.g. 45m, 1h 30m; blank = don't log</span>
    <span><button class="sm primary" data-ok>Done</button> <button class="sm" data-x>Cancel</button></span>`);
  const q = s => box.querySelector(s), input = q("[data-m]");
  input.focus(); input.select();
  q("[data-x]").onclick = () => box.remove();
  const go = async () => {
    const m = readMins(input);
    if (m === undefined) return;
    q("[data-ok]").disabled = true;
    try {
      const r = m ? await post("/api/tasks/done_at", { uuid: btn.dataset.uuid, end: `${todayKey}T${hhmm(new Date())}`, minutes: m })
                  : await post("/api/tasks/done", { uuid: btn.dataset.uuid });
      r.warning ? toast(r.warning, true) : toast(`Done ✓${m ? ` · ${fmtMins(m)} logged` : ""}`, false, true);
      await refreshTasks(); loadTime(); loadTimeLog();
    } catch (e) { toast("Taskwarrior: " + e.message, true); q("[data-ok]").disabled = false; }
  };
  q("[data-ok]").onclick = go;
  input.onkeydown = e => { if (e.key === "Enter") go(); if (e.key === "Escape") box.remove(); };
}

// "Done on…": complete as of an earlier day/time, optionally logging how long it took.
function askDoneOn(btn) {
  const due = btn.dataset.due, past = due && due < todayKey;
  const day = past ? due : todayKey;
  const box = openPanel(btn, "done-on", `
    <label>Done on <input class="txt" type="date" data-d value="${day}" max="${todayKey}"></label>
    <label>at <input class="txt" type="time" data-t value="${day === todayKey ? hhmm(new Date()) : "20:00"}"></label>
    <label>took ${TOOK_INPUT} <span class="small muted">(blank = don't log time)</span></label>
    <span><button class="sm primary" data-ok>Mark done</button> <button class="sm" data-x>Cancel</button></span>`);
  const q = s => box.querySelector(s);
  q("[data-d]").onchange = e => { if (e.target.value !== todayKey && q("[data-t]").value === hhmm(new Date())) q("[data-t]").value = "20:00"; };
  q("[data-x]").onclick = () => box.remove();
  q("[data-ok]").onclick = async () => {
    const d = q("[data-d]").value, t = q("[data-t]").value || "20:00", m = readMins(q("[data-m]"));
    if (m === undefined) return;
    if (!d) return toast("Pick a date", true);
    if (d > todayKey) return toast("That's in the future", true);
    q("[data-ok]").disabled = true;
    try {
      const r = await post("/api/tasks/done_at", { uuid: btn.dataset.uuid, end: `${d}T${t}`, minutes: m });
      r.warning ? toast(r.warning, true) : toast(`Marked done on ${fmtDay(new Date(d + "T12:00:00"))}${m ? ` · ${fmtMins(m)} logged` : ""}`, false, true);
      await refreshTasks(); loadTime(); loadTimeLog();
    } catch (e) { toast(e.message, true); q("[data-ok]").disabled = false; }
  };
}

// "Catch up": go through each missed copy of a repeating task and mark it done that day or skip it.
function askCatchUp(btn) {
  const today = startOfDay(now);
  const missed = tasks.filter(t => t.parent === btn.dataset.parent && twDate(t.due) < today).sort((a, b) => a.due.localeCompare(b.due));
  const box = openPanel(btn, "catchup", `
    <div class="small muted">Mark each missed day as done (on that day) or skip it.
      Time per session ${TOOK_INPUT} <span>(blank = don't log time)</span></div>
    ${missed.map(t => `<div class="cu-row" data-u="${t.uuid}" data-day="${dayKey(twDate(t.due))}"><span>${esc(fmtDay(twDate(t.due)))}</span>
      <span class="cu-btns">${CU_BTNS}</span></div>`).join("")}
    <div class="rep-actions"><span class="cu-btns" data-rest>${CU_REST}</span><button class="sm" data-close>Close</button></div>`);
  let changed = false, lastNote = "";
  const finish = async () => { box.remove(); if (changed) { await refreshTasks(); loadTime(); loadTimeLog(); } };
  const rows = () => [...box.querySelectorAll(".cu-row:not(.handled)")];
  // Skip asks why first (optional), in place of the buttons. The last reason is prefilled,
  // since missed days in a row usually share one.
  const askWhy = (slot, label, restore, go) => {
    slot.innerHTML = `<input class="txt" maxlength="300" placeholder="Why? (optional)" value="${esc(lastNote)}">
      <button class="sm primary">${label}</button><button class="sm">Cancel</button>`;
    const [input, ok, cancel] = slot.children;
    input.focus(); input.select();
    const confirm = async () => {
      ok.disabled = true; lastNote = input.value.trim();
      try { await go(lastNote); } catch (e) { toast(e.message, true); ok.disabled = false; }
    };
    ok.onclick = confirm;
    cancel.onclick = () => { slot.innerHTML = restore; };
    input.onkeydown = e => { if (e.key === "Enter") confirm(); if (e.key === "Escape") cancel.onclick(); };
  };
  const skipped = (row, note) => {
    row.querySelector(".cu-btns").innerHTML = `<span class="why">↷ skipped · ${whyBtn(note, `data-why-uuids="${row.dataset.u}"`)}</span>`;
    row.classList.add("handled"); changed = true;
  };
  box.onclick = async e => {
    const b = e.target.closest("button");
    if (!b) return;
    if (b.dataset.cu === "skip") {
      const row = b.closest(".cu-row");
      return askWhy(b.parentElement, "Skip", CU_BTNS, async note => {
        await post("/api/tasks/skip", { uuids: [row.dataset.u], note, kind: "skip" });
        skipped(row, note);
      });
    }
    if (b.dataset.cu === "done") {
      const row = b.closest(".cu-row"), m = readMins(box.querySelector("[data-m]"));
      if (m === undefined) return;
      row.querySelectorAll("button").forEach(x => x.disabled = true);
      try {
        const r = await post("/api/tasks/done_at", { uuid: row.dataset.u, end: `${row.dataset.day}T20:00`, minutes: m });
        row.querySelector(".cu-btns").innerHTML = `<span class="ok">✓ done${m ? ` · ${fmtMins(m)}` : ""}</span>${r.warning ? ` <span class="err small">${esc(r.warning)}</span>` : ""}`;
        row.classList.add("handled"); changed = true;
      } catch (err) { toast(err.message, true); row.querySelectorAll("button").forEach(x => x.disabled = false); }
      return;
    }
    if (b.hasAttribute("data-skip-rest")) {
      const rest = rows();
      if (!rest.length) return finish();
      return askWhy(b.parentElement, `Skip ${rest.length}`, CU_REST, async note => {
        await post("/api/tasks/skip", { uuids: rest.map(r => r.dataset.u), note, kind: "skip" });
        rest.forEach(r => skipped(r, note));
        toast(`Skipped ${rest.length}${note ? " · reason saved" : ""}`, false, true);
        finish();
      });
    }
    if (b.hasAttribute("data-close")) finish();
  };
}
const CU_BTNS = `<button class="sm primary" data-cu="done">Done that day</button><button class="sm ghost" data-cu="skip">Skip</button>`;
const CU_REST = `<button class="sm ghost" data-skip-rest>Skip all remaining</button>`;

// Inline "why?" box under the task's buttons. The reason is optional.
function askReason(btn, act) {
  const actions = btn.closest(".actions");
  actions.parentElement.querySelectorAll(".reason, .panel").forEach(x => x.remove());
  const uuids = btn.dataset.uuids.split(",");
  const what = act === "dismiss" ? "Dismiss" : uuids.length > 1 ? `Skip ${uuids.length}` : "Skip";
  const box = document.createElement("div");
  box.className = "reason";
  box.innerHTML = `<input class="txt" maxlength="300" placeholder="Why? e.g. sick, worked late, rest day (optional)">
    <button class="sm primary">${what}</button><button class="sm">Cancel</button>`;
  actions.after(box);
  const [input, ok, cancel] = box.children;
  input.focus();
  cancel.onclick = () => box.remove();
  const go = async () => {
    ok.disabled = true;
    try {
      await post("/api/tasks/skip", { uuids, note: input.value, kind: act });
      toast(act === "dismiss" ? "Dismissed" : `Skipped${uuids.length > 1 ? " " + uuids.length : ""}${input.value.trim() ? " · reason saved" : ""}`, false, true);
      await refreshTasks();
    } catch (e) { toast("Taskwarrior: " + e.message, true); ok.disabled = false; }
  };
  ok.onclick = go;
  input.onkeydown = e => { if (e.key === "Enter") go(); if (e.key === "Escape") box.remove(); };
}
