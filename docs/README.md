# Health Dashboard

A local, private dashboard for getting healthier: your daily checklist and training, weight, what you've eaten (from MyFitnessPal), tasks and time tracking, and an AI you can ask about your food or tell what you ate. It runs on your own computer; your data never leaves it.

- **Today:** today's workout, a checklist with streaks, weight trend, calories and macros from MyFitnessPal (log and edit foods right there), tasks due, time tracked. Drag the cards to arrange them.
- **Tasks:** [Taskwarrior](https://taskwarrior.org) tasks with repeating schedules, skips with reasons and time tracking in [Timewarrior](https://timewarrior.net).
- **History:** activity heatmap, activity feed, time log.
- **Docs:** your own notes (goals, nutrition plan, workout plan…), which the AI also reads.
- **Anki:** your decks and due counts, add cards, search and edit notes, plus an Anki card on Today and your review history in History (through the AnkiConnect add-on).
- **MCPs:** chat with Claude Code, Codex or Gemini CLI through the [MyFitnessPal](https://github.com/Mason-Levyy/myfitnesspal-mcp) and [Anki](https://github.com/ankimcp/anki-mcp-server) MCPs: "what should I eat tonight?", "log 2 eggs for breakfast", "make flashcards from my notes".
- **Noctalia bar plugin** (optional): checklist, due tasks and a task timer on the [Noctalia](https://github.com/noctalia-dev/noctalia-shell) bar.

## Get started

You need [Nix](https://nixos.org/download) with flakes, and [Taskwarrior 3](https://taskwarrior.org/download/) for the Tasks tab.

```sh
git clone https://github.com/shibahex/daily-dashboard.git
cd daily-dashboard/site
nix run            # opens on http://localhost:1628
```

The terminal prints a link to create your account: a username and a password of at least 12 characters. The link includes a one-time setup code, so nobody else on your computer can create the account first. If you missed it, the code is also in `site/data/setup-code`.

On first run it creates your private files, which git ignores:

- `docs/*.md`: starter notes copied from `docs/templates/`
- `site/plan.json`: your checklist, weekly training and weight target, from `site/plan.example.json`

## Make it yours

1. **Fill in your notes.** Open the files in `docs/` (`overview.md` first) and replace the placeholders. They show in the Docs tab, and the AI uses them for advice. Add any other `.md` file you like.
2. **Edit your plan.** On the Today tab, use **Edit** on the training, checklist, weight and reminder cards.
3. **Connect MyFitnessPal** (optional, for nutrition and the MCPs tab). Sign in with the MyFitnessPal MCP once:
   ```sh
   uvx --from 'mfp-mcp[autorefresh]' mfp-mcp auth
   ```
   It asks for your MyFitnessPal session cookie; [its README](https://github.com/Mason-Levyy/myfitnesspal-mcp#authentication) shows where to find it. Then reload the dashboard.
4. **Connect Anki** (optional, for the Anki tab and the Anki MCP): in Anki, **Tools → Add-ons → Get Add-ons…**, code `2055492159` (AnkiConnect), then restart Anki.
5. **Sign in to an AI** (optional, for the MCPs tab): run `claude`, `codex login` or `gemini` once in a terminal.
6. **Local settings** (optional): copy `site/.env.example` to `site/.env` for things like a local MCP checkout or API keys.

## Keeping your data private

The dashboard only answers on your own computer (127.0.0.1), and every page needs you to be logged in, so other programs and other users on the computer can't read or change your data through it. Your files are readable only by you. See [Account and security](dashboard.md#account-and-security).

Everything personal is git-ignored, so you can pull updates or share your fork without publishing anything about yourself: `docs/*.md` (except the guides), `site/plan.json`, `site/data/` (history, weigh-ins, MyFitnessPal cache, chats) and `site/.env`. Only the app, the guides and the templates are tracked.

## More

- [Dashboard guide](dashboard.md): every feature, MyFitnessPal sync, the MCPs tab
- [Noctalia plugin](noctalia-plugin.md): install and use the bar plugin
- [AGENTS.md](../AGENTS.md): notes for AI coding agents working on this repo
