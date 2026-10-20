// Moving Today cards

// ---------- moving cards ----------
// Drag a Today card by its title to another spot or column. The order is saved in plan.json
// (plan.layout: one list of card ids per column). Narrow screens keep their fixed order.
const todayCols = () => [...document.querySelectorAll("#tab-today .col")];
const readLayout = () => todayCols().map(c => [...c.querySelectorAll(":scope > .card")].map(x => x.id));
const defaultLayout = readLayout();
function applyLayout(layout) {
  const cols = todayCols();
  if (!Array.isArray(layout) || layout.length !== cols.length) layout = defaultLayout;
  layout.forEach((ids, i) => ids.forEach(id => { const card = document.getElementById(id); if (card) cols[i].append(card); }));
  $("#layout-reset").hidden = !plan.layout;
}
(() => {
  const tab = $("#tab-today");
  let dragging = null;
  // Only the title starts a drag, so inputs and text inside cards work as usual.
  tab.addEventListener("pointerdown", e => {
    const h = e.target.closest(".card-head h2");
    if (h && matchMedia("(min-width: 1001px)").matches) h.closest(".card").draggable = true;
  });
  tab.addEventListener("dragstart", e => {
    dragging = e.target.closest?.(".card");
    if (!dragging?.draggable) return;
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", dragging.id);
    requestAnimationFrame(() => { dragging?.classList.add("dragging"); tab.classList.add("moving"); });
  });
  tab.addEventListener("dragover", e => {
    const col = e.target.closest(".col");
    if (!dragging || !col) return;
    e.preventDefault();
    // Insert before the first card whose middle is below the pointer.
    const next = [...col.querySelectorAll(":scope > .card:not(.dragging)")].find(c => { const r = c.getBoundingClientRect(); return e.clientY < r.top + r.height / 2; });
    if (next) { if (next.previousElementSibling !== dragging) col.insertBefore(dragging, next); }
    else if (col.lastElementChild !== dragging) col.append(dragging);
  });
  tab.addEventListener("drop", e => { if (dragging) e.preventDefault(); });
  tab.addEventListener("dragend", () => {
    if (!dragging) return;
    dragging.classList.remove("dragging"); dragging.draggable = false; dragging = null;
    tab.classList.remove("moving");
    const layout = readLayout();
    if (JSON.stringify(layout) === JSON.stringify(plan.layout || defaultLayout)) return;
    plan.layout = layout; $("#layout-reset").hidden = false;
    savePlan("Layout saved");
  });
  tab.addEventListener("pointerup", () => { if (!dragging) tab.querySelectorAll(".card[draggable=true]").forEach(c => c.draggable = false); });
  $("#layout-reset").onclick = () => { delete plan.layout; applyLayout(defaultLayout); savePlan("Layout reset"); };
})();
