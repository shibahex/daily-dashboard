// Sealed notes, for everyone but the host: the server keeps them as one blob it can't read, and
// this page does everything with them. It decrypts them with the key kept in this browser (see
// keys.js), shows and edits them on the Docs tab, and encrypts them again to save. Notes still
// in plain files from before are sealed the first time they're opened here, and then deleted
// from the server. The AI chat gets the notes decrypted here, with each message.

const SealedNotes = (() => {
  let state = null; // { key, version, notes: [{ id, title, text }] }
  let current = null; // the open note's id
  let editing = false;

  const newId = () => crypto.getRandomValues(new Uint32Array(2)).join("-");
  const titleOf = name => {
    const words = name.replace(/\.md$/, "").replace(/-/g, " ");
    return words.charAt(0).toUpperCase() + words.slice(1);
  };

  // ---------- loading and saving ----------

  async function load() {
    const key = await Keys.notesKey();
    if (!key) return null;
    const response = await fetch("/notes/data", { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error("Couldn't load your notes");
    const answer = await response.json();
    state = { key, version: answer.version, notes: [] };
    if (answer.blob) {
      state.notes = JSON.parse(await Keys.openText(key, answer.blob)).notes || [];
    } else if (answer.plain?.length) {
      // Notes from before: seal them, and the server deletes the plain copies.
      state.notes = answer.plain.map(p => ({ id: newId(), title: titleOf(p.name), text: p.text }));
      await save(answer.plain.map(p => p.name));
    }
    return state;
  }

  async function save(sealedNames) {
    const body = new FormData();
    body.set("version", state.version);
    body.set("blob", await Keys.sealText(state.key, JSON.stringify({ notes: state.notes })));
    if (sealedNames) body.set("sealed", sealedNames.join("\n"));
    const response = await fetch("/notes/data", { method: "POST", body, headers: { "HX-Request": "true" } });
    const answer = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(answer.error || "Couldn't save your notes");
    state.version = answer.version;
  }

  // change applies an edit and saves it; if saving fails (another tab saved first), it reloads.
  async function change(edit, message) {
    edit();
    try {
      await save();
      if (message) toast(message);
    } catch (e) {
      toast(e.message, { bad: true });
      await load();
    }
    render();
  }

  // ---------- the Docs tab ----------

  const el = (tag, props = {}, ...children) => {
    const node = Object.assign(document.createElement(tag), props);
    node.append(...children.filter(c => c != null));
    return node;
  };
  const button = (label, props) => el("button", { type: "button", ...props }, label);
  const markdown = source => DOMPurify.sanitize(marked.parse(source || ""));

  let list, main;

  function render() {
    if (!list) return;
    list.replaceChildren();
    main.replaceChildren();
    if (!state) return;
    const notes = state.notes;
    if (!notes.some(n => n.id === current)) current = notes[0]?.id ?? null;

    const guideOpen = !!document.querySelector(".docs-main[data-guide]");
    list.append(el("div", { className: "doclist-head" }, "Notes"));
    notes.forEach((note, i) => {
      const open = !guideOpen && note.id === current;
      const link = el("a", { href: "?note=" + note.id }, note.title || "Untitled");
      link.onclick = event => { event.preventDefault(); current = note.id; editing = false; show(); };
      const row = el("div", { className: "doclink" + (open ? " on" : "") }, link);
      if (open && !editing) {
        const up = button("↑", { className: "icon", title: "Move up", disabled: i === 0 });
        const down = button("↓", { className: "icon", title: "Move down", disabled: i === notes.length - 1 });
        up.onclick = () => change(() => notes.splice(i - 1, 0, ...notes.splice(i, 1)));
        down.onclick = () => change(() => notes.splice(i + 1, 0, ...notes.splice(i, 1)));
        row.append(el("span", { className: "move" }, up, down));
      }
      list.append(row);
    });
    const name = el("input", { className: "txt", name: "title", maxLength: 60, placeholder: "New note…", required: true });
    const add = el("form", { className: "new-note" }, name, el("button", { className: "sm" }, "Add"));
    add.onsubmit = event => {
      event.preventDefault();
      const note = { id: newId(), title: name.value.trim(), text: "# " + name.value.trim() + "\n\n" };
      current = note.id;
      editing = true;
      change(() => notes.push(note), "Note added");
    };
    list.append(add);

    if (document.querySelector(".docs-main[data-guide]")) return; // a guide is open there
    const note = notes.find(n => n.id === current);
    if (!note) {
      main.append(el("div", { className: "card md" }, el("p", { className: "muted" }, "No notes yet. Add one on the left.")));
    } else if (editing) {
      main.append(editor(note));
    } else {
      const edit = button("Edit", { className: "edit-btn" });
      edit.onclick = () => { editing = true; render(); };
      const card = el("div", { className: "card md" }, el("div", { className: "doc-tools" }, edit));
      card.insertAdjacentHTML("beforeend", markdown(note.text));
      main.append(card);
    }
  }

  function editor(note) {
    const title = el("input", { className: "txt", name: "title", value: note.title, maxLength: 60, required: true });
    const text = el("textarea", { className: "txt", name: "content", value: note.text, spellcheck: true, autofocus: true });
    const remove = button("Delete note", { className: "sm ghost" });
    const cancel = button("Cancel", { className: "sm" });
    const form = el("form", { className: "card doc-editor" },
      el("label", { className: "lbl" }, "Name ", title), text,
      el("div", { className: "foot" }, remove,
        el("span", {}, el("span", { className: "small muted" }, "Markdown · Ctrl+Enter saves · encrypted in your browser"),
          cancel, el("button", { className: "sm primary" }, "Save"))));
    form.dataset.ctrlEnter = "";
    form.onsubmit = event => {
      event.preventDefault();
      editing = false;
      change(() => { note.title = title.value.trim(); note.text = text.value; }, "Note saved");
    };
    cancel.onclick = () => { editing = false; render(); };
    remove.onclick = () => {
      if (!confirm(`Delete “${note.title}”? This can't be undone.`)) return;
      editing = false;
      change(() => state.notes.splice(state.notes.indexOf(note), 1), "Note deleted");
    };
    return form;
  }

  // show opens the current note in place of a guide, and puts it in the address.
  function show() {
    const area = document.querySelector(".docs-main");
    if (area?.dataset.guide !== undefined) {
      delete area.dataset.guide;
      area.querySelector(":scope > .card")?.remove(); // the guide that was open
      document.querySelectorAll(".doclist .doclink.on").forEach(l => l.classList.remove("on"));
    }
    if (current) history.replaceState(null, "", "/docs?note=" + current);
    render();
  }

  function locked(message) {
    const password = el("input", { className: "txt", type: "password", autocomplete: "current-password", required: true });
    const error = el("p", { className: "sign-in-error", hidden: !message }, message || "");
    const form = el("form", { className: "card unlock" },
      el("h2", {}, "Your notes are locked"),
      el("p", { className: "muted" }, "They're encrypted, and only your password opens them, here in your browser. The server never sees them."),
      error, el("label", {}, "Password ", password), el("button", { className: "sm primary" }, "Unlock"));
    form.onsubmit = async event => {
      event.preventDefault();
      try {
        await Keys.unlockWithPassword(password.value);
        await start();
      } catch (e) {
        error.textContent = e.message;
        error.hidden = false;
      }
    };
    main.replaceChildren(form);
  }

  async function start() {
    if (!Keys.supported) {
      main.replaceChildren(el("div", { className: "card" }, el("p", { className: "sign-in-error" },
        "Your notes are encrypted in your browser, which only works over HTTPS (or on localhost). Open the dashboard with https://.")));
      return;
    }
    try {
      if (!(await load())) return locked();
    } catch (e) {
      return locked(e.message);
    }
    current = new URLSearchParams(location.search).get("note") || current;
    render();
  }

  htmx.onLoad(root => {
    const box = root.querySelector?.("[data-sealed-notes]");
    if (!box) return;
    list = box;
    main = document.querySelector(".docs-main [data-sealed-view]") || document.querySelector("[data-sealed-view]");
    start();
  });

  // ---------- the AI chat ----------
  // Each message carries the notes decrypted here, for that one answer.

  let forChat = null;
  htmx.onLoad(async root => {
    if (!root.querySelector?.("#chat-card") || !document.querySelector("meta[name=dashboard-sealed]")) return;
    try {
      if (await load()) forChat = JSON.stringify(state.notes.map(n => ({ title: n.title, text: n.text })));
    } catch { forChat = null; }
  });
  document.body.addEventListener("htmx:configRequest", event => {
    if (forChat && event.detail.path === "/mcps/messages") event.detail.parameters.notes = forChat;
  });
})();
