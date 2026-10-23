// Moving your data to another dashboard (Account → Privacy). The server's "Download your data"
// zip has what it can read; this adds what only this browser can: the notes (as Markdown files),
// the AI chat and, if asked, the AI settings and keys, decrypted, so the file works on any server.
// "Import data" reads such a zip: the notes, chat and settings are sealed again here with this
// account's key (the server never sees them), and the rest is posted to /account/import.

const Backup = (() => {
  // The server's files, as the export names them and /account/import takes them.
  const SERVER_FILES = ["plan.json", "checklist.json", "weights.json", "lifting.json", "tasks.json", "time.json", "anki-history.json"];

  // ---------- zip files ----------
  // Just enough of the format for these exports: no zip64, no encryption; stored or deflated.

  const crcTable = (() => {
    const table = new Uint32Array(256);
    for (let n = 0; n < 256; n++) {
      let c = n;
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      table[n] = c >>> 0;
    }
    return table;
  })();
  function crc32(bytes) {
    let c = 0xffffffff;
    for (const b of bytes) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8);
    return (c ^ 0xffffffff) >>> 0;
  }

  async function through(bytes, stream) {
    return new Uint8Array(await new Response(new Blob([bytes]).stream().pipeThrough(stream)).arrayBuffer());
  }

  // readZip answers the files in a zip, as { name: Uint8Array }.
  async function readZip(buffer) {
    const bytes = new Uint8Array(buffer), view = new DataView(buffer);
    let end = -1;
    for (let i = bytes.length - 22; i >= Math.max(0, bytes.length - 65557); i--) {
      if (view.getUint32(i, true) === 0x06054b50) { end = i; break; }
    }
    if (end < 0) throw new Error("That isn't a .zip file");
    const count = view.getUint16(end + 10, true);
    let at = view.getUint32(end + 16, true);
    const files = {};
    for (let i = 0; i < count; i++) {
      if (view.getUint32(at, true) !== 0x02014b50) throw new Error("The .zip file is damaged");
      const method = view.getUint16(at + 10, true), size = view.getUint32(at + 20, true);
      const nameLength = view.getUint16(at + 28, true), extra = view.getUint16(at + 30, true), comment = view.getUint16(at + 32, true);
      const local = view.getUint32(at + 42, true);
      const name = new TextDecoder().decode(bytes.subarray(at + 46, at + 46 + nameLength));
      at += 46 + nameLength + extra + comment;
      if (name.endsWith("/")) continue;
      const start = local + 30 + view.getUint16(local + 26, true) + view.getUint16(local + 28, true);
      const data = bytes.subarray(start, start + size);
      if (method === 0) files[name] = data.slice();
      else if (method === 8) files[name] = await through(data, new DecompressionStream("deflate-raw"));
      else throw new Error(`The .zip file uses a compression this page can't read (${name})`);
    }
    return files;
  }

  // writeZip makes a zip of { name: Uint8Array }, deflated.
  async function writeZip(files) {
    const parts = [], central = [];
    let offset = 0;
    const now = new Date();
    const time = (now.getHours() << 11) | (now.getMinutes() << 5) | (now.getSeconds() >> 1);
    const date = ((now.getFullYear() - 1980) << 9) | ((now.getMonth() + 1) << 5) | now.getDate();
    for (const [name, data] of Object.entries(files)) {
      const nameBytes = new TextEncoder().encode(name);
      const packed = await through(data, new CompressionStream("deflate-raw"));
      const crc = crc32(data);
      const header = (size, signature) => {
        const h = new DataView(new ArrayBuffer(size));
        h.setUint32(0, signature, true);
        return h;
      };
      const local = header(30, 0x04034b50);
      [[4, 20], [6, 0x0800], [8, 8], [10, time], [12, date], [26, nameBytes.length]].forEach(([at, v]) => local.setUint16(at, v, true));
      [[14, crc], [18, packed.length], [22, data.length]].forEach(([at, v]) => local.setUint32(at, v, true));
      const entry = header(46, 0x02014b50);
      [[4, 20], [6, 20], [8, 0x0800], [10, 8], [12, time], [14, date], [28, nameBytes.length]].forEach(([at, v]) => entry.setUint16(at, v, true));
      [[16, crc], [20, packed.length], [24, data.length], [42, offset]].forEach(([at, v]) => entry.setUint32(at, v, true));
      parts.push(local, nameBytes, packed);
      central.push(entry, nameBytes);
      offset += 30 + nameBytes.length + packed.length;
    }
    const size = central.reduce((n, p) => n + p.byteLength, 0);
    const end = new DataView(new ArrayBuffer(22));
    end.setUint32(0, 0x06054b50, true);
    end.setUint16(8, Object.keys(files).length, true);
    end.setUint16(10, Object.keys(files).length, true);
    end.setUint32(12, size, true);
    end.setUint32(16, offset, true);
    return new Blob([...parts, ...central, end], { type: "application/zip" });
  }

  // ---------- what only this browser can read ----------

  const text = value => new TextEncoder().encode(value);
  const json = value => text(JSON.stringify(value, null, 1));
  // A note's file name: its title, without what file systems refuse.
  const fileName = title => (String(title).replace(/[\\/:*?"<>|\x00-\x1f]/g, "-").replace(/^\.+/, "").trim() || "Untitled").slice(0, 120);

  async function sealedJSON(path) {
    const response = await fetch(path, { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error((await response.json().catch(() => ({}))).error || response.statusText);
    return response.json();
  }

  async function postSealed(path, fields) {
    let body = fields;
    if (!(fields instanceof FormData)) {
      body = new FormData();
      for (const [k, v] of Object.entries(fields)) body.set(k, v);
    }
    const response = await fetch(path, { method: "POST", body, headers: { "HX-Request": "true" } });
    const answer = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(answer.error || response.statusText);
    return answer;
  }

  // privateFiles are the notes, the chat and (withKeys) the AI settings, decrypted.
  async function privateFiles(key, withKeys) {
    const files = {}, names = new Set();
    for (const note of await SealedNotes.all() || []) {
      let name = fileName(note.title || "Untitled");
      for (let n = 2; names.has(name.toLowerCase()); n++) name = `${fileName(note.title || "Untitled")} (${n})`;
      names.add(name.toLowerCase());
      files[`notes/${name}.md`] = text(note.text || "");
    }
    const chat = await sealedJSON("/assistant/chat");
    if (chat.blob) {
      const { messages = [], api = [], server } = await Keys.openBlob(key, "chat", chat.blob);
      if (messages.length) files["private/chat.json"] = json({ messages, api, server });
    }
    if (withKeys) {
      const settings = await sealedJSON("/assistant/keys");
      if (settings.blob) {
        const { seq, ...values } = await Keys.openBlob(key, "settings", settings.blob);
        if (Object.keys(values).length) files["private/ai-settings.json"] = json(values);
      }
    }
    return files;
  }

  const privateReadme = withKeys => `

## Added by your browser

| File | What it is |
|---|---|
| notes/*.md | Your notes, one Markdown file each, named after its title |
| private/chat.json | Your AI chat (the MCPs tab) |
${withKeys ? "| private/ai-settings.json | Your AI keys and the local model's address |\n" : ""}
These were encrypted on the server; here they're plain text${withKeys ? ", and the AI keys work for anyone who has this file" : ""}. Keep it somewhere safe.
To move to another dashboard, open its Account page → Privacy → Import data, and pick this file.
`;

  // ---------- download ----------

  async function download(button) {
    const withKeys = !!button.closest("[data-backup]").querySelector("[data-backup-keys]")?.checked;
    button.disabled = true;
    try {
      const response = await fetch("/account/export");
      if (!response.ok) throw new Error("Couldn't make the export: " + response.statusText);
      const files = await readZip(await response.arrayBuffer());
      const key = await Keys.notesKey().catch(() => null);
      let added = null;
      if (key) {
        added = await privateFiles(key, withKeys).catch(e => {
          toast("Your notes and chat aren't in this download: " + e.message, { bad: true });
          return null;
        });
      }
      if (added) {
        Object.assign(files, added);
        const readme = new TextDecoder().decode(files["README.md"] || new Uint8Array());
        files["README.md"] = text(readme.replace(/\nNot included: your notes, AI chat and AI keys \([^)]*\), your/,
          `\nNot included: ${withKeys ? "" : "your AI keys (you left them out), "}your`) + privateReadme(withKeys));
      } else if (!key) {
        toast("Your notes are locked, so they aren't in this download: unlock them on the Docs tab first", { bad: true });
      }
      const link = Object.assign(document.createElement("a"), {
        href: URL.createObjectURL(await writeZip(files)),
        download: `dashboard-export-${new Date().toISOString().slice(0, 10)}.zip`,
      });
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(link.href), 60_000);
    } catch (e) {
      toast(e.message, { bad: true });
    } finally {
      button.disabled = false;
    }
  }

  // ---------- import ----------

  async function importZip(form, file) {
    const result = form.querySelector("[data-import-result]");
    const show = (lines, bad) => {
      result.hidden = false;
      result.className = bad ? "sign-in-error" : "muted";
      result.replaceChildren(...lines.flatMap((line, i) => [i ? document.createElement("br") : "", line]));
    };
    const files = await readZip(await file.arrayBuffer());
    const done = [], problems = [];

    // The server's part.
    const body = new FormData();
    let any = false;
    for (const name of SERVER_FILES) {
      if (files[name]) { body.set(name, new Blob([files[name]]), name); any = true; }
    }
    if (any) {
      const answer = await postSealed("/account/import", body);
      done.push(...answer.done);
      problems.push(...answer.problems);
    }

    // This browser's part, sealed with this account's key.
    const notes = Object.entries(files).filter(([name]) => /^notes\/[^/]+\.md$/.test(name))
      .map(([name, data]) => ({ title: name.slice(6, -3), text: new TextDecoder().decode(data) }));
    const chatFile = files["private/chat.json"], settingsFile = files["private/ai-settings.json"];
    if (notes.length || chatFile || settingsFile) {
      const key = await Keys.notesKey().catch(() => null);
      if (!key) {
        problems.push("Your notes, chat and AI settings weren't imported: unlock your notes on the Docs tab, then import again");
      } else {
        try {
          if (notes.length) {
            const count = await SealedNotes.importNotes(notes);
            done.push(`${count} ${count === 1 ? "note" : "notes"}${count < notes.length ? ` (${notes.length - count} already here)` : ""}`);
          }
          if (chatFile) done.push(await importChat(key, JSON.parse(new TextDecoder().decode(chatFile))));
          if (settingsFile) done.push(await importSettings(key, JSON.parse(new TextDecoder().decode(settingsFile))));
        } catch (e) {
          problems.push(e.message);
        }
      }
    }
    if (!done.length && !problems.length) problems.push("There was nothing to import: pick a file from Download your data");
    show([...(done.length ? ["Imported " + done.join(", ") + "."] : []), ...problems], problems.length > 0 && !done.length);
    if (done.length) toast("Imported your data");
    if (problems.length) toast(problems[0], { bad: true });
  }

  // importChat takes the exported chat, unless there's one here already.
  async function importChat(key, imported) {
    const here = await sealedJSON("/assistant/chat");
    if (here.blob && (await Keys.openBlob(key, "chat", here.blob)).messages?.length) return "not the chat (there's one here already: start a New chat first to replace it)";
    const chat = { messages: imported.messages || [], api: imported.api || [], ...(imported.server ? { server: imported.server } : {}) };
    await postSealed("/assistant/chat", { version: here.version, blob: await Keys.sealBlob(key, "chat", chat) });
    Keys.saved("chat", chat);
    return "the AI chat";
  }

  // importSettings adds the exported AI settings this account doesn't have yet.
  async function importSettings(key, imported) {
    const here = await sealedJSON("/assistant/keys");
    const settings = here.blob ? await Keys.openBlob(key, "settings", here.blob) : {};
    let added = 0;
    for (const [field, value] of Object.entries(imported)) {
      if (field !== "seq" && typeof value === "string" && value && !settings[field]) { settings[field] = value; added++; }
    }
    if (!added) return "no AI settings (they're all set here already)";
    await postSealed("/assistant/keys", { version: here.version, blob: await Keys.sealBlob(key, "settings", settings) });
    Keys.saved("settings", settings);
    return `${added} AI ${added === 1 ? "setting" : "settings"}`;
  }

  // ---------- the Privacy tab ----------

  htmx.onLoad(node => {
    const cards = node.matches?.("[data-backup]") ? [node] : [...(node.querySelectorAll?.("[data-backup]") || [])];
    for (const card of cards) {
      card.querySelector("[data-backup-download]")?.addEventListener("click", event => {
        event.preventDefault();
        download(event.currentTarget);
      });
      const form = card.querySelector("form[data-import]");
      form?.addEventListener("submit", async event => {
        event.preventDefault();
        const file = form.elements.file.files[0];
        if (!file) return;
        if (!confirm("Import this file? Your plan is replaced by its plan; everything else is added to what's here, and nothing is deleted.")) return;
        const button = form.querySelector("button");
        button.disabled = true;
        try { await importZip(form, file); }
        catch (e) { toast(e.message, { bad: true }); }
        finally { button.disabled = false; form.reset(); }
      });
    }
  });

  return { readZip, writeZip };
})();
