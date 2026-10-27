# Agent instructions

## Layout

- `site/`: the dashboard: a Go server (`cmd/dashboard`, `internal/…`) and a Nix flake. See `docs/dashboard.md`.
  - `internal/` has one package per area: `store` (plan, checklist, weigh-ins, docs), `lifting` (routines and workouts), `taskwarrior`, `timewarrior`, `tasks` (task changes that touch both), `taskmcp` (the AI's Taskwarrior tools: `dashboard task-mcp` runs in its sandbox and talks to a gateway on a socket; add a tool in `tools.go` and `gateway.go`, going through `tasks.Service` so it gets the same checks as the Tasks tab), `dashmcp` (the AI's tools for the rest of the dashboard: Today, Lifting, Timewarrior; the browser's AI calls its gateway directly, an AI on the server runs `dashboard dashboard-mcp` in its sandbox; add a tool in `tools.go` and `gateway.go`, marking it `taskmcp.ReadOnly` only if it changes nothing, since the chat asks before running the others), `myfitnesspal`, `anki`, `assistant` (what the server gives the MCPs tab's AI, which runs in the browser, and the AI CLIs the server runs for people who opt in), `mcp` (starting MCP tools and calling them, for that AI), `websearch` (the web search and page reading the server does for local models, with the admin's SearXNG), `auth` (the account, sessions and the plugin's token; `Guard` wraps every route), `workspace` (each user's folder and services), `vault` (that folder encrypted on disk, unlocked into memory while they're signed in: keep any new per-user data inside the folder so it's encrypted too), `web` (the pages) and `api` (the small JSON API the Noctalia plugin uses). Dependencies are passed in through `web.UI` and `api.API`, not globals. Errors that should reach the page are `problem` errors, which carry their HTTP status; show errors with `problem.Public(err)`, which logs anything else and shows a general message, so paths and command output never reach a page.
  - `myfitnesspal` reads and changes the diary over HTTP itself (one kept login per user, requests sent at once); the MyFitnessPal MCP (Python, through `uv`) is only for connecting, renewing an expired login in its browser, and the AI's tools.
- `site/web/`: what the pages are made of. The server renders them with `html/template` and they update with [HTMX](https://htmx.org): every card, list and panel has its own URL, and a change answers with the new HTML plus `HX-Trigger` events (`tasks-changed`, `toast`…) that other cards listen for to reload themselves.
  - `templates/`: one file per tab (`today.html`, `tasks.html`…) plus `page.html` (the frame and shared pieces). They're re-read when they change, so editing one only needs a reload.
  - `css/`: one file per area (`today.css`, `anki.css`, `chat.css`…), with `base.css` first (colour tokens for light and dark, layout) and `components.css` last (buttons, inputs, badges). When two rules match an element equally, the later file wins, so keep that order.
  - `js/htmx.js`, `js/htmx-sse.js`, `js/marked.js`, `js/purify.js`: pinned copies of htmx 2.0.4, htmx-ext-sse 2.2.2, marked 18.1.0 and DOMPurify 3.4.16 (their integrity hashes are in `page.html`). The pages may only run scripts the dashboard serves, so don't load any from another site.
  - `js/app.js`: toasts, keyboard shortcuts, dragging Today's cards, the Lifting tab's clocks and remembering open meals. Keep page logic on the server; reach for JS only for what HTMX and CSS can't do.
  - `js/keys.js`, `js/notes.js`, `js/assistant.js` and `js/backup.js` (download and import with the notes, chat and AI keys): the exception, because the server must never see what they handle: the password stays in the browser (forms marked `data-keys` send proofs instead); everyone's notes, AI chat and AI keys are encrypted and shown there, and the MCPs tab's AI runs there, talking to Anthropic or a model on the user's own computer directly (the server only runs its MCP tools, `internal/web/browserai.go`). After changing one of the scripts in `web/js/`, its hash in `docs/dashboard.md` is updated by the Script hashes workflow on push (or run `go run ./cmd/scripthashes` in `site/`). Never send a password, a decrypted note or chat to the server, except to the AI on the server (`/assistant/run`) for people who turned it on (`User.ServerAI`), and through the model relay (`/assistant/model-relay`, `internal/web/modelrelay.go`) when an admin turned off certificate checks for testing and the person agreed.
- `noctalia-plugin/`: the Noctalia bar plugin. See `docs/noctalia-plugin.md`.
- `docs/`: the user's own notes (`*.md`, git-ignored), the guides (`README.md`, `dashboard.md`, `noctalia-plugin.md`) and the starter notes in `docs/templates/`.

## Keep personal data out of git

This repo is public. Never commit the user's notes, plan, history or settings: `docs/*.md` (other than the guides), `site/plan.json`, `site/data/`, `site/.env`, `noctalia-plugin/settings.json`. They're git-ignored; don't force-add them, and don't copy anything from them into tracked files (examples, templates, tests, commit messages). Use made-up values in `site/plan.example.json` and `docs/templates/`.

## Use Nix for every package

The project assumes Nix, and tools like `python3`, `node` and `jq` may **not** be on the PATH. When you need a program, get it from a Nix shell:

- **Dashboard work (`site/`):** run commands inside the flake's dev shell, which provides `go`, `timew` and `uv`:
  ```sh
  cd site && nix develop --command go run ./cmd/dashboard 8080
  ```
  Check changes with `go vet ./...` and `gofmt -l .` (both should print nothing).
- **One-off tools:** use a temporary shell:
  ```sh
  nix shell nixpkgs#python3 nixpkgs#jq --command jq . site/plan.example.json
  ```
- **A tool the project needs from now on:** add it to `packages` in `devShells.default` in `site/flake.nix` (or `runtimeInputs` if the server needs it at runtime). Don't reach for `pip install`, `npm -g`, `apt` or `nix-env -i`.

If a command fails with `command not found`, rerun it through `nix shell` instead of looking for another tool.

## Environment notes

- The user's shell may not be bash (for example nushell), so `&&`, `$(...)` and heredocs may not work there. When you need bash, wrap the command: `nix shell nixpkgs#bash --command bash -c '...'`, or put it in a script.
- Run the dashboard with `nix run` from `site/` (see `docs/dashboard.md`). After changing Go code, restart it so it rebuilds. New files must be `git add`ed before `nix run`/`nix build` can see them.
