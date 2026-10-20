# Daily Dashboard

A local web dashboard for daily habits: today's workout, a daily checklist (with skips and reasons), goals, weight, and full Taskwarrior + Timewarrior management.

- **Today:** training for the day, checklist with streaks, weight log, nutrition from MyFitnessPal, due tasks, time tracked
- **Tasks:** add, edit, complete (asks how long it took, default 30 min, and logs it in Timewarrior; also on past days), skip with reasons, repeating schedules (every N days, specific weekdays every N weeks, monthly), undo
- **History:** GitHub-style activity heatmap (tasks done, hours tracked, checklist), activity feed, time log editor, skip reasons
- **Docs:** renders the Markdown files in the parent folder

## Run

Needs Nix (flakes) and Taskwarrior 3.

```sh
nix run            # or ./run.sh — serves http://localhost:1628
nix run . -- 8080  # another port
nix develop        # shell with python3 + timew
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
- If the login expires and the automatic refresh can't renew it, run `uv run mfp-mcp auth` in the myfitnesspal-mcp folder.

## MCPs tab

A chat for your MyFitnessPal diary: ask about your food and plan, or say what you ate and it logs it. It uses the [MyFitnessPal MCP](https://github.com/Mason-Levyy/myfitnesspal-mcp), with your docs, `plan.json` and `data/` as context and read-only file access.

- **Pick the AI** at the top: Claude Code, Codex or Gemini CLI. Each one has to be signed in once in a terminal (`claude`, `codex login`, `gemini`). Codex and Gemini run through Nix if they aren't installed. Switching starts a new chat.
- **Set up the MyFitnessPal MCP** if the tab asks you to: `uvx --from 'mfp-mcp[autorefresh]' mfp-mcp auth`, then paste your MyFitnessPal session cookie (the MCP's README shows where to find it). A local checkout at `~/Documents/gitLibs/myfitnesspal-mcp` (or `MFP_MCP_DIR`) is used if present; otherwise the released package is used.
- The conversation is kept in `data/chat.json` until **New chat**. Each answer uses that AI's plan or API credit.

## Your data

Everything stays local. The server binds to 127.0.0.1 only and rejects cross-site requests.

- `plan.json`: your goals, checklist, routines and reminders (created from `plan.example.json` on first run; edit it from the page)
- `data/`: checklist history and weigh-ins

Both are git-ignored.
