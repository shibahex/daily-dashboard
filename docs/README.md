# Daily Dashboard

**Your food, workouts, tasks and flashcards in one place, with MCP support that can manage all of it.**

<img src="screenshots/mashup.png" alt="The Today, Tasks, Anki, History, MCPs and Docs tabs">

A private, self-hosted dashboard for getting healthier. Check in once a day, see what's left, and ask the AI to log a meal, quiz you on your Anki flashcards, manage all your Anki flashcards, or suggest what to eat tonight. It runs on your own computer or server, for you or for several people. Every account is separate, and nobody sees anyone else's data.

- **Today:** workout, checklist with streaks, weight trend, calories and macros from MyFitnessPal (log and edit foods right there), tasks due, time tracked. Drag the cards to arrange them.
- **Tasks:** [Taskwarrior](https://taskwarrior.org) tasks with repeating schedules, skips with reasons, and time tracking in [Timewarrior](https://timewarrior.net).
- **Lifting:** routines with days of exercises, a workout logger that fills in last time's weights, a rest timer, notes per set, each set's estimated 1RM as you type, PRs and progress charts. A Strength card on Today charts your lifts over time; you pick which. Finishing a workout can tick its task and log the time.
- **Your data is yours:** one click downloads all of it (plan, checklist, weigh-ins, lifting as JSON and CSV, tasks, time) in open formats.
- **History:** activity heatmap, activity feed, time log.
- **Docs:** your own notes (goals, nutrition plan, workout plan…), which the AI can also read.
- **Anki:** decks and due counts, add cards, search and edit notes, plus an Anki card on Today and your review history in History (through the AnkiConnect add-on).
- **MCPs:** chat with Claude Code, Codex or Gemini CLI through the [MyFitnessPal](https://github.com/Mason-Levyy/myfitnesspal-mcp) and [Anki](https://github.com/ankimcp/anki-mcp-server) MCPs: "what should I eat tonight?", "log 2 eggs for breakfast", "make flashcards from my notes".
- **Noctalia bar plugin** (optional): checklist, due tasks and a task timer on the [Noctalia](https://github.com/noctalia-dev/noctalia-shell) bar.

More screenshots in [`screenshots/`](screenshots/).

## Why it works

MyFitnessPal, Taskwarrior and Anki each cover one part of your day. Here they share one screen, so a single check-in replaces three apps.

- **One daily check.** Workout, checklist, remaining macros, tasks due and Anki cards due are all on Today.
- **One history.** Food, tasks, skips, tracked time and flashcard reviews are collected together, so a day or a week is easy to look back on.
- **Skips come with context.** Any task can be skipped, but every skip asks why: sick, traveling, no time, didn't feel like it, or your own words. Skip from the Tasks tab or the Today card, and a repeating task rolls on to its next occurrence. The reason is saved and shows in the History feed and heatmap next to what you completed.
- **An AI that can review it.** With an AI key set, it reads the same data, skip reasons included. Ask "what should I eat tonight?" and it works from your remaining macros and today's workout. Ask "why was last week bad?" and it can go through your skips, food and reviews together.
- **Your plan next to your results.** Your goals and plans live in Docs, so you (and the AI) can compare what you meant to do with what you did.

Reasons are what make the data useful. Three skipped workouts that say "tired" point at sleep or food. Three that say "no time" point at your schedule. Without them, all you have is a broken streak, and you and the AI are both guessing.

Each piece also works alone. MyFitnessPal and Anki take a few minutes to link (steps 3 and 4 in [Make it yours](#make-it-yours)).

## Why the AI support is built in
Tracking tools only help if you keep them up to date, and that upkeep can kill the habit. The AI takes the busywork so you can spend your time on the habits themselves. Also since all this data exists in one dashboard, it can help you review your routine and plan what to change next.

- **Turn conversations into actions.** "Log 2 eggs and toast for breakfast" goes into MyFitnessPal without you opening the app or hunting through its food database, or talk about starting a new plan with target macros and the MCP will make a new plan for you.
- **Cards without the chore.** Point it at your notes and it drafts flashcards into Anki. Ask it to find, fix or tidy up existing cards, and it edits them for you. Or ask about existing decks and manage them for you.
- **Better answers when you give it context.** Your tasks, your plan and the notes you write about your goals and routine are also there for the AI to draw on, so you get advice that fits your week instead of generic tips.
- **Less time in the tools.** Spend less time setting up tasks, setting up myfitness plans, and setting up productivity tools. Talk with the AI and let it do all the manual labor based on your goals and input.

Think of it as an assistant that keeps your tools tidy while you get on with the day. The dashboard works fine without it, and the AI is switched on by adding a key on the Account page (step 5 in [Make it yours](#make-it-yours)).

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

1. **Fill in your notes.** They're in the Docs tab (`overview.md` first); replace the placeholders. The AI uses them for context, and if your admin allows it, it can edit them for you.
2. **Edit your plan.** On the Today tab, use **Edit** on the training, checklist, weight and reminder cards.
3. **Connect MyFitnessPal** (optional, for nutrition and the AI): on the **Account** page, paste the Cookie header of a myfitnesspal.com request. The page says where to find it.
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

Unlike my other repos, this one was mostly written by AI just as a means to help me, but decided to open-source release it because I haven't seen a good productivity app that doesn't have a subscription.
