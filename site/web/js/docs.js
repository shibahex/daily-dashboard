// Docs tab

// ---------- docs ----------
async function loadDocs() {
  const names = await (await fetch("/api/docs")).json();
  // Your docs first (overview, then the usual ones, then anything else A–Z), the app guides last.
  const order = ["overview.md", "background.md", "nutrition-plan.md", "workout-plan.md", "food-history.md"];
  const guides = ["README.md", "dashboard.md", "noctalia-plugin.md"];
  const rank = n => guides.includes(n) ? 100 + guides.indexOf(n) : (order.indexOf(n) + 1) || 50;
  names.sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
  const label = n => ({ "README.md": "Getting started", "dashboard.md": "Dashboard guide", "noctalia-plugin.md": "Noctalia plugin" })[n]
    || n.replace(/\.md$/, "").replace(/-/g, " ").replace(/^\w/, c => c.toUpperCase());
  $("#doclist").innerHTML = names.map(n => `<button data-doc="${esc(n)}">${esc(label(n))}</button>`).join("");
  $("#doclist").querySelectorAll("button").forEach(b => b.onclick = () => openDoc(b.dataset.doc));
  let last = null; try { last = localStorage.getItem("doc"); } catch {}
  openDoc(names.includes(last) ? last : names[0]);
}

async function openDoc(name) {
  document.querySelectorAll("#doclist button").forEach(b => b.classList.toggle("on", b.dataset.doc === name));
  const text = await (await fetch("/api/docs/" + encodeURIComponent(name))).text();
  $("#doc").innerHTML = window.marked ? marked.parse(text) : `<pre style="white-space:pre-wrap">${esc(text)}</pre>`;
  $("#doc").querySelectorAll("a[href$='.md']").forEach(a => a.onclick = ev => { ev.preventDefault(); openDoc(a.getAttribute("href")); });
  try { localStorage.setItem("doc", name); } catch {}
}
