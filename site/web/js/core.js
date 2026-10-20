// Shared state and helpers: $, esc, dates, toast, post, card editors

const $ = s => document.querySelector(s);
const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const WEEK_ORDER = [1, 2, 3, 4, 5, 6, 0];
const dayKey = d => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
let now = new Date(), todayKey = dayKey(now);
let plan, tasks = [], checklist = {}, weights = [];
const editing = new Set();   // which cards are in edit mode

// ---------- helpers ----------
let toastTimer;
// `undo`: show an Undo button that runs `task undo` (for Taskwarrior changes).
function toast(msg, bad = false, undo = false) {
  const t = $("#toast");
  t.textContent = msg; t.className = "show" + (bad ? " bad" : "") + (undo ? " act" : "");
  if (undo) {
    const b = document.createElement("button");
    b.textContent = "Undo"; b.onclick = () => { t.className = ""; undoLast(); };
    t.append(b);
  }
  clearTimeout(toastTimer); toastTimer = setTimeout(() => t.className = "", bad ? 4000 : undo ? 5000 : 1600);
}
async function post(url, body) {
  const r = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json", "X-Dashboard": "1" }, body: JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (r.status === 404) throw new Error(`${j.error || "endpoint missing"}. Restart ./run.sh once to pick up the new server.`);
  if (!r.ok || j.ok === false) throw new Error(j.error || r.statusText);
  return j;
}
async function savePlan(msg = "Saved") {
  try { await post("/api/plan", { plan }); toast(msg); }
  catch (e) { toast("Couldn't save: " + e.message, true); }
}
function cardHead(title, key) {
  return `<div class="card-head"><h2>${esc(title)}</h2>${key ? `<button class="edit-btn" data-edit="${key}">${editing.has(key) ? "Close" : "Edit"}</button>` : ""}</div>`;
}
function wireEditBtn(el) {
  el.querySelectorAll("[data-edit]").forEach(b => b.onclick = () => {
    const k = b.dataset.edit;
    editing.has(k) ? editing.delete(k) : editing.add(k);
    renderToday();
  });
}

// A reusable editor for a list of rows. `fields` describes the inputs in each row.
function rowEditor(el, rows, fields, onSave, addLabel = "Add") {
  let data = rows.map(r => (Array.isArray(r) ? r : typeof r === "object" ? fields.map(f => r[f.key]) : [r]).map(v => String(v ?? "")));
  const draw = () => {
    el.innerHTML = `<div class="editor">${data.map((r, i) => `<div class="row">
        ${fields.map((f, j) => `<input class="txt${f.narrow ? " narrow" : ""}" data-i="${i}" data-j="${j}" value="${esc(r[j])}" placeholder="${esc(f.placeholder || "")}">`).join("")}
        <button class="icon" data-up="${i}" title="Move up"${i === 0 ? " disabled" : ""}>↑</button>
        <button class="icon" data-down="${i}" title="Move down"${i === data.length - 1 ? " disabled" : ""}>↓</button>
        <button class="icon del" data-del="${i}" title="Remove">✕</button>
      </div>`).join("")}
      <div class="foot"><button class="sm" data-add>+ ${esc(addLabel)}</button>
        <span><button class="sm" data-cancel>Cancel</button> <button class="sm primary" data-save>Save</button></span></div></div>`;
    el.querySelectorAll("input[data-i]").forEach(inp => inp.oninput = () => data[inp.dataset.i][inp.dataset.j] = inp.value);
    el.querySelectorAll("[data-up]").forEach(b => b.onclick = () => { const i = +b.dataset.up; [data[i - 1], data[i]] = [data[i], data[i - 1]]; draw(); });
    el.querySelectorAll("[data-down]").forEach(b => b.onclick = () => { const i = +b.dataset.down; [data[i + 1], data[i]] = [data[i], data[i + 1]]; draw(); });
    el.querySelectorAll("[data-del]").forEach(b => b.onclick = () => { data.splice(+b.dataset.del, 1); draw(); });
    el.querySelector("[data-add]").onclick = () => { data.push(fields.map(() => "")); draw(); el.querySelector(`input[data-i="${data.length - 1}"]`)?.focus(); };
    el.querySelector("[data-cancel]").onclick = () => onSave(null);
    el.querySelector("[data-save]").onclick = () => onSave(data.map(r => r.map(v => v.trim())).filter(r => r[0]));
    el.querySelectorAll("input").forEach(inp => inp.onkeydown = e => { if (e.key === "Enter") el.querySelector("[data-save]").click(); });
  };
  draw();
}
