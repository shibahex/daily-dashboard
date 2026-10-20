// Repeat schedules for tasks

// ---------- repeat schedules ----------
// Taskwarrior keeps one hidden "template" per repeating series; "Mon, Wed, Fri" is three series
// with the same name. A "group" is all templates sharing a name + project.
const SHORT_DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
function recurInfo(r) {
  r = String(r || "").toLowerCase();
  const named = { daily: [1, "d"], day: [1, "d"], weekly: [1, "w"], week: [1, "w"], biweekly: [2, "w"], fortnight: [2, "w"],
    monthly: [1, "m"], month: [1, "m"], quarterly: [3, "m"], yearly: [1, "y"], annual: [1, "y"], weekdays: [1, "weekdays"] };
  if (named[r]) return { n: named[r][0], unit: named[r][1] };
  let m = r.match(/^p(\d+)([dwmy])$/);
  if (m) { const n = +m[1], u = m[2]; return u === "d" && n % 7 === 0 ? { n: n / 7, unit: "w" } : { n, unit: u }; }
  m = r.match(/^(\d+)\s*(d|days?|w|wks?|weeks?|mo|mths?|months?|y|yrs?|years?)$/);
  if (m) { const u = m[2][0] === "m" ? "m" : m[2][0]; return u === "d" && +m[1] % 7 === 0 ? { n: +m[1] / 7, unit: "w" } : { n: +m[1], unit: u }; }
  return { n: 0, unit: r };
}
function templateOf(t) { return !t ? null : t.status === "recurring" ? t : t.parent ? allTasks?.find(x => x.uuid === t.parent) : null; }
function groupOf(t) {
  const tpl = templateOf(t);
  if (!tpl || !allTasks) return [];
  return allTasks.filter(x => x.status === "recurring" && x.description === tpl.description && (x.project || "") === (tpl.project || ""));
}
function scheduleSummary(group) {
  if (!group.length) return "Doesn't repeat";
  const infos = group.map(g => ({ ...recurInfo(g.recur), day: twDate(g.due)?.getDay(), due: twDate(g.due) }));
  if (infos.every(i => i.unit === "w" && i.n === infos[0].n)) {
    const days = [...new Set(infos.map(i => i.day))].sort().map(d => SHORT_DAYS[d]).join(", ");
    return `${infos[0].n === 1 ? "Every week" : `Every ${infos[0].n} weeks`} on ${days}`;
  }
  return infos.map(i => i.unit === "d" ? (i.n === 1 ? "Every day" : `Every ${i.n} days`)
    : i.unit === "m" ? `${i.n === 1 ? "Monthly" : `Every ${i.n} months`} on the ${i.due ? ordinal(i.due.getDate()) : "same day"}`
    : i.unit === "w" ? `Every ${i.n === 1 ? "week" : i.n + " weeks"} on ${SHORT_DAYS[i.day]}`
    : i.unit === "y" ? "Yearly" : i.unit === "weekdays" ? "Every weekday" : `Repeats (${esc(i.unit)})`).join(" + ");
}
const ordinal = n => n + (n % 10 === 1 && n !== 11 ? "st" : n % 10 === 2 && n !== 12 ? "nd" : n % 10 === 3 && n !== 13 ? "rd" : "th");
function nextDueOf(group) {
  const ids = new Set(group.map(g => g.uuid));
  return tasks.filter(t => ids.has(t.parent)).map(t => twDate(t.due)).filter(Boolean).sort((a, b) => a - b)[0] || null;
}
// Starting values for the schedule form, from the current schedule.
function ruleFrom(group, t) {
  const next = nextDueOf(group) || twDate(t.due) || new Date();
  const info = group.length ? recurInfo(group[0].recur) : { n: 1, unit: "w" };
  const type = info.unit === "d" ? "days" : info.unit === "m" ? "monthly" : "weekly";
  const days = group.length && info.unit === "w" ? [...new Set(group.map(g => twDate(g.due).getDay()))] : [next.getDay()];
  return { type, every: info.n || 1, days, start: dayKey(next) };
}
// Next few dates a rule would produce (for the preview line); mirrors the server's series_plan.
function previewRule(r) {
  const today = startOfDay(now), out = [];
  let start = new Date(r.start + "T12:00:00"); if (isNaN(start)) start = new Date(today);
  if (r.type === "days") { while (start < today) start.setDate(start.getDate() + r.every); for (let i = 0; i < 4; i++) { out.push(new Date(start)); start.setDate(start.getDate() + r.every); } }
  else if (r.type === "weekly") {
    for (const d of r.days) {
      const f = new Date(start); f.setDate(f.getDate() + ((d - f.getDay() + 7) % 7));
      while (f < today) f.setDate(f.getDate() + 7 * r.every);
      for (let i = 0; i < 4; i++) { out.push(new Date(f)); f.setDate(f.getDate() + 7 * r.every); }
    }
    out.sort((a, b) => a - b); out.length = Math.min(out.length, 5);
  } else { const f = new Date(start); f.setDate(Math.min(f.getDate(), 28)); while (f < today) f.setMonth(f.getMonth() + 1); for (let i = 0; i < 3; i++) { out.push(new Date(f)); f.setMonth(f.getMonth() + 1); } }
  return out;
}

function repeatHtml(t) {
  const group = groupOf(t), r = ruleFrom(group, t);
  return `<div class="wide repeat" data-rule='${esc(JSON.stringify(r))}'>
    <span>Repeat</span>
    <div class="rep-summary"><b>${esc(scheduleSummary(group))}</b>${group.length ? ` <span class="small muted">· next ${esc(fmtDay(nextDueOf(group) || twDate(t.due) || now))}</span>` : ""}
      <button type="button" class="sm" data-rep-open>${group.length ? "Change schedule" : "Make it repeat"}</button></div>
    <div class="rep-form" hidden>
      <div class="seg rep-type"><button type="button" data-rtype="days">Every N days</button><button type="button" data-rtype="weekly">On weekdays</button><button type="button" data-rtype="monthly">Monthly</button></div>
      <div class="rep-row" data-show="days">Every <input class="txt num" type="number" min="1" max="60" data-every-days> day(s)</div>
      <div class="rep-row" data-show="weekly"><span class="daypick">${SHORT_DAYS.map((d, i) => `<button type="button" data-wd="${i}">${d}</button>`).join("")}</span>
        every <select class="txt" data-every-weeks>${[1, 2, 3, 4].map(n => `<option value="${n}">${n === 1 ? "week" : n === 2 ? "2 weeks (every other)" : n + " weeks"}</option>`).join("")}</select></div>
      <div class="rep-row" data-show="monthly">On the same day each month as the start date.</div>
      <div class="rep-row">Starting <input class="txt" type="date" data-start></div>
      <div class="rep-preview small muted"></div>
      <div class="rep-actions">${group.length ? `<button type="button" class="sm ghost" data-rep-stop>Stop repeating</button>` : "<span></span>"}
        <span><button type="button" class="sm" data-rep-cancel>Cancel</button> <button type="button" class="sm primary" data-rep-save>Save schedule</button></span></div>
      <div class="small muted">Saving rebuilds the repeating series in Taskwarrior; missed copies of the old schedule are removed.</div>
    </div>
  </div>`;
}

function wireRepeat(form, t) {
  const box = form.querySelector(".repeat");
  if (!box) return;
  const rule = JSON.parse(box.dataset.rule), group = groupOf(t);
  const q = s => box.querySelector(s);
  const draw = () => {
    box.querySelectorAll("[data-rtype]").forEach(b => b.classList.toggle("on", b.dataset.rtype === rule.type));
    box.querySelectorAll("[data-show]").forEach(row => row.hidden = row.dataset.show !== rule.type);
    box.querySelectorAll("[data-wd]").forEach(b => b.classList.toggle("on", rule.days.includes(+b.dataset.wd)));
    const dates = previewRule(rule);
    q(".rep-preview").textContent = dates.length ? "Next: " + dates.map(fmtDay).join(", ") + "…" : "";
  };
  q("[data-every-days]").value = rule.type === "days" ? rule.every : 1;
  q("[data-every-weeks]").value = rule.type === "weekly" ? Math.min(rule.every, 4) : 1;
  q("[data-start]").value = rule.start;
  q("[data-rep-open]").onclick = () => { q(".rep-form").hidden = false; q(".rep-summary").hidden = true; draw(); };
  q("[data-rep-cancel]").onclick = () => { q(".rep-form").hidden = true; q(".rep-summary").hidden = false; };
  box.querySelectorAll("[data-rtype]").forEach(b => b.onclick = () => {
    rule.type = b.dataset.rtype;
    rule.every = rule.type === "days" ? +q("[data-every-days]").value || 1 : rule.type === "weekly" ? +q("[data-every-weeks]").value : 1;
    draw();
  });
  box.querySelectorAll("[data-wd]").forEach(b => b.onclick = () => {
    const d = +b.dataset.wd;
    rule.days = rule.days.includes(d) ? rule.days.filter(x => x !== d) : [...rule.days, d];
    draw();
  });
  q("[data-every-days]").oninput = e => { rule.every = Math.max(1, Math.min(60, +e.target.value || 1)); draw(); };
  q("[data-every-weeks]").onchange = e => { rule.every = +e.target.value; draw(); };
  q("[data-start]").onchange = e => { rule.start = e.target.value; draw(); };
  q("[data-rep-save]").onclick = async () => {
    if (rule.type === "weekly" && !rule.days.length) return toast("Pick at least one day", true);
    try {
      const r = await post("/api/series/schedule", { source: t.uuid, templates: group.map(g => g.uuid), rule: { ...rule, every: rule.type === "monthly" ? 1 : rule.every } });
      openEditor = null; toast(`Schedule saved${r.series > 1 ? ` (${r.series} weekly series)` : ""}`, false, true); await refreshTasks();
    } catch (e) { toast(e.message, true); }
  };
  const stop = q("[data-rep-stop]");
  if (stop) stop.onclick = async () => {
    if (!confirm(`Stop repeating “${t.description}”? Its pending copies are removed too.`)) return;
    try { await post("/api/series/stop", { templates: group.map(g => g.uuid) }); openEditor = null; toast("No longer repeats", false, true); await refreshTasks(); }
    catch (e) { toast(e.message, true); }
  };
}
