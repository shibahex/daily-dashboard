# Noctalia plugin

Puts the dashboard on the Noctalia bar.

- **Bar:** heartbeat icon with today's checklist progress (`done/total`) and how many tasks are due (`· 2`). Red when something is overdue, accent color with a ✓ when the day is complete. While a task is being timed, a clock with the elapsed time (`12:34`) shows next to it.
- **Left click:** panel with a task timer, today's checklist (tick, skip, add or edit a skip reason) and tasks due today (start/stop timer, done, skip with an optional reason). Done asks how long it took (default 30 min, e.g. `45m`, `1h 30m`) and logs it in Timewarrior; leave it blank to log nothing. Tasks timed with Start skip the question.
- **Timer:** works like the dashboard's Start/Stop buttons: it runs `task start`/`task stop`, and the dashboard's Taskwarrior hook logs the time in Timewarrior. Pick any pending task in the panel and press Start, or use ▶ on a due task. The panel shows the running task with a live clock and Stop, plus time tracked today and this week. Marking the task done also stops it.
- **Middle click:** opens the full dashboard (`chromium --app=…` by default, set in the plugin settings).
- **Right click:** stop the timer, open, refresh, start/restart the server, settings.

If the dashboard isn't reachable, the plugin starts it in the background (`nix run` in `site/`, the same as `./run.sh`). A server you started yourself in a terminal is used as is. A server the plugin started stops when the shell exits.

## Install

Symlink the plugin folder into Noctalia, from the repo's root folder:

```sh
ln -s "$PWD/noctalia-plugin" ~/.config/noctalia/plugins/health-dashboard
```

The plugin follows the symlink to find `site/`, so the repo can live anywhere. To use a different folder, set it in the plugin settings.

Restart Noctalia, then enable **Health Dashboard** under Settings → Plugins. It's added to the right side of the bar.

Then give it a token. The dashboard's API refuses requests without one. On the dashboard, open **Account** (top right), press **Make a token**, and paste the token into the plugin's settings under **API token**. Making a new token stops the old one from working.

IPC (for keybinds): `noctalia-shell ipc call plugin:health-dashboard toggle` (also `open`, `refresh`, `restart`, `stop` to stop the timer)
