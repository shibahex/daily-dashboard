// Tab switching

// ---------- tabs ----------
const TABS = ["today", "tasks", "history", "docs", "anki", "mcp"];
document.querySelectorAll("nav button").forEach(b => b.onclick = () => showTab(b.dataset.tab));
// The chat fills the window below the tabs; measure where it starts.
function sizeChat() {
  const card = document.querySelector("#tab-mcp .chat-card");
  if (card && !$("#tab-mcp").hidden) document.documentElement.style.setProperty("--chat-top", card.getBoundingClientRect().top + scrollY + "px");
}
addEventListener("resize", sizeChat);
function showTab(t) {
  document.querySelectorAll("nav button").forEach(b => b.classList.toggle("on", b.dataset.tab === t));
  TABS.forEach(x => $("#tab-" + x).hidden = x !== t);
  sizeChat();
  if (t === "anki") loadAnki();
  try { localStorage.setItem("tab", t); } catch {}
  history.replaceState(null, "", "#" + t);
}

addEventListener("hashchange", () => { const t = location.hash.slice(1); if (TABS.includes(t)) showTab(t); });
