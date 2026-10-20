import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Services.UI

// Keeps the dashboard server running and polls it for today's checklist and due tasks.
// The bar widget and panel read everything from here.
Item {
  id: root

  property var pluginApi: null

  readonly property var cfg: pluginApi?.pluginSettings || ({})
  readonly property var defaults: pluginApi?.manifest?.metadata?.defaultSettings || ({})
  readonly property string baseUrl: String(cfg.url ?? defaults.url ?? "http://localhost:1628").replace(/\/+$/, "")
  // The site folder from the settings, or found next to this plugin (see findSite).
  readonly property string siteDir: (cfg.siteDir || defaults.siteDir) ? expandHome(cfg.siteDir || defaults.siteDir) : foundSiteDir
  property string foundSiteDir: ""
  readonly property bool autoStart: cfg.autoStart ?? defaults.autoStart ?? true
  readonly property string openCommand: cfg.openCommand ?? defaults.openCommand ?? "chromium --app={url}"
  readonly property string port: (baseUrl.match(/:(\d+)$/) || [])[1] || "1628"

  property bool online: false
  property bool loaded: false          // at least one successful refresh
  property string lastError: ""
  property string todayKey: dayKey(new Date())
  property var checklistItems: []      // [{ name, state: "done" | "skip" | "", note }]
  property var dueTasks: []            // [{ uuid, overdueUuids, description, project, due, overdue, copies, active }]
  property var pendingTasks: []        // [{ uuid, description, project, active }] for the timer's task picker
  property var tracking: null          // { uuid, label, start } — uuid is "" when timew was started outside Taskwarrior
  property bool timeAvailable: false
  property var intervals: []           // this week's Timewarrior intervals
  property real now: Date.now()        // ticks every second while tracking so elapsed times update

  readonly property real todayMs: sumTracked(true)
  readonly property real weekMs: sumTracked(false)

  readonly property int doneCount: checklistItems.filter(i => i.state === "done").length
  readonly property int skipCount: checklistItems.filter(i => i.state === "skip").length
  readonly property bool dayComplete: checklistItems.length > 0 && doneCount + skipCount === checklistItems.length
  readonly property bool serverStarting: serverProc.running && !online
  readonly property bool ownsServer: serverProc.running   // false when it was started from a terminal

  function expandHome(p) { return String(p).replace(/^~(?=\/|$)/, Quickshell.env("HOME")); }
  function dayKey(d) { return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`; }
  function twDate(s) { return s ? new Date(s.replace(/^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z$/, "$1-$2-$3T$4:$5:$6Z")) : null; }
  function cellState(v) { return typeof v === "object" && v ? v.state : (v === true ? "done" : (v || "")); }
  function cellNote(v) { return typeof v === "object" && v ? (v.note || "") : ""; }
  // Same format as the dashboard: 42m, 1h 05m. withSeconds for the live clock: 4:07, 1:02:09.
  function fmtDur(ms, withSeconds) {
    if (withSeconds) {
      const s = Math.max(0, Math.floor(ms / 1000)), h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60;
      return (h ? h + ":" + String(m).padStart(2, "0") : m) + ":" + String(s % 60).padStart(2, "0");
    }
    const m = Math.round(ms / 60000);
    return m < 60 ? `${m}m` : `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
  }
  // Same parser as the dashboard: "30", "45m", "1h", "1.5h", "1h30", "1h 30m", "1:30".
  // Minutes, null when blank, NaN when unreadable.
  function parseMins(s) {
    s = String(s || "").trim().toLowerCase();
    if (!s) return null;
    let m = s.match(/^(\d+):(\d{1,2})$/);
    if (m) return +m[1] * 60 + +m[2];
    m = s.match(/^(?:(\d+(?:\.\d+)?)\s*h(?:ours?|rs?)?)?\s*(?:(\d+)\s*(?:m(?:ins?|inutes?)?)?)?$/);
    if (!m || (!m[1] && !m[2])) return NaN;
    return Math.round((+m[1] || 0) * 60 + (+m[2] || 0));
  }
  function sumTracked(todayOnly) {
    const dayStart = new Date(now); dayStart.setHours(0, 0, 0, 0);
    let total = 0;
    for (const iv of intervals) {
      const s = twDate(iv.start).getTime(), e = iv.end ? twDate(iv.end).getTime() : now;
      total += todayOnly ? Math.max(0, e - Math.max(s, dayStart.getTime())) : e - s;
    }
    return total;
  }

  // ---------- HTTP ----------
  function request(method, path, body, cb) {
    const xhr = new XMLHttpRequest();
    xhr.onreadystatechange = () => {
      if (xhr.readyState !== XMLHttpRequest.DONE) return;
      let data = null;
      try { data = JSON.parse(xhr.responseText || "null"); } catch (e) {}
      if (xhr.status >= 200 && xhr.status < 300 && !(data && data.ok === false)) cb(null, data);
      else cb(xhr.status === 0 ? "Dashboard not reachable" : ((data && data.error) || ("HTTP " + xhr.status)), data);
    };
    xhr.open(method, baseUrl + path);
    if (method === "POST") {
      xhr.setRequestHeader("Content-Type", "application/json");
      xhr.setRequestHeader("X-Dashboard", "1");
    }
    xhr.send(body ? JSON.stringify(body) : null);
  }
  function post(path, body, okMsg) {
    request("POST", path, body, err => {
      if (err) ToastService.showError("Health Dashboard", err);
      else if (okMsg) ToastService.showNotice("Health Dashboard", okMsg, "heartbeat");
      refresh();
    });
  }

  function refresh() {
    todayKey = dayKey(new Date());
    let plan = null, days = null, tasks = null, failed = false;
    const done = (err, which, data) => {
      if (failed) return;
      if (err) {
        failed = true; online = false; lastError = err;
        if (autoStart && !autoStartFailed && !serverProc.running && err === "Dashboard not reachable") startServer();
        return;
      }
      if (which === "plan") plan = data; else if (which === "checklist") days = data; else tasks = data;
      if (plan && days && tasks) apply(plan, days, tasks);
    };
    request("GET", "/api/plan", null, (e, d) => done(e, "plan", d));
    request("GET", "/api/checklist", null, (e, d) => done(e, "checklist", d || {}));
    request("GET", "/api/tasks", null, (e, d) => done(e, "tasks", d || []));
    // Time is optional: if timew is missing or fails, the rest still works.
    request("GET", "/api/time", null, (e, d) => {
      timeAvailable = !e && !!(d && d.available);
      intervals = timeAvailable ? (d.intervals || []) : [];
      updateTracking();
    });
  }

  // What's running: the most recently started active task, or else an open timew interval.
  property var activeTasks: []
  function updateTracking() {
    now = Date.now();
    const t = activeTasks[0];
    const open = intervals.find(iv => !iv.end);
    if (t) tracking = { uuid: t.uuid, label: t.description, start: twDate(t.start).getTime() };
    else if (open) tracking = { uuid: "", label: (open.tags || []).join(" · ") || "untagged", start: twDate(open.start).getTime() };
    else tracking = null;
  }

  function apply(plan, days, tasks) {
    online = true; loaded = true; lastError = ""; autoStartFailed = false;
    const today = days[todayKey] || {};
    checklistItems = (plan.checklist || []).map(name => ({ name, state: cellState(today[name]), note: cellNote(today[name]) }));

    // Same grouping as the dashboard: copies of a repeating task are one row, keyed by parent.
    const start = new Date(); start.setHours(0, 0, 0, 0);
    const end = new Date(start); end.setDate(start.getDate() + 1);
    const groups = {};
    for (const t of tasks) {
      const due = twDate(t.due);
      if (!due || due >= end) continue;
      const key = t.parent || t.uuid;
      const g = groups[key] || (groups[key] = { uuid: t.uuid, description: t.description, project: t.project || "", due, overdueUuids: [], copies: 0, active: "" });
      g.copies++;
      if (due < g.due) Object.assign(g, { uuid: t.uuid, description: t.description, due });
      if (due < start) g.overdueUuids.push(t.uuid);
      if (t.start) g.active = t.uuid;   // the copy that was started, so Stop hits the right one
    }
    dueTasks = Object.values(groups).map(g => Object.assign(g, { overdue: g.due < start })).sort((a, b) => a.due - b.due);

    // Every pending task can be timed, like the Start button on the dashboard's task list.
    pendingTasks = tasks.filter(t => t.status === "pending" || !t.status)
                        .map(t => ({ uuid: t.uuid, description: t.description, project: t.project || "", active: !!t.start }))
                        .sort((a, b) => (b.active - a.active) || a.description.localeCompare(b.description));
    activeTasks = tasks.filter(t => t.start).sort((a, b) => b.start.localeCompare(a.start));
    updateTracking();
  }

  // ---------- actions (used by the panel) ----------
  // Re-reads the day before writing so a change made in the browser isn't overwritten.
  function setCheck(name, state, note) {
    request("GET", "/api/checklist", null, (err, days) => {
      if (err) return ToastService.showError("Health Dashboard", err);
      const st = Object.assign({}, (days || {})[todayKey] || {});
      const n = String(note || "").trim();
      if (!state) delete st[name];
      else st[name] = state === "skip" && n ? { state: "skip", note: n } : state;
      post("/api/checklist", { date: todayKey, items: st });
    });
  }
  // With minutes, also logs that much time in Timewarrior, ending now.
  function completeTask(uuid, minutes) {
    if (!minutes) return post("/api/tasks/done", { uuid }, "Done ✓");
    const d = new Date();
    const end = `${dayKey(d)}T${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
    request("POST", "/api/tasks/done_at", { uuid, end, minutes }, (err, data) => {
      if (err) ToastService.showError("Health Dashboard", err);
      else if (data && data.warning) ToastService.showWarning("Health Dashboard", data.warning);
      else ToastService.showNotice("Health Dashboard", "Done ✓ · " + fmtDur(minutes * 60000) + " logged", "heartbeat");
      refresh();
    });
  }
  // `task start/stop`; the dashboard's on-modify hook starts and stops Timewarrior to match.
  function startTask(uuid) { post("/api/tasks/start", { uuid }, "Timer started"); }
  function stopTask(uuid) {
    const ms = tracking && tracking.uuid === uuid ? Date.now() - tracking.start : 0;
    post("/api/tasks/stop", { uuid }, ms ? "Stopped · " + fmtDur(ms) : "Stopped");
  }
  function skipTask(uuids, note) { post("/api/tasks/skip", { uuids, note: note || "", kind: "skip" }, note ? "Skipped · reason saved" : "Skipped"); }

  function openDashboard() {
    const cmd = openCommand.indexOf("{url}") >= 0 ? openCommand.split("{url}").join(baseUrl) : openCommand + " " + baseUrl;
    Quickshell.execDetached(["sh", "-c", cmd]);
  }

  // ---------- server ----------
  // Runs `nix run` from the site folder, like ./run.sh. If the dashboard is already running
  // (e.g. started from a terminal) this is never started.
  property bool autoStartFailed: false   // a failed auto-start isn't retried until you start it by hand
  function startServer() {
    if (serverProc.running) return;
    if (!siteDir) { lastError = "Couldn't find the dashboard's site folder. Set it in the plugin settings."; return; }
    lastError = ""; autoStartFailed = false;
    serverProc.running = true;
  }
  function stopServer() { serverProc.running = false; }
  function restartServer() {
    if (serverProc.running) { restartAfterStop = true; serverProc.running = false; }
    else startServer();
  }
  property bool restartAfterStop: false

  Process {
    id: serverProc
    workingDirectory: root.siteDir
    command: ["nix", "run", ".", "--", root.port]
    stderr: StdioCollector { id: serverErr }
    onExited: (code, status) => {
      if (root.restartAfterStop) { root.restartAfterStop = false; root.startServer(); return; }
      if (code !== 0 && !root.online) {
        // Keep the last useful line; "address in use" means it's already running elsewhere.
        const lines = String(serverErr.text || "").trim().split("\n");
        root.lastError = "Server exited (" + code + "): " + (lines[lines.length - 1] || "no output");
        root.autoStartFailed = true;
        Logger.w("HealthDashboard", "server exited", code, serverErr.text);
      }
      root.online = false;
      pollTimer.restart();
    }
  }

  // Noctalia loads plugins from ~/.config/noctalia/plugins/<id>, a symlink to noctalia-plugin/ in
  // the repo, so the site folder is that symlink's target's sibling: <repo>/site.
  Process {
    id: findSite
    running: true
    command: ["sh", "-c", 'd=$(readlink -f "$1") && [ -f "${d%/*}/site/flake.nix" ] && echo "${d%/*}/site"', "sh",
              decodeURIComponent(String(Qt.resolvedUrl(".")).replace(/^file:\/\//, "").replace(/[?#].*$/, "").replace(/\/$/, ""))]
    stdout: StdioCollector { onStreamFinished: root.foundSiteDir = text.trim() }
  }

  // Poll quickly while the server is starting, then every 30 s.
  Timer {
    id: pollTimer
    interval: root.online ? 30000 : (root.serverStarting ? 2000 : 15000)
    running: true
    repeat: true
    triggeredOnStart: true
    onTriggered: root.refresh()
  }

  Timer {
    interval: 1000
    running: root.tracking !== null
    repeat: true
    onTriggered: root.now = Date.now()
  }

  IpcHandler {
    target: "plugin:health-dashboard"
    function toggle() { if (pluginApi) pluginApi.withCurrentScreen(s => pluginApi.togglePanel(s)); }
    function stop() { if (root.tracking && root.tracking.uuid) root.stopTask(root.tracking.uuid); }
    function open() { root.openDashboard(); }
    function refresh() { root.refresh(); }
    function restart() { root.restartServer(); }
  }
}
