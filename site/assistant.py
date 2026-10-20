"""The MCPs tab: chat with an AI coding agent that has the MyFitnessPal and/or Anki MCP servers.

Each message runs one agent CLI headless in the background (Claude Code, Codex or Gemini CLI)
with only the MCP servers turned on in the tab and read-only access to the health folder. Each CLI streams
JSON in its own format; `parse` turns those into the same few events for the page:
("session", id) · ("text", delta) · ("break",) · ("tool", id, name, input) · ("tool_error", id)
· ("error", message).
"""
import json
import os
import re
import shutil
import subprocess
import threading
import time
from pathlib import Path

# "models" are offered in the tab's model menu ("" = the CLI's own default); any other model name
# can be typed in with Custom.
PROVIDERS = {
    "claude": {"label": "Claude Code", "bin": "claude", "nix": None,
               "signin": "Run `claude` in a terminal once and sign in.",
               "models": [["opus", "Opus 5.5"], ["sonnet", "Sonnet 5.5"], ["fable", "Fable 5.1"], ["haiku", "Haiku 5.5"]]},
    "codex": {"label": "Codex", "bin": "codex", "nix": "nixpkgs#codex",
              "signin": "Run `codex login` in a terminal, or set OPENAI_API_KEY.",
              "models": [["gpt-5.5", "GPT-5.5"]]},
    "gemini": {"label": "Gemini CLI", "bin": "gemini", "nix": "nixpkgs#gemini-cli",
               "signin": "Run `gemini` in a terminal once and sign in with Google, or set GEMINI_API_KEY.",
               "models": [["gemini-3-pro-preview", "Gemini 3 Pro (preview)"], ["gemini-3-flash-preview", "Gemini 3 Flash (preview)"],
                          ["gemini-2.5-pro", "Gemini 2.5 Pro"], ["gemini-2.5-flash", "Gemini 2.5 Flash"],
                          ["gemini-3.1-flash-lite", "Gemini 3.1 Flash-Lite"]]},
}
MODEL_RE = re.compile(r"^[\w.:/-]{1,80}$")

# Claude Code reports the models its /model menu offers (aliases like "opus" plus older full
# names) in its SDK "initialize" handshake. That needs no API call; the list is cached and
# refreshed in the background, with PROVIDERS' list as the fallback.
_claude_models = {"list": None, "at": 0.0, "busy": False}
CLAUDE_MODELS_TTL = 6 * 3600


def fetch_claude_models(workdir):
    base = base_cmd("claude")
    if not base:
        return None
    req = json.dumps({"type": "control_request", "request_id": "models", "request": {"subtype": "initialize"}})
    workdir.mkdir(parents=True, exist_ok=True)
    try:
        p = subprocess.run(base + ["-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
                                   "--tools", ""], input=req + "\n", capture_output=True, text=True, timeout=60, cwd=workdir)
    except (subprocess.TimeoutExpired, OSError):
        return None
    for line in p.stdout.splitlines():
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        if ev.get("type") == "control_response":
            models = (ev.get("response", {}).get("response") or {}).get("models") or []
            # "default" is the CLI's own default, which the tab already offers as "Default model".
            return [[m["value"], m.get("displayName") or m["value"], m.get("description", "")]
                    for m in models if m.get("value") and m["value"] != "default"] or None
    return None


def claude_models(workdir):
    """The cached list, refreshing it in the background when it's missing or old."""
    c = _claude_models
    if not c["busy"] and time.time() - c["at"] > CLAUDE_MODELS_TTL:
        c["busy"] = True

        def refresh():
            try:
                c["list"] = fetch_claude_models(workdir) or c["list"]
                c["at"] = time.time()
            finally:
                c["busy"] = False
        threading.Thread(target=refresh, daemon=True).start()
    return c["list"] or PROVIDERS["claude"]["models"]

PROMPT = """You are the assistant in the user's local health dashboard (its MCPs tab).
Today is {today}. Replies show in a chat panel: keep them short and use plain Markdown.
{mcps}
Context in {docs} (read what's relevant before giving advice, and base advice on it):
- docs/*.md: the user's own notes, e.g. overview.md (who they are, targets), background.md,
  nutrition-plan.md, workout-plan.md, food-history.md, and anything else they added. Ignore
  docs/README.md, docs/dashboard.md and docs/noctalia-plugin.md (app guides) and docs/templates/.
- site/plan.json: daily checklist, weekly training schedule, weight target
- site/data/weights.json (weigh-ins), site/data/checklist.json (checklist history)
- site/data/mfp.json: today's MyFitnessPal diary as the dashboard sees it, synced every 10 minutes, with
  every nutrient (fiber, sodium, sat fat, cholesterol, sugar...) per food and meal, plus the goals. Use it
  for nutrients the MCP's day summary doesn't include.
Don't change files or run commands that change anything."""

MCP_PROMPTS = {
    "myfitnesspal": """MyFitnessPal: the fitness_* tools read and write the user's real diary. To log food, search
first, prefer verified matches and foods the user has logged before, then say exactly what you logged
with calories and protein/carbs/fat. If the food or amount is unclear, ask before logging. Use today's
date unless the user says otherwise.""",
    "anki": """Anki: the Anki tools manage the user's real Anki collection through AnkiConnect (Anki must be
open). For new cards: one fact per card, short clear questions, check for duplicates with findNotes
first, ask which deck if it isn't clear, then say what you added and to which deck. Only delete notes,
decks or note types when the user explicitly asks.""",
}

# MyFitnessPal tools that only read; any other fitness_* tool changed the diary, goals or weight.
MFP_READ = ("get_", "search", "list_", "draft_", "bulk_export")


def base_cmd(pid):
    """The CLI on the PATH, or (for ones nixpkgs has) `nix run`, which downloads it on first use."""
    p = PROVIDERS[pid]
    if shutil.which(p["bin"]):
        return [shutil.which(p["bin"])]
    if p["nix"] and shutil.which("nix"):
        return ["nix", "run", p["nix"], "--"]
    return None


def provider_list(workdir):
    return [{"id": pid, "label": p["label"], "installed": bool(shutil.which(p["bin"])),
             "available": base_cmd(pid) is not None, "signin": p["signin"],
             "models": claude_models(workdir) if pid == "claude" else p["models"]}
            for pid, p in PROVIDERS.items()]


def build(pid, message, session_id, servers, docs, workdir, today, model=""):
    """Returns (cmd, cwd, env) for one message. servers is {name: {command, args, env}}; model ""
    leaves the CLI's default."""
    base, env = base_cmd(pid), dict(os.environ)
    pick = ["--model", model] if model else []
    mcps = "\n".join(MCP_PROMPTS[n] + "\n" for n in servers) or "No MCP servers are turned on; you can only read the docs.\n"
    prompt = PROMPT.format(today=today, docs=docs, mcps="\n" + mcps)
    workdir.mkdir(parents=True, exist_ok=True)
    if pid == "claude":
        cfg = workdir / "claude-mcp.json"
        cfg.write_text(json.dumps({"mcpServers": {n: {"type": "stdio", **srv} for n, srv in servers.items()}}))
        cmd = base + ["-p", message, "--output-format", "stream-json", "--verbose", "--include-partial-messages",
                      "--strict-mcp-config", "--mcp-config", str(cfg),
                      "--tools", "Read", "Glob", "Grep", "--allowedTools", *(f"mcp__{n}" for n in servers), "Read", "Glob", "Grep",
                      "--append-system-prompt", prompt, *pick]
        return cmd + (["--resume", session_id] if session_id else []), docs, env
    # Codex and Gemini get the instructions with the first message of a conversation.
    text = message if session_id else f"{prompt}\n\n---\n\n{message}"
    if pid == "codex":
        toml = lambda v: json.dumps(v)  # JSON strings and string arrays are valid TOML
        cfg = ["-c", 'approval_policy="never"']
        for n, srv in servers.items():
            cfg += ["-c", f"mcp_servers.{n}.command={toml(srv['command'])}", "-c", f"mcp_servers.{n}.args={toml(srv['args'])}"]
            for k, v in (srv.get("env") or {}).items():
                cfg += ["-c", f"mcp_servers.{n}.env.{k}={toml(v)}"]
        cmd = base + ["exec", "--json", "--skip-git-repo-check", "-C", str(docs), "-s", "read-only", *pick, *cfg]
        return cmd + (["resume", session_id, text] if session_id else [text]), docs, env
    if pid == "gemini":
        # A settings file the dashboard owns (system settings), so ~/.gemini is left alone. Folder
        # trust is off so the workdir's MCP config and auto-approval apply; only read-only
        # built-in tools are enabled.
        settings = workdir / "gemini-settings.json"
        settings.write_text(json.dumps({
            "security": {"folderTrust": {"enabled": False}},
            "tools": {"core": ["read_file", "read_many_files", "glob", "search_file_content", "list_directory"]},
            "mcpServers": servers,
        }))
        env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"] = str(settings)
        cmd = base + ["-p", text, "-o", "stream-json", "--approval-mode", "yolo", *pick,
                      *(["--allowed-mcp-server-names", *servers] if servers else []), "--include-directories", str(docs)]
        # Gemini resumes by "latest" or index; this workdir only holds dashboard chats.
        return cmd + (["--resume", "latest"] if session_id else []), workdir, env
    raise ValueError(f"unknown AI {pid}")


def parse(pid, ev):
    """One JSON line from the CLI -> list of normalized events."""
    t = ev.get("type")
    out = []
    if pid == "claude":
        if t == "system" and ev.get("subtype") == "init":
            out.append(("session", ev.get("session_id")))
        elif t == "stream_event":
            d = ev.get("event", {}).get("delta", {})
            if d.get("type") == "text_delta":
                out.append(("text", d["text"]))
        elif t == "assistant":
            for b in ev.get("message", {}).get("content", []):
                if b.get("type") == "text":
                    out.append(("break",))
                elif b.get("type") == "tool_use":
                    out.append(("tool", b.get("id"), b["name"], b.get("input") or {}))
        elif t == "user":
            for b in ev.get("message", {}).get("content", []) or []:
                if isinstance(b, dict) and b.get("type") == "tool_result" and b.get("is_error"):
                    out.append(("tool_error", b.get("tool_use_id")))
        elif t == "result":
            out.append(("session", ev.get("session_id")))
            if ev.get("is_error"):
                out.append(("error", str(ev.get("result") or "failed")))
    elif pid == "codex":
        item = ev.get("item") or {}
        if t == "thread.started":
            out.append(("session", ev.get("thread_id")))
        elif t == "item.started" and item.get("type") == "mcp_tool_call":
            out.append(("tool", item.get("id"), item.get("tool", "tool"), item.get("arguments") or {}))
        elif t == "item.started" and item.get("type") == "command_execution":
            out.append(("tool", item.get("id"), "run", {"command": item.get("command", "")}))
        elif t == "item.completed":
            if item.get("type") == "agent_message" and item.get("text"):
                out += [("text", item["text"]), ("break",)]
            elif item.get("type") in ("mcp_tool_call", "command_execution") and (item.get("status") == "failed" or item.get("error")):
                out.append(("tool_error", item.get("id")))
        elif t == "turn.failed":
            out.append(("error", (ev.get("error") or {}).get("message", "failed")))
    elif pid == "gemini":
        if t == "init":
            out.append(("session", ev.get("session_id")))
        elif t == "message" and ev.get("role") == "assistant" and ev.get("content"):
            out.append(("text", ev["content"]))
        elif t == "tool_use":
            out.append(("tool", ev.get("tool_id"), ev.get("tool_name", "tool"), ev.get("parameters") or {}))
        elif t == "tool_result" and ev.get("status") != "success":
            out.append(("tool_error", ev.get("tool_id")))
        elif t == "result" and ev.get("status") != "success":
            out.append(("error", (ev.get("error") or {}).get("message", "failed")))
    return out


def friendly_error(pid, msg):
    """Point at signing in when the CLI isn't logged in."""
    low = msg.lower()
    if any(k in low for k in ("401", "unauthorized", "api key", "not logged in", "login", "auth", "credential")):
        return f"{PROVIDERS[pid]['label']} isn't signed in. {PROVIDERS[pid]['signin']}"
    return msg[:300]


def tool_label(name):
    """mcp__myfitnesspal__fitness_log_food -> "log food"; mcp__anki__addNote -> "add note"; read_file -> "read file"."""
    short = re.sub(r"(?<=[a-z])(?=[A-Z])", "_", name.rsplit("__", 1)[-1])
    return short.removeprefix("fitness_").replace("_", " ").lower()


def tool_detail(inp):
    note = inp.get("note") if isinstance(inp.get("note"), dict) else {}
    if note.get("deckName"):
        return str(note["deckName"])[:60]
    if isinstance(inp.get("notes"), list) and inp["notes"]:
        return f"{len(inp['notes'])} notes"
    for k in ("query", "deck", "deckName", "file_path", "absolute_path", "path", "pattern", "command", "name", "date"):
        if inp.get(k):
            v = str(inp[k])
            return (Path(v).name if k in ("file_path", "absolute_path", "path") else v)[:60]
    return ""


ANKI_READ = ("list", "find", "notes_info", "deck_stats", "get", "are", "model", "collection_stats",
             "review_stats", "present", "gui", "sync")


def changes_anki(name):
    """Anki write tools (add/update/delete...) mean the Anki tab should reload."""
    if "__" in name and not name.startswith("mcp__anki__"):
        return False
    short = re.sub(r"(?<=[a-z])(?=[A-Z])", "_", name.rsplit("__", 1)[-1]).lower()
    return not short.startswith("fitness_") and not short.startswith(ANKI_READ) and short not in (
        "read", "glob", "grep", "read_file", "read_many_files", "search_file_content", "list_directory", "run")


def changes_diary(name):
    short = name.rsplit("__", 1)[-1]
    return short.startswith("fitness_") and not short.removeprefix("fitness_").startswith(MFP_READ)
