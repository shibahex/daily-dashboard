# Dashboard guide

The web dashboard in `site/`: a self-hosted page for daily habits: today's workout, a daily checklist (with skips and reasons), goals, weight, and full Taskwarrior + Timewarrior management. Several people can use one server, each with their own account and their own data: nobody sees anyone else's.

- **Today:** training for the day, checklist with streaks, weight log, nutrition from MyFitnessPal, due tasks, time tracked. The ‹ › arrows around the date at the top go to earlier days (say, after midnight) to see or fix them: that day's training, checklist, weigh-in, food and time. Due tasks and Anki always show now.
- **Tasks:** add, edit, complete (asks how long it took, default 30 min, and logs it in Timewarrior; also on past days), skip with reasons, repeating schedules (every N days, specific weekdays every N weeks, monthly), undo
- **History:** GitHub-style activity heatmap (tasks done, hours tracked, checklist), activity feed, time log editor, skip reasons
- **Docs:** your own Markdown notes, encrypted in your browser (see **Encrypted data**), plus these guides. **Edit** a note, add one with the box under the list, move the open one with ↑ ↓, or delete it. Each note keeps its last 10 versions (**Earlier versions**, under it) and deleted notes stay in **Recently deleted** until 20 newer ones push them out.
- **Anki:** your decks and due cards, add cards, search and edit notes (below)
- **MCPs:** chat with an AI about your notes, MyFitnessPal diary and Anki cards. It runs in your browser (below)

## Run

Needs Nix (flakes) and Taskwarrior 3. From `site/`:

```sh
nix run            # or ./run.sh — serves http://localhost:1628
nix run . -- 8080  # another port
nix develop        # shell with go, timew, uv and the sandbox tools (go run ./cmd/dashboard 8080)
nix run . -- reset-password <username>   # a reset link, when the only admin is locked out (stop the server first)
```

On start it installs a Taskwarrior `on-modify` hook so starting/stopping a task tracks time in Timewarrior.

## Sharing it on a network

By default the dashboard only answers on this computer. To let others use it, put it behind a reverse proxy that handles HTTPS and tell the dashboard the address people use, in `site/.env`:

```sh
DASHBOARD_PUBLIC_URL=https://health.example.lan
# DASHBOARD_LISTEN=127.0.0.1:1628             # keep this when the proxy runs on the same computer
# DASHBOARD_TRUSTED_PROXIES=127.0.0.1/32,::1/128
```

[Caddy](https://caddyserver.com) (gets a certificate by itself, or `tls internal` on a LAN):

```caddyfile
health.example.lan {
	reverse_proxy 127.0.0.1:1628
}
```

nginx (keep the `Host` header: the dashboard refuses names it doesn't know):

```nginx
location / {
    proxy_pass http://127.0.0.1:1628;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
```

With [Tailscale](https://tailscale.com), `tailscale serve --bg 1628` gives it an HTTPS address on your tailnet; use that address as `DASHBOARD_PUBLIC_URL`.

- The dashboard refuses to listen beyond this computer without HTTPS, since passwords would cross the network in the clear. `DASHBOARD_ALLOW_HTTP=1` overrides that if you accept the risk.
- Over HTTPS the session cookie is `Secure` (`__Host-session`). Requests must use `localhost` or the public address; anything else is refused.
- Login rate limits use the client's address, which it takes from `X-Forwarded-For` only when the request comes from a trusted proxy.

## MyFitnessPal

The **Nutrition today** card shows what you've eaten against your MyFitnessPal goals: calories (goal − food + exercise), protein/carbs/fat, where the calories came from, the cholesterol-related limits (sat fat, fiber, sodium, cholesterol, sugar, potassium) and each meal's foods.

- Connect it on the **Account** page: paste the Cookie header of a myfitnesspal.com request (DevTools → Network → the first request → Request Headers → `cookie` → Copy value; the page shows the steps). MyFitnessPal needs the whole header, since its `remember_me` cookie is what keeps you signed in. The [MyFitnessPal MCP](https://github.com/Mason-Levyy/myfitnesspal-mcp) checks it and saves it in your own folder. `mfp_fetch.py` runs with that package through `uv`.
- The server syncs every 10 minutes, when the page is opened with stale data, and with **↻ Sync**. The result is cached in `data/mfp.json`.
- **Log food:** **+ Log food** (or **+ Add to …** under a meal) searches MyFitnessPal's food database. Pick a result, then the number of servings, the serving size and the meal; a preview shows the calories and macros before you add it.
- **Edit or remove:** open a meal and use ✎ to change the amount, serving size or meal (MyFitnessPal's own edit form) or ✕ to remove a food. Changes go straight to your MyFitnessPal diary for the day shown.
- Weigh-ins in MyFitnessPal are copied into the weight log (and tick "Weigh in"). A weight logged on the dashboard is sent to MyFitnessPal.
- Goals are read from MyFitnessPal; change them there. If MyFitnessPal can't be reached, the card falls back to the goals in `plan.json`.
- If the login expires and the automatic refresh can't renew it, paste a fresh Cookie header on the Account page.

## Anki tab

Works through the [AnkiConnect](https://ankiweb.net/shared/info/2055492159) add-on, so Anki has to be open. To install it: in Anki, **Tools → Add-ons → Get Add-ons…**, code `2055492159`, then restart Anki. The tab shows these steps (and an **Open Anki** button) until it can connect.

- **Decks:** new / learning / due counts per deck, cards reviewed today and a 30-day chart. **Study** opens the deck in Anki, where you review.
- **Add card:** pick a deck (or make a new one) and a note type; the fields follow the note type. Ctrl+Enter adds it. The deck and type are remembered.
- **Browse:** search with Anki's syntax (`deck:Japanese`, `tag:verbs`, `is:due`, `added:7`). Edit fields and tags, suspend or unsuspend, open the note in Anki's browser, or delete it.
- **↻ Sync** syncs with AnkiWeb.
- **Today tab:** an Anki card with cards due (new / learning / due), reviews today, your study streak, the last 14 days, and the decks with the most due cards (Study opens them in Anki). Drag it like the other cards.
- **History tab:** an **Anki reviews** heatmap mode, and "Reviewed N Anki cards" in the activity feed. The last review history is saved in `site/data/anki-history.json`, so it still shows while Anki is closed.
- The page only calls the AnkiConnect actions in `site/internal/anki/`. If you set an API key in AnkiConnect, put it in `site/.env` as `ANKI_CONNECT_API_KEY`.

## MCPs tab

A chat with an AI that knows your notes, plan and data and can use your MyFitnessPal diary and Anki cards: ask about your food and plan, say what you ate and it logs it, or have it make flashcards. It uses the [MyFitnessPal MCP](https://github.com/Mason-Levyy/myfitnesspal-mcp) and the [Anki MCP](https://github.com/ankimcp/anki-mcp-server).

The AI runs **in your browser** (`web/js/assistant.js`): your browser talks to the AI directly, with your notes in its prompt, so they never pass through the dashboard's server.

- **Pick the AI** at the top (the ones your admin allows):
  - **Claude:** needs your Anthropic API key (`sk-ant-api…`; a Claude subscription token only works with Claude Code), added on the Account page.
  - **Local model:** Ollama, LM Studio or llama.cpp (`llama-server`) running **on the same computer as your browser**, with the OpenAI-style API. Set its address on the Account page (`localhost:11434` for Ollama, `localhost:1234` for LM Studio, `localhost:8080` for llama.cpp), list its models and pick one. It must let the page reach it: start Ollama with `OLLAMA_ORIGINS=<the dashboard's address>`, turn on CORS in LM Studio; llama.cpp allows it already. The page can't reach a model elsewhere on your network. The model needs to handle tool calls.
  - Your keys and the local model's address are encrypted in your browser like your notes: the server never has them. (Keys saved on the server before are moved into your browser the first time you open the tab, then deleted there.)
- **Pick the MCPs** with the toggles. A green dot means it's ready; one that needs setup links to the Account page. **Attach prompt** adds the dashboard's instructions (how to answer, how to use the MCPs); your notes, plan and data go either way.
- **Saving notes:** if your admin allows it, the AI can save a note when you ask. It asks you before replacing one, and the old text stays in the note's history.
- The chat is encrypted in your browser like your notes, until **New chat**.

### What the tools can reach

The AI itself runs in your browser and has no web search. The MCP tools it calls run on the server, each in a sandbox ([bubblewrap](https://github.com/containers/bubblewrap)):

- It sees an empty home folder and an empty workspace: none of your files, not the dashboard's, not other people's, not the server account's logins.
- It has **no network of its own**. Its only way out is the dashboard's proxy, which reaches public internet addresses on the web ports (80/443) and never the server itself, the local network or cloud metadata.
- The MyFitnessPal MCP uses **your** MyFitnessPal login; the Anki MCP reaches **your** Anki through the dashboard's Anki gateway, which refuses media from URLs or files and links, scripts or remote content in cards. So planted text in a shared deck or food entry can't use Anki to fetch or leak anything. Foods it creates in MyFitnessPal are private.
- The MCP programs are installed once by the server (`site/data/tools/`, pinned versions) and mounted read-only: nothing is downloaded inside a sandbox. The tools stop after 10 idle minutes, and when your data locks.
- The server sees what goes to and from these tools (food searches and entries, cards), not your notes or the rest of the chat.

## Accounts and admins

- **First account:** the terminal prints `Create the first (admin) account: …/setup?code=…`. The setup code is also saved in `data/setup-code` (readable only by the server's owner) and deleted once the account exists, so nobody else can claim the server first. This account is the **host**: it keeps the server computer's own Taskwarrior, Timewarrior, MyFitnessPal login and Anki, and gets a one-time copy of the notes and data from before the dashboard had accounts.
- **Inviting people:** **Admin** (top right, admins only) → **Invite someone**: pick a username and role, and send the one-time link (it works once, for 7 days). They choose their own password. Each person gets their own plan, notes, checklist, weigh-ins, tasks, time log, MyFitnessPal, Anki connection, chats and keys, in `site/data/users/<id>/`.
- **Admins** manage accounts: password-reset links, turning an account off (logged out at once), making someone an admin, and deleting an account with everything in it (type the username to confirm). There's always at least one active admin, and an admin can't do these to their own account. Admins can't see anyone's data, and neither can whoever runs the server while it's locked (see **Encrypted data**).
- **What each person's AI may use:** on the Admin page, per person: which AIs (Claude, a local model on their own computer), which MCPs, and whether their AI may save notes. **New people start with** sets the default.
- **Anki for other people:** their Anki runs on their own computer. They install AnkiConnect, set `"webBindAddress": "0.0.0.0"` and an `"apiKey"` in its config, and enter the address and key on their Account page. The dashboard refuses an AnkiConnect without a key (anyone on the network could use it) and, for anyone but the host, addresses on the server itself.

## Security

- **Passwords:** at least 12 characters, and they never reach the server: the sign-in page sends a proof made from them (PBKDF2-SHA256, 600,000 rounds), which the server hashes again before keeping it in `data/accounts.json`.
- **Sessions:** logging in gives the browser a random cookie. It's `HttpOnly`, `SameSite=Strict` and, over HTTPS, `Secure`; the server keeps only a hash of it in `data/sessions.json`. It lasts 30 days, or until it goes unused for 7. The Account page lists your signed-in browsers and logs out any one of them, or all but this one; changing your password logs out the others.
- **Wrong passwords:** after 5 wrong tries in a row for a username, or from one address, each try waits twice as long as the last (1 s, 2 s, 4 s… up to 15 minutes), and a restart doesn't reset it. A wrong username and a wrong password look the same.
- **Bar plugin:** each person makes their own token on the Account page; it only works for the plugin's API (`/api/…`), only shows that person's data, and only its hash is stored.
- **Keys:** AI keys and the local model's address are encrypted in each person's browser, like their notes: the server never has them.
- **Other websites:** changes that another origin starts are refused, including other servers on `localhost`. Requests for any host name but `localhost` and the public address are refused (DNS rebinding). The pages send a strict Content-Security-Policy, can't be framed, and run only scripts the dashboard serves itself (pinned copies of htmx, marked and DOMPurify): no other site's script can run on them.
- **Files:** `site/data/`, every user's folder, `plan.json`, `.env` and notes are readable only by the server's account (folders `0700`, files `0600`).
- **Forgot your password?** Ask an admin for a reset link. If your data is encrypted, the reset page asks for your **recovery code**: with it you keep your data, without it the account starts over empty. If the only admin is locked out, whoever runs the server stops it and runs `nix run . -- reset-password <username>` in `site/`.

## Encrypted data

Everyone's notes, AI chat and AI keys are encrypted **end to end**: only their browser can read them. Their other data is encrypted on the server's disk, unless they choose otherwise.

- **Your password never leaves your browser.** The sign-in page turns it into two things: a proof that signs you in (all the server gets) and a key that only your browser has.
- **Notes, chat and keys:** your browser encrypts and decrypts them with that key (`web/js/keys.js`, `notes.js`, `assistant.js`); the server stores scrambled blobs, each labelled with whose it is and what it is, and numbered, so it can't swap them or hand back an older copy unnoticed. Even with someone's login, or watching the network, they can't be read without the password. Notes from before are sealed the first time you open the Docs tab, and their plain copies deleted.
- **The AI runs in your browser** (MCPs tab), so your notes go straight to the AI (Anthropic, or a model on your computer) and never through the server.
- **Everything else** (plan, checklist, weight, tasks, time, food, logins) is encrypted on the server's disk (AES-256-GCM, file by file, names hidden) in `site/data/vaults/<id>/`, with a random key locked by your password's proof, your recovery code and each signed-in browser. Signed in, it's decrypted into memory (a tmpfs: `$XDG_RUNTIME_DIR/dashboard-vaults` or `/dev/shm`) for the server to use, since it builds your pages from it, and saved back every few seconds. It's locked again (saved, wiped from memory) when you log out of your last browser, after 30 minutes without a request, and when the server stops. After a restart, your next page load unlocks it without a new sign-in. The bar plugin's token keeps it unlocked while the plugin runs.
- **Your choice:** on the Account page you can keep that other data as plain files instead (**Stop encrypting it**): on a server whose disk is already encrypted and that only you use, the encryption adds little and losing both your password and recovery code would lose that data. **Encrypt it** turns it back on, with a new recovery code. Notes, chat and keys stay end to end either way.
- **Recovery code:** made by your browser and shown once (your first sign-in, or joining). It's the only way back in if you forget your password: an admin's reset link keeps your data only if you enter it. Make a new one on the Account page.
- **Older accounts** sign in once more with their password (to move to proofs and get their notes key); their data is encrypted then. The Admin page shows who isn't encrypted, and who chose plain files.
- **Needs HTTPS** (or `localhost`): browsers only do this encryption on secure pages.
- **What it doesn't stop:** whoever runs the server could change the page's code to catch passwords as they're typed (true of any website: see **Checking the page's code**), or read the non-note data in memory while you're signed in. The MyFitnessPal tools and sync go through the server, so it sees your food diary.

## Checking the page's code

Your notes are only as private as the scripts that handle them, and those come from the server. So the dashboard tells your browser the exact hash of each script it loads (Subresource Integrity), and a changed script won't run. To check that the server is running the published code, open the page's source (Ctrl+U) and compare each script's `integrity` value with this list from the repository (a test keeps it up to date):

| Script | Hash |
|---|---|
| `app.js` | `sha384-Gg1ffnqWJoxRV2v807lyn4nf54HEVaTsKi9WZolWdENUdRZpeBH5dU0GCm74jARd` |
| `keys.js` | `sha384-x7klcKfrC1CwDgEGr/LFl9sbE57XelWNP0gAGhEAM6o5/W996bZ6cF1zOd544sAS` |
| `notes.js` | `sha384-iW4AiXCfNgQnKveb3A39gXIDQGefd5rlBvVGT5CeA8cMmPOz2/eGusX9YGt9ctl+` |
| `assistant.js` | `sha384-iqwgf6Po+R10AwXPCrKlgI7YcxrcXD9Xk2gBEKJ3s0jm5VIi4LfJll/NLl6sM2VH` |
| `htmx.js` | `sha384-HGfztofotfshcF7+8n44JQL2oJmowVChPTg48S+jvZoztPfvwD79OC/LTtG6dMp+` |
| `htmx-sse.js` | `sha384-fw+eTlCc7suMV/1w/7fr2/PmwElUIt5i82bi+qTiLXvjRXZ2/FkiTNA/w0MhXnGI` |
| `marked.js` | `sha384-IvVdUXkhyoD0shbjhiiOnuiTbervTaB0VJcnEcg3shUAMVOOF6lKj4ozJrCwvz6s` |
| `purify.js` | `sha384-a7SzOxErzJ3ZpQz0zJ32d67dSitNzPcbfybc/ykU9KJhMgZkwqfSxlhhdJRS+XGL` |

## Your data

None of this is tracked by git:

- `site/data/users/<id>/`: one folder per person: `plan.json`, `docs/` (their notes, from `docs/templates/` on first use), checklist history, weigh-ins, the MyFitnessPal cache and login, Anki connection, chat, sealed keys, and their Taskwarrior and Timewarrior (except the host's, which use the computer's own)
- `site/data/vaults/<id>/`: everyone else's folder, encrypted (see above); `site/data/users/<id>/` holds only the host's, and older accounts' until they're encrypted
- `site/data/`: accounts and sessions (hashes only), invites, the server's key, the installed MCP programs
- `site/.env`: local settings (see `site/.env.example`)
- `docs/*.md` and `site/plan.json` from before accounts stay as a backup after being copied to the host's folder

## Code

- `site/cmd/dashboard`: the Go server's entry point; `site/internal/` holds one package per area: `auth` (accounts, sessions, invites, policy), `workspace` (each user's folder and services), `taskwarrior`, `timewarrior`, `myfitnesspal`, `anki` (and its gateway), `assistant` (what the server gives the AI in the browser), `mcp` (starting MCP tools and talking to them), `sandbox` and `egress` (the tools' sandbox and its proxy), `vault` (encrypting each person's data), `web` and `api` (the HTTP handlers). One dependency (goldmark, for Markdown). `nix run` builds it.
- `site/mfp_fetch.py`: MyFitnessPal reads and writes, run through the MyFitnessPal MCP's Python package.
- `site/web/`: the pages. `templates/` has the HTML (one file per tab), rendered on the server and updated in place with [HTMX](https://htmx.org); styles are in `css/` (one file per area) and `js/app.js` does the few things HTMX can't (toasts, shortcuts, dragging cards, timed refreshes); `js/keys.js`, `notes.js` and `assistant.js` handle what the server must never see (passwords, notes, the AI chat). No build step: edit a file and reload the page.
