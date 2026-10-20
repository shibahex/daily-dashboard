"""The MCPs tab: chat with an AI coding agent that has the MyFitnessPal MCP server.

Each message runs one agent CLI headless in the background (Claude Code, Codex or Gemini CLI)
with only the MyFitnessPal server and read-only access to the health folder. Each CLI streams
JSON in its own format; `parse` turns those into the same few events for the page:
("session", id) · ("text", delta) · ("break",) · ("tool", id, name, input) · ("tool_error", id)
· ("error", message).
"""
import json
import os
import shutil
from pathlib import Path

PROVIDERS = {
    "claude": {"label": "Claude Code", "bin": "claude", "nix": None,
               "signin": "Run `claude` in a terminal once and sign in."},
    "codex": {"label": "Codex", "bin": "codex", "nix": "nixpkgs#codex",
              "signin": "Run `codex login` in a terminal, or set OPENAI_API_KEY."},
    "gemini": {"label": "Gemini CLI", "bin": "gemini", "nix": "nixpkgs#gemini-cli",
               "signin": "Run `gemini` in a terminal once and sign in with Google, or set GEMINI_API_KEY."},
}

PROMPT = """You are the MyFitnessPal assistant in the user's local health dashboard (its MCPs tab).
Today is {today}. Replies show in a small chat panel: keep them short and use plain Markdown.

Your main job is the user's MyFitnessPal diary, through the MyFitnessPal MCP tools (fitness_*), which
read and write the real diary. To log food, search first, prefer verified matches and foods the user
has logged before, then say exactly what you logged with calories and protein/carbs/fat. If the food or
amount is unclear, ask before logging. Use today's date unless the user says otherwise.

Context in {docs} (read what's relevant before giving advice, and base advice on it):
- README.md (overview), background.md, nutrition-plan.md (calorie/macro goals, cholesterol and sodium
  guidance, swaps), workout-plan.md, food-history.md, career.md
- site/plan.json: daily checklist, weekly training schedule, weight target
- site/data/weights.json (weigh-ins), site/data/checklist.json (checklist history)
- site/data/mfp.json: today's diary as the dashboard sees it, synced every 10 minutes, with every
  nutrient (fiber, sodium, sat fat, cholesterol, sugar...) per food and meal, plus the goals. Use it
  for nutrients the MCP's day summary doesn't include.
Don't change files or run commands that change anything."""

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


def provider_list():
    return [{"id": pid, "label": p["label"], "installed": bool(shutil.which(p["bin"])),
             "available": base_cmd(pid) is not None, "signin": p["signin"]} for pid, p in PROVIDERS.items()]


def build(pid, message, session_id, mcp_server, docs, workdir, today):
    """Returns (cmd, cwd, env) for one message. mcp_server is {command, args, env}."""
    base, env = base_cmd(pid), dict(os.environ)
    prompt = PROMPT.format(today=today, docs=docs)
    workdir.mkdir(parents=True, exist_ok=True)
    if pid == "claude":
        cfg = workdir / "claude-mcp.json"
        cfg.write_text(json.dumps({"mcpServers": {"myfitnesspal": {"type": "stdio", **mcp_server}}}))
        cmd = base + ["-p", message, "--output-format", "stream-json", "--verbose", "--include-partial-messages",
                      "--strict-mcp-config", "--mcp-config", str(cfg),
                      "--tools", "Read", "Glob", "Grep", "--allowedTools", "mcp__myfitnesspal", "Read", "Glob", "Grep",
                      "--append-system-prompt", prompt]
        return cmd + (["--resume", session_id] if session_id else []), docs, env
    # Codex and Gemini get the instructions with the first message of a conversation.
    text = message if session_id else f"{prompt}\n\n---\n\n{message}"
    if pid == "codex":
        toml = lambda v: json.dumps(v)  # JSON strings and string arrays are valid TOML
        cfg = ["-c", 'approval_policy="never"',
               "-c", f"mcp_servers.myfitnesspal.command={toml(mcp_server['command'])}",
               "-c", f"mcp_servers.myfitnesspal.args={toml(mcp_server['args'])}"]
        for k, v in (mcp_server.get("env") or {}).items():
            cfg += ["-c", f"mcp_servers.myfitnesspal.env.{k}={toml(v)}"]
        cmd = base + ["exec", "--json", "--skip-git-repo-check", "-C", str(docs), "-s", "read-only", *cfg]
        return cmd + (["resume", session_id, text] if session_id else [text]), docs, env
    if pid == "gemini":
        # A settings file the dashboard owns (system settings), so ~/.gemini is left alone. Folder
        # trust is off so the workdir's MCP config and auto-approval apply; only read-only
        # built-in tools are enabled.
        settings = workdir / "gemini-settings.json"
        settings.write_text(json.dumps({
            "security": {"folderTrust": {"enabled": False}},
            "tools": {"core": ["read_file", "read_many_files", "glob", "search_file_content", "list_directory"]},
            "mcpServers": {"myfitnesspal": mcp_server},
        }))
        env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"] = str(settings)
        cmd = base + ["-p", text, "-o", "stream-json", "--approval-mode", "yolo",
                      "--allowed-mcp-server-names", "myfitnesspal", "--include-directories", str(docs)]
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
    """mcp__myfitnesspal__fitness_log_food / fitness_log_food -> "log food"; read_file -> "read file"."""
    return name.rsplit("__", 1)[-1].removeprefix("fitness_").replace("_", " ").lower()


def tool_detail(inp):
    for k in ("query", "file_path", "absolute_path", "path", "pattern", "command", "name", "date"):
        if inp.get(k):
            v = str(inp[k])
            return (Path(v).name if k in ("file_path", "absolute_path", "path") else v)[:60]
    return ""


def changes_diary(name):
    short = name.rsplit("__", 1)[-1]
    return short.startswith("fitness_") and not short.removeprefix("fitness_").startswith(MFP_READ)
