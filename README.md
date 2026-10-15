# Daily Dashboard

A local web dashboard for daily habits: today's workout, a daily checklist (with skips and reasons), goals, weight, and full Taskwarrior + Timewarrior management.

- **Today:** training for the day, checklist with streaks, weight log, goals, due tasks, time tracked
- **Tasks:** add, edit, complete (including on past days), skip with reasons, repeating schedules (every N days, specific weekdays every N weeks, monthly), undo
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

## Your data

Everything stays local. The server binds to 127.0.0.1 only and rejects cross-site requests.

- `plan.json`: your goals, checklist, routines and reminders (created from `plan.example.json` on first run; edit it from the page)
- `data/`: checklist history and weigh-ins

Both are git-ignored.
