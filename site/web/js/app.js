// The little the server can't do: toasts, keyboard shortcuts, dragging Today's cards, the
// workout clocks, and remembering which meals are open. Everything else is HTMX (see web/templates).

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

// A card redrawn in place keeps the page where it was. Phones otherwise jump: htmx puts the new
// card in before taking the old one out, and the browser's scroll anchoring follows the old one
// down the page. Following a link (boosted) still goes to the new page's top.
let scrollBefore = null;
document.body.addEventListener("htmx:beforeSwap", event => {
  scrollBefore = event.detail.boosted ? null : window.scrollY;
});
document.body.addEventListener("htmx:afterSwap", () => {
  if (scrollBefore !== null && Math.abs(window.scrollY - scrollBefore) > 1) window.scrollTo({ top: scrollBefore, behavior: "instant" });
  scrollBefore = null;
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

// ---------- workout clocks ----------
// The Lifting tab's clocks: how long the workout has gone (data-since) and the rest or hold left
// (data-rest-until, out of data-rest-for seconds). The server keeps the times; this only counts,
// chimes (and buzzes, on a phone) when one is over, and for a hold (data-auto-done) presses its
// Done button, which logs the time held.

function clock(seconds) {
  const s = Math.max(0, Math.round(seconds));
  const h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60), rest = String(s % 60).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${rest}` : `${m}:${rest}`;
}

// Browsers only let a page play sound once it's been tapped: the sound is readied on the end of
// a tap or click or on a key (a touch's pointerdown doesn't count), with a silent sound played
// then, as iPhones need, and woken again on each (a phone suspends it in the background).
let sound;
function readySound() {
  try {
    if (!sound) {
      sound = new AudioContext();
      const silence = sound.createBufferSource();
      silence.buffer = sound.createBuffer(1, 1, 22050);
      silence.connect(sound.destination);
      silence.start();
    }
    if (sound.state !== "running") sound.resume();
  } catch {}
}
for (const type of ["click", "touchend", "keydown"]) document.addEventListener(type, readySound, { capture: true, passive: true });

// chime plays a soft two-note bell at an audio-clock time (now by default) and answers the node
// it plays through, so a chime planned ahead can be called off by disconnecting it (stopping a
// tone twice throws in some browsers, which left the old chime ringing). Planned on the audio
// clock, it rings on time even while the page's timers are slowed (another tab, a dimmed screen).
function chime(at) {
  if (!sound || sound.state !== "running") return null;
  const start = Math.max(at ?? 0, sound.currentTime), out = sound.createGain();
  out.connect(sound.destination);
  for (const [pitch, delay] of [[659.25, 0], [987.77, 0.2]]) {
    const tone = sound.createOscillator(), volume = sound.createGain(), when = start + delay;
    tone.type = "sine";
    tone.frequency.value = pitch;
    volume.gain.setValueAtTime(0.0001, when);
    volume.gain.exponentialRampToValueAtTime(0.3, when + 0.02);
    volume.gain.exponentialRampToValueAtTime(0.0001, when + 1.4);
    tone.connect(volume).connect(out);
    tone.start(when);
    tone.stop(when + 1.45);
  }
  return out;
}

// The chime planned for the timer showing: which end it's for and what it plays through.
let planned = { until: null, out: null };
function planChime(until, left) {
  if (planned.until === until) return;
  try { planned.out?.disconnect(); } catch {}
  planned = { until, out: left > 0 ? chime(sound ? sound.currentTime + left : 0) : null };
  if (!planned.out) planned.until = null; // no sound yet: try again on the next tick
}

function tickClocks() {
  const now = Date.now();
  document.querySelectorAll("[data-since]").forEach(el => { el.textContent = clock((now - Date.parse(el.dataset.since)) / 1000); });
  const boxes = document.querySelectorAll("[data-rest-until]");
  if (!boxes.length) planChime(null, 0);
  boxes.forEach(box => {
    const left = (Date.parse(box.dataset.restUntil) - now) / 1000, total = Number(box.dataset.restFor) || 1;
    box.querySelector(".rest-left").textContent = left > 0 ? clock(left) : "over";
    box.style.setProperty("--left", Math.max(0, Math.min(1, left / total)));
    if (left > 0) planChime(box.dataset.restUntil, left);
    if (left <= 0 && !box.classList.contains("over")) {
      box.classList.add("over");
      try { navigator.vibrate?.([120, 80, 120]); } catch {}
      // Rung now only if it wasn't planned (the sound wasn't ready) and it just ended.
      if (planned.until !== box.dataset.restUntil && left > -10) chime();
      planned = { until: box.dataset.restUntil, out: null };
      if (box.hasAttribute("data-auto-done")) box.querySelector("[data-timer-done]")?.click();
    }
  });
}
setInterval(tickClocks, 1000);
htmx.onLoad(tickClocks);

// Adding or taking off time (data-bump, in seconds) shows on the timer at once; the server's
// answer then redraws it.
document.addEventListener("click", event => {
  const button = event.target.closest("[data-bump]");
  const box = button?.closest("[data-rest-until]");
  if (!box) return;
  const seconds = Number(button.dataset.bump), now = Date.now();
  const until = Math.max(Date.parse(box.dataset.restUntil), now) + seconds * 1000;
  const left = Math.max(0, (until - now) / 1000);
  box.dataset.restUntil = new Date(until).toISOString();
  box.dataset.restFor = String(Math.max(left, (Number(box.dataset.restFor) || 0) + seconds));
  box.classList.remove("over");
  box.classList.add("bumped");
  setTimeout(() => box.classList.remove("bumped"), 180);
  tickClocks();
}, true);

// An exercise's estimated one-rep max (in its heading), redrawn as its sets' weights and reps are
// typed: the best set's, by Epley as lifting.go works it out (a single is itself), to the nearest
// plate (the form's data-plate: 2.5 lb, 1.25 kg). Skipped sets have no inputs, so they don't count.
document.addEventListener("input", event => {
  const exercise = event.target.closest(".lift-ex");
  const shown = exercise?.querySelector(".ex-1rm-now");
  if (!shown) return;
  let best = 0;
  exercise.querySelectorAll(".lift-set").forEach(row => {
    const [weightBox, repsBox] = row.querySelectorAll("input.num");
    if (!weightBox || !repsBox) return;
    const weight = parseFloat(weightBox.value.replace(",", ".")), reps = parseInt(repsBox.value, 10);
    if (weight > 0 && reps > 0) best = Math.max(best, reps === 1 ? weight : weight * (1 + reps / 30));
  });
  const step = Number(exercise.closest("[data-plate]")?.dataset.plate) || 2.5;
  shown.textContent = best > 0 ? String(parseFloat((Math.round(best / step) * step).toFixed(2))) : "";
});

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
