// Sealed notes: the server keeps them as one blob it can't read, and this page does everything
// with them. It decrypts them with the key kept in this browser (see keys.js), shows and edits
// them on the Docs tab, and encrypts them again to save. Notes still in plain files from before
// are sealed the first time they're opened here, and then deleted from the server. The AI, which
// runs in this browser too (assistant.js), reads and saves them through all() and saveNote().
//
// Nothing is lost for good by one click: each note keeps its last versions (history), and
// deleted notes stay in "Recently deleted" (trash) until newer ones push them out or they're
// deleted from there for good.

const SealedNotes = (() => {
  const HISTORY = 10; // versions kept per note
  const TRASH = 3; // deleted notes kept

  // { key, version, seq, notes: [{ id, title, text, history: [{ title, text, at }] }], trash: [...] }
  let state = null;
  let current = null; // the open note's id
  let editing = false;

  const newId = () => crypto.getRandomValues(new Uint32Array(2)).join("-");
  const titleOf = name => {
    const words = name.replace(/\.md$/, "").replace(/-/g, " ");
    return words.charAt(0).toUpperCase() + words.slice(1);
  };
  const when = at => new Date(at).toLocaleString([], { dateStyle: "medium", timeStyle: "short" });

  // ---------- loading and saving ----------

  async function load() {
    const key = await Keys.notesKey();
    if (!key) return null;
    const response = await fetch("/notes/data", { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error("Couldn't load your notes");
    const answer = await response.json();
    state = { key, version: answer.version, seq: 0, notes: [], trash: [] };
    if (answer.blob) {
      const value = await Keys.openBlob(key, "notes", answer.blob);
      Object.assign(state, { seq: value.seq || 0, notes: value.notes || [], trash: (value.trash || []).slice(0, TRASH) });
    } else if (answer.plain?.length) {
      // Notes from before: seal them, and the server deletes the plain copies.
      state.notes = answer.plain.map(p => ({ id: newId(), title: titleOf(p.name), text: p.text }));
      await save(answer.plain.map(p => p.name));
    }
    return state;
  }

  async function save(sealedNames) {
    const value = { seq: state.seq, notes: state.notes, trash: state.trash };
    const body = new FormData();
    body.set("version", state.version);
    body.set("blob", await Keys.sealBlob(state.key, "notes", value));
    if (sealedNames) body.set("sealed", sealedNames.join("\n"));
    const response = await fetch("/notes/data", { method: "POST", body, headers: { "HX-Request": "true" } });
    const answer = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(answer.error || "Couldn't save your notes");
    state.version = answer.version;
    state.seq = value.seq;
    Keys.saved("notes", value);
  }

  // change applies an edit and saves it; if saving fails (another tab saved first), it reloads.
  async function change(edit, message) {
    edit();
    try {
      await save();
      if (message) toast(message);
    } catch (e) {
      toast(e.message, { bad: true });
      await load().catch(() => {});
    }
    render();
  }

  // rewrite changes a note, keeping what it was in its history.
  function rewrite(note, title, text) {
    if (note.title === title && note.text === text) return;
    note.history = [{ title: note.title, text: note.text, at: Date.now() }, ...(note.history || [])].slice(0, HISTORY);
    note.title = title;
    note.text = text;
  }

  // remove moves a note to Recently deleted.
  function remove(note) {
    state.notes.splice(state.notes.indexOf(note), 1);
    state.trash = [{ ...note, deletedAt: Date.now() }, ...state.trash].slice(0, TRASH);
  }

  // ---------- the Docs tab ----------

  const el = (tag, props = {}, ...children) => {
    const node = Object.assign(document.createElement(tag), props);
    node.append(...children.filter(c => c != null));
    return node;
  };
  const button = (label, props) => el("button", { type: "button", ...props }, label);
  const markdown = source => DOMPurify.sanitize(marked.parse(source || ""), {
    // Text to read, never a form or anything this page's scripts react to (data-*).
    FORBID_TAGS: ["form", "input", "button", "textarea", "select", "option", "style", "iframe", "object", "embed"],
    ALLOW_DATA_ATTR: false,
  });

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
    if (state.trash.length) list.append(trashList());

    if (guideOpen) return; // a guide is open there
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
      if (note.history?.length) card.append(historyList(note));
      main.append(card);
    }
  }

  // historyList is a note's earlier versions, each one a click from coming back.
  function historyList(note) {
    const box = el("details", { className: "note-history" }, el("summary", {}, `Earlier versions (${note.history.length})`));
    note.history.forEach((old, i) => {
      const restore = button("Restore", { className: "sm ghost" });
      restore.onclick = () => change(() => {
        note.history.splice(i, 1);
        rewrite(note, old.title, old.text);
      }, "Earlier version restored");
      box.append(el("div", { className: "version" }, el("span", {}, when(old.at) + (old.title !== note.title ? " · " + old.title : "")), restore));
    });
    return box;
  }

  // trashList is "Recently deleted", under the notes.
  function trashList() {
    const box = el("details", { className: "note-trash" }, el("summary", {}, `Recently deleted (${state.trash.length})`));
    state.trash.forEach((gone, i) => {
      const restore = button("Restore", { className: "sm ghost" });
      restore.onclick = () => change(() => {
        state.trash.splice(i, 1);
        const { deletedAt, ...note } = gone;
        state.notes.push(note);
        current = note.id;
      }, "Note restored");
      const forget = button("Delete", { className: "sm ghost", title: "Delete for good" });
      forget.onclick = () => {
        if (!confirm(`Delete “${gone.title || "Untitled"}” for good? It can't be brought back.`)) return;
        change(() => state.trash.splice(state.trash.indexOf(gone), 1), "Note deleted for good");
      };
      box.append(el("div", { className: "version" }, el("span", { title: "Deleted " + when(gone.deletedAt) }, gone.title || "Untitled"),
        el("span", { className: "trash-buttons" }, restore, forget)));
    });
    return box;
  }

  function editor(note) {
    const title = el("input", { className: "txt", name: "title", value: note.title, maxLength: 60, required: true });
    const text = el("textarea", { className: "txt", name: "content", value: note.text, spellcheck: true, autofocus: true });
    const del = button("Delete note", { className: "sm ghost" });
    const cancel = button("Cancel", { className: "sm" });
    const form = el("form", { className: "card doc-editor" },
      el("label", { className: "lbl" }, "Name ", title), text,
      el("div", { className: "foot" }, del,
        el("span", {}, el("span", { className: "small muted" }, "Markdown · Ctrl+Enter saves · encrypted in your browser"),
          cancel, el("button", { className: "sm primary" }, "Save"))));
    form.dataset.ctrlEnter = "";
    form.onsubmit = event => {
      event.preventDefault();
      editing = false;
      change(() => rewrite(note, title.value.trim(), text.value), "Note saved");
    };
    cancel.onclick = () => { editing = false; render(); };
    del.onclick = () => {
      if (!confirm(`Delete “${note.title}”? You can bring it back from Recently deleted.`)) return;
      editing = false;
      change(() => remove(note), "Note deleted");
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

  // ---------- for the AI in the browser (assistant.js) ----------

  // all is every note, loaded (and unlocked) if needed; null while they're locked.
  async function all() {
    if (!state && !(await load())) return null;
    return state.notes;
  }

  const byTitle = title => state.notes.find(n => (n.title || "").toLowerCase() === String(title || "").trim().toLowerCase());

  // exists reports whether a note has this title (any case).
  async function exists(title) {
    return !!(await all()) && !!byTitle(title);
  }

  // saveNote replaces the note with this title (any case; its old text stays in its history), or
  // adds it, and saves.
  async function saveNote(title, text) {
    if (!(await all())) throw new Error("the notes are locked");
    const cleanTitle = String(title || "").trim() || "Untitled";
    const note = byTitle(cleanTitle);
    if (note) rewrite(note, note.title, String(text ?? ""));
    else state.notes.push({ id: newId(), title: cleanTitle, text: String(text ?? "") });
    try {
      await save();
    } catch (e) {
      await load(); // another tab saved first: try once more on top of it
      return saveNote(title, text);
    }
    return note ? "updated (its old text is kept in its history)" : "added";
  }

  // importNotes adds notes from an export (backup.js), or gives a note with the same title the
  // imported text (its old text stays in its history), and saves once. It answers how many.
  async function importNotes(list) {
    if (!(await all())) throw new Error("the notes are locked");
    let count = 0;
    for (const { title, text } of list) {
      const cleanTitle = String(title || "").trim() || "Untitled";
      const note = byTitle(cleanTitle);
      if (note && note.text === text) continue;
      if (note) rewrite(note, note.title, String(text ?? ""));
      else state.notes.push({ id: newId(), title: cleanTitle, text: String(text ?? "") });
      count++;
    }
    if (count) await save();
    render();
    return count;
  }

  return { all, exists, saveNote, importNotes, unlock: Keys.unlockWithPassword };
})();
