# Dashboard guide

The web dashboard in `site/`: a self-hosted page for daily habits: today's workout, a daily checklist (with skips and reasons), goals, weight, and full Taskwarrior + Timewarrior management. Several people can use one server, each with their own account and their own data: nobody sees anyone else's.

- **Today:** training for the day, checklist with streaks, weight log, nutrition from MyFitnessPal, due tasks, time tracked
- **Tasks:** add, edit, complete (asks how long it took, default 30 min, and logs it in Timewarrior; also on past days), skip with reasons, repeating schedules (every N days, specific weekdays every N weeks, monthly), undo
- **History:** GitHub-style activity heatmap (tasks done, hours tracked, checklist), activity feed, time log editor, skip reasons
- **Docs:** your own Markdown notes, plus these guides
- **Anki:** your decks and due cards, add cards, search and edit notes (below)
- **MCPs:** chat with an AI about your MyFitnessPal diary and Anki cards (below)

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
- **Edit or remove:** open a meal and use ✎ to change the amount, serving size or meal (MyFitnessPal's own edit form) or ✕ to remove a food. Changes go straight to your MyFitnessPal diary for today.
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

A chat with an AI that can use your MyFitnessPal diary and your Anki cards: ask about your food and plan, say what you ate and it logs it, or have it make flashcards. It uses the [MyFitnessPal MCP](https://github.com/Mason-Levyy/myfitnesspal-mcp) and the [Anki MCP](https://github.com/ankimcp/anki-mcp-server), with your notes, plan, weigh-ins, checklist history and today's diary as context.

- **Add your key** on the Account page for the AI you use: an Anthropic, OpenAI or Google API key. For Claude you can also use your subscription: run `claude setup-token` and paste the token it prints. Everyone uses their own key, including the admin, so chats use your own credit.
- **Pick the AI** at the top: Claude Code, Codex or Gemini CLI (the ones your admin allows). Codex and Gemini are built with Nix on first use. Switching starts a new chat.
- **Pick the MCPs** with the toggles. A green dot means it's ready; one that needs setup links to the Account page. With both off, the AI can only talk about your notes and data.
- The conversation is kept in your own `chat.json` until **New chat**.

### What the AI can reach

Each answer runs in a sandbox ([bubblewrap](https://github.com/containers/bubblewrap)) made for that one run:

- It sees an empty home folder and a `/workspace` holding a **copy** of your notes, plan and data, only if your admin lets your AI read files. Nothing else from the server is visible: not the dashboard's files, not other people's folders, not the server account's logins.
- It has **no network of its own**. Its only way out is the dashboard's proxy, which reaches public internet addresses on the web ports (80/443) and never the server itself, the local network or cloud metadata. Web search and fetch are only on if your admin allows them.
- **Edits** come back only if your admin lets your AI edit files, and only for your notes (`*.md`) and a valid `plan.json`. A note you changed in the page while the AI was working is never overwritten. Its other changes are thrown away with the sandbox.
- Its MCP servers use **your** MyFitnessPal login and reach **your** Anki through the dashboard's Anki gateway, which refuses media from URLs or files and links, scripts or remote content in cards. So planted text in a shared deck or food entry can't use Anki to fetch or leak anything. Foods it creates in MyFitnessPal are private.
- The MCP programs are installed once by the server (`site/data/tools/`, pinned versions) and mounted read-only: nothing is downloaded inside a sandbox.
- Your own CLI settings, extensions and other MCP servers are left out. The chat refuses to run at all if the sandbox can't (it needs `nix run`, which provides bubblewrap and socat).

## Accounts and admins

- **First account:** the terminal prints `Create the first (admin) account: …/setup?code=…`. The setup code is also saved in `data/setup-code` (readable only by the server's owner) and deleted once the account exists, so nobody else can claim the server first. This account is the **host**: it keeps the server computer's own Taskwarrior, Timewarrior, MyFitnessPal login and Anki, and gets a one-time copy of the notes and data from before the dashboard had accounts.
- **Inviting people:** **Admin** (top right, admins only) → **Invite someone**: pick a username and role, and send the one-time link (it works once, for 7 days). They choose their own password. Each person gets their own plan, notes, checklist, weigh-ins, tasks, time log, MyFitnessPal, Anki connection, chats and keys, in `site/data/users/<id>/`.
- **Admins** manage accounts: password-reset links, turning an account off (logged out at once), making someone an admin, and deleting an account with everything in it (type the username to confirm). There's always at least one active admin, and an admin can't do these to their own account. Admins can't see anyone's data.
- **What each person's AI may use:** on the Admin page, per person: which AIs, which MCPs, web search and fetch, reading their own files, editing their notes and plan. **New people start with** sets the default.
- **Anki for other people:** their Anki runs on their own computer. They install AnkiConnect, set `"webBindAddress": "0.0.0.0"` and an `"apiKey"` in its config, and enter the address and key on their Account page. The dashboard refuses an AnkiConnect without a key (anyone on the network could use it) and, for anyone but the host, addresses on the server itself.

## Security

- **Passwords:** at least 12 characters, stored only as a salted PBKDF2-SHA256 hash (600,000 rounds) in `data/accounts.json`.
- **Sessions:** logging in gives the browser a random cookie that lasts 30 days. It's `HttpOnly`, `SameSite=Strict` and, over HTTPS, `Secure`; the server keeps only a hash of it in `data/sessions.json`. The Account page logs you out, logs out your other browsers, or changes your password (which also logs out your other browsers).
- **Wrong passwords:** after 5 wrong tries in a row for a username, or from one address, each try waits twice as long as the last (1 s, 2 s, 4 s… up to 15 minutes). A wrong username and a wrong password look the same.
- **Bar plugin:** each person makes their own token on the Account page; it only works for the plugin's API (`/api/…`), only shows that person's data, and only its hash is stored.
- **Keys:** AI keys are encrypted with the server's key (`data/secret.key`) and tied to their owner, so a copied user folder doesn't give them away.
- **Other websites:** changes that another origin starts are refused, including other servers on `localhost`. Requests for any host name but `localhost` and the public address are refused (DNS rebinding). The pages send a strict Content-Security-Policy, can't be framed, and load HTMX from jsDelivr pinned with integrity hashes.
- **Files:** `site/data/`, every user's folder, `plan.json`, `.env` and notes are readable only by the server's account (folders `0700`, files `0600`).
- **Forgot your password?** Ask an admin for a reset link. If the only admin is locked out, whoever runs the server stops it and runs `nix run . -- reset-password <username>` in `site/`.

## Your data

None of this is tracked by git:

- `site/data/users/<id>/`: one folder per person: `plan.json`, `docs/` (their notes, from `docs/templates/` on first use), checklist history, weigh-ins, the MyFitnessPal cache and login, Anki connection, chat, sealed keys, and their Taskwarrior and Timewarrior (except the host's, which use the computer's own)
- `site/data/`: accounts and sessions (hashes only), invites, the server's key, the installed MCP programs
- `site/.env`: local settings (see `site/.env.example`)
- `docs/*.md` and `site/plan.json` from before accounts stay as a backup after being copied to the host's folder

## Code

- `site/cmd/dashboard`: the Go server's entry point; `site/internal/` holds one package per area: `auth` (accounts, sessions, invites, policy), `workspace` (each user's folder and services), `taskwarrior`, `timewarrior`, `myfitnesspal`, `anki` (and its gateway), `assistant` (the AI CLIs), `sandbox` and `egress` (the agents' sandbox and its proxy), `web` and `api` (the HTTP handlers). One dependency (goldmark, for Markdown). `nix run` builds it.
- `site/mfp_fetch.py`: MyFitnessPal reads and writes, run through the MyFitnessPal MCP's Python package.
- `site/web/`: the pages. `templates/` has the HTML (one file per tab), rendered on the server and updated in place with [HTMX](https://htmx.org); styles are in `css/` (one file per area) and `js/app.js` does the few things HTMX can't (toasts, shortcuts, dragging cards). No build step: edit a file and reload the page.
