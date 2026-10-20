// Time tracked card (Timewarrior)

// ---------- time tracking (Timewarrior) ----------
let intervals = [], timeAvailable = true;
const fmtDur = ms => { const m = Math.round(ms / 60000); return m < 60 ? `${m}m` : `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`; };

async function loadTime() {
  try {
    const r = await fetch("/api/time");
    const j = await r.json();
    if (!r.ok) throw new Error(j.error);
    timeAvailable = j.available; intervals = j.intervals;
  } catch (e) { $("#time-body").innerHTML = `<p class="err">Timewarrior: ${esc(e.message)}</p>`; return; }
  renderTime();
}

function renderTime() {
  const body = $("#time-body");
  body.classList.remove("muted");
  if (!timeAvailable) {
    body.innerHTML = `<p class="muted small">Timewarrior isn't available. Start the dashboard with <code>nix run</code> (or <code>./run.sh</code>) from the site folder.</p>`;
    return;
  }
  const t = Date.now(), dayStart = startOfDay(new Date()).getTime();
  const per = new Map(); let week = 0, today = 0, running = null;
  for (const iv of intervals) {
    const s = twDate(iv.start).getTime(), e = iv.end ? twDate(iv.end).getTime() : t;
    const label = (iv.tags || []).join(" · ") || "untagged";
    per.set(label, (per.get(label) || 0) + (e - s));
    week += e - s;
    today += Math.max(0, e - Math.max(s, dayStart));
    if (!iv.end) running = { label, s };
  }
  const rows = [...per.entries()].sort((a, b) => b[1] - a[1]);
  const max = rows[0]?.[1] || 1;
  body.innerHTML =
    (running ? `<div class="small" style="color:var(--accent);margin-bottom:8px">● Tracking <b>${esc(running.label)}</b> · ${fmtDur(t - running.s)}</div>` : "") +
    `<div style="display:flex;gap:24px;margin-bottom:10px"><div><div class="big">${fmtDur(today)}</div><div class="small muted">today</div></div>
      <div><div class="big">${fmtDur(week)}</div><div class="small muted">this week</div></div></div>` +
    (rows.length ? rows.map(([label, ms]) => `<div style="margin:6px 0"><div class="small" style="display:flex;justify-content:space-between;gap:8px"><span style="overflow-wrap:anywhere">${esc(label)}</span><span class="muted">${fmtDur(ms)}</span></div>
      <div class="progress" style="margin:3px 0 0"><div class="d" style="width:${(ms / max) * 100}%"></div></div></div>`).join("")
      : `<p class="small muted" style="margin:0">Nothing tracked this week. Press <b>Start</b> on a task to start the clock; <b>Stop</b> or <b>Done</b> stops it.</p>`) +
    `<p style="margin:10px 0 0"><a href="#history" class="small link" id="tl-link">Edit or add time →</a></p>`;
  $("#tl-link").onclick = () => setTimeout(() => $("#card-timelog").scrollIntoView({ behavior: "smooth" }), 50);
}
setInterval(() => { if (!document.hidden && intervals.some(iv => !iv.end)) renderTime(); }, 30_000);
