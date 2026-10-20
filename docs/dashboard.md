# Dashboard guide

The web dashboard in `site/`: a local page for daily habits: today's workout, a daily checklist (with skips and reasons), goals, weight, and full Taskwarrior + Timewarrior management.

- **Today:** training for the day, checklist with streaks, weight log, nutrition from MyFitnessPal, due tasks, time tracked
- **Tasks:** add, edit, complete (asks how long it took, default 30 min, and logs it in Timewarrior; also on past days), skip with reasons, repeating schedules (every N days, specific weekdays every N weeks, monthly), undo
- **History:** GitHub-style activity heatmap (tasks done, hours tracked, checklist), activity feed, time log editor, skip reasons
- **Docs:** your Markdown notes in `docs/`, plus these guides
- **Anki:** your decks and due cards, add cards, search and edit notes (below)
- **MCPs:** chat with an AI about your MyFitnessPal diary and Anki cards (below)

## Run

Needs Nix (flakes) and Taskwarrior 3. From `site/`:

```sh
nix run            # or ./run.sh — serves http://localhost:1628
nix run . -- 8080  # another port
nix develop        # shell with go, timew and uv (go run ./cmd/dashboard 8080)
```

On start it installs a Taskwarrior `on-modify` hook so starting/stopping a task tracks time in Timewarrior.

## MyFitnessPal

The **Nutrition today** card shows what you've eaten against your MyFitnessPal goals: calories (goal − food + exercise), protein/carbs/fat, where the calories came from, the cholesterol-related limits (sat fat, fiber, sodium, cholesterol, sugar, potassium) and each meal's foods.

- It uses the login saved by the [MyFitnessPal MCP](https://github.com/Mason-Levyy/myfitnesspal-mcp) (see the MCPs tab to set it up). `mfp_fetch.py` runs with that package through `uv`.
- The server syncs every 10 minutes, when the page is opened with stale data, and with **↻ Sync**. The result is cached in `data/mfp.json`.
- **Log food:** **+ Log food** (or **+ Add to …** under a meal) searches MyFitnessPal's food database. Pick a result, then the number of servings, the serving size and the meal; a preview shows the calories and macros before you add it.
- **Edit or remove:** open a meal and use ✎ to change the amount, serving size or meal (MyFitnessPal's own edit form) or ✕ to remove a food. Changes go straight to your MyFitnessPal diary for today.
- Weigh-ins in MyFitnessPal are copied into the weight log (and tick "Weigh in"). A weight logged on the dashboard is sent to MyFitnessPal.
- Goals are read from MyFitnessPal; change them there. If MyFitnessPal can't be reached, the card falls back to the goals in `plan.json`.
- If the login expires and the automatic refresh can't renew it, sign in again: `uvx --from 'mfp-mcp[autorefresh]==0.4.1' mfp-mcp auth`.

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

A chat with an AI that can use your MyFitnessPal diary and your Anki cards: ask about your food and plan, say what you ate and it logs it, or have it make flashcards. It uses the [MyFitnessPal MCP](https://github.com/Mason-Levyy/myfitnesspal-mcp) and the [Anki MCP](https://github.com/ankimcp/anki-mcp-server), with your notes, `plan.json`, weigh-ins, checklist history and today's diary put into its instructions as context.

- **Pick the MCPs** with the toggles at the top. A green dot means it's ready; turned-on MCPs that need setup show their steps. With both off, the AI can only talk about your notes and data.
- The Anki MCP needs Anki open with AnkiConnect (see the Anki tab). It runs on Node 22 from Nix, pinned to a tested version.
- **What the AI can reach:** only your own MyFitnessPal and Anki, through the turned-on MCPs. It has no web access and can't read files (Codex keeps a shell, but read-only and offline), and your own CLI settings, extensions and MCP servers are left out. The Anki MCP goes through a gateway in the dashboard that refuses media from URLs or files and links, scripts or remote content in cards, so planted text in a shared deck or food entry can't use Anki to fetch or leak anything. Foods it creates in MyFitnessPal are private.

- **Pick the AI** at the top: Claude Code, Codex or Gemini CLI. Each one has to be signed in once in a terminal (`claude`, `codex login`, `gemini`). Codex and Gemini run through Nix if they aren't installed. Switching starts a new chat.
- **Set up the MyFitnessPal MCP** if the tab asks you to: `uvx --from 'mfp-mcp[autorefresh]==0.4.1' mfp-mcp auth`, then paste your MyFitnessPal session cookie (the MCP's README shows where to find it). The released package (pinned to 0.4.1) is used unless `MFP_MCP_DIR` in `site/.env` points at a local checkout.
- The conversation is kept in `data/chat.json` until **New chat**. Each answer uses that AI's plan or API credit.

## Account and security

The dashboard only listens on 127.0.0.1, but every program and every user on the computer can reach that. So everything except the login page needs you to be logged in.

- **First run:** the terminal prints `Create your account: http://localhost:1628/setup?code=…`. The setup code is also saved in `data/setup-code` (readable only by you) and deleted once the account exists, so nobody else on the computer can create the account before you.
- **Password:** at least 12 characters. It's stored only as a salted PBKDF2-SHA256 hash (600,000 rounds) in `data/account.json`.
- **Sessions:** logging in gives the browser a random cookie that lasts 30 days. It's `HttpOnly` (page scripts can't read it) and `SameSite=Strict`, and the server keeps only a hash of it in `data/sessions.json`. **Account** (top right) logs you out, logs out other browsers, or changes the password, which also logs out every other browser.
- **Wrong passwords:** after 5 wrong tries in a row, each try waits twice as long as the last (1 s, 2 s, 4 s… up to 15 minutes). A wrong username and a wrong password look the same.
- **Bar plugin:** the plugin uses a token instead of a password. Make one on the Account page and paste it into the plugin's settings. The token only works for the plugin's API (`/api/…`), not the pages, and only its hash is stored.
- **Other websites:** changes that another origin starts are refused, including other servers on `localhost`. Requests for another host name are refused too (DNS rebinding). The pages send a strict Content-Security-Policy, can't be framed, and load HTMX from jsDelivr pinned with integrity hashes.
- **Files:** `site/data/`, `plan.json`, `.env` and your notes are readable only by you (folders `0700`, files `0600`). Older installs are tightened on start.
- **Forgot your password?** Stop the dashboard, delete `site/data/account.json` and start it again. You'll get a new setup link, and your data stays.

## Your data

Everything stays local. The server binds to 127.0.0.1 only and rejects cross-site requests. None of this is tracked by git:

- `docs/*.md`: your notes (copied from `docs/templates/` on first run)
- `site/plan.json`: your goals, checklist, routines and reminders (created from `plan.example.json` on first run; edit it from the page)
- `site/data/`: checklist history, weigh-ins, the MyFitnessPal cache, the chat, and your account and sessions (hashes only)
- `site/.env`: local paths and keys (see `site/.env.example`)

## Code

- `site/cmd/dashboard`: the Go server's entry point; `site/internal/` holds one package per area (Taskwarrior, Timewarrior, MyFitnessPal, Anki, the MCPs tab's AI agents, the HTTP handlers). Standard library only. `nix run` builds it.
- `site/mfp_fetch.py`: MyFitnessPal reads and writes, run through the MyFitnessPal MCP's Python package.
- `site/web/`: the pages. `templates/` has the HTML (one file per tab), rendered on the server and updated in place with [HTMX](https://htmx.org); styles are in `css/` (one file per area) and `js/app.js` does the few things HTMX can't (toasts, shortcuts, dragging cards). No build step: edit a file and reload the page.
