// Your password stays in this browser. From it the browser makes two things: a proof that signs
// you in (all the server ever sees), and the key that opens your notes' key, which the server
// can't make. Your notes are encrypted and decrypted here (see notes.js); the server only stores
// what it can't read. A recovery code, made here and shown once, opens the notes' key too.
//
// The sign-in and account forms marked data-keys are sent with proofs in place of passwords.

const Keys = (() => {
  const ITERATIONS = 600_000; // PBKDF2-SHA256, as the server uses for its own copy of the proof
  const text = new TextEncoder();
  const b64 = bytes => {
    const all = new Uint8Array(bytes);
    let binary = "";
    for (let i = 0; i < all.length; i += 0x8000) binary += String.fromCharCode(...all.subarray(i, i + 0x8000));
    return btoa(binary);
  };
  const unb64 = s => Uint8Array.from(atob(s), c => c.charCodeAt(0));
  const subtle = window.crypto && crypto.subtle;

  // ---------- making keys ----------

  const aesKey = (raw, extractable = false) =>
    subtle.importKey("raw", raw, "AES-GCM", extractable, ["encrypt", "decrypt"]);

  // fromPassword is the proof for the server and the key that wraps the notes' key.
  async function fromPassword(username, password) {
    const base = await subtle.importKey("raw", text.encode(password), "PBKDF2", false, ["deriveBits"]);
    const bits = new Uint8Array(await subtle.deriveBits(
      { name: "PBKDF2", hash: "SHA-256", salt: text.encode("dashboard/v1/" + username.toLowerCase()), iterations: ITERATIONS },
      base, 512));
    return { proof: b64(bits.slice(0, 32)), kek: await aesKey(bits.slice(32)) };
  }

  // A recovery code: 160 random bits as eight groups of four letters and digits.
  function newRecoveryCode() {
    const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
    const bytes = crypto.getRandomValues(new Uint8Array(20));
    let bits = "", out = "";
    for (const b of bytes) bits += b.toString(2).padStart(8, "0");
    for (let i = 0; i < bits.length; i += 5) out += alphabet[parseInt(bits.slice(i, i + 5), 2)];
    return out.match(/.{4}/g).join("-");
  }

  // fromRecovery is like fromPassword for a recovery code (typed with any case, spaces or dashes).
  async function fromRecovery(username, code) {
    const clean = code.toUpperCase().replace(/[^A-Z2-7]/g, "");
    const base = await subtle.importKey("raw", text.encode(clean), "HKDF", false, ["deriveBits"]);
    const derive = info => subtle.deriveBits({ name: "HKDF", hash: "SHA-256",
      salt: text.encode("dashboard/v1/recovery/" + username.toLowerCase()), info: text.encode(info) }, base, 256);
    return { proof: b64(await derive("server")), kek: await aesKey(await derive("notes")) };
  }

  // ---------- sealing ----------

  async function seal(key, bytes) {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const sealed = new Uint8Array(await subtle.encrypt({ name: "AES-GCM", iv }, key, bytes));
    const out = new Uint8Array(12 + sealed.length);
    out.set(iv);
    out.set(sealed, 12);
    return b64(out);
  }

  async function open(key, sealed) {
    const bytes = unb64(sealed);
    return new Uint8Array(await subtle.decrypt({ name: "AES-GCM", iv: bytes.slice(0, 12) }, key, bytes.slice(12)));
  }

  const sealText = (key, s) => seal(key, text.encode(s));
  const openText = async (key, sealed) => new TextDecoder().decode(await open(key, sealed));

  // ---------- kept in this browser ----------
  // The notes' key is kept as a key the page can use but never read out (non-extractable).

  function db() {
    return new Promise((resolve, reject) => {
      const request = indexedDB.open("dashboard-keys", 1);
      request.onupgradeneeded = () => request.result.createObjectStore("keys");
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
  }
  async function store(mode, action) {
    const database = await db();
    return new Promise((resolve, reject) => {
      const request = action(database.transaction("keys", mode).objectStore("keys"));
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
  }
  const keep = (name, value) => store("readwrite", s => s.put(value, name));
  const kept = name => store("readonly", s => s.get(name)).catch(() => undefined);
  const forget = name => store("readwrite", s => s.delete(name)).catch(() => {});
  const forgetAll = () => store("readwrite", s => s.clear()).catch(() => {});

  // ---------- the notes' key ----------

  const username = () => document.querySelector("meta[name=dashboard-user]")?.content || "";

  async function wrappedKey() {
    const response = await fetch("/notes/key", { headers: { Accept: "application/json" } });
    return response.ok ? response.json() : null;
  }

  // unlock opens the notes' key with the password (wrapped) or a key that opens it, and keeps it.
  async function unlockWith(kek, wrapped) {
    const raw = await open(kek, wrapped);
    await keep("notes", await aesKey(raw));
  }

  // notesKey is the kept key, after finishing a sign-in that left its password key pending.
  async function notesKey() {
    const pending = await kept("pending");
    if (pending) {
      await forget("pending");
      const wrapped = await wrappedKey();
      if (wrapped) await unlockWith(pending, wrapped.password).catch(() => {});
    }
    return kept("notes");
  }

  async function unlockWithPassword(password) {
    const wrapped = await wrappedKey();
    if (!wrapped) throw new Error("This account's notes aren't encrypted");
    const { kek } = await fromPassword(username(), password);
    await unlockWith(kek, wrapped.password).catch(() => { throw new Error("That password doesn't open your notes"); });
  }

  // ---------- forms ----------

  function checkNewPassword(name, password, confirm) {
    if (!password) return "Pick a password";
    if ([...password].length > 256) return "Use at most 256 characters for your password";
    if (password.toLowerCase() === name.toLowerCase()) return "Your password can't be your username";
    if (password.split("").every(c => c === password[0])) return "Your password can't be one character repeated";
    if (confirm !== undefined && password !== confirm) return "The two passwords don't match";
    return "";
  }

  // set puts a hidden field in the form (replacing one of that name).
  function set(form, name, value) {
    let input = form.querySelector(`input[type=hidden][name="${name}"]`);
    if (!input) {
      input = document.createElement("input");
      input.type = "hidden";
      input.name = name;
      form.append(input);
    }
    input.value = value;
  }

  // hide stops a typed secret from being sent: it keeps its value but loses its name.
  function hide(field) {
    if (field) { field.dataset.name = field.name; field.removeAttribute("name"); }
  }

  // offerNewKeys adds a new recovery code's proof and a new notes key, used only when the account
  // has none yet (its first sign-in); the code is kept to show on the next page.
  async function offerNewKeys(form, name, kek) {
    const code = newRecoveryCode();
    const recovery = await fromRecovery(name, code);
    const raw = crypto.getRandomValues(new Uint8Array(32));
    set(form, "recovery", recovery.proof);
    set(form, "notes_password", await seal(kek, raw));
    set(form, "notes_recovery", await seal(recovery.kek, raw));
    sessionStorage.setItem("recoveryCode", code);
  }

  const prepare = {
    // Logging in: the proof; on an upgrade, the real password once more.
    async login(form) {
      const name = form.elements.username.value.trim();
      const password = form.querySelector("[data-password]");
      const { proof, kek } = await fromPassword(name, password.value);
      set(form, "proof", proof);
      await offerNewKeys(form, name, kek);
      await keep("pending", kek);
      if (!form.hasAttribute("data-upgrade")) hide(password);
    },
    async setup(form) {
      const name = form.elements.username.value.trim();
      const password = form.querySelector("[data-password]"), confirm = form.querySelector("[data-confirm]");
      const problem = checkNewPassword(name, password.value, confirm.value);
      if (problem) throw new Error(problem);
      set(form, "proof", (await fromPassword(name, password.value)).proof);
      hide(password); hide(confirm);
    },
    // Joining from an invite, or a reset: with the old recovery code typed, the notes' key is
    // opened with it and wrapped with the new password, so the notes are kept.
    async invite(form) {
      const name = form.elements.username.value.trim();
      const password = form.querySelector("[data-password]"), confirm = form.querySelector("[data-confirm]");
      const problem = checkNewPassword(name, password.value, confirm.value);
      if (problem) throw new Error(problem);
      const { proof, kek } = await fromPassword(name, password.value);
      set(form, "proof", proof);
      await offerNewKeys(form, name, kek);
      const typed = form.querySelector("[data-recovery]");
      if (typed && typed.value.trim()) {
        const old = await fromRecovery(name, typed.value);
        set(form, "old_recovery", old.proof);
        if (form.dataset.notesRecovery) {
          const raw = await open(old.kek, form.dataset.notesRecovery)
            .catch(() => { throw new Error("That recovery code doesn't match"); });
          set(form, "notes_password", await seal(kek, raw));
        }
        sessionStorage.removeItem("recoveryCode"); // the old code stays theirs
      }
      hide(typed);
      await keep("pending", kek);
      hide(password); hide(confirm);
    },
    // Changing the password: proofs of both, and the notes' key wrapped again for the new one.
    async password(form) {
      const name = username();
      const current = form.querySelector("[data-current]");
      const password = form.querySelector("[data-password]"), confirm = form.querySelector("[data-confirm]");
      const problem = checkNewPassword(name, password.value, confirm.value);
      if (problem) throw new Error(problem);
      const old = await fromPassword(name, current.value);
      const next = await fromPassword(name, password.value);
      set(form, "current", old.proof);
      set(form, "password", next.proof);
      if (form.hasAttribute("data-sealed")) {
        const wrapped = await wrappedKey();
        const raw = await open(old.kek, wrapped.password)
          .catch(() => { throw new Error("Your current password is wrong"); });
        set(form, "notes_password", await seal(next.kek, raw));
      }
      hide(current); hide(password); hide(confirm);
    },
    // A new recovery code: made here, shown once; the notes' key is wrapped with it.
    async recovery(form) {
      const name = username();
      const password = form.querySelector("[data-password]");
      const code = newRecoveryCode();
      const recovery = await fromRecovery(name, code);
      set(form, "recovery", recovery.proof);
      if (form.hasAttribute("data-sealed")) {
        const { kek } = await fromPassword(name, password.value);
        const raw = await open(kek, (await wrappedKey()).password)
          .catch(() => { throw new Error("That password is wrong"); });
        set(form, "notes_recovery", await seal(recovery.kek, raw));
      }
      sessionStorage.setItem("newRecoveryCode", code);
      hide(password);
    },
  };

  // Forms marked data-keys are stopped, prepared, then sent again (with proofs, not passwords).
  document.addEventListener("submit", async event => {
    const form = event.target;
    const kind = form.dataset?.keys;
    if (!kind || form.dataset.ready) {
      delete form.dataset.ready;
      return;
    }
    event.preventDefault();
    event.stopImmediatePropagation();
    const error = form.querySelector("[data-keys-error]");
    const button = form.querySelector("button:not([type=button])");
    try {
      if (!subtle) throw new Error("Your browser can only keep your password private over HTTPS (or on localhost). Open the dashboard with https://.");
      if (button) { button.disabled = true; button.dataset.label ??= button.textContent; button.textContent = "Working…"; }
      await prepare[kind](form);
      form.dataset.ready = "1";
      if (button) button.disabled = false;
      form.requestSubmit(button || undefined);
    } catch (e) {
      if (error) { error.textContent = e.message; error.hidden = false; }
      else alert(e.message);
    } finally {
      if (button && !form.dataset.ready) { button.disabled = false; button.textContent = button.dataset.label; }
      // The typed secrets get their names back, so a second try works.
      form.querySelectorAll("[data-name]").forEach(f => { if (!form.dataset.ready) f.name = f.dataset.name; });
    }
  }, true);

  // ---------- around the pages ----------

  function showCode(slot, code) {
    slot.value = code;
    slot.closest("[hidden]")?.removeAttribute("hidden");
  }

  document.addEventListener("DOMContentLoaded", () => {
    // Signed out: nothing kept from before (the next sign-in sets it up again).
    if (document.querySelector("form[data-keys=login], form[data-keys=setup]")) {
      forgetAll();
    }
    // The page after a first sign-in shows the recovery code made for it.
    const slot = document.querySelector("[data-recovery-code]");
    if (slot) {
      const code = sessionStorage.getItem("recoveryCode");
      sessionStorage.removeItem("recoveryCode");
      if (code) showCode(slot, code);
      else document.querySelector("[data-recovery-lost]")?.removeAttribute("hidden");
    }
  });

  // After making a new recovery code on the Account page, show it.
  document.addEventListener("htmx:afterSettle", () => {
    const slot = document.querySelector("[data-new-recovery-code]");
    const code = sessionStorage.getItem("newRecoveryCode");
    if (slot && code) {
      sessionStorage.removeItem("newRecoveryCode");
      showCode(slot, code);
    }
  });

  return { supported: !!subtle, notesKey, unlockWithPassword, sealText, openText, forgetAll };
})();
