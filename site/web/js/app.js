// The little the server can't do: toasts, keyboard shortcuts, dragging Today's cards, and
// remembering which meals are open. Everything else is HTMX (see web/templates).

// ---------- toasts ----------
// The server sends {"toast": {message, bad, undo}} in HX-Trigger; errors come back as plain text.

let toastTimer;
function toast(message, { bad = false, undo = false } = {}) {
  const box = document.getElementById("toast");
  box.textContent = message;
  box.className = "show" + (bad ? " bad" : "") + (undo ? " act" : "");
  if (undo) {
    const button = document.createElement("button");
    button.textContent = "Undo";
    button.onclick = () => { box.className = ""; htmx.ajax("POST", "/tasks/undo", { swap: "none" }); };
    box.append(button);
  }
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { box.className = ""; }, bad ? 4000 : undo ? 5000 : 1600);
}

document.body.addEventListener("toast", event => toast(event.detail.message, event.detail));
document.body.addEventListener("htmx:responseError", event => toast(event.detail.xhr.responseText || event.detail.xhr.statusText, { bad: true }));
document.body.addEventListener("htmx:sendError", () => toast("Couldn't reach the dashboard. Is it still running?", { bad: true }));

// ---------- keyboard ----------

document.addEventListener("keydown", event => {
  const field = event.target;
  if (event.key !== "Enter") return;
  // The chat box sends on Enter; Shift+Enter is a new line.
  if (field.matches("textarea[data-enter-sends]") && !event.shiftKey) {
    event.preventDefault();
    field.form.requestSubmit();
  }
  // Ctrl+Enter adds the Anki card.
  if (field.closest("form[data-ctrl-enter]") && (event.ctrlKey || event.metaKey)) {
    event.preventDefault();
    field.closest("form").requestSubmit();
  }
  // A box whose button sits next to it (Add note) presses that button instead of saving the form.
  if (field.matches("input[data-enter-next]")) {
    event.preventDefault();
    field.nextElementSibling.click();
  }
});

// Picking a task in the time log fills in the tags its timer uses.
document.addEventListener("change", event => {
  const picker = event.target.closest("select[data-fills]");
  if (!picker || !picker.value) return;
  const field = picker.form.elements[picker.dataset.fills];
  field.value = picker.value;
  field.focus();
});

// A new plugin token is selected when clicked, ready to copy.
document.addEventListener("focusin", event => {
  if (event.target.matches("input[data-select-all]")) event.target.select();
});

// ---------- panels and scrolling ----------

// Cards don't refresh themselves on a timer while a form or panel is open in them.
function hasOpenPanel(card) {
  return !!card.querySelector(".panel, .reason, .task-editor, .why-box, .task-slot:not(:empty)");
}

// ---------- refreshing cards ----------
// The pages may not evaluate code from attributes (the content policy has no 'unsafe-eval'), so
// the conditions on cards' hx-triggers live here instead:
// - "tick-N from:body" fires every N seconds (1, 30, 120 or 300), only while the page is visible;
// - a card reloading on a "…-changed" event skips the one it came from when that request already
//   redrew it (not one with hx-swap="none", like a task's Done, Start or Skip);
// - a timer never reloads a card with a form or panel open.

for (const seconds of [1, 30, 120, 300]) {
  setInterval(() => { if (!document.hidden) htmx.trigger(document.body, `tick-${seconds}`); }, seconds * 1000);
}
function swappedNothing(source, card) {
  const swap = source.closest("[hx-swap]");
  return !!swap && card.contains(swap) && swap.getAttribute("hx-swap").trim().startsWith("none");
}
document.body.addEventListener("htmx:confirm", event => {
  const trigger = event.detail.triggeringEvent;
  const card = event.detail.elt;
  if (!trigger) return;
  const source = trigger.target instanceof Element ? trigger.target : null;
  if (trigger.type.endsWith("-changed") && source && card.contains(source) && !swappedNothing(source, card)) {
    event.preventDefault();
  } else if (trigger.type.startsWith("tick-") && hasOpenPanel(card)) {
    event.preventDefault();
  }
});

// The heatmap starts at this week and the chat at the newest message.
// A freshly swapped log jumps to the end at once (the CSS smooth scroll would visibly run up from
// the top); new text in a log that is already there glides.
function scrollToEnd(root, behavior = "instant") {
  root.querySelectorAll("[data-scroll-end]").forEach(box => {
    box.scrollTo({ left: box.scrollWidth, top: box.scrollHeight, behavior });
  });
}
htmx.onLoad(root => scrollToEnd(root));
document.body.addEventListener("htmx:sseMessage", () => scrollToEnd(document, "smooth"));

// ---------- meals stay open ----------

const openMeals = new Set(JSON.parse(sessionStorage.getItem("openMeals") || "[]"));
document.addEventListener("toggle", event => {
  const meal = event.target.dataset?.meal;
  if (!meal) return;
  event.target.open ? openMeals.add(meal) : openMeals.delete(meal);
  sessionStorage.setItem("openMeals", JSON.stringify([...openMeals]));
}, true);
htmx.onLoad(root => root.querySelectorAll("details[data-meal]").forEach(details => {
  if (openMeals.has(details.dataset.meal)) details.open = true;
}));

// ---------- moving Today's cards ----------
// Drag a card by its title to another spot or column; the order is saved in the plan. Narrow
// screens keep their fixed one-column order.

let draggedSlot = null;
const layoutOf = grid => [...grid.querySelectorAll(".col")].map(column => [...column.querySelectorAll(":scope > .slot")].map(slot => slot.dataset.card));

document.addEventListener("pointerdown", event => {
  const title = event.target.closest("#tab-today .card-head h2");
  if (title && matchMedia("(min-width: 1001px)").matches) title.closest(".slot").draggable = true;
});
document.addEventListener("dragstart", event => {
  draggedSlot = event.target.closest?.("#tab-today .slot[draggable=true]");
  if (!draggedSlot) return;
  event.dataTransfer.effectAllowed = "move";
  event.dataTransfer.setData("text/plain", draggedSlot.dataset.card);
  draggedSlot.dataset.layoutBefore = JSON.stringify(layoutOf(draggedSlot.closest(".grid")));
  requestAnimationFrame(() => { draggedSlot?.classList.add("dragging"); document.getElementById("tab-today").classList.add("moving"); });
});
document.addEventListener("dragover", event => {
  const column = event.target.closest("#tab-today .col");
  if (!draggedSlot || !column) return;
  event.preventDefault();
  // Insert before the first card whose middle is below the pointer.
  const next = [...column.querySelectorAll(":scope > .slot:not(.dragging)")]
    .find(slot => { const box = slot.getBoundingClientRect(); return event.clientY < box.top + box.height / 2; });
  if (next) { if (next.previousElementSibling !== draggedSlot) column.insertBefore(draggedSlot, next); }
  else if (column.lastElementChild !== draggedSlot) column.append(draggedSlot);
});
document.addEventListener("drop", event => { if (draggedSlot) event.preventDefault(); });
document.addEventListener("dragend", () => {
  if (!draggedSlot) return;
  const slot = draggedSlot;
  draggedSlot = null;
  slot.classList.remove("dragging");
  slot.draggable = false;
  document.getElementById("tab-today").classList.remove("moving");
  const layout = JSON.stringify(layoutOf(slot.closest(".grid")));
  if (layout !== slot.dataset.layoutBefore) htmx.ajax("POST", "/layout", { values: { layout }, swap: "none" });
});
document.addEventListener("pointerup", () => {
  if (!draggedSlot) document.querySelectorAll("#tab-today .slot[draggable=true]").forEach(slot => { slot.draggable = false; });
});

// ---------- staying current ----------
// Coming back to the page refreshes what may have changed elsewhere, and a new day reloads it.

const loadedOn = new Date().toDateString();
let hiddenSince = 0;
document.addEventListener("visibilitychange", () => {
  if (document.hidden) { hiddenSince = Date.now(); return; }
  if (new Date().toDateString() !== loadedOn) return location.reload();
  if (Date.now() - hiddenSince > 60_000) {
    for (const event of ["tasks-changed", "time-changed", "nutrition-changed", "anki-changed"]) htmx.trigger(document.body, event);
  }
});
setInterval(() => {
  if (new Date().toDateString() !== loadedOn && !document.hidden && !hasOpenPanel(document.body)) location.reload();
}, 60_000);
