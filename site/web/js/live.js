// Live updates: refresh tasks, roll over at midnight

// ---------- live updates ----------
// Refresh tasks when you come back to the tab and every 2 minutes; roll over at midnight.
function tick() {
  const n = new Date();
  if (dayKey(n) !== todayKey) { now = n; todayKey = dayKey(n); renderToday(); renderStreak(); loadTasks(); }
  else now = n;
}
document.addEventListener("visibilitychange", () => { if (!document.hidden) { tick(); loadTasks(); loadTime(); loadMfp(); loadAnkiToday(); } });
setInterval(() => { if (!document.hidden) { loadMfp(); loadAnkiToday(); } }, 300_000);
setInterval(tick, 60_000);
setInterval(() => { if (!document.hidden) loadTasks(); }, 120_000);
