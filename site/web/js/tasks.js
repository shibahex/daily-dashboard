// Tasks: dates, grouping repeating copies, task rows, editor markup

// ---------- tasks ----------
function twDate(s) { return s ? new Date(s.replace(/^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z$/, "$1-$2-$3T$4:$5:$6Z")) : null; }
function startOfDay(d) { const x = new Date(d); x.setHours(0, 0, 0, 0); return x; }
// Taskwarrior date -> what goes in an edit box: "2026-10-21", or "2026-10-21T14:30" when it has a real time.
function twInput(s) {
  const d = twDate(s); if (!d) return "";
  const pad = n => String(n).padStart(2, "0");
  const day = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return hm === "00:00" || hm === "23:59" ? day : `${day}T${hm}`;
}
const fmtDay = d => d.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });

let allTasks = null;          // every task (any status), for Completed/Deleted views and History
let taskView = "pending";     // pending | completed | deleted
let openEditor = null;        // uuid of the task whose editor is open
let doneLimit = 50;

// Recurring tasks pile up one pending copy per missed occurrence; show each series once.
function groupTasks(list) {
  const groups = new Map();
  for (const t of list) {
    const key = t.parent || t.uuid;
    const g = groups.get(key);
    const due = twDate(t.due);
    if (!g) groups.set(key, { ...t, dueDate: due, copies: 1, overdueCopies: 0 });
    else {
      g.copies++;
      if (due && (!g.dueDate || due < g.dueDate)) Object.assign(g, { ...t, dueDate: due, copies: g.copies, overdueCopies: g.overdueCopies });
    }
  }
  const today = startOfDay(now);
  for (const g of groups.values()) g.overdueUuids = [];
  for (const t of list) {
    const d = twDate(t.due);
    if (d && d < today) {
      const g = groups.get(t.parent || t.uuid);
      g.overdueCopies++;
      g.overdueUuids.push(t.uuid);
    }
  }
  return [...groups.values()];
}

function bucket(t) {
  if (!t.dueDate) return "none";
  const today = startOfDay(now), tomorrow = new Date(today); tomorrow.setDate(today.getDate() + 1);
  const week = new Date(today); week.setDate(today.getDate() + 7);
  if (t.dueDate < today) return "overdue";
  if (t.dueDate < tomorrow) return "today";
  if (t.dueDate < week) return "week";
  return "later";
}

function badgesFor(t) {
  return [
    t.priority ? `<span class="badge pri-${esc(t.priority)}">${{ H: "high", M: "medium", L: "low" }[t.priority] || esc(t.priority)}</span>` : "",
    t.project ? `<span class="badge">${esc(t.project)}</span>` : "",
    t.recur ? `<span class="badge">↻ ${esc(t.recur)}</span>` : "",
    t.overdueCopies > 1 ? `<span class="badge warn">${t.overdueCopies} missed</span>` : "",
    t.start && t.status === "pending" ? `<span class="badge active">active</span>` : "",
    t.annotations?.length ? `<span class="badge" title="${esc(t.annotations.map(a => a.description).join("\n"))}">✎ ${t.annotations.length}</span>` : "",
    ...(t.tags || []).map(x => `<span class="badge">+${esc(x)}</span>`),
  ].join("");
}

function taskRow(t) {
  const b = bucket(t);
  const dueText = t.dueDate ? (b === "overdue" ? "was due " : "due ") + esc(fmtDay(t.dueDate)) : "no due date";
  // Recurring series: skip every missed copy (or today's copy) so only the next one is left.
  // One-off task: dismiss it. Both ask for an optional reason, saved as a Taskwarrior annotation.
  const skipBtn = (uuids, label) => `<button class="sm ghost" data-act="skip" data-uuids="${uuids.join(",")}" data-name="${esc(t.description)}">${label}</button>`;
  const dismiss = t.recur
    ? (t.overdueCopies ? skipBtn(t.overdueUuids, `Skip ${t.overdueCopies} missed`) : b === "today" ? skipBtn([t.uuid], "Skip today") : "")
    : `<button class="sm ghost" data-act="dismiss" data-uuids="${t.uuid}" data-name="${esc(t.description)}">Dismiss</button>`;
  return `<div class="task" data-task="${t.uuid}">
    <div class="title" data-open="${t.uuid}" title="Click to edit">${esc(t.description)}</div>
    <div class="meta"><span>${dueText}</span>${badgesFor(t)}</div>
    <div class="actions">
      <button class="sm primary" data-act="done" data-uuid="${t.uuid}" data-active="${t.start ? 1 : ""}" title="${t.copies > 1 ? "Completes the oldest copy" : ""}">Done</button>
      <button class="sm" data-act="done-on" data-uuid="${t.uuid}" data-due="${t.dueDate ? dayKey(t.dueDate) : ""}" title="Mark done on an earlier day">Done on…</button>
      ${t.recur && t.overdueCopies ? `<button class="sm" data-act="catchup" data-parent="${t.parent}">Catch up</button>` : ""}
      <button class="sm" data-act="${t.start ? "stop" : "start"}" data-uuid="${t.uuid}">${t.start ? "Stop" : "Start"}</button>
      <button class="sm" data-open="${t.uuid}">Edit</button>
      ${dismiss}
    </div>
    ${openEditor === t.uuid ? editorHtml(t) : ""}
  </div>`;
}

// A skip reason you can click to change later. Tasks pass data-why-uuids; checklist items
// pass data-why-day and data-why-item.
const whyBtn = (note, attrs) => `<button class="why-edit${note ? "" : " empty"}" ${attrs} data-why-note="${esc(note)}" title="Edit reason">${note ? esc(note) : "+ add a reason"}</button>`;
// { label: "Skipped" | "Dismissed", note } from a deleted task's annotation, or null.
function skipInfo(t) {
  const a = (t.annotations || []).map(a => a.description).find(a => /^(Skipped|Dismissed)/.test(a));
  return a ? { label: a.startsWith("Dismissed") ? "Dismissed" : "Skipped", note: a.includes(":") ? a.split(":").slice(1).join(":").trim() : "" } : null;
}
const skipLine = t => { const s = skipInfo(t); return s ? `<span class="why small">${s.label} · ${whyBtn(s.note, `data-why-uuids="${t.uuid}"`)}</span>` : ""; };

document.addEventListener("click", e => { const b = e.target.closest(".why-edit"); if (b) editWhy(b); });
function editWhy(b) {
  const box = document.createElement("span");
  box.className = "why-box";
  box.innerHTML = `<input class="txt" maxlength="300" placeholder="Why? (optional)"><button class="sm primary">Save</button><button class="sm">Cancel</button>`;
  const [input, ok, cancel] = box.children;
  input.value = b.dataset.whyNote;
  b.replaceWith(box);
  input.focus(); input.select();
  const close = () => box.replaceWith(b);
  const save = async () => {
    const note = input.value.trim().replace(/\s+/g, " ");
    ok.disabled = true;
    try {
      if (b.dataset.whyUuids) {
        const uuids = b.dataset.whyUuids.split(",");
        await post("/api/tasks/skip_note", { uuids, note });
        for (const t of allTasks || []) {
          if (!uuids.includes(t.uuid)) continue;
          const a = (t.annotations || []).find(a => /^(Skipped|Dismissed)/.test(a.description));
          if (a) { const label = a.description.startsWith("Dismissed") ? "Dismissed" : "Skipped"; a.description = note ? `${label}: ${note}` : label; }
        }
      } else {
        const day = b.dataset.whyDay, item = b.dataset.whyItem, st = { ...(checklist[day] || {}) };
        if (cellState(st[item]) !== "skip") return close();
        st[item] = note ? { state: "skip", note } : "skip";
        await post("/api/checklist", { date: day, items: st });
        checklist[day] = st;
        if (day === todayKey) renderChecklist();
      }
      b.dataset.whyNote = note;
      b.classList.toggle("empty", !note);
      b.innerHTML = note ? esc(note) : "+ add a reason";
      close();
      toast("Reason saved");
      renderHistory();
      if (!b.closest(".panel")) renderTasks();   // don't wipe an open Catch up panel
    } catch (err) { toast(err.message, true); ok.disabled = false; }
  };
  ok.onclick = save;
  cancel.onclick = close;
  input.onkeydown = e => { if (e.key === "Enter") save(); if (e.key === "Escape") close(); };
}

function closedRow(t) {
  const end = twDate(t.end);
  return `<div class="task" data-task="${t.uuid}">
    <div class="title" data-open="${t.uuid}" title="Click to edit">${esc(t.description)}</div>
    <div class="meta"><span>${t.status === "completed" ? "completed" : "deleted"} ${end ? esc(fmtDay(end)) : ""}</span>${badgesFor(t)}${skipLine(t)}</div>
    <div class="actions">
      <button class="sm" data-act="reopen" data-uuid="${t.uuid}">${t.status === "completed" ? "Reopen" : "Restore"}</button>
      <button class="sm" data-open="${t.uuid}">Edit</button>
    </div>
    ${openEditor === t.uuid ? editorHtml(t) : ""}
  </div>`;
}

function editorHtml(t) {
  const field = (name, label, value, ph = "") => `<label><span>${label}</span><input class="txt" name="${name}" value="${esc(value)}" placeholder="${esc(ph)}"></label>`;
  return `<form class="task-editor" data-editor="${t.uuid}">
    <label class="wide"><span>Description</span><input class="txt" name="description" value="${esc(t.description)}" maxlength="300" required></label>
    ${field("project", "Project", t.project || "", "e.g. study")}
    ${field("tags", "Tags", (t.tags || []).join(" "), "space separated")}
    <label><span>Priority</span><select class="txt" name="priority">${[["", "None"], ["H", "High"], ["M", "Medium"], ["L", "Low"]].map(([v, l]) => `<option value="${v}"${(t.priority || "") === v ? " selected" : ""}>${l}</option>`).join("")}</select></label>
    ${t.status === "completed" || t.status === "deleted" ? field("end", t.status === "completed" ? "Completed on" : "Deleted on", twInput(t.end) || "", "2026-10-12T20:00") : ""}
    ${t.status === "recurring" ? "" : `${field("due", t.parent ? "Due (this copy)" : "Due", twInput(t.due), "today, fri, 2026-10-20")}
    ${field("scheduled", "Scheduled", twInput(t.scheduled), "when to start")}
    ${field("wait", "Hide until", twInput(t.wait), "hidden until this date")}`}
    ${t.status === "pending" || t.status === "recurring" || t.status === "waiting" ? repeatHtml(t) : ""}
    ${t.parent ? `<div class="wide scope"><span>Apply name, project, tags and priority to</span>
      <label><input type="radio" name="scope" value="all" checked> every repeat of this task</label>
      <label><input type="radio" name="scope" value="one"> just this copy</label></div>`
      : t.status === "recurring" ? `<input type="hidden" name="scope" value="all">` : ""}
    <div class="wide notes-edit"><span>Notes</span>
      ${(t.annotations || []).map((a, i) => `<div class="anno"><span class="small muted">${esc(fmtDay(twDate(a.entry)))}</span> <span>${esc(a.description)}</span>
        <button type="button" class="icon del" data-denotate="${i}" title="Remove note">✕</button></div>`).join("") || `<div class="small muted">No notes yet.</div>`}
      <div class="anno-add"><input class="txt" name="note" placeholder="Add a note…" maxlength="500"><button type="button" class="sm" data-annotate>Add note</button></div>
    </div>
    <div class="wide foot"><span class="small muted">${t.entry ? `added ${esc(fmtDay(twDate(t.entry)))}` : ""} · <code>${esc(t.uuid.slice(0, 8))}</code></span>
      <span><button type="button" class="sm" data-cancel>Cancel</button> <button class="sm primary">Save</button></span></div>
  </form>`;
}

function findTask(uuid) { return tasks.find(t => t.uuid === uuid) || allTasks?.find(t => t.uuid === uuid); }
