// Boot

// ---------- boot ----------
(async () => {
  [plan, checklist, weights] = await Promise.all(["/api/plan", "/api/checklist", "/api/weights"].map(u => fetch(u).then(r => r.ok ? r.json() : Promise.reject(new Error(u)))))
    .catch(async () => [await (await fetch("/plan.json")).json(), {}, []]);  // older server without these endpoints
  applyLayout(plan.layout); renderToday(); renderStreak();
  loadTasks(); loadAllTasks(); loadTime(); loadTimeLog(); loadDocs(); loadMfp(); loadChat(); loadAnkiToday(); loadAnkiHistory();
  let t = location.hash.slice(1); if (!TABS.includes(t)) { t = null; try { t = localStorage.getItem("tab"); } catch {} }
  if (t) showTab(t);
})();
