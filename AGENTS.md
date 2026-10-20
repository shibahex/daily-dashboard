# Agent instructions

## Layout

- `site/`: the dashboard: a Go server (`cmd/dashboard`, `internal/…`) and a Nix flake. See `docs/dashboard.md`.
  - `internal/` has one package per area: `store` (plan, checklist, weigh-ins, docs), `taskwarrior`, `timewarrior`, `tasks` (task changes that touch both), `myfitnesspal`, `anki`, `assistant` (the MCPs tab's AI agents), `web` (the pages) and `api` (the small JSON API the Noctalia plugin uses). Dependencies are passed in through `web.UI` and `api.API`, not globals. Errors that should reach the page are `problem` errors, which carry their HTTP status.
  - `mfp_fetch.py` stays Python: it runs inside the MyFitnessPal MCP's Python package.
- `site/web/`: what the pages are made of. The server renders them with `html/template` and they update with [HTMX](https://htmx.org): every card, list and panel has its own URL, and a change answers with the new HTML plus `HX-Trigger` events (`tasks-changed`, `toast`…) that other cards listen for to reload themselves.
  - `templates/`: one file per tab (`today.html`, `tasks.html`…) plus `page.html` (the frame and shared pieces). They're re-read when they change, so editing one only needs a reload.
  - `css/`: one file per area (`today.css`, `anki.css`, `chat.css`…), with `base.css` first (colour tokens for light and dark, layout) and `components.css` last (buttons, inputs, badges). When two rules match an element equally, the later file wins, so keep that order.
  - `js/app.js`: the only script: toasts, keyboard shortcuts, dragging Today's cards and remembering open meals. Keep page logic on the server; reach for JS only for what HTMX and CSS can't do.
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
