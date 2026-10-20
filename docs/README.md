# Health Dashboard

A private, self-hosted dashboard for getting healthier: your daily checklist and training, weight, what you've eaten (from MyFitnessPal), tasks and time tracking, and an AI you can ask about your food or tell what you ate. It runs on your own computer or server, for you alone or for several people: everyone has their own account and nobody sees anyone else's data.

- **Today:** today's workout, a checklist with streaks, weight trend, calories and macros from MyFitnessPal (log and edit foods right there), tasks due, time tracked. Drag the cards to arrange them.
- **Tasks:** [Taskwarrior](https://taskwarrior.org) tasks with repeating schedules, skips with reasons and time tracking in [Timewarrior](https://timewarrior.net).
- **History:** activity heatmap, activity feed, time log.
- **Docs:** your own notes (goals, nutrition plan, workout plan…), which the AI can also read.
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

The terminal prints a link to create the first account: a username and a password of at least 12 characters. The link includes a one-time setup code, so nobody else can claim the server first. If you missed it, the code is also in `site/data/setup-code`. The first account is an admin, and the **host**: it uses this computer's own Taskwarrior, MyFitnessPal login and Anki.

Each account gets its own private folder (`site/data/users/<id>/`, ignored by git) with starter notes from `docs/templates/` and a plan from `site/plan.example.json`.

## Make it yours

1. **Fill in your notes.** They're in the Docs tab (`overview.md` first); replace the placeholders. The AI uses them for advice, and if your admin allows it, it can edit them for you.
2. **Edit your plan.** On the Today tab, use **Edit** on the training, checklist, weight and reminder cards.
3. **Connect MyFitnessPal** (optional, for nutrition and the AI): on the **Account** page, paste your MyFitnessPal session token. The page says where to find it.
4. **Connect Anki** (optional, for the Anki tab and the AI): install the AnkiConnect add-on in Anki (**Tools → Add-ons → Get Add-ons…**, code `2055492159`) and restart Anki. On the host's computer that's all; everyone else also sets an address and API key in AnkiConnect's config and enters them on their Account page.
5. **Add an AI key** (optional, for the MCPs tab): on the Account page, an Anthropic, OpenAI or Google API key, or for Claude a subscription token from `claude setup-token`.
6. **Invite people** (optional): put the dashboard behind an HTTPS reverse proxy ([Sharing it on a network](dashboard.md#sharing-it-on-a-network)), then use **Admin → Invite someone**.
7. **Local settings** (optional): copy `site/.env.example` to `site/.env` for the network address, a local MCP checkout and similar.

## Keeping your data private

Every page needs you to be logged in, and every account only ever reaches its own folder: its plan, notes, tasks, food, Anki, chats and keys. Admins manage accounts but can't see anyone's data. The AI runs in a sandbox that sees only a copy of your own files (if allowed) and reaches only the public internet through the dashboard. See [Accounts and admins](dashboard.md#accounts-and-admins), [What the AI can reach](dashboard.md#what-the-ai-can-reach) and [Security](dashboard.md#security).

Everything personal is git-ignored, so you can pull updates or share your fork without publishing anything about yourself: `site/data/` (accounts, everyone's folders), `site/.env`, and the older `docs/*.md` and `site/plan.json`. Only the app, the guides and the templates are tracked.

## More

- [Dashboard guide](dashboard.md): every feature, MyFitnessPal sync, the MCPs tab
- [Noctalia plugin](noctalia-plugin.md): install and use the bar plugin
- [AGENTS.md](../AGENTS.md): notes for AI coding agents working on this repo
