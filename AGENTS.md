# Agent instructions

## Layout

- `site/`: the dashboard (Python server `server.py`, Nix flake). See `docs/dashboard.md`.
- `site/web/`: the page the server serves.
  - `index.html`: markup only. It loads the CSS and JS files below, in order.
  - `css/`: one file per area (`today.css`, `anki.css`, `chat.css`…), with `base.css` first (colour tokens for light and dark, layout) and `components.css` last (buttons, inputs, badges). When two rules match an element equally, the later file wins, so keep that order.
  - `js/`: plain scripts (not modules) that share one global scope and run in the order `index.html` lists them. `core.js` has the shared state and helpers; each tab has its own file; `main.js` boots the page. Top-level code may only use what earlier files define.
- `noctalia-plugin/`: the Noctalia bar plugin. See `docs/noctalia-plugin.md`.
- `docs/`: the user's own notes (`*.md`, git-ignored), the guides (`README.md`, `dashboard.md`, `noctalia-plugin.md`) and the starter notes in `docs/templates/`.

## Keep personal data out of git

This repo is public. Never commit the user's notes, plan, history or settings: `docs/*.md` (other than the guides), `site/plan.json`, `site/data/`, `site/.env`, `noctalia-plugin/settings.json`. They're git-ignored; don't force-add them, and don't copy anything from them into tracked files (examples, templates, tests, commit messages). Use made-up values in `site/plan.example.json` and `docs/templates/`.

## Use Nix for every package

The project assumes Nix, and tools like `python3`, `node` and `jq` may **not** be on the PATH. When you need a program, get it from a Nix shell:

- **Dashboard work (`site/`):** run commands inside the flake's dev shell, which provides `python3`, `timew` and `uv`:
  ```sh
  nix develop ./site --command python3 server.py 8080
  ```
- **One-off tools:** use a temporary shell:
  ```sh
  nix shell nixpkgs#python3 nixpkgs#jq --command jq . site/plan.example.json
  ```
- **A tool the project needs from now on:** add it to `packages` in `devShells.default` in `site/flake.nix` (or `runtimeInputs` if the server needs it at runtime). Don't reach for `pip install`, `npm -g`, `apt` or `nix-env -i`.

If a command fails with `command not found`, rerun it through `nix shell` instead of looking for another tool.

## Environment notes

- The user's shell may not be bash (for example nushell), so `&&`, `$(...)` and heredocs may not work there. When you need bash, wrap the command: `nix shell nixpkgs#bash --command bash -c '...'`, or put it in a script.
- Run the dashboard with `nix run` from `site/` (see `docs/dashboard.md`). The server restarts itself when `server.py` or `assistant.py` changes.
