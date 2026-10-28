# Dashboard guide

The web dashboard in `site/`: a self-hosted page for daily habits: today's workout, a daily checklist (with skips and reasons), goals, weight, and full Taskwarrior + Timewarrior management. Several people can use one server, each with their own account and their own data: nobody sees anyone else's.

- **Today:** training for the day, checklist with streaks, weight log, strength (estimated one-rep max per lift), nutrition from MyFitnessPal, due tasks, time tracked. The ‹ › arrows around the date at the top go to earlier days (say, after midnight) to see or fix them: that day's training, checklist, weigh-in, food and time. Due tasks and Anki always show now.
- **Tasks:** add, edit, complete (asks how long it took, default 30 min, and logs it in Timewarrior; also on past days), skip with reasons, repeating schedules (every N days, specific weekdays every N weeks, monthly), undo
- **Lifting:** routines, a workout logger with a rest timer, progress per exercise and past workouts (below)
- **History:** GitHub-style activity heatmap (tasks done, hours tracked, checklist), activity feed, time log editor, skip reasons
- **Docs:** your own Markdown notes, encrypted in your browser (see **Encrypted data**), plus these guides. **Edit** a note, add one with the box under the list, move the open one with ↑ ↓, or delete it. Each note keeps its last 10 versions (**Earlier versions**, under it) and deleted notes stay in **Recently deleted** until 20 newer ones push them out.
- **Anki:** your decks and due cards, add cards, search and edit notes (below)
- **MCPs:** chat with an AI about your notes, MyFitnessPal diary, Anki cards and tasks. It runs in your browser (below)

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

By default the dashboard only answers on this computer. To let others use it, put it behind a reverse proxy that handles HTTPS and tell the dashboard it's reached over HTTPS, in `site/.env`. It then answers at whatever hostname the proxy sends; to allow only some, list them instead (`https://health.example.lan,https://other.example.lan`):

```sh
DASHBOARD_PUBLIC_URL=https://*
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
    client_max_body_size 64m; # Import data uploads your export
}
```

With [Tailscale](https://tailscale.com), `tailscale serve --bg 1628` gives it an HTTPS address on your tailnet.

- The dashboard refuses to listen beyond this computer without HTTPS, since passwords would cross the network in the clear. `DASHBOARD_ALLOW_HTTP=1` overrides that if you accept the risk.
- Over HTTPS the session cookie is `Secure` (`__Host-session`). Requests must use `localhost` or a public address; anything else is refused, unless the address is `https://*`.
- Login rate limits use the client's address, which it takes from `X-Forwarded-For` only when the request comes from a trusted proxy.

## Docker

GitHub builds an image from `main` (`ghcr.io/shibahex/daily-dashboard`, tagged `latest`), and `docker-compose.yml` runs it. Each release (a `v1.2.0` tag) is also published as `:1.2.0` and `:1.2`; to stay on one, set `DASHBOARD_IMAGE=ghcr.io/shibahex/daily-dashboard:1.2.0` in `.env`. Everything people save lives on one volume, mounted at `/app/site/data`.

```sh
git clone https://github.com/shibahex/daily-dashboard.git
cd daily-dashboard
cp .env.example .env     # set TZ
docker compose up -d
docker compose logs dashboard   # the sign-up link for the first account
```

`.env` has one setting to fill in:

- `TZ`: your time zone, like `America/New_York`, so "today" starts at your midnight.

The dashboard answers at whatever hostname nginx sends it, and invite links use the address you're on, so there's no domain to set.

Then point nginx at port 1628. The browser only does the dashboard's password and notes encryption on an `https://` page, so you can't sign in over plain `http://<server>:1628` (only on `http://localhost:1628` on the server itself). Keep the `Host` header: invite links use it.

```nginx
server {
    listen 443 ssl;
    server_name health.example.com;
    # ssl_certificate and ssl_certificate_key as for your other sites

    location / {
        proxy_pass http://127.0.0.1:1628;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;     # the AI chat streams its replies
        proxy_read_timeout 1h;
        client_max_body_size 64m; # Import data uploads your export
    }
}
```

In Nginx Proxy Manager: a proxy host for `health.example.com` that forwards to `http://<server's IP>:1628`, with an SSL certificate.

**Portainer:** **Stacks → Add stack → Repository**, with the URL `https://github.com/shibahex/daily-dashboard`, reference `refs/heads/main` and compose path `docker-compose.yml`. Add `TZ` under **Environment variables**, deploy, and find the sign-up link in the container's logs. To update, use **Pull and redeploy**; the volume keeps the data.

- Updating with plain Docker: `docker compose pull && docker compose up -d`.
- Optional settings in `.env`: `ANKI_CONNECT_URL` (and `ANKI_CONNECT_API_KEY`) for AnkiConnect on the computer that runs Anki, `DASHBOARD_PORT` for another port.
- `docker-compose.yml` turns off Docker's seccomp and AppArmor filters and its masking of `/proc` for this container (`security_opt`), because the AI's MyFitnessPal and Anki tools run in a bubblewrap sandbox, which makes user namespaces and mounts its own `/proc`. The server itself still runs as an unprivileged user. Remove those lines if you don't use the AI's tools.
- **Ubuntu 23.10 and later:** add `DASHBOARD_APPARMOR=podman` to `.env` (or Portainer's environment variables) and redeploy. Without it the AI's tools fail with `bwrap: loopback: Failed RTM_NEWADDR: Operation not permitted`: Ubuntu takes the permissions away from user namespaces made by containers without an AppArmor profile, including `apparmor=unconfined`. Ubuntu's own `podman` profile allows everything `unconfined` does plus user namespaces, so nothing on the host changes. Don't set it on other systems: where AppArmor has no `podman` profile (Debian, older Ubuntu), the container won't start.
- A reset link for a locked-out admin: `docker compose stop`, then `docker compose run --rm dashboard reset-password <username>`.
- No image for your machine (it's built for x86-64) or offline? `docker compose up -d --build` builds it from the `Dockerfile`, with Nix, in a few minutes.
- With Nix, `nix build ./site#image && ./result | docker load` makes the same image GitHub publishes, with a layer per package.

## MyFitnessPal

The **Nutrition today** card shows what you've eaten against your MyFitnessPal goals: calories (goal − food + exercise), protein/carbs/fat, where the calories came from, the cholesterol-related limits (sat fat, fiber, sodium, cholesterol, sugar, potassium) and each meal's foods.

- Connect it on the **Account** page, which walks you through it: log in at myfitnesspal.com, open the developer tools (F12) → **Application** → **Cookies**, and paste the `remember_me` cookie's value (all the sign-in needs: MyFitnessPal hands out fresh session cookies from it). The page also takes the login cookies out of whatever else you paste: a request copied as cURL (bash, cmd or PowerShell), the request headers, the `cookie` header's value, or a cookie export (JSON or cookies.txt), and says what's wrong when they aren't there. MyFitnessPal needs its `remember_me` cookie, which is what keeps you signed in. The [MyFitnessPal MCP](https://github.com/Mason-Levyy/myfitnesspal-mcp) checks it and saves it in your own folder. For some accounts that sign in by email, MyFitnessPal won't say whose cookie it is: the page then asks for your MyFitnessPal username (on your profile, not your email) and connects again with it. The dashboard then reads and changes your diary itself, keeping the login between requests, so a day loads in under a second; when MyFitnessPal stops accepting the login, the MCP renews it in its headless browser (if set up).
- The server syncs every 10 minutes, when the page is opened with stale data, and with **↻ Sync**. The result is cached in `data/mfp.json`.
- **Log food:** **+ Log food** (or **+ Add to …** under a meal) searches MyFitnessPal's food database. Pick a result, then the number of servings, the serving size and the meal; a preview shows the calories and macros before you add it.
- **Your foods:** the food panel lists the foods you've added from the dashboard and your MyFitnessPal saved meals, narrowed while you type. Sort them by **Most eaten (30 days)** or **Recent**; picking one starts from the amount and serving you used last time. **✕** takes a food off the list (the diary keeps it). Press **Search** to look in MyFitnessPal's database instead.
- **Saved meals:** **Save as meal** under a meal saves its foods as one named meal in MyFitnessPal (the phone app has it too). Log it again from **+ Log food** like any food.
- **Edit or remove:** open a meal and use ✎ to change the amount, serving size or meal (MyFitnessPal's own edit form) or ✕ to remove a food. Changes go straight to your MyFitnessPal diary for the day shown.
- Weigh-ins in MyFitnessPal are copied into the weight log (and tick "Weigh in"). A weight logged on the dashboard is sent to MyFitnessPal too while the **MyFitnessPal** box next to **Log** is ticked (it shows once MyFitnessPal is connected, and remembers your choice).
- Goals are read from MyFitnessPal; change them there. If MyFitnessPal can't be reached, the card falls back to the goals in `plan.json`.
- If the login expires and the automatic refresh can't renew it, connect again on the Account page (**Reconnect or switch accounts**).

## Lifting tab

- **Routines** are made of days (Push, Pull, Legs…), each a list of exercises with sets, target reps (`10`, `8-12`, `max`) and rest in seconds. **+ New routine** or **Edit** opens the editor; ↑ ↓ reorder days and exercises. Under each exercise:
  - **Or instead** lists exercises that can take its place (`Cable row, DB row`): they're one tap away in the workout.
  - **Hold** makes it timed (plank, dead hang): Reps is then its seconds (`45`, `1:30`), and each set gets a hold timer. A target like `45 s` turns this on by itself.
  - **Optional** folds it in the workout: open it with **Do it** if you have time, or leave it.
  - **Superset with next** pairs it with the exercise below: you do a set of each back to back, and the rest timer starts after the last one. Pair several in a row for a giant set. The first time you open the tab, the training sessions in your plan (Today's training) are copied in as a routine called "My plan".
- **A day can tick a task:** type the task's name in **Task it ticks** (it suggests your pending tasks). That task then gets a **Start workout** button on Today's due tasks, and finishing the workout marks it done.
- **During a workout** each exercise shows its sets, filled in from the last time you did it (set by set), or the target reps the first time. Change the weight or reps and tick ✓ to log a set: that starts the rest timer at the top (−15s, +30s, Skip), which chimes softly (and buzzes, on a phone) when it's over. The next set to do is highlighted. For a hold, ▶ starts its timer (−15s, +15s, **Done**, **Cancel**): it chimes and logs the set when it runs out, and **Done** (or ■ on the set) logs the time held so far; ✎ has **Log** to enter seconds without the timer. Added time shows on a timer at once. For an exercise you haven't done before, logging a set fills its weight and reps into the sets after it (not ones you've typed in); with a last session, the sets keep last time's numbers. Each exercise's heading shows its estimated one-rep max (**Est.**, from its best set) as you type, next to your best so far; it turns into **PR** when a set you ticked beats it, and that set's number becomes **PR**. Beside it, **1RM** is your actual one-rep max and the goal, if you set one. A ticked set of one rep is a single: its number becomes **1RM**, or **GOAL** when it reaches the goal. **✎** on a set opens its note, **Skip set** (the note says why) and ✕ to remove it. **⤼** skips a whole exercise, with an optional reason; **Undo** brings either back. **⇄** swaps an exercise for one of its choices or any other (it suggests ones you've done), with an optional reason: its sets are filled in from the new one's last time, sets you already did stay with the first one, and the history shows "instead of" and why. Swapping back to the planned exercise undoes it. **⛓** pairs an exercise with the next one as a superset (or unpairs it) for this workout. Skipped sets and exercises are kept with the workout and show in its history with the reason. The box under the exercises is a note for the whole workout. Add sets, add exercises (it suggests ones you've done), reorder or remove them. What you type is kept, so you can close the page mid-workout.
- **Finish workout** asks how long it took (the clock so far) and for the workout's note, then saves the sets you ticked. If the day's task is due it's ticked, and the time is logged to it in Timewarrior; otherwise the time is logged as `Lifting` plus the workout's name. **Discard** throws the workout away.
- **PR** marks a set that beats your best estimated one-rep max for that exercise (Epley: weight × (1 + reps / 30)). **Progress** shows one exercise: its actual 1RM, best estimated 1RM, heaviest weight, a chart per session and the last sessions (a session with a single shows that instead of the estimate).
- **Actual 1RM and goals:** a set of exactly one rep is an actual one-rep max, never estimated: your heaviest single is the lift's **actual 1RM**. Set a **1RM goal** for a lift on its Progress card (blank clears it). Only a single at that weight or more reaches it; a set of 5 that estimates higher doesn't. Finishing the workout that first reaches it says so.
- **MyFitnessPal:** once MyFitnessPal is connected, **Finish workout** has **Also log in MyFitnessPal** (remembered). Each lift goes to that day's exercise diary as one strength row: the sets you did, at your heaviest set's weight and reps (MyFitnessPal keeps one weight per lift). The time goes in as "Strength training" cardio, with the calories MyFitnessPal works out for it. Holds aren't sent. The first time a lift is sent it's matched to MyFitnessPal's exercise of the same name (or its top search result); **MyFitnessPal matches** at the top of the tab shows the matches and changes them. A past workout that wasn't sent has **Send to MyFitnessPal**. Changing a workout after it's sent doesn't change MyFitnessPal. **Workouts** lists past workouts with their time, sets and volume; open one to see its sets, **Edit** it or **Delete** it.
- **Editing a past workout:** change its name, day, start time, how long it took, note, exercises (add, remove, reorder) and each set's weight, reps (or a hold's seconds) and note (✎). Sets without reps are dropped. PRs, progress and "last time" follow the change. It doesn't change the task it ticked or the time it logged: edit those on the Tasks and History tabs.
- **1RM calculator** (folded into the Strength card on Today): type one set, say 225 × 5, for its estimated one-rep max (Epley: 262.5; Brzycki, a little lower: 252.5) and a table of what that suggests for 1 to 15 reps. It takes one set's reps: how many sets you did doesn't change the estimate.
- **Strength card on Today:** a chart of your logged lifts, one line per lift: each session's best estimated one-rep max, or switch to **1RM** for your actual one-rep maxes (sessions with a single) or **Heaviest** for the heaviest weight you logged, over 3 months, 6 months, a year or everything. Hover or tap a point for the day, best set and estimate. **Choose lifts** picks up to 4 (by default your 3 most-done); a lift keeps its colour while it's shown. A lift with a 1RM goal gets a dashed line at it. Under the chart: each lift's latest number, its goal (and what's left to it, or ✓), its gain over the range, and your best in the range × your bodyweight (from your latest weigh-in). **Table** lists the same numbers. Your choices are saved.
- **Estimates are rounded to a weight you can load:** the nearest 2.5 lb (or 1.25 kg), everywhere a one-rep max or a weight for some reps is estimated. Logged weights are shown as you typed them.
- **Export (CSV)** at the top downloads every finished set, one row each, for a spreadsheet or another lifting app. A hold's seconds are in `hold_s`, and a swapped exercise has `instead_of` and `swap_reason`.
- **lb or kg:** the switch at the top changes how weights are shown and typed. They're kept in pounds, so switching back and forth loses nothing. A blank weight is a bodyweight set (BW).

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

A chat with an AI that knows your notes, plan and data and can use your MyFitnessPal diary, Anki cards and Taskwarrior tasks: ask about your food and plan, say what you ate and it logs it, have it make flashcards, or tell it to add, reschedule, finish or skip a task. It uses the [MyFitnessPal MCP](https://github.com/Mason-Levyy/myfitnesspal-mcp) and the [Anki MCP](https://github.com/ankimcp/anki-mcp-server), and the dashboard's own Taskwarrior MCP (`dashboard task-mcp`).

**Dashboard:** the AI can read and change your own dashboard, so you can just say "just did 225 bench for 5, log it", "skip stretching today, I'm sick", "move legs to Thursday" or "log 30 minutes of reading from 7pm". Its tools: `dashboard_overview` (the Today tab for a day), `checklist_set` (tick, skip with a reason, clear, or change a skip reason), `checklist_items`, `weight_log` (it follows the Weight card's MyFitnessPal box unless you say otherwise), `weight_plan`, `week_set`, `reminders_set`, `goals_set`, `lifting_overview`, `lifting_history`, `lifting_goal` (a lift's one-rep max goal), `lifting_log` (sets you did go into the workout in progress, else into one that finished in the last two hours, else a new "Quick log"), `lifting_routine_save`, `lifting_routine_delete`, `lifting_start`, and `time_list`, `time_track`, `time_start`, `time_stop`, `time_edit`, `time_delete` for Timewarrior. Every account has them (it's your own data); turn **Dashboard** off in the chat to hide them.

**Allow first:** the AI in your browser asks before every tool call that changes something (logging food, adding a card or task, ticking the checklist, saving a note…): a card under its reply shows what it wants to do, with **Allow**, **Allow all for this answer** and **Deny**. Tools that only read run without asking. Stop counts as Deny. The AIs on the server (Claude Code, Codex, Gemini CLI) don't ask yet.

**Tasks:** the Taskwarrior tools (`tasks_list`, `tasks_add`, `tasks_modify`, `tasks_done`, `tasks_reopen`, `tasks_start`, `tasks_stop`, `tasks_annotate`, `tasks_delete`) work on **your** tasks, the ones on the Tasks tab, with the same checks on every value as the tab. They never run an arbitrary `task` command, and there is no undo or import for the AI. Repeating schedules can only be changed on the Tasks tab; the AI can change or skip single copies. Deleting a task keeps why, as a note, so History shows it. Existing accounts get this tool when an admin ticks **Taskwarrior** under the account's MCPs on the Admin page (new accounts and the first admin have it from the start); each person can turn it off in the chat.

The AI runs **in your browser** (`web/js/assistant.js`): your browser talks to the AI directly, with your notes in its prompt, so they never pass through the dashboard's server. The exception is the AIs **on the server**, which you have to turn on (below).

- **Pick the AI** at the top (the ones your admin allows):
  - **Claude:** needs your Anthropic API key (`sk-ant-api…`; a Claude subscription token only works with Claude Code), added on the Account page.
  - **On the server: Claude Code, Codex, Gemini CLI.** These can't run in a browser. Turn on **AI on the server** on the Account page to get them (your admin must allow them too). Claude Code takes a Claude subscription token (`claude setup-token`, `sk-ant-oat…`) or an API key; Codex an OpenAI API key; Gemini CLI a Gemini API key. For each message your browser sends the server the message, your notes and the key; the CLI answers in your sandbox (`internal/web/serverai.go`) and the answer streams back. The server keeps none of it (the chat stays encrypted in your browser), but it sees it while the AI answers. The CLI's own record of the conversation, used to continue it, stays in your encrypted folder. If your admin lets the AI save notes, it gets them as files and your browser saves what it changed, asking before replacing a note. Without Docker, Codex and Gemini CLI are fetched with Nix the first time and Claude Code must be installed on the server; the Docker image comes with all three.
  - **Local model:** Ollama, LM Studio or llama.cpp (`llama-server`) running **on the same computer as your browser**, with the OpenAI-style API. Set its address on the Account page (`localhost:11434` for Ollama, `localhost:1234` for LM Studio, `localhost:8080` for llama.cpp), list its models and pick one. It must let the page reach it: start Ollama with `OLLAMA_ORIGINS=<the dashboard's address>`, turn on CORS in LM Studio; llama.cpp allows it already. The page can't reach a model elsewhere on your network unless an admin lists its server under **Model servers on your network** on the Admin page (then use its address, like `https://models.example.lan`). Browsers check HTTPS certificates themselves and the page can't skip that: for a self-signed or expired one, open the address once and accept the warning, or add your certificate authority to the browser. For testing, an admin can instead **Skip certificate checks**: the dashboard's server then passes the requests on to the https model servers without checking, so chats and notes go through it (each person is asked first, and the MCPs tab says "through the server"). The model needs to handle tool calls. With llama.cpp's router (several models behind one `llama-server`), the MCPs tab shows whether the model is in memory, offers **Load now** when it isn't, and says when an answer is waiting for it to load; with other servers it says so when the first words are slow to come.
  - Your keys and the local model's address are encrypted in your browser like your notes: the server never has them. (Keys saved on the server before are moved into your browser the first time you open the tab, then deleted there.)
- **The model menu** lists what your key can use, asked live: from Anthropic by your browser for Claude, from your local server for a local model, and by the server (with your key) for the AIs on the server. Claude Code's short names (Opus, Sonnet…) always come first.
- **Pick the MCPs** with the toggles. A green dot means it's ready; one that needs setup links to the Account page. **Attach prompt** adds the dashboard's instructions (how to answer, how to use the MCPs); your notes, plan and data go either way.
- **Saving notes:** if your admin allows it, the AI can save a note when you ask. It asks you before replacing one, and the old text stays in the note's history.
- The chat is encrypted in your browser like your notes, until **New chat**.

### Web search

**Web search** above the chat lets the AI search the web and read pages (off until you turn it on; an admin can turn it off for someone on the Admin page):

- **Claude** uses Anthropic's own web search and page reading, which run at Anthropic: your server never sees them. Anthropic charges your API key for each search (and your organization may need web search allowed in the Anthropic Console). Answers list the pages they cite.
- **A local model** has no search of its own, so the server searches for it with a [SearXNG](https://docs.searxng.org) the admin runs (Admin page → Web search) and reads the pages it picks, only public web pages, through the same rules as the tools' proxy below. Those searches and pages go through the server; your notes and the chat still don't. Without a SearXNG the model can still read pages you name.
- **The AIs on the server** use their own search (Claude Code's, Codex's, Gemini CLI's), through the proxy below.

For SearXNG, turn on its JSON format (`search.formats: [html, json]` in its `settings.yml`). With Docker, run it next to the dashboard and set the address to `http://searxng:8080`.

### What the tools can reach

The AI itself runs in your browser. The MCP tools it calls run on the server, each in a sandbox ([bubblewrap](https://github.com/containers/bubblewrap)):

- It sees an empty home folder and an empty workspace: none of your files, not the dashboard's, not other people's, not the server account's logins.
- It has **no network of its own**. Its only way out is the dashboard's proxy, which reaches public internet addresses on the web ports (80/443) and never the server itself, the local network or cloud metadata.
- The MyFitnessPal MCP uses **your** MyFitnessPal login; the Anki MCP reaches **your** Anki through the dashboard's Anki gateway, which refuses media from URLs or files and links, scripts or remote content in cards. So planted text in a shared deck or food entry can't use Anki to fetch or leak anything. Foods it creates in MyFitnessPal are private.
- The Taskwarrior MCP is the dashboard's own program, started in the sandbox with nothing but a unix socket to **your** tasks: your Taskwarrior data, taskrc and the `task` program are not inside the sandbox, and the socket only offers the tools above.
- The MCP programs are installed once by the server (`site/data/tools/`, pinned versions) and mounted read-only: nothing is downloaded inside a sandbox. The tools stop after 10 idle minutes, and when your data locks.
- The server sees what goes to and from these tools (food searches and entries, cards), not your notes or the rest of the chat.

## Accounts and admins

- **First account:** the terminal prints `Create the first (admin) account: …/setup?code=…`. The setup code is also saved in `data/setup-code` (readable only by the server's owner) and deleted once the account exists, so nobody else can claim the server first. This account is the **host**: it keeps the server computer's own Taskwarrior, Timewarrior, MyFitnessPal login and Anki, and gets a one-time copy of the notes and data from before the dashboard had accounts.
- **Inviting people:** **Admin** (top right, admins only) → **Invite someone**: pick a username and role, and send the one-time link (it works once, for 7 days). They choose their own password. Each person gets their own plan, notes, checklist, weigh-ins, tasks, time log, MyFitnessPal, Anki connection, chats and keys, in `site/data/users/<id>/`.
- **Admins** manage accounts: password-reset links, turning an account off (logged out at once), making someone an admin, and deleting an account with everything in it (type the username to confirm). There's always at least one active admin, and an admin can't do these to their own account. Admins can't see anyone's data, and neither can whoever runs the server while it's locked (see **Encrypted data**).
- **What each person's AI may use:** on the Admin page, per person: which AIs (Claude, a local model on their own computer), which MCPs, and whether their AI may save notes. **New people start with** sets the default. **Model servers on your network** lists the model servers, besides each person's own computer, that browsers may talk to for the local model.
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
- **The AI runs in your browser** (MCPs tab), so your notes go straight to the AI (Anthropic, or a model on your computer) and never through the server. Only if you turn on **AI on the server** (Account page) do your messages, notes and key go to the server, while one of those AIs answers.
- **Everything else** (plan, checklist, weight, tasks, time, food, logins) is encrypted on the server's disk (AES-256-GCM, file by file, names hidden) in `site/data/vaults/<id>/`, with a random key locked by your password's proof, your recovery code and each signed-in browser. Signed in, it's decrypted into memory (a tmpfs: `$XDG_RUNTIME_DIR/dashboard-vaults` or `/dev/shm`) for the server to use, since it builds your pages from it, and saved back every few seconds. It's locked again (saved, wiped from memory) when you log out of your last browser, after 30 minutes without a request, and when the server stops. After a restart, your next page load unlocks it without a new sign-in. The bar plugin's token keeps it unlocked while the plugin runs.
- **Your choice:** on the Account page you can keep that other data as plain files instead (**Stop encrypting it**): on a server whose disk is already encrypted and that only you use, the encryption adds little and losing both your password and recovery code would lose that data. **Encrypt it** turns it back on, with a new recovery code. Notes, chat and keys stay end to end either way.
- **Recovery code:** made by your browser and shown once (your first sign-in, or joining). It's the only way back in if you forget your password: an admin's reset link keeps your data only if you enter it. Make a new one on the Account page.
- **Older accounts** sign in once more with their password (to move to proofs and get their notes key); their data is encrypted then. The Admin page shows who isn't encrypted, and who chose plain files.
- **Needs HTTPS** (or `localhost`): browsers only do this encryption on secure pages.
- **What it doesn't stop:** whoever runs the server could change the page's code to catch passwords as they're typed (true of any website: see **Checking the page's code**), or read the non-note data in memory while you're signed in. The MyFitnessPal tools and sync go through the server, so it sees your food diary.

## Checking the page's code

Your notes are only as private as the scripts that handle them, and those come from the server. So the dashboard tells your browser the exact hash of each script it loads (Subresource Integrity), and a changed script won't run. To check that the server is running the published code, open the page's source (Ctrl+U) and compare each script's `integrity` value with this list from the repository (updated automatically on every push):

| Script | Hash |
|---|---|
| `app.js` | `sha384-pm5KsYFE+ac+hV7zNxgfU95LNQ1vG2ChChTvgucaXuQTupYX6Ny5qO3vdviv7dm6` |
| `keys.js` | `sha384-nwe1d2+ABaJt3Q/12zr1QRW8BolKLItadMXGeqOorCsyP4qfCfXFCb1JdeSaVU2g` |
| `notes.js` | `sha384-AfMYqfyrh1ctxfqOKdVHMa363w7VG3KevRVtPk4h/8s2lM91Imr4obUH9Apa8X8o` |
| `backup.js` | `sha384-Y/pu3exPYWdYgVyNW/qbSljWfBE5nxJTGCj/X/XM4YXwnnbu3HvijZaMlMKshMIJ` |
| `assistant.js` | `sha384-iXt2rEZ2/SiuX3KVvMLjVvJH2CjmcCA2Y63Klv3PQPXAR+8GjsVsGUTeyG3b3q/c` |
| `htmx.js` | `sha384-HGfztofotfshcF7+8n44JQL2oJmowVChPTg48S+jvZoztPfvwD79OC/LTtG6dMp+` |
| `htmx-sse.js` | `sha384-fw+eTlCc7suMV/1w/7fr2/PmwElUIt5i82bi+qTiLXvjRXZ2/FkiTNA/w0MhXnGI` |
| `marked.js` | `sha384-IvVdUXkhyoD0shbjhiiOnuiTbervTaB0VJcnEcg3shUAMVOOF6lKj4ozJrCwvz6s` |
| `purify.js` | `sha384-a7SzOxErzJ3ZpQz0zJ32d67dSitNzPcbfybc/ykU9KJhMgZkwqfSxlhhdJRS+XGL` |

## Your data

**Download it:** Account → **Data**, tick what you want (everything but your AI keys is ticked at first; tick one thing to download just that) and **Download (.zip)**. With everything ticked you get your plan, checklist history, weigh-ins (JSON and CSV), `lifting.json` and `lifting-sets.csv` (every set, one row each: date, workout, exercise, weight in lb and kg, reps, estimated 1RM, notes, skips and why), your Anki review history, and every Taskwarrior task (`task import tasks.json` loads them back) and Timewarrior entry, with a README saying what each file is. Your browser adds your notes (one Markdown file each) and AI chat, decrypted, and your AI keys if you tick them, so keep the file safe. Logins aren't in it, nor your food diary (MyFitnessPal has its own export). The Lifting tab's **Export (CSV)** downloads just the sets.

**Move it to another server:** sign up there, then Account → **Data** → **Import data** and pick the zip. The page lists what's in it, all ticked: untick what you don't want, to import one thing at a time. Your plan is replaced by the file's; everything else is added and nothing is deleted: checklist days and weigh-ins (the file's win for days both have), routines and workouts, tasks (`task import`, so ones already there are updated, not doubled), time entries (ones already there or overlapping are skipped), Anki history, notes (a note with the same title gets the file's text, its old text kept in its history), the chat (if there's none yet) and AI settings you haven't set. Importing the same file twice adds nothing. Your browser seals the notes, chat and keys again with your new account's key; the server never sees them. Behind nginx, allow the upload with `client_max_body_size` (above).

None of this is tracked by git:

- `site/data/users/<id>/`: one folder per person: `plan.json`, `docs/` (their notes, from `docs/templates/` on first use), checklist history, weigh-ins, routines and workouts (`lifting.json`), the MyFitnessPal cache and login, Anki connection, chat, sealed keys, and their Taskwarrior and Timewarrior (except the host's, which use the computer's own)
- `site/data/vaults/<id>/`: everyone else's folder, encrypted (see above); `site/data/users/<id>/` holds only the host's, and older accounts' until they're encrypted
- `site/data/`: accounts and sessions (hashes only), invites, the server's key, the installed MCP programs
- `site/.env`: local settings (see `site/.env.example`)
- `docs/*.md` and `site/plan.json` from before accounts stay as a backup after being copied to the host's folder

## Code

- `site/cmd/dashboard`: the Go server's entry point; `site/internal/` holds one package per area: `auth` (accounts, sessions, invites, policy), `workspace` (each user's folder and services), `taskwarrior`, `taskmcp` (the AI's Taskwarrior tools), `dashmcp` (its tools for the rest of the dashboard), `timewarrior`, `myfitnesspal`, `anki` (and its gateway), `lifting` (routines and workouts), `assistant` (what the server gives the AI in the browser, and the AI CLIs it runs for people who turn that on), `mcp` (starting MCP tools and talking to them), `sandbox` and `egress` (the tools' sandbox and its proxy), `vault` (encrypting each person's data), `web` and `api` (the HTTP handlers). Two dependencies: goldmark (Markdown) and golang.org/x/net/html (reading MyFitnessPal's pages). `nix run` builds it.
- `site/web/`: the pages. `templates/` has the HTML (one file per tab), rendered on the server and updated in place with [HTMX](https://htmx.org); styles are in `css/` (one file per area) and `js/app.js` does the few things HTMX can't (toasts, shortcuts, dragging cards, timed refreshes, the workout clocks); `js/keys.js`, `notes.js` and `assistant.js` handle what the server must never see (passwords, notes, the AI chat). No build step: edit a file and reload the page.
