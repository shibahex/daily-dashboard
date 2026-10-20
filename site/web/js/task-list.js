// Task lists and loading tasks

function matches(t, q) { return !q || `${t.description} ${t.project || ""} ${(t.tags || []).join(" ")} ${(t.annotations || []).map(a => a.description).join(" ")}`.toLowerCase().includes(q); }

function renderTasks() {
  const grouped = groupTasks(tasks).sort((a, b) => (a.dueDate || Infinity) - (b.dueDate || Infinity) || b.urgency - a.urgency);
  const q = $("#filter").value.toLowerCase();
  const el = $("#all-tasks");
  el.classList.remove("muted");
  document.querySelectorAll("#task-views button").forEach(b => b.classList.toggle("on", b.dataset.view === taskView));
  if (taskView === "pending") {
    const shown = grouped.filter(t => matches(t, q));
    const sections = [["overdue", "Overdue"], ["today", "Due today"], ["week", "Next 7 days"], ["later", "Later"], ["none", "No due date"]];
    el.innerHTML = shown.length ? sections.map(([k, label]) => {
      const items = shown.filter(t => bucket(t) === k);
      return items.length ? `<div class="section-title${k === "overdue" ? " warn" : ""}">${label} · ${items.length}</div>` + items.map(taskRow).join("") : "";
    }).join("") : `<p class="muted">No pending tasks${q ? " match" : ""}.</p>`;
  } else if (!allTasks) {
    el.innerHTML = `<p class="muted">Loading…</p>`;
  } else if (taskView === "repeating") {
    el.innerHTML = renderRepeating(q);
  } else {
    // Copies of a repeating task are one row (expandable). In Deleted, copies Taskwarrior or a
    // reschedule removed without a reason are hidden unless asked for.
    const why = t => (t.annotations || []).some(a => /^(Skipped|Dismissed)/.test(a.description));
    let list = allTasks.filter(t => t.status === taskView && matches(t, q));
    let hidden = 0;
    if (taskView === "deleted" && !showAutoDeleted) {
      const keep = list.filter(t => !((t.parent || t.recur) && !why(t)));
      hidden = list.length - keep.length; list = keep;
    }
    const groups = new Map();
    for (const t of list) {
      const key = t.parent || t.recur ? "r|" + t.description + "|" + (t.project || "") : t.uuid;
      if (!groups.has(key)) groups.set(key, { key, items: [] });
      groups.get(key).items.push(t);
    }
    const rows = [...groups.values()].map(g => (g.items.sort((a, b) => (b.end || "").localeCompare(a.end || "")), g))
      .sort((a, b) => (b.items[0].end || "").localeCompare(a.items[0].end || ""));
    el.innerHTML = (taskView === "deleted" ? `<label class="small muted toggle"><input type="checkbox" id="show-auto"${showAutoDeleted ? " checked" : ""}> Show repeat copies removed without a reason${hidden ? ` (${hidden} hidden)` : ""}</label>` : "") +
      (rows.length
        ? `<div class="section-title">${list.length} ${taskView}${rows.length !== list.length ? ` · ${rows.length} tasks` : ""}</div>` + rows.slice(0, doneLimit).map(groupRow).join("") +
          (rows.length > doneLimit ? `<button class="sm" id="more-done" style="margin-top:10px">Show ${Math.min(50, rows.length - doneLimit)} more</button>` : "")
        : `<p class="muted">No ${taskView} tasks${q ? " match" : ""}.</p>`);
    const more = $("#more-done"); if (more) more.onclick = () => { doneLimit += 50; renderTasks(); };
    const auto = $("#show-auto"); if (auto) auto.onchange = () => { showAutoDeleted = auto.checked; renderTasks(); };
    el.querySelectorAll("[data-expand]").forEach(b => b.onclick = () => { const k = b.dataset.expand; expanded.has(k) ? expanded.delete(k) : expanded.add(k); renderTasks(); });
  }
  wire(el);
}

let showAutoDeleted = false;
const expanded = new Set();
function groupRow(g) {
  if (g.items.length === 1) return closedRow(g.items[0]);
  const last = g.items[0], first = g.items.at(-1), verb = last.status === "completed" ? "completed" : "deleted";
  const open = expanded.has(g.key);
  return `<div class="task">
    <div class="title">${esc(last.description)} <span class="badge">×${g.items.length}</span></div>
    <div class="meta"><span>${verb} ${g.items.length} times · last ${esc(fmtDay(twDate(last.end)))}${first.end ? `, first ${esc(fmtDay(twDate(first.end)))}` : ""}</span>${last.project ? `<span class="badge">${esc(last.project)}</span>` : ""}</div>
    <div class="actions"><button class="sm" data-expand="${esc(g.key)}">${open ? "Hide copies" : "Show copies"}</button></div>
    ${open ? `<div class="copies">${g.items.map(t => {
      const why = skipLine(t);
      return `<div class="copy"><span>${esc(fmtDay(twDate(t.end)))}${t.due ? ` <span class="muted small">(was due ${esc(fmtDay(twDate(t.due)))})</span>` : ""}${why ? ` · ${why}` : ""}</span>
        <button class="sm" data-act="reopen" data-uuid="${t.uuid}">${verb === "completed" ? "Reopen" : "Restore"}</button></div>`;
    }).join("")}</div>` : ""}
  </div>`;
}

// One row per repeating task (all its series together), so you edit the task, not a copy.
function renderRepeating(q) {
  const groups = new Map();
  for (const t of allTasks.filter(t => t.status === "recurring")) {
    const k = t.description + "|" + (t.project || "");
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k).push(t);
  }
  const rows = [...groups.values()].filter(g => matches(g[0], q))
    .map(g => ({ g, next: nextDueOf(g) })).sort((a, b) => (a.next || Infinity) - (b.next || Infinity));
  if (!rows.length) return `<p class="muted">No repeating tasks${q ? " match" : ""}. Use “Make it repeat” when editing a task, or pick a repeat when adding one.</p>`;
  return `<div class="section-title">${rows.length} repeating</div>` + rows.map(({ g, next }) => {
    const ids = new Set(g.map(x => x.uuid));
    const rep = tasks.filter(t => ids.has(t.parent)).sort((a, b) => (a.due || "").localeCompare(b.due || ""))[0] || g[0];
    const missed = tasks.filter(t => ids.has(t.parent) && twDate(t.due) < startOfDay(now)).length;
    return `<div class="task">
      <div class="title" data-open="${rep.uuid}" title="Click to edit">${esc(g[0].description)}</div>
      <div class="meta"><span>${esc(scheduleSummary(g))}${next ? ` · next ${esc(fmtDay(next))}` : ""}</span>${g[0].project ? `<span class="badge">${esc(g[0].project)}</span>` : ""}${missed ? `<span class="badge warn">${missed} missed</span>` : ""}${(g[0].tags || []).map(x => `<span class="badge">+${esc(x)}</span>`).join("")}</div>
      <div class="actions"><button class="sm" data-open="${rep.uuid}">Edit schedule &amp; details</button></div>
      ${openEditor === rep.uuid ? editorHtml(rep) : ""}
    </div>`;
  }).join("");
}

function renderTodayTasks() {
  const urgent = groupTasks(tasks).sort((a, b) => (a.dueDate || Infinity) - (b.dueDate || Infinity)).filter(t => ["overdue", "today"].includes(bucket(t)));
  const el = $("#today-tasks");
  el.classList.remove("muted");
  el.innerHTML = urgent.length ? urgent.map(taskRow).join("") : `<p class="muted">Nothing due today. 🎉</p>`;
  wire(el);
}

function renderAllTaskViews() { renderTasks(); renderTodayTasks(); }

async function loadTasks() {
  try {
    const r = await fetch("/api/tasks");
    if (!r.ok) throw new Error((await r.json()).error);
    tasks = await r.json();
    renderAllTaskViews();
  } catch (e) {
    $("#all-tasks").innerHTML = $("#today-tasks").innerHTML = `<p class="err">Couldn't load Taskwarrior: ${esc(e.message)}</p>`;
  }
}

async function loadAllTasks() {
  try {
    const r = await fetch("/api/tasks/all");
    if (!r.ok) throw new Error((await r.json()).error);
    allTasks = await r.json();
  } catch (e) { toast("Couldn't load task history: " + e.message, true); return; }
  $("#projects").innerHTML = [...new Set(allTasks.map(t => t.project).filter(Boolean))].sort().map(p => `<option value="${esc(p)}">`).join("");
  if (taskView !== "pending") renderTasks();
  renderHistory(); renderActivity(); renderTaskPicker($("#tl-add").task);
}

async function refreshTasks() { await loadTasks(); loadAllTasks(); }

async function undoLast() {
  try { await post("/api/tasks/undo", {}); toast("Undone"); await refreshTasks(); loadTime(); }
  catch (e) { toast("Couldn't undo: " + e.message, true); }
}

$("#filter").oninput = () => { doneLimit = 50; renderTasks(); };
document.querySelectorAll("#task-views button").forEach(b => b.onclick = () => { taskView = b.dataset.view; openEditor = null; doneLimit = 50; renderTasks(); });
$("#undo").onclick = undoLast;
$("#add").onsubmit = async e => {
  e.preventDefault();
  const f = new FormData(e.target);
  const tags = String(f.get("tags") || "").split(/[\s,]+/).map(s => s.replace(/^\+/, "")).filter(Boolean);
  try {
    await post("/api/tasks/add", { description: f.get("description"), project: f.get("project"), due: f.get("due"), priority: f.get("priority"), recur: f.get("recur"), tags });
    e.target.reset(); toast("Task added", false, true); await refreshTasks();
  } catch (err) { toast(err.message, true); }
};
