// Nutrition card: MyFitnessPal sync, logging and editing foods

// ---------- MyFitnessPal ----------
// The server syncs every 10 minutes (and when the page asks for stale data). Goals come from
// MyFitnessPal, so the card shows them instead of the ones typed into the plan.
let mfp = null;   // { data: { totals, goals, meals, exercise_calories, weights }, fetched_at, error, syncing }
let mfpTimer = null;
async function loadMfp(force = false) {
  const before = mfp?.fetched_at;
  try {
    mfp = force ? await post("/api/mfp/refresh", {}) : await (await fetch("/api/mfp")).json();
  } catch (e) { mfp = { ...(mfp || {}), error: e.message, syncing: false }; }
  if (force && mfp.error) toast("MyFitnessPal: " + mfp.error, true);
  renderGoals();
  // A sync merges new weigh-ins (and may tick "Weigh in"), so pick those up.
  if (mfp.fetched_at && mfp.fetched_at !== before) {
    try {
      [weights, checklist] = await Promise.all(["/api/weights", "/api/checklist"].map(u => fetch(u).then(r => r.json())));
      renderWeight(); renderChecklist(); renderStreak();
    } catch {}
  }
  clearTimeout(mfpTimer);
  if (mfp.syncing) mfpTimer = setTimeout(loadMfp, 5000);
}

const n0 = v => Math.round(v || 0).toLocaleString();
const pct = (v, goal) => goal ? Math.min(100, (v / goal) * 100) : 0;
const ago = t => { const m = Math.round((Date.now() / 1000 - t) / 60); return m < 1 ? "just now" : m < 60 ? `${m} min ago` : `${Math.round(m / 60)} h ago`; };

function renderNutrition() {
  const el = $("#card-goals"), { totals: t, goals: g, meals, exercise_calories: ex = 0 } = mfp.data;
  const left = g.calories - t.calories + ex, over = left < 0;
  // Calories from each macro (4/4/9 kcal per gram), for the split bar.
  const kcal = { protein: t.protein * 4, carbs: t.carbohydrates * 4, fat: t.fat * 9 };
  const kTotal = kcal.protein + kcal.carbs + kcal.fat;
  const share = k => kTotal ? Math.round((kcal[k] / kTotal) * 100) : 0;
  const macro = (label, v, goal, unit, color) => `<div class="macro"><div class="top"><span>${label}</span><span>${n0(v)} / ${n0(goal)} ${unit}</span></div>
    <div class="progress"><div style="width:${pct(v, goal)}%;background:var(--${color})"></div></div></div>`;
  // Limits (stay under) and fiber (reach it), the ones that matter most for cholesterol.
  const limit = (label, v, goal, unit, atLeast = false) => {
    const cls = !goal ? "" : atLeast ? (v >= goal ? "good" : "") : v > goal ? "bad" : "";
    return `<div class="limit ${cls}"><b>${n0(v)} <small>${unit}</small></b><span>${label}</span>${goal ? `<span>goal ${atLeast ? "≥" : "≤"} ${n0(goal)}</span>` : ""}</div>`;
  };
  el.innerHTML = `<div class="card-head"><h2>Nutrition today</h2><span><button class="edit-btn" id="mfp-log">+ Log food</button>
      <button class="edit-btn" id="mfp-sync" title="Sync with MyFitnessPal now">${mfp.syncing ? "Syncing…" : "↻ Sync"}</button></span></div>
    <div class="nut-cal"><span class="big">${n0(t.calories)}</span><span class="of">/ ${n0(g.calories)} kcal eaten</span></div>
    <div class="progress"><div class="${t.calories > g.calories + ex ? "over" : "d"}" style="width:${pct(t.calories, g.calories + ex)}%"></div></div>
    <div class="nut-eq">${n0(g.calories)} goal − ${n0(t.calories)} food${ex ? ` + ${n0(ex)} exercise` : ""} = <b class="${over ? "over" : ""}">${over ? `${n0(-left)} over` : `${n0(left)} left`}</b></div>
    ${macro("Protein", t.protein, g.protein, "g", "protein")}
    ${macro("Carbs", t.carbohydrates, g.carbohydrates, "g", "carbs")}
    ${macro("Fat", t.fat, g.fat, "g", "fat")}
    ${kTotal ? `<div class="split" title="Where today's calories came from">${["protein", "carbs", "fat"].map(k => `<div style="width:${share(k)}%;background:var(--${k})"></div>`).join("")}</div>
    <div class="split-key">${[["protein", "Protein"], ["carbs", "Carbs"], ["fat", "Fat"]].map(([k, l]) => `<span><i style="background:var(--${k})"></i>${l} ${share(k)}%</span>`).join("")}</div>` : ""}
    <div class="limits">
      ${limit("Sat fat", t.saturated_fat, g.saturated_fat, "g")}
      ${limit("Fiber", t.fiber, g.fiber, "g", true)}
      ${limit("Sodium", t.sodium, g.sodium, "mg")}
      ${limit("Cholesterol", t.cholesterol, g.cholesterol, "mg")}
      ${limit("Sugar", t.sugar, g.sugar, "g")}
      ${limit("Potassium", t.potassium, g.potassium, "mg", true)}
    </div>
    <div class="meals">${meals.map(m => `<details data-meal="${esc(m.name)}"${mfpOpenMeals.has(m.name) ? " open" : ""}>
      <summary><span>${esc(m.name)}</span><span>${m.entries.length ? `${n0(m.totals.calories)} kcal · ${n0(m.totals.protein)}p ${n0(m.totals.carbohydrates)}c ${n0(m.totals.fat)}f` : "—"}</span></summary>
      ${m.entries.map(e => `<div class="food"><span class="n">${esc(e.name)}<small>${esc(e.amount)}</small></span>
        <span class="k">${n0(e.totals.calories)} kcal<br>${n0(e.totals.protein)}p ${n0(e.totals.carbohydrates)}c ${n0(e.totals.fat)}f</span>
        ${e.entry_id ? `<span class="food-btns"><button class="icon" data-food-edit="${e.entry_id}" title="Change amount, serving or meal">✎</button><button class="icon del" data-food-del="${e.entry_id}" data-name="${esc(e.name)}" title="Remove">✕</button></span>` : ""}</div>`).join("")}
      <div class="meal-add"><button data-food-add="${esc(m.name)}">+ Add to ${esc(m.name)}</button></div>
    </details>`).join("")}</div>
    <p class="small muted" style="margin:10px 0 0">From MyFitnessPal · updated ${ago(mfp.fetched_at)}. Goals are set in MyFitnessPal.${mfp.error ? ` <span class="err">Last sync failed: ${esc(mfp.error)}</span>` : ""}</p>`;
  $("#mfp-sync").onclick = async e => { e.target.disabled = true; e.target.textContent = "Syncing…"; await loadMfp(true); };
  $("#mfp-log").onclick = () => openFoodAdd($("#mfp-log").closest(".card-head"), mealForNow());
  el.querySelectorAll("details[data-meal]").forEach(d => d.ontoggle = () => d.open ? mfpOpenMeals.add(d.dataset.meal) : mfpOpenMeals.delete(d.dataset.meal));
  el.querySelectorAll("[data-food-add]").forEach(b => b.onclick = () => openFoodAdd(b.parentElement, b.dataset.foodAdd));
  el.querySelectorAll("[data-food-edit]").forEach(b => b.onclick = () => openFoodEdit(b.closest(".food"), b.dataset.foodEdit));
  el.querySelectorAll("[data-food-del]").forEach(b => b.onclick = async () => {
    if (!confirm(`Remove ${b.dataset.name} from today's MyFitnessPal diary?`)) return;
    b.disabled = true;
    try { await foodWrite("delete", { entry_id: b.dataset.foodDel }); toast("Removed from MyFitnessPal"); }
    catch (e) { toast("MyFitnessPal: " + e.message, true); b.disabled = false; }
  });
}

// ---------- logging and editing foods ----------
// One form open at a time. While it's open the 5-minute refresh doesn't redraw the card.
const mfpOpenMeals = new Set();
let foodForm = null;
const mealForNow = () => { const h = new Date().getHours(); return h < 11 ? "Breakfast" : h < 15 ? "Lunch" : h < 21 ? "Dinner" : "Snacks"; };
function closeFoodForm() { foodForm?.remove(); foodForm = null; }
function foodPanel(anchor, html) {
  closeFoodForm();
  foodForm = document.createElement("div");
  foodForm.className = "panel food-panel";
  foodForm.innerHTML = html;
  anchor.after(foodForm);
  return foodForm;
}
// Writes answer with the refreshed day, so the card redraws with the change.
async function foodWrite(op, body) {
  const r = await post("/api/mfp/" + op, body);
  mfp = r; closeFoodForm(); renderGoals();
  return r;
}
const macroLine = n => `${n0(n.calories)} kcal · ${n0(n.protein)}p ${n0(n.carbs)}c ${n0(n.fat)}f`;
const mealOptions = (selected) => mfp.data.meals.map(m => `<option${m.name === selected ? " selected" : ""}>${esc(m.name)}</option>`).join("");

function openFoodAdd(anchor, meal) {
  const box = foodPanel(anchor, `
    <div class="row"><input class="txt grow" data-q placeholder="Search MyFitnessPal foods, e.g. greek yogurt" maxlength="100">
      <button class="sm primary" data-go>Search</button><button class="sm" data-x>Cancel</button></div>
    <div class="fp-results"></div>`);
  const q = s => box.querySelector(s), input = q("[data-q]"), results = q(".fp-results");
  input.focus();
  q("[data-x]").onclick = closeFoodForm;
  let found = [];
  const go = async () => {
    const query = input.value.trim();
    if (!query) return input.focus();
    q("[data-go]").disabled = true;
    results.innerHTML = `<p class="small muted" style="margin:4px 0">Searching…</p>`;
    try {
      found = (await post("/api/mfp/search", { query })).results;
      results.innerHTML = found.length ? found.map((f, i) => {
        const s0 = f.servings[0];
        const n = s0 ? Object.fromEntries(Object.entries(f.nutrition).map(([k, v]) => [k, (v || 0) * s0.nutrition_multiplier])) : f.nutrition;
        return `<button data-i="${i}"><span>${esc(f.name)}${f.verified ? " ✓" : ""}<small>${esc(f.brand || "")}${f.brand && s0 ? " · " : ""}${s0 ? esc(s0.label) : ""}</small></span><span class="k">${macroLine(n)}</span></button>`;
      }).join("") : `<p class="small muted" style="margin:4px 0">Nothing found. Try other words.</p>`;
      results.querySelectorAll("[data-i]").forEach(b => b.onclick = () => pickFood(box, found[+b.dataset.i], meal, () => { box.querySelector(".fp-pick")?.remove(); results.hidden = false; box.querySelector(".row").hidden = false; }));
    } catch (e) { results.innerHTML = `<p class="err small" style="margin:4px 0">${esc(e.message)}</p>`; }
    q("[data-go]").disabled = false;
  };
  q("[data-go]").onclick = go;
  input.onkeydown = e => { if (e.key === "Enter") go(); if (e.key === "Escape") closeFoodForm(); };
}

// Second step of adding: how much, which serving, which meal, with a live calorie preview.
function pickFood(box, f, meal, back) {
  box.querySelector(".fp-results").hidden = true;
  box.querySelector(".row").hidden = true;
  const servings = f.servings.length ? f.servings : [{ weight_id: f.default_weight_id, label: "1 serving", nutrition_multiplier: 1 }];
  const pick = document.createElement("div");
  pick.className = "fp-pick";
  pick.innerHTML = `<div style="margin-bottom:6px"><b>${esc(f.name)}</b>${f.brand ? ` <span class="small muted">${esc(f.brand)}</span>` : ""}</div>
    <div class="row"><input class="txt num" data-qty type="number" min="0.01" step="any" value="1" title="Number of servings"> ×
      <select class="txt" data-serving>${servings.map((s, i) => `<option value="${i}">${esc(s.label)}</option>`).join("")}</select>
      to <select class="txt" data-meal>${mealOptions(meal)}</select></div>
    <div class="row" style="justify-content:space-between;margin-top:6px"><span class="fp-preview"></span>
      <span><button class="sm" data-back>Back</button> <button class="sm primary" data-add>Add</button></span></div>`;
  box.append(pick);
  const q = s => pick.querySelector(s);
  const preview = () => {
    const m = servings[+q("[data-serving]").value].nutrition_multiplier * (parseFloat(q("[data-qty]").value) || 0);
    q(".fp-preview").textContent = "≈ " + macroLine(Object.fromEntries(Object.entries(f.nutrition).map(([k, v]) => [k, (v || 0) * m])));
  };
  q("[data-qty]").oninput = q("[data-serving]").onchange = preview;
  preview(); q("[data-qty]").select();
  q("[data-back]").onclick = back;
  const add = async () => {
    const quantity = parseFloat(q("[data-qty]").value);
    if (!(quantity > 0)) return toast("Enter how many servings", true);
    q("[data-add]").disabled = true; q("[data-add]").textContent = "Adding…";
    const meal = q("[data-meal]").value;
    try {
      await foodWrite("add", { food_id: f.food_id, weight_id: servings[+q("[data-serving]").value].weight_id, quantity, meal });
      mfpOpenMeals.add(meal); renderGoals();
      toast(`Added to ${meal}`);
    } catch (e) { toast("MyFitnessPal: " + e.message, true); q("[data-add]").disabled = false; q("[data-add]").textContent = "Add"; }
  };
  q("[data-add]").onclick = add;
  q("[data-qty]").onkeydown = e => { if (e.key === "Enter") add(); };
}

// Edit uses MyFitnessPal's own edit dialog: amount, serving size and meal.
async function openFoodEdit(row, entryId) {
  const box = foodPanel(row, `<p class="small muted" style="margin:0">Loading…</p>`);
  let f;
  try { f = await post("/api/mfp/entry", { entry_id: entryId }); }
  catch (e) { box.innerHTML = `<p class="err small" style="margin:0">${esc(e.message)}</p>`; return; }
  if (foodForm !== box) return;  // closed or replaced while loading
  box.innerHTML = `<div class="row"><input class="txt num" data-qty type="number" min="0.01" step="any" value="${f.quantity}" title="Number of servings"> ×
      <select class="txt" data-serving>${f.servings.map(s => `<option value="${esc(s.id)}"${s.selected ? " selected" : ""}>${esc(s.label)}</option>`).join("")}</select>
      in <select class="txt" data-meal>${f.meals.map(m => `<option value="${esc(m.id)}"${m.selected ? " selected" : ""}>${esc(m.label)}</option>`).join("")}</select></div>
    <div class="row" style="justify-content:flex-end"><button class="sm" data-x>Cancel</button><button class="sm primary" data-save>Save</button></div>`;
  const q = s => box.querySelector(s);
  q("[data-qty]").select();
  q("[data-x]").onclick = closeFoodForm;
  const save = async () => {
    const quantity = parseFloat(q("[data-qty]").value);
    if (!(quantity > 0)) return toast("Enter how many servings", true);
    q("[data-save]").disabled = true; q("[data-save]").textContent = "Saving…";
    try {
      await foodWrite("edit", { entry_id: entryId, quantity, weight_id: q("[data-serving]").value, meal_id: q("[data-meal]").value });
      toast("Updated in MyFitnessPal");
    } catch (e) { toast("MyFitnessPal: " + e.message, true); q("[data-save]").disabled = false; q("[data-save]").textContent = "Save"; }
  };
  q("[data-save]").onclick = save;
  q("[data-qty]").onkeydown = e => { if (e.key === "Enter") save(); if (e.key === "Escape") closeFoodForm(); };
}

function renderWeek() {
  const el = $("#card-week");
  el.innerHTML = cardHead("This week", "week") + `<div id="wk-body"></div>`;
  wireEditBtn(el);
  const body = $("#wk-body");
  const keys = Object.keys(plan.sessions);
  if (editing.has("week")) {
    body.innerHTML = `<div class="editor">${WEEK_ORDER.map(i => `<div class="row"><span style="flex:0 0 96px">${DAYS[i]}</span>
        <select class="txt" data-day="${DAYS[i]}" style="flex:1">${keys.map(k => `<option value="${esc(k)}"${plan.schedule[DAYS[i]] === k ? " selected" : ""}>${esc(plan.sessions[k].title)}</option>`).join("")}</select></div>`).join("")}
      <div class="row" style="margin-top:12px"><input class="txt" id="new-session" placeholder="New session name (e.g. Mountain bike)"><button class="sm" id="add-session">Add</button></div>
      <div class="foot"><span class="small muted">Edit a session's exercises from “Today's training” on its day.</span>
        <span><button class="sm" id="wk-cancel">Cancel</button> <button class="sm primary" id="wk-save">Save</button></span></div></div>`;
    $("#add-session").onclick = () => {
      const title = $("#new-session").value.trim();
      if (!title) return;
      let k = title.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "session";
      while (plan.sessions[k]) k += "-2";
      plan.sessions[k] = { title };
      body.querySelectorAll("select").forEach(s => s.add(new Option(title, k)));
      $("#new-session").value = "";
      toast(`Added “${title}”. Pick it for a day, then Save.`);
    };
    $("#wk-cancel").onclick = () => { editing.delete("week"); renderToday(); };
    $("#wk-save").onclick = () => {
      body.querySelectorAll("select").forEach(s => plan.schedule[s.dataset.day] = s.value);
      savePlan("Schedule saved"); editing.delete("week"); renderToday();
    };
    return;
  }
  body.innerHTML = `<table class="ex week">${WEEK_ORDER.map(i => {
    const sess = plan.sessions[plan.schedule[DAYS[i]]];
    return `<tr${i === now.getDay() ? ' class="cur"' : ""}><td>${DAYS[i]}</td><td>${esc(sess ? sess.title : "—")}</td></tr>`;
  }).join("")}</table>`;
}

function renderNotes() {
  const el = $("#card-notes");
  el.innerHTML = cardHead("Reminders", "notes") + `<div id="n-body"></div>`;
  wireEditBtn(el);
  const body = $("#n-body");
  if (editing.has("notes")) {
    rowEditor(body, plan.notes, [{ placeholder: "Reminder" }], rows => {
      if (rows) { plan.notes = rows.map(r => r[0]); savePlan("Reminders saved"); }
      editing.delete("notes"); renderNotes();
    }, "Add reminder");
    return;
  }
  body.innerHTML = plan.notes.length ? `<ul class="notes">${plan.notes.map(n => `<li>${esc(n)}</li>`).join("")}</ul>` : `<p class="muted">No reminders yet.</p>`;
}
