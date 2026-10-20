// History tab: skip history, activity heatmap and feed, time log

// ---------- history ----------
// Why things were skipped: checklist skips + tasks deleted with a "Skipped:"/"Dismissed:" note.
function taskSkips() {
  const out = [];
  for (const t of allTasks || []) {
    if (t.status !== "deleted") continue;
    for (const a of t.annotations || []) {
      const text = a.description || "";
      if (/^(Skipped|Dismissed)/.test(text))
        out.push({ uuid: t.uuid, description: t.description, due: t.due, kind: text.startsWith("Dismissed") ? "dismiss" : "skip", note: text.includes(":") ? text.split(":").slice(1).join(":").trim() : "", at: a.entry });
    }
  }
  return out;
}
function loadHistory() { renderHistory(); }

function renderHistory() {
  const days = new Map();
  const day = k => { if (!days.has(k)) days.set(k, { checks: [], tasks: new Map() }); return days.get(k); };
  for (const [k, items] of Object.entries(checklist))
    for (const [item, v] of Object.entries(items))
      if (cellState(v) === "skip") day(k).checks.push({ item, note: cellNote(v) });
  // Task skips, by the day you skipped them. Copies skipped together are one entry.
  for (const s of taskSkips()) {
    const k = dayKey(twDate(s.at));
    const g = day(k).tasks, key = `${s.description}|${s.kind}|${s.note}|${s.at}`;
    if (!g.has(key)) g.set(key, { ...s, count: 0, dues: [], uuids: [] });
    const e = g.get(key); e.count++; e.uuids.push(s.uuid); if (s.due) e.dues.push(twDate(s.due));
  }
  const el = $("#history");
  el.classList.remove("muted");
  const fmt = d => d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  const keys = [...days.keys()].filter(k => days.get(k).checks.length || days.get(k).tasks.size).sort().reverse();
  el.innerHTML = keys.length ? keys.map(k => {
    const d = days.get(k), date = new Date(k + "T12:00:00");
    const r = dayRatio(k), st = checklist[k] || {};
    const doneN = plan.checklist.filter(i => cellState(st[i]) === "done").length, skipN = plan.checklist.filter(i => cellState(st[i]) === "skip").length;
    return `<div class="hist-day"><h3><span>${date.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", year: "numeric" })}</span>
        <span class="small muted">${checklist[k] ? `checklist ${doneN} done · ${skipN} skipped${r >= 1 ? " · complete ✓" : ""}` : ""}</span></h3>
      ${d.checks.map(c => `<div class="hist-item">${esc(c.item)} · ${whyBtn(c.note, `data-why-day="${k}" data-why-item="${esc(c.item)}"`)}</div>`).join("")}
      ${[...d.tasks.values()].map(t => {
        const dues = t.dues.sort((a, b) => a - b);
        const span = t.count > 1 && dues.length ? ` <span class="muted small">${t.count} copies, ${fmt(dues[0])}–${fmt(dues.at(-1))}</span>` : "";
        return `<div class="hist-item${t.kind === "dismiss" ? " dismiss" : ""}"><b>${esc(t.description)}</b> <span class="badge">task</span>${span} · ${whyBtn(t.note, `data-why-uuids="${t.uuids.join(",")}"`)}</div>`;
      }).join("")}</div>`;
  }).join("") : `<p class="muted">Nothing skipped yet. When you skip a checklist item or a task, the day and your reason show up here.</p>`;
}

// ---------- activity: heatmap + feed ----------
let hmMode = "tasks", selectedDay = null, feedDays = 10, yearIntervals = null;
try { hmMode = localStorage.getItem("hmMode") || "tasks"; } catch {}

async function loadYearTime() {
  try { const j = await (await fetch("/api/time?range=year")).json(); yearIntervals = j.available ? j.intervals : []; }
  catch { yearIntervals = []; }
  renderActivity();
}

function hoursByDay() {
  const m = new Map();
  for (const iv of yearIntervals || []) {
    const s = twDate(iv.start), e = iv.end ? twDate(iv.end) : new Date();
    const k = dayKey(s); m.set(k, (m.get(k) || 0) + (e - s) / 3.6e6);
  }
  return m;
}

// One entry per thing that happened, by local day.
function taskEvents() {
  const ev = [];
  for (const t of allTasks || []) {
    if (t.status === "completed" && t.end) ev.push({ day: dayKey(twDate(t.end)), type: "done", t });
    if (t.status === "deleted" && t.end) {
      const why = (t.annotations || []).map(a => a.description).find(a => /^(Skipped|Dismissed)/.test(a));
      ev.push({ day: dayKey(twDate(t.end)), type: why?.startsWith("Dismissed") ? "dismiss" : why ? "skip" : "deleted", t, note: why && why.includes(":") ? why.split(":").slice(1).join(":").trim() : "" });
    }
    if (!t.parent && t.entry) ev.push({ day: dayKey(twDate(t.entry)), type: t.status === "recurring" ? "repeat" : "added", t });
    if (t.start && t.status === "pending") ev.push({ day: dayKey(twDate(t.start)), type: "started", t });
  }
  return ev;
}

function heatValues() {
  const m = new Map();
  if (hmMode === "tasks") for (const e of taskEvents()) { if (e.type === "done") m.set(e.day, (m.get(e.day) || 0) + 1); }
  else if (hmMode === "hours") for (const [k, h] of hoursByDay()) m.set(k, h);
  else for (const k of Object.keys(checklist)) { const r = dayRatio(k); if (r > 0) m.set(k, r); }
  return m;
}

function renderActivity() {
  const hm = $("#heatmap");
  if (!hm) return;
  document.querySelectorAll("#hm-mode button").forEach(b => b.classList.toggle("on", b.dataset.mode === hmMode));
  if (!allTasks) { hm.innerHTML = `<p class="muted">Loading…</p>`; return; }
  if (hmMode === "hours" && !yearIntervals) { hm.innerHTML = `<p class="muted">Loading Timewarrior…</p>`; loadYearTime(); return; }
  // 53 weeks ending this week, Sunday-first columns like GitHub.
  const end = startOfDay(now), start = new Date(end);
  start.setDate(start.getDate() - 364 - start.getDay());
  const startKey = dayKey(start);
  const vals = new Map([...heatValues()].filter(([k]) => k >= startKey && k <= todayKey));
  // Shade by quartiles of the active days, so one huge day doesn't wash out the rest.
  const sorted = [...vals.values()].filter(Boolean).sort((a, b) => a - b);
  const q = f => sorted[Math.min(sorted.length - 1, Math.floor(f * sorted.length))] ?? 0;
  const [q1, q2, q3] = [q(.25), q(.5), q(.75)];
  const level = v => !v ? 0 : hmMode === "checklist" ? (v >= 1 ? 4 : v >= .66 ? 3 : v >= .33 ? 2 : 1) : v > q3 ? 4 : v > q2 ? 3 : v > q1 ? 2 : 1;
  const cells = [], months = [];
  let col = 0, lastMonth = -1;
  for (const d = new Date(start); d <= end; d.setDate(d.getDate() + 1)) {
    if (d.getDay() === 0) {
      if (d.getMonth() !== lastMonth) { months.push(`<span style="grid-column:${col + 1}">${d.toLocaleDateString(undefined, { month: "short" })}</span>`); lastMonth = d.getMonth(); }
      col++;
    }
    const k = dayKey(d), v = vals.get(k) || 0;
    const label = hmMode === "tasks" ? `${v} task${v === 1 ? "" : "s"} done` : hmMode === "hours" ? `${fmtDur(v * 3.6e6)} tracked` : `checklist ${Math.round(v * 100)}%`;
    cells.push(`<button class="hm-cell l${level(v)}${k === selectedDay ? " sel" : ""}" data-day="${k}" style="grid-row:${d.getDay() + 1}" title="${esc(fmtDay(d))}: ${label}"></button>`);
  }
  // Summary line + streak of active days.
  const total = [...vals.values()].reduce((a, b) => a + b, 0);
  let streak = 0; const s = new Date(end);
  if (!vals.get(dayKey(s))) s.setDate(s.getDate() - 1);
  while (vals.get(dayKey(s))) { streak++; s.setDate(s.getDate() - 1); }
  const activeDays = [...vals.values()].filter(Boolean).length;
  const summary = hmMode === "tasks" ? `${total} task${total === 1 ? "" : "s"} completed in the last year · ${activeDays} active day${activeDays === 1 ? "" : "s"}`
    : hmMode === "hours" ? `${fmtDur(total * 3.6e6)} tracked in the last year · ${activeDays} day${activeDays === 1 ? "" : "s"}`
    : `${[...vals.values()].filter(v => v >= 1).length} complete checklist days · ${activeDays} days with progress`;
  hm.innerHTML = `<div class="hm-wrap"><div class="hm-months" style="grid-template-columns:repeat(${col},11px)">${months.join("")}</div>
      <div class="hm-grid" style="grid-template-columns:repeat(${col},11px)">${cells.join("")}</div></div>
    <div class="hm-foot"><span class="small muted">${summary}${streak ? ` · current streak ${streak} day${streak === 1 ? "" : "s"}` : ""}</span>
      <span class="hm-legend small muted">Less <i class="hm-cell l0"></i><i class="hm-cell l1"></i><i class="hm-cell l2"></i><i class="hm-cell l3"></i><i class="hm-cell l4"></i> More</span></div>`;
  const wrap = hm.querySelector(".hm-wrap"); wrap.scrollLeft = wrap.scrollWidth;
  hm.querySelectorAll("[data-day]").forEach(c => c.onclick = () => { selectedDay = selectedDay === c.dataset.day ? null : c.dataset.day; renderActivity(); });
  renderFeed();
}

function renderFeed() {
  const byDay = new Map();
  for (const e of taskEvents()) { if (!byDay.has(e.day)) byDay.set(e.day, []); byDay.get(e.day).push(e); }
  for (const k of Object.keys(checklist)) if (!byDay.has(k) && Object.keys(checklist[k]).length) byDay.set(k, []);
  const hours = yearIntervals ? hoursByDay() : null;
  let keys = [...byDay.keys()].filter(k => k <= todayKey).sort().reverse();
  if (selectedDay) keys = [selectedDay];
  const shown = keys.slice(0, feedDays);
  const LABEL = { done: "Completed", added: "Added", repeat: "Added repeating", started: "Started", skip: "Skipped", dismiss: "Dismissed", deleted: "Deleted" };
  const ICON = { done: "✓", added: "+", repeat: "↻", started: "▶", skip: "↷", dismiss: "✕", deleted: "✕" };
  $("#feed-title").textContent = selectedDay ? new Date(selectedDay + "T12:00:00").toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", year: "numeric" }) : "Recent activity";
  $("#feed-clear").hidden = !selectedDay;
  $("#feed").innerHTML = shown.length ? shown.map(k => {
    const evs = byDay.get(k) || [];
    // Collapse repeats (e.g. 30 skipped copies of one task) into one line.
    const groups = new Map();
    for (const e of evs) {
      const key = `${e.type}|${e.t.description}|${e.note || ""}`;
      if (!groups.has(key)) groups.set(key, { ...e, count: 0 });
      groups.get(key).count++;
    }
    const order = ["done", "started", "added", "repeat", "skip", "dismiss", "deleted"];
    const items = [...groups.values()].sort((a, b) => order.indexOf(a.type) - order.indexOf(b.type));
    const st = checklist[k], r = st ? dayRatio(k) : 0, h = hours?.get(k);
    const doneN = evs.filter(e => e.type === "done").length;
    return `<div class="hist-day"><h3><span>${esc(fmtDay(new Date(k + "T12:00:00")))}</span>
        <span class="small muted">${[doneN ? `${doneN} done` : "", h ? `${fmtDur(h * 3.6e6)} tracked` : "", st ? `checklist ${Math.round(r * 100)}%` : ""].filter(Boolean).join(" · ")}</span></h3>
      ${items.map(e => `<div class="feed-item ev-${e.type}"><span class="ev-icon">${ICON[e.type]}</span><span>${LABEL[e.type]} <b>${esc(e.t.description)}</b>${e.count > 1 ? ` <span class="badge">×${e.count}</span>` : ""}${e.t.project ? ` <span class="badge">${esc(e.t.project)}</span>` : ""}${e.note ? ` · <span class="why">${esc(e.note)}</span>` : ""}</span></div>`).join("") || `<div class="small muted">No task activity.</div>`}
    </div>`;
  }).join("") + (keys.length > feedDays ? `<button class="sm" id="feed-more" style="margin-top:10px">Show older</button>` : "")
    : `<p class="muted">${selectedDay ? "Nothing happened this day." : "No activity yet."}</p>`;
  const more = $("#feed-more"); if (more) more.onclick = () => { feedDays += 10; renderFeed(); };
}

document.querySelectorAll("#hm-mode button").forEach(b => b.onclick = () => {
  hmMode = b.dataset.mode; try { localStorage.setItem("hmMode", hmMode); } catch {}
  renderActivity();
});
$("#feed-clear").onclick = () => { selectedDay = null; renderActivity(); };

// ---------- time log: view, add and edit Timewarrior entries, any week ----------
let tlWeek = 0, tlIntervals = [], tlEdit = null;
function tlRange() {
  const mon = startOfDay(now); mon.setDate(mon.getDate() - ((mon.getDay() + 6) % 7) + tlWeek * 7);
  const sun = new Date(mon); sun.setDate(mon.getDate() + 6);
  return [mon, sun];
}
const localDT = d => `${dayKey(d)}T${hhmm(d)}`;
async function loadTimeLog() {
  const [mon, sun] = tlRange();
  try {
    const j = await (await fetch(`/api/time?from=${dayKey(mon)}&to=${dayKey(sun)}`)).json();
    if (j.error) throw new Error(j.error);
    tlIntervals = j.available ? j.intervals : null;
  } catch (e) { $("#timelog").innerHTML = `<p class="err">Timewarrior: ${esc(e.message)}</p>`; return; }
  renderTimeLog();
}
function renderTimeLog() {
  const [mon, sun] = tlRange();
  $("#wk-label").textContent = `${fmtDay(mon)} – ${fmtDay(sun)}`;
  const add = $("#tl-add");
  if (!add.day.value || add.day.value < dayKey(mon) || add.day.value > dayKey(sun)) add.day.value = tlWeek === 0 ? todayKey : dayKey(mon);
  // Suggest task names (pending + repeating) for the "what" box.
  $("#tl-tags").innerHTML = [...new Set([...tasks, ...(allTasks || []).filter(t => t.status === "recurring")].map(t => t.description))].sort().map(d => `<option value="${esc(d)}">`).join("");
  renderTaskPicker(add.task);
  const el = $("#timelog");
  el.classList.remove("muted");
  if (tlIntervals === null) { el.innerHTML = `<p class="muted small">Timewarrior isn't available. Start the dashboard with <code>nix run</code>.</p>`; return; }
  const byDay = new Map();
  for (const iv of tlIntervals) { const k = dayKey(twDate(iv.start)); if (!byDay.has(k)) byDay.set(k, []); byDay.get(k).push(iv); }
  const days = [...byDay.keys()].sort().reverse();
  const total = tlIntervals.reduce((s, iv) => s + ((iv.end ? twDate(iv.end) : new Date()) - twDate(iv.start)), 0);
  el.innerHTML = (days.length ? `<div class="small muted" style="margin-bottom:6px">${fmtDur(total)} this week</div>` : "") + (days.length ? days.map(k => {
    const ivs = byDay.get(k).sort((a, b) => a.start.localeCompare(b.start));
    const sum = ivs.reduce((s, iv) => s + ((iv.end ? twDate(iv.end) : new Date()) - twDate(iv.start)), 0);
    return `<div class="hist-day"><h3><span>${esc(fmtDay(new Date(k + "T12:00:00")))}</span><span class="small muted">${fmtDur(sum)}</span></h3>
      ${ivs.map(iv => {
        const s = twDate(iv.start), e = iv.end ? twDate(iv.end) : null, key = `${iv.id}|${iv.start}`;
        if (tlEdit === key) return `<form class="tl-edit" data-key="${esc(key)}">
            <input class="txt" type="datetime-local" name="start" value="${localDT(s)}" required>
            ${e ? `– <input class="txt" type="datetime-local" name="end" value="${localDT(e)}" required>` : `<span class="small muted">– still running</span>`}
            <input class="txt" name="tags" value="${esc((iv.tags || []).join(", "))}" required>
            <span><button class="sm primary">Save</button> <button type="button" class="sm" data-tl-cancel>Cancel</button></span></form>`;
        return `<div class="tl-row"><span class="tl-time">${timeRange(s, e)}</span><span class="tl-dur">${fmtDur((e || new Date()) - s)}</span>
          <span class="tl-tags">${(iv.tags || []).map(t => `<span class="badge">${esc(t)}</span>`).join(" ") || `<span class="muted small">untagged</span>`}</span>
          <span class="tl-btns"><button class="sm" data-tl-edit="${esc(key)}">Edit</button><button class="sm ghost" data-tl-del="${esc(key)}">Delete</button></span></div>`;
      }).join("")}</div>`;
  }).join("") : `<p class="muted small">Nothing tracked this week. Log time above, or press Start on a task.</p>`);
  const split = key => { const [id, start] = key.split("|"); return { id: +id, start_was: start }; };
  el.querySelectorAll("[data-tl-edit]").forEach(b => b.onclick = () => { tlEdit = b.dataset.tlEdit; renderTimeLog(); });
  el.querySelectorAll("[data-tl-cancel]").forEach(b => b.onclick = () => { tlEdit = null; renderTimeLog(); });
  el.querySelectorAll("[data-tl-del]").forEach(b => b.onclick = async () => {
    if (!confirm("Delete this time entry?")) return;
    try { await post("/api/time/delete", split(b.dataset.tlDel)); toast("Entry deleted"); tlEdit = null; loadTimeLog(); loadTime(); yearIntervals = null; renderActivity(); }
    catch (e) { toast(e.message, true); }
  });
  el.querySelectorAll("form.tl-edit").forEach(f => f.onsubmit = async e => {
    e.preventDefault();
    const iv = tlIntervals.find(x => `${x.id}|${x.start}` === f.dataset.key);
    const body = split(f.dataset.key);
    if (f.start.value !== localDT(twDate(iv.start))) body.start = f.start.value;
    if (f.end && f.end.value !== localDT(twDate(iv.end))) body.end = f.end.value;
    const tags = f.tags.value.split(",").map(t => t.trim()).filter(Boolean);
    if (tags.join("|") !== (iv.tags || []).join("|")) body.tags = tags;
    if (f.end && (body.end || localDT(twDate(iv.end))) <= (body.start || localDT(twDate(iv.start)))) return toast("End must be after start", true);
    try { await post("/api/time/modify", body); toast("Entry updated"); tlEdit = null; loadTimeLog(); loadTime(); yearIntervals = null; renderActivity(); }
    catch (err) { toast(err.message, true); }
  });
}
// Task picker for logging time: pending tasks, repeating ones, and tasks done in the last
// two weeks. Picking one fills "what" with the tags the Taskwarrior hook uses when you press
// Start (description, project, task tags), so the time lands on the same task.
const timewTags = t => [t.description, ...(t.project ? [t.project] : []), ...(t.tags || [])];
function renderTaskPicker(sel) {
  const was = sel.value, seen = new Set(), recent = new Date(); recent.setDate(recent.getDate() - 14);
  const group = (label, list) => {
    const opts = list.map(t => ({ t, tags: timewTags(t).join(", ") })).filter(o => !seen.has(o.tags) && seen.add(o.tags))
      .sort((a, b) => a.t.description.localeCompare(b.t.description))
      .map(o => `<option value="${esc(o.tags)}">${esc(o.t.description)}${o.t.project ? ` · ${esc(o.t.project)}` : ""}</option>`).join("");
    return opts ? `<optgroup label="${label}">${opts}</optgroup>` : "";
  };
  sel.innerHTML = `<option value="">Pick a task…</option>` +
    group("Pending", tasks) +
    group("Repeating", (allTasks || []).filter(t => t.status === "recurring")) +
    group("Done recently", (allTasks || []).filter(t => t.status === "completed" && t.end && twDate(t.end) >= recent));
  sel.value = [...sel.options].some(o => o.value === was) ? was : "";
}
$("#tl-add").task.onchange = e => { if (e.target.value) { $("#tl-add").tags.value = e.target.value; $("#tl-add").tags.focus(); } };
$("#tl-add").tags.oninput = e => { if (e.target.value !== e.target.form.task.value) e.target.form.task.value = ""; };
$("#tl-add").onsubmit = async e => {
  e.preventDefault();
  const f = e.target, tags = f.tags.value.split(",").map(t => t.trim()).filter(Boolean);
  let endDay = f.day.value;
  if (f.end.value <= f.start.value) { const d = new Date(f.day.value + "T12:00:00"); d.setDate(d.getDate() + 1); endDay = dayKey(d); }  // past midnight
  try {
    await post("/api/time/track", { start: `${f.day.value}T${f.start.value}`, end: `${endDay}T${f.end.value}`, tags });
    toast("Time logged"); f.start.value = f.end.value = f.tags.value = f.task.value = "";
    loadTimeLog(); loadTime(); yearIntervals = null; renderActivity();
  } catch (err) { toast(err.message, true); }
};
document.querySelectorAll("[data-wk]").forEach(b => b.onclick = () => { tlWeek = +b.dataset.wk === 0 ? 0 : Math.min(0, tlWeek + +b.dataset.wk); tlEdit = null; loadTimeLog(); });
