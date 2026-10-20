#!/usr/bin/env python3
"""Local health dashboard: serves your docs and editable plan, wraps Taskwarrior/Timewarrior and
syncs MyFitnessPal.

Binds to 127.0.0.1 only. Writes require a JSON body and an X-Dashboard header,
so other websites open in the browser can't trigger them.
Restarts itself when this file changes, so edits never leave a stale server running.
"""
import json
import os
import re
import shutil
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from datetime import date, datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import assistant

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 1628
SITE = Path(__file__).resolve().parent
ROOT = SITE.parent
DOCS = ROOT / "docs"            # your Markdown docs (git-ignored), plus the guides
TEMPLATES = DOCS / "templates"  # starter docs, copied into docs/ when missing


def load_env(path):
    """KEY=value lines from site/.env (local paths and keys; see .env.example). Real
    environment variables win."""
    try:
        lines = path.read_text().splitlines()
    except FileNotFoundError:
        return
    for line in lines:
        key, sep, value = line.strip().partition("=")
        if sep and key and not key.startswith("#"):
            os.environ.setdefault(key.strip(), os.path.expanduser(value.strip().strip('"\'')))


load_env(SITE / ".env")
WEB = SITE / "web"             # the page: index.html, css/, js/
STATIC = re.compile(r"/(css|js)/([\w-]+)\.(css|js)")
STATIC_TYPES = {"css": "text/css; charset=utf-8", "js": "text/javascript; charset=utf-8"}
PLAN = SITE / "plan.json"
DATA = SITE / "data"
DATA.mkdir(exist_ok=True)
CHECKLIST = DATA / "checklist.json"
WEIGHTS = DATA / "weights.json"
ALLOWED_HOSTS = {f"127.0.0.1:{PORT}", f"localhost:{PORT}"}

TASK = ["task", "rc.confirmation=off", "rc.recurrence.confirmation=no", "rc.verbose=nothing"]
UUID_RE = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
PROJECT_RE = re.compile(r"^[\w.-]{1,40}$")
DUE_RE = re.compile(r"^[\w:+-]{1,30}$")
TAG_RE = re.compile(r"^\w{1,30}$")
DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
RECUR_RE = re.compile(r"^\w{1,20}$")
DATE_FIELDS = ("due", "scheduled", "wait")
PLAN_KEYS = {"goals", "weight", "checklist", "schedule", "sessions", "notes"}
CHECK_STATES = {"done", "skip"}
lock = threading.Lock()

# MyFitnessPal, through the MyFitnessPal MCP (mfp-mcp) and its saved login (see mfp_fetch.py).
# MFP_MCP_DIR points at a local checkout of it; otherwise uv fetches the released package.
MFP_DIR = Path(os.environ["MFP_MCP_DIR"]).expanduser() if os.environ.get("MFP_MCP_DIR") else None
MFP_PACKAGE = "mfp-mcp[autorefresh]"
MFP_REPO = "https://github.com/Mason-Levyy/myfitnesspal-mcp"
MFP_CACHE = DATA / "mfp.json"
MFP_EVERY = 600       # background sync, seconds
MFP_STALE = 300       # a page load older than this triggers a sync
mfp_lock = threading.Lock()
MFP_ID_RE = re.compile(r"^\d{1,20}$")


task_lock = threading.Lock()


def run_task(args, stdin_text=None):
    # stdin closed (or fed fixed input): if Taskwarrior ever asks a question, it fails instead of hanging.
    io = {"stdin": subprocess.DEVNULL} if stdin_text is None else {"input": stdin_text}
    # One at a time: every `task` run creates missing copies of repeating tasks, and two
    # running at once (parallel page requests) would both create the same copy.
    with task_lock:
        p = subprocess.run(TASK + args, capture_output=True, text=True, timeout=30, **io)
    return p.returncode, p.stdout, p.stderr


LOCAL_DT_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$")
TIMEW_WORDS = {"-", "from", "to", "for", "before", "after", "since", "until", "ago", "in", "on"}


def run_timew(args):
    """Run timew with :yes so it never prompts. Returns (code, stdout, stderr)."""
    try:
        p = subprocess.run(["timew", *args, ":yes"], capture_output=True, text=True, timeout=15, stdin=subprocess.DEVNULL)
    except FileNotFoundError:
        return 127, "", "Timewarrior isn't available (start the dashboard with nix run)"
    return p.returncode, p.stdout, p.stderr


def clean_tags(tags):
    """Timewarrior tags may contain spaces, but can't look like hints (:x), ids (@1) or range words."""
    if not isinstance(tags, list) or not tags or len(tags) > 10:
        raise ValueError("add at least one tag (usually the task name)")
    out = []
    for t in tags:
        t = " ".join(str(t).split())
        if not t or len(t) > 100 or t[0] in ":@" or t.lower() in TIMEW_WORDS:
            raise ValueError(f"bad tag: {t or '(empty)'}")
        out.append(t)
    return out


def find_interval(iid, start_was):
    """Timewarrior ids shift as entries are added, so confirm the id still points at the same entry."""
    code, out, _ = run_timew(["export"])
    for iv in json.loads(out or "[]") if code == 0 else []:
        if iv.get("id") == iid and iv.get("start") == start_was:
            return iv
    return None


def task_tags_for_timew(task):
    """The same tags the on-modify hook uses: description, project, task tags."""
    return [task["description"], *([task["project"]] if task.get("project") else []), *task.get("tags", [])]


def attr_args(fields):
    """Turn {project, priority, due, scheduled, wait, recur, tags_add, tags_remove} into task args.

    An empty string clears the attribute. Returns (series_args, date_args); raises ValueError.
    """
    series, dates = [], []
    if "project" in fields:
        v = str(fields["project"]).strip()
        if v and not PROJECT_RE.match(v):
            raise ValueError("bad project")
        series.append(f"project:{v}")
    if "priority" in fields:
        v = str(fields["priority"]).strip().upper()
        if v not in ("", "H", "M", "L"):
            raise ValueError("bad priority")
        series.append(f"priority:{v}")
    for key in DATE_FIELDS:
        if key in fields:
            v = str(fields[key]).strip()
            if v and not DUE_RE.match(v):
                raise ValueError(f"bad {key}")
            dates.append(f"{key}:{v}")
    if fields.get("recur"):
        v = str(fields["recur"]).strip()
        if not RECUR_RE.match(v):
            raise ValueError("bad recur")
        series.append(f"recur:{v}")
    for key, sign in (("tags_add", "+"), ("tags_remove", "-")):
        for tag in fields.get(key, []):
            if not TAG_RE.match(str(tag)):
                raise ValueError("bad tag")
            series.append(f"{sign}{tag}")
    return series, dates


def series_plan(rule):
    """Schedule rule -> [(first due date "YYYY-MM-DD", taskwarrior recur), ...].

    {"type": "days", "every": 2, "start": "2026-10-15"}            every 2 days
    {"type": "weekly", "days": [4], "every": 2, "start": ...}       every other Thursday (0 = Sunday)
    {"type": "monthly", "start": ...}                               same day each month
    The first date is never in the past, so no missed copies get created.
    """
    if not isinstance(rule, dict):
        raise ValueError("bad rule")
    today = date.today()
    try:
        start = date.fromisoformat(str(rule.get("start") or today.isoformat()))
    except ValueError:
        raise ValueError("bad start date")
    kind = rule.get("type")
    every = rule.get("every", 1)
    if not isinstance(every, int) or not 1 <= every <= 60:
        raise ValueError("bad interval")
    if kind == "days":
        while start < today:
            start += timedelta(days=every)
        return [(start.isoformat(), "daily" if every == 1 else f"{every}d")]
    if kind == "weekly":
        days = rule.get("days", [])
        if not days or not all(isinstance(d, int) and 0 <= d <= 6 for d in days) or every > 8:
            raise ValueError("pick at least one weekday")
        out = []
        for d in sorted(set(days)):
            first = start + timedelta(days=(d - (start.isoweekday() % 7)) % 7)
            while first < today:
                first += timedelta(weeks=every)
            out.append((first.isoformat(), "weekly" if every == 1 else f"{every}w"))
        return out
    if kind == "monthly":
        y, m = start.year, start.month
        while date(y, m, min(start.day, 28)) < today:
            y, m = (y + 1, 1) if m == 12 else (y, m + 1)
        return [(date(y, m, min(start.day, 28)).isoformat(), "monthly")]
    raise ValueError("bad rule type")


def get_task(uuid):
    code, out, _ = run_task([uuid, "export"])
    tasks = json.loads(out or "[]") if code == 0 else []
    return tasks[0] if tasks else None


def ensure_plan():
    """plan.json is personal and not in git; start from the example on first run."""
    if not PLAN.exists():
        PLAN.write_bytes((SITE / "plan.example.json").read_bytes())


def seed_docs():
    """First run: give a new user the starter docs to fill in. Never overwrites."""
    for t in TEMPLATES.glob("*.md"):
        if not (DOCS / t.name).exists():
            shutil.copyfile(t, DOCS / t.name)


def doc_names():
    return sorted(p.name for p in DOCS.glob("*.md"))


def read_json(path, default):
    try:
        return json.loads(path.read_text())
    except (FileNotFoundError, json.JSONDecodeError):
        return default


def write_json(path, data):
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(data, indent=1, ensure_ascii=False))
    tmp.replace(path)


def clean_check(v):
    """A checklist value: "done", "skip", or {"state": "skip", "note": "..."}. Older saves used true for done."""
    if v is True:
        return "done"
    if isinstance(v, str) and v in CHECK_STATES:
        return v
    if isinstance(v, dict) and v.get("state") == "skip":
        note = str(v.get("note", "")).strip()[:300]
        return {"state": "skip", "note": note} if note else "skip"
    return None


def uv_cmd():
    return ["uv"] if shutil.which("uv") else ["nix", "run", "nixpkgs#uv", "--"]


def mfp_local():
    return bool(MFP_DIR) and (MFP_DIR / "pyproject.toml").exists()


def mfp_env():
    """MFP_BROWSER_PATH: the browser the MCP's session refresh uses (needed on NixOS)."""
    return {"MFP_BROWSER_PATH": os.environ["MFP_BROWSER_PATH"]} if os.environ.get("MFP_BROWSER_PATH") else {}


def mfp_mcp_server():
    """How an AI agent starts the MyFitnessPal MCP server: {command, args, env}."""
    uv = uv_cmd()
    args = (["run", "--python", "3.12", "--managed-python", "--directory", str(MFP_DIR), "--extra", "autorefresh", "mfp-mcp"]
            if mfp_local() else ["tool", "run", "--python", "3.12", "--managed-python", "--from", MFP_PACKAGE, "mfp-mcp"])
    server = {"command": uv[0], "args": uv[1:] + args}
    if mfp_env():
        server["env"] = mfp_env()
    return server


# ---------- Anki ----------
# Through the AnkiConnect add-on, so Anki has to be open. The page can only call the actions
# listed here; the server adds a few of its own (status, overview, open).
ANKI_URL = os.environ.get("ANKI_CONNECT_URL", "http://127.0.0.1:8765")
ANKICONNECT = "2055492159"  # AnkiConnect's add-on code
ANKI_MCP = "@ankimcp/anki-mcp-server@0.27.0"
ANKI_ACTIONS = {
    "findNotes", "notesInfo", "areSuspended", "suspend", "unsuspend", "updateNoteFields", "updateNoteTags",
    "deleteNotes", "addNote", "canAddNotes", "modelFieldNames", "createDeck", "getTags", "sync",
    "guiDeckOverview", "guiBrowse", "cardsInfo",
}


def anki(action, **params):
    payload = {"action": action, "version": 6, "params": params}
    if os.environ.get("ANKI_CONNECT_API_KEY"):
        payload["key"] = os.environ["ANKI_CONNECT_API_KEY"]
    req = urllib.request.Request(ANKI_URL, json.dumps(payload).encode(), {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        body = json.load(r)
    if body.get("error"):
        raise RuntimeError(body["error"])
    return body.get("result")


def anki_running():
    return subprocess.run(["pgrep", "-x", r"anki|\.anki-wrapped"], capture_output=True).returncode == 0


def anki_status():
    try:
        return {"connected": True, "version": anki("version")}
    except (urllib.error.URLError, OSError, RuntimeError, ValueError):
        return {"connected": False, "installed": bool(shutil.which("anki")), "running": anki_running(),
                "addon": ANKICONNECT}


def anki_overview():
    """Everything the Anki tab shows at once: decks with due counts, reviews, note types."""
    decks = anki("deckNamesAndIds")
    stats = anki("getDeckStats", decks=list(decks))
    # getDeckStats only has each deck's last name part ("Japanese::Verbs" -> "Verbs"); use the full name.
    full = {str(did): name for name, did in decks.items()}
    return {
        "decks": sorted(({"name": full.get(did, st["name"]), "id": int(did), "new": st["new_count"], "learn": st["learn_count"],
                          "review": st["review_count"], "total": st["total_in_deck"]} for did, st in stats.items()),
                        key=lambda d: d["name"].lower()),
        "reviewed_today": anki("getNumCardsReviewedToday"),
        "by_day": anki("getNumCardsReviewedByDay")[:60],
        "models": anki("modelNames"),
    }


ANKI_HISTORY = DATA / "anki-history.json"


def anki_history():
    """Cards reviewed per day, all time: {"by_day": [["2026-10-20", 31], ...], "live": bool}.
    Saved on each read, so History still has it while Anki is closed."""
    try:
        days = anki("getNumCardsReviewedByDay")
        write_json(ANKI_HISTORY, {"by_day": days, "at": time.time()})
        return {"by_day": days, "live": True}
    except (urllib.error.URLError, OSError, RuntimeError, ValueError):
        saved = read_json(ANKI_HISTORY, {})
        return {"by_day": saved.get("by_day", []), "live": False, "at": saved.get("at")}


def anki_mcp_server():
    """The Anki MCP server for the AI agents. Needs Node 22.12+, so it runs on Nix's Node 22."""
    node = ["nix", "shell", "nixpkgs#nodejs_22", "--command", "npx"] if shutil.which("nix") else ["npx"]
    server = {"command": node[0], "args": node[1:] + ["-y", ANKI_MCP, "--stdio"], "env": {"ANKI_CONNECT_URL": ANKI_URL}}
    if os.environ.get("ANKI_CONNECT_API_KEY"):
        server["env"]["ANKI_CONNECT_API_KEY"] = os.environ["ANKI_CONNECT_API_KEY"]
    return server


def mcp_list(chat):
    """The MCP servers the MCPs tab can turn on, and whether each is ready to use."""
    mfp, ank = mfp_setup(), anki_status()
    on = chat.get("mcps", ["myfitnesspal", "anki"])
    return [
        {"id": "myfitnesspal", "label": "MyFitnessPal", "enabled": "myfitnesspal" in on, "ready": mfp["signed_in"], "setup": mfp},
        {"id": "anki", "label": "Anki", "enabled": "anki" in on, "ready": ank["connected"], "setup": ank},
    ]


def mfp_setup():
    """Is the MyFitnessPal MCP signed in? Its login lives in ~/.config/myfitnesspal-mcp."""
    cfg = Path(os.environ.get("XDG_CONFIG_HOME") or Path.home() / ".config") / "myfitnesspal-mcp" / "cookies.json"
    return {"source": "local" if mfp_local() else "package", "dir": str(MFP_DIR) if mfp_local() else None,
            "signed_in": bool(os.environ.get("MFP_COOKIE")) or cfg.exists(), "repo": MFP_REPO}


def run_mfp(req, timeout=150):
    """Run mfp_fetch.py with a request like {"op": "fetch"} where the MyFitnessPal MCP
    package is installed. Returns (data, error)."""
    if not mfp_setup()["signed_in"]:
        return None, "MyFitnessPal isn't connected yet. Set up the MyFitnessPal MCP (see the MCPs tab)."
    env = {**os.environ, **mfp_env()}
    where = (["--directory", str(MFP_DIR), "--extra", "autorefresh"] if mfp_local()
             else ["--no-project", "--with", MFP_PACKAGE])
    cmd = uv_cmd() + ["run", "--quiet", "--python", "3.12", "--managed-python", *where,
                      "python", str(SITE / "mfp_fetch.py"), json.dumps(req)]
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, stdin=subprocess.DEVNULL, env=env)
    except subprocess.TimeoutExpired:
        return None, "MyFitnessPal took too long to answer"
    except FileNotFoundError:
        return None, "uv isn't available (start the dashboard with nix run)"
    try:
        out = json.loads(p.stdout.strip().splitlines()[-1])
    except (IndexError, json.JSONDecodeError):
        return None, (p.stderr.strip().splitlines() or ["MyFitnessPal sync failed"])[-1]
    return (None, out["error"]) if "error" in out else (out, None)


def sync_mfp():
    """Fetch today's food, goals and weigh-ins; merge the weigh-ins into the weight log."""
    if not mfp_lock.acquire(blocking=False):
        with mfp_lock:  # one is already running: wait for it instead of starting another
            return read_json(MFP_CACHE, {})
    try:
        return store_mfp(*run_mfp({"op": "fetch", "date": date.today().isoformat()}))
    finally:
        mfp_lock.release()


def write_mfp(req):
    """A diary change (add/edit/delete food, weigh-in). mfp_fetch.py answers with the
    refreshed day, which becomes the cache. Returns (cache, error)."""
    with mfp_lock:
        data, err = run_mfp(req)
        return (store_mfp(data, None) if data else read_json(MFP_CACHE, {})), err


def store_mfp(data, err):
    cache = read_json(MFP_CACHE, {})
    if data:
        cache = {"data": data, "fetched_at": time.time(), "error": None}
        merge_mfp_weights(data.get("weights", []))
    else:
        cache = {**cache, "error": err, "failed_at": time.time()}
    write_json(MFP_CACHE, cache)
    return cache


def merge_mfp_weights(mfp_weights):
    """MyFitnessPal wins for days it has a weigh-in; days only logged here are kept.
    A weigh-in today also ticks the "Weigh in" checklist item."""
    today = date.today().isoformat()
    with lock:
        weights = {w["date"]: w for w in read_json(WEIGHTS, [])}
        for w in mfp_weights:
            weights[w["date"]] = {"date": w["date"], "lb": w["lb"]}
        write_json(WEIGHTS, sorted(weights.values(), key=lambda w: w["date"]))
        if today in {w["date"] for w in mfp_weights}:
            item = next((i for i in read_json(PLAN, {}).get("checklist", []) if "weigh" in i.lower()), None)
            days = read_json(CHECKLIST, {})
            if item and not days.get(today, {}).get(item):
                days.setdefault(today, {})[item] = "done"
                write_json(CHECKLIST, days)


def mfp_status():
    cache = read_json(MFP_CACHE, {})
    data = cache.get("data")
    stale = not data or data.get("day") != date.today().isoformat() or time.time() - cache.get("fetched_at", 0) > MFP_STALE
    return cache, stale


def mfp_loop():
    while True:
        try:
            sync_mfp()
        except Exception as e:  # never let the background thread die
            print("MyFitnessPal sync failed:", e, flush=True)
        time.sleep(MFP_EVERY)


# ---------- MCPs tab: chat with an AI agent ----------
# See assistant.py. One answer at a time; the conversation (which AI, its session id and the
# messages) is kept in data/chat.json until "New chat".
CHAT = DATA / "chat.json"
CHAT_DIR = DATA / "assistant"
chat_lock = threading.Lock()
chat_proc = None
chat_stopped = False  # Stop was pressed (CLIs exit with codes like 143 on SIGTERM, not a signal)


# Claude model names saved before the tab used Claude Code's own menu values.
OLD_CLAUDE_MODELS = {"claude-opus-5-5": "opus", "claude-sonnet-5-5": "sonnet", "claude-fable-5-1": "fable",
                     "claude-haiku-5-5": "haiku"}


def load_chat():
    chat = read_json(CHAT, {"messages": []})
    models = chat.get("models", {})
    if models.get("claude") in OLD_CLAUDE_MODELS:
        models["claude"] = OLD_CLAUDE_MODELS[models["claude"]]
    if chat.get("provider") not in assistant.PROVIDERS:
        chat["provider"] = next((p["id"] for p in assistant.provider_list(CHAT_DIR) if p["installed"]), "claude")
    return chat


def load_checklist():
    data = read_json(CHECKLIST, {})
    return {day: {k: c for k, v in items.items() if (c := clean_check(v))} for day, items in data.items()}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def send(self, code, body, ctype="application/json"):
        data = body if isinstance(body, bytes) else json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(data)

    def chat(self, message):
        """Stream the AI's answer as NDJSON lines: {type: text|break|tool|tool_error|done}."""
        global chat_proc, chat_stopped
        if not 0 < len(message) <= 4000:
            return self.send(400, {"error": "type a message"})
        chat = load_chat()
        pid = chat["provider"]
        if not assistant.base_cmd(pid):
            return self.send(400, {"error": f"{assistant.PROVIDERS[pid]['label']} isn't installed"})
        if not chat_lock.acquire(blocking=False):
            return self.send(409, {"error": "still answering the last message"})
        try:
            servers = {m["id"]: (mfp_mcp_server() if m["id"] == "myfitnesspal" else anki_mcp_server())
                       for m in mcp_list(chat) if m["enabled"]}
            model = chat.get("models", {}).get(pid, "")
            cmd, cwd, env = assistant.build(pid, message, chat.get("session_id"), servers, ROOT,
                                            CHAT_DIR, date.today().strftime("%A, %B %-d, %Y"), model)
            chat["messages"].append({"role": "user", "text": message, "at": time.time()})
            reply = {"role": "assistant", "provider": pid, "model": model, "text": "", "tools": [], "at": time.time()}

            self.send_response(200)
            self.send_header("Content-Type", "application/x-ndjson")
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            gone = False

            def emit(obj):
                nonlocal gone
                if gone:
                    return
                try:
                    self.wfile.write((json.dumps(obj) + "\n").encode())
                    self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    gone = True  # page closed: keep going so the answer is still saved

            chat_stopped = False
            chat_proc = subprocess.Popen(cmd, cwd=cwd, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                         stderr=subprocess.PIPE, text=True, bufsize=1)
            changed, anki_changed, error, tools, gap = False, False, None, {}, False
            for line in chat_proc.stdout:
                try:
                    ev = json.loads(line)
                except json.JSONDecodeError:
                    continue
                for kind, *a in assistant.parse(pid, ev):
                    if kind == "session" and a[0]:
                        chat["session_id"] = a[0]
                    elif kind == "text":
                        reply["text"] += ("\n\n" if gap and reply["text"] else "") + a[0]
                        gap = False
                        emit({"type": "text", "text": a[0]})
                    elif kind == "break":
                        gap = True
                        emit({"type": "break"})
                    elif kind == "tool":
                        tool = {"label": assistant.tool_label(a[1]), "detail": assistant.tool_detail(a[2])}
                        tools[a[0]] = tool
                        reply["tools"].append(tool)
                        changed = changed or assistant.changes_diary(a[1])
                        anki_changed = anki_changed or assistant.changes_anki(a[1])
                        emit({"type": "tool", **tool})
                    elif kind == "tool_error" and a[0] in tools:
                        tools[a[0]]["error"] = True
                        emit({"type": "tool_error", **tools[a[0]]})
                    elif kind == "error":
                        error = a[0]
            chat_proc.wait()
            err = chat_proc.stderr.read().strip()
            if chat_stopped or (chat_proc.returncode or 0) < 0:
                error = "Stopped"
            elif error or chat_proc.returncode:
                print(f"{pid} exited {chat_proc.returncode}: {err[-2000:]}", flush=True)
                if error or not reply["text"]:
                    error = assistant.friendly_error(pid, error or (err.splitlines() or [f"{assistant.PROVIDERS[pid]['label']} exited with code {chat_proc.returncode}"])[-1])
            if error:
                reply["error"] = error
            chat["messages"].append(reply)
            write_json(CHAT, chat)
            if changed:
                sync_mfp()  # the Today card shows the change right away
            emit({"type": "done", "changed": changed, "anki": anki_changed, "error": error})
        finally:
            chat_proc = None
            chat_lock.release()

    def host_ok(self):
        if self.headers.get("Host") not in ALLOWED_HOSTS:
            self.send(403, {"error": "bad host"})
            return False
        return True

    def do_GET(self):
        if not self.host_ok():
            return
        path = self.path.split("?", 1)[0]
        if path in ("/", "/index.html"):
            return self.send(200, (WEB / "index.html").read_bytes(), "text/html; charset=utf-8")
        m = STATIC.fullmatch(path)
        if m and m[1] == m[3] and (WEB / m[1] / f"{m[2]}.{m[3]}").is_file():
            return self.send(200, (WEB / m[1] / f"{m[2]}.{m[3]}").read_bytes(), STATIC_TYPES[m[3]])
        if path in ("/plan.json", "/api/plan"):
            ensure_plan()
            return self.send(200, PLAN.read_bytes())
        if path == "/api/docs":
            return self.send(200, doc_names())
        if path.startswith("/api/docs/"):
            name = path[len("/api/docs/"):]
            if name not in doc_names():
                return self.send(404, {"error": "no such doc"})
            return self.send(200, (DOCS / name).read_bytes(), "text/markdown; charset=utf-8")
        if path == "/api/tasks/all":
            # Every task ever (pending, waiting, completed, deleted, recurring templates) for history.
            code, out, err = run_task(["export"])
            if code != 0:
                return self.send(500, {"error": err.strip() or "task export failed"})
            return self.send(200, out.encode())
        if path == "/api/tasks":
            code, out, err = run_task(["status:pending", "export"])
            if code != 0:
                return self.send(500, {"error": err.strip() or "task export failed"})
            return self.send(200, out.encode())
        if path == "/api/checklist":
            return self.send(200, load_checklist())
        if path == "/api/weights":
            return self.send(200, read_json(WEIGHTS, []))
        if path == "/api/history":
            # Tasks skipped or dismissed from the dashboard (deleted, with a reason annotation).
            code, out, err = run_task(["status:deleted", "export"])
            if code != 0:
                return self.send(500, {"error": err.strip() or "task export failed"})
            skips = []
            for t in json.loads(out or "[]"):
                for a in t.get("annotations", []):
                    text = a.get("description", "")
                    if text.startswith(("Skipped", "Dismissed")):
                        skips.append({"description": t.get("description"), "project": t.get("project"), "due": t.get("due"),
                                      "kind": "dismiss" if text.startswith("Dismissed") else "skip",
                                      "note": text.split(":", 1)[1].strip() if ":" in text else "", "at": a.get("entry")})
            return self.send(200, {"skips": skips})
        if path == "/api/chat":
            chat = load_chat()
            return self.send(200, {**chat, "busy": chat_lock.locked(), "providers": assistant.provider_list(CHAT_DIR),
                                   "mcp": mfp_setup(), "mcps": mcp_list(chat)})
        if path == "/api/anki/status":
            return self.send(200, anki_status())
        if path == "/api/anki/history":
            return self.send(200, anki_history())
        if path == "/api/anki/overview":
            try:
                return self.send(200, anki_overview())
            except (urllib.error.URLError, OSError) as e:
                return self.send(502, {"error": f"Anki isn't reachable ({e}). Is Anki open with AnkiConnect installed?"})
            except RuntimeError as e:
                return self.send(502, {"error": str(e)})
        if path == "/api/mfp":
            # Cached so the page loads instantly; a stale cache refreshes in the background.
            cache, stale = mfp_status()
            if stale and not mfp_lock.locked():
                threading.Thread(target=sync_mfp, daemon=True).start()
            return self.send(200, {**cache, "syncing": stale or mfp_lock.locked()})
        if path == "/api/time":
            # Intervals since Monday (or the past year with ?range=year), from Timewarrior.
            # ?from=YYYY-MM-DD&to=YYYY-MM-DD picks any range (to is inclusive).
            query = dict(kv.split("=", 1) for kv in self.path.partition("?")[2].split("&") if "=" in kv)
            span = [":week"]
            if query.get("range") == "year":
                span = ["from", (date.today() - timedelta(days=371)).isoformat()]
            elif DATE_RE.match(query.get("from", "")) and DATE_RE.match(query.get("to", "")):
                span = ["from", query["from"], "to", (date.fromisoformat(query["to"]) + timedelta(days=1)).isoformat()]
            code, out, err = run_timew(["export", *span])
            if code == 127:
                return self.send(200, {"available": False, "intervals": []})
            if code != 0:
                return self.send(500, {"error": err.strip() or "timew export failed"})
            return self.send(200, {"available": True, "intervals": json.loads(out or "[]")})
        self.send(404, {"error": f"unknown endpoint {path}"})

    def do_POST(self):
        if not self.host_ok():
            return
        if self.headers.get("X-Dashboard") != "1" or not self.headers.get("Content-Type", "").startswith("application/json"):
            return self.send(403, {"error": "missing dashboard headers"})
        length = int(self.headers.get("Content-Length", 0))
        if length > 256_000:
            return self.send(413, {"error": "too large"})
        try:
            body = json.loads(self.rfile.read(length) or b"{}")
        except json.JSONDecodeError:
            return self.send(400, {"error": "bad json"})
        if not isinstance(body, dict):
            return self.send(400, {"error": "expected an object"})

        if self.path == "/api/tasks/add":
            desc = str(body.get("description", "")).strip()
            if not desc or len(desc) > 300:
                return self.send(400, {"error": "description required"})
            fields = {k: body[k] for k in ("project", "priority", "due", "scheduled", "wait", "recur") if str(body.get(k, "")).strip()}
            fields["tags_add"] = body.get("tags", [])
            if fields.get("recur") and not fields.get("due"):
                return self.send(400, {"error": "a repeating task needs a due date"})
            try:
                series, dates = attr_args(fields)
            except ValueError as e:
                return self.send(400, {"error": str(e)})
            code, out, err = run_task(["add"] + series + dates + ["--", desc])
            return self.send(200 if code == 0 else 500, {"ok": code == 0, "error": err.strip()})

        if self.path == "/api/tasks/modify":
            # scope "all" on a recurring copy: name/project/tags/priority go to the whole series.
            # Dates always change on this copy only (changing them on the series would give
            # every pending copy the same date).
            uuid = str(body.get("uuid", ""))
            fields = body.get("fields", {})
            if not UUID_RE.match(uuid) or not isinstance(fields, dict):
                return self.send(400, {"error": "bad request"})
            fields.pop("recur", None)
            end = fields.pop("end", None)
            try:
                series, dates = attr_args(fields)
            except ValueError as e:
                return self.send(400, {"error": str(e)})
            desc = " ".join(str(fields.get("description", "")).split())[:300] if "description" in fields else None
            if desc == "":
                return self.send(400, {"error": "description can't be empty"})
            task = get_task(uuid)
            if not task:
                return self.send(404, {"error": "task not found"})
            if end is not None:  # when a completed task was finished
                end = str(end).strip()
                if task.get("status") not in ("completed", "deleted") or not end or not DUE_RE.match(end):
                    return self.send(400, {"error": "can only set the completion date of a finished task"})
                dates.append(f"end:{end}")
            # A repeating task can be several series (e.g. Mon + Wed + Fri); the page sends all their templates.
            templates = [str(u) for u in body.get("templates", []) if UUID_RE.match(str(u))]
            if task.get("status") == "recurring" and uuid not in templates:
                templates.append(uuid)
            if not templates and task.get("parent"):
                templates = [task["parent"]]
            series_args = series + (["--", desc] if desc else [])
            steps = []
            if body.get("scope") == "all" and templates:
                for tpl in templates:
                    if series_args:
                        steps.append(["rc.recurrence.confirmation=yes", tpl, "modify"] + series_args)
                if dates and task.get("status") != "recurring":
                    steps.append([uuid, "modify"] + dates)
            elif series_args or dates:
                steps.append([uuid, "modify"] + series + dates + (["--", desc] if desc else []))
            for step in steps:
                code, out, err = run_task(step)
                if code != 0:
                    return self.send(500, {"ok": False, "error": err.strip()})
            return self.send(200, {"ok": True})

        if self.path == "/api/tasks/done_at":
            # Complete a task as of a past date/time; optionally log how long it took in Timewarrior.
            uuid, end, minutes = str(body.get("uuid", "")), str(body.get("end", "")), body.get("minutes")
            if not UUID_RE.match(uuid) or not LOCAL_DT_RE.match(end):
                return self.send(400, {"error": "bad request"})
            if minutes not in (None, "", 0) and not (isinstance(minutes, int) and 0 < minutes <= 24 * 60):
                return self.send(400, {"error": "minutes must be between 1 and 1440"})
            task = get_task(uuid)
            if not task:
                return self.send(404, {"error": "task not found"})
            code, out, err = run_task([uuid, "done", f"end:{end}"])
            if code != 0:
                return self.send(500, {"ok": False, "error": err.strip()})
            warning = ""
            if minutes:
                stop = datetime.fromisoformat(end)
                start = stop - timedelta(minutes=minutes)
                tcode, _, terr = run_timew(["track", start.strftime("%Y-%m-%dT%H:%M"), "-", stop.strftime("%Y-%m-%dT%H:%M"), *task_tags_for_timew(task)])
                if tcode != 0:
                    warning = "Completed, but the time wasn't logged: " + (terr.strip().splitlines() or ["timew error"])[0]
            return self.send(200, {"ok": True, "warning": warning})

        if self.path == "/api/time/track":
            start, end = str(body.get("start", "")), str(body.get("end", ""))
            if not LOCAL_DT_RE.match(start) or not LOCAL_DT_RE.match(end) or end <= start:
                return self.send(400, {"error": "end must be after start"})
            try:
                tags = clean_tags(body.get("tags"))
            except ValueError as e:
                return self.send(400, {"error": str(e)})
            code, out, err = run_timew(["track", start, "-", end, *tags])
            return self.send(200 if code == 0 else 409, {"ok": code == 0, "error": (err.strip().splitlines() or [""])[0]})

        if self.path in ("/api/time/modify", "/api/time/delete"):
            iid, start_was = body.get("id"), str(body.get("start_was", ""))
            if not isinstance(iid, int) or iid < 1 or not find_interval(iid, start_was):
                return self.send(409, {"error": "that entry changed since the page loaded; refresh and try again"})
            if self.path.endswith("delete"):
                code, out, err = run_timew(["delete", f"@{iid}"])
                return self.send(200 if code == 0 else 500, {"ok": code == 0, "error": err.strip()})
            steps = []
            if "tags" in body:
                try:
                    steps.append(["retag", f"@{iid}", *clean_tags(body["tags"])])
                except ValueError as e:
                    return self.send(400, {"error": str(e)})
            # Retagging and changing times don't shift ids, so @iid stays valid across the steps.
            for key in ("start", "end"):
                if key in body:
                    v = str(body[key])
                    if not LOCAL_DT_RE.match(v):
                        return self.send(400, {"error": f"bad {key}"})
                    steps.append(["modify", key, f"@{iid}", v])
            for step in steps:
                code, out, err = run_timew(step)
                if code != 0:
                    return self.send(409, {"ok": False, "error": (err.strip().splitlines() or ["timew error"])[0]})
            return self.send(200, {"ok": True})

        if self.path == "/api/series/schedule":
            # Replace a task's repeat schedule. Taskwarrior has one period per series, so
            # "Mon, Wed, Fri" becomes three weekly series with the same name.
            # New series are created first; the old ones (and their pending copies) are deleted after.
            source = str(body.get("source", ""))
            templates = [str(u) for u in body.get("templates", []) if UUID_RE.match(str(u))]
            if not UUID_RE.match(source):
                return self.send(400, {"error": "bad source"})
            task = get_task(source)
            if not task:
                return self.send(404, {"error": "task not found"})
            try:
                plan = series_plan(body.get("rule", {}))
            except ValueError as e:
                return self.send(400, {"error": str(e)})
            base = []
            if task.get("project"):
                base.append(f"project:{task['project']}")
            if task.get("priority"):
                base.append(f"priority:{task['priority']}")
            base += [f"+{t}" for t in task.get("tags", []) if TAG_RE.match(t)]
            created = []
            for due, recur in plan:
                code, out, err = run_task(["rc.verbose=new-uuid", "add", *base, f"due:{due}T23:59:59", f"recur:{recur}", "--", task["description"]])
                m = re.search(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}", out)
                if code != 0 or not m:
                    if created:  # roll back the half-made schedule
                        run_task(["rc.bulk=0", "rc.recurrence.confirmation=yes", *created, "delete"])
                    return self.send(500, {"ok": False, "error": err.strip() or "couldn't create the new schedule"})
                created.append(m.group(0))
            old = templates + ([] if task.get("parent") or task.get("status") == "recurring" else [source])
            if old:
                run_task(["rc.bulk=0", "rc.recurrence.confirmation=yes", *old, "delete"])
            return self.send(200, {"ok": True, "series": len(created)})

        if self.path == "/api/series/stop":
            # Stop repeating: delete the series templates and their pending copies.
            templates = [str(u) for u in body.get("templates", []) if UUID_RE.match(str(u))]
            if not templates:
                return self.send(400, {"error": "no series given"})
            code, out, err = run_task(["rc.bulk=0", "rc.recurrence.confirmation=yes", *templates, "delete"])
            return self.send(200 if code == 0 else 500, {"ok": code == 0, "error": err.strip()})

        if self.path in ("/api/tasks/annotate", "/api/tasks/denotate"):
            uuid, text = str(body.get("uuid", "")), " ".join(str(body.get("text", "")).split())[:500]
            if not UUID_RE.match(uuid) or not text:
                return self.send(400, {"error": "bad request"})
            code, out, err = run_task([uuid, self.path.rsplit("/", 1)[1], "--", text])
            return self.send(200 if code == 0 else 500, {"ok": code == 0, "error": err.strip()})

        if self.path == "/api/tasks/reopen":
            uuid = str(body.get("uuid", ""))
            if not UUID_RE.match(uuid):
                return self.send(400, {"error": "bad uuid"})
            code, out, err = run_task([uuid, "modify", "status:pending"])
            return self.send(200 if code == 0 else 500, {"ok": code == 0, "error": err.strip()})

        if self.path == "/api/tasks/undo":
            code, out, err = run_task(["undo"])
            return self.send(200 if code == 0 else 500, {"ok": code == 0, "error": err.strip()})

        if self.path in ("/api/tasks/done", "/api/tasks/start", "/api/tasks/stop"):
            uuid = str(body.get("uuid", ""))
            if not UUID_RE.match(uuid):
                return self.send(400, {"error": "bad uuid"})
            action = self.path.rsplit("/", 1)[1]
            code, out, err = run_task([uuid, action])
            return self.send(200 if code == 0 else 500, {"ok": code == 0, "error": err.strip()})

        if self.path == "/api/tasks/delete":
            uuids = body.get("uuids", [])
            if not isinstance(uuids, list) or not 0 < len(uuids) <= 500 or not all(UUID_RE.match(str(u)) for u in uuids):
                return self.send(400, {"error": "bad uuids"})
            code, out, err = run_task(["rc.bulk=0"] + [str(u) for u in uuids] + ["delete"])
            return self.send(200 if code == 0 else 500, {"ok": code == 0, "error": err.strip()})

        if self.path == "/api/tasks/skip":
            # Annotate with the reason first, then delete. Deleted tasks keep their
            # annotations, so `task status:deleted` (and the History tab) shows why.
            uuids = body.get("uuids", [])
            if not isinstance(uuids, list) or not 0 < len(uuids) <= 500 or not all(UUID_RE.match(str(u)) for u in uuids):
                return self.send(400, {"error": "bad uuids"})
            note = " ".join(str(body.get("note", "")).split())[:300]
            label = "Dismissed" if body.get("kind") == "dismiss" else "Skipped"
            uuids = [str(u) for u in uuids]
            code, out, err = run_task(["rc.bulk=0"] + uuids + ["annotate", "--", f"{label}: {note}" if note else label])
            if code == 0:
                code, out, err = run_task(["rc.bulk=0"] + uuids + ["delete"])
            return self.send(200 if code == 0 else 500, {"ok": code == 0, "error": err.strip()})

        if self.path == "/api/tasks/skip_note":
            # Change the reason on already skipped/dismissed tasks. Rewritten through
            # export/import so the annotation keeps its date (History groups by it).
            uuids = body.get("uuids", [])
            if not isinstance(uuids, list) or not 0 < len(uuids) <= 500 or not all(UUID_RE.match(str(u)) for u in uuids):
                return self.send(400, {"error": "bad uuids"})
            note = " ".join(str(body.get("note", "")).split())[:300]
            code, out, err = run_task([str(u) for u in uuids] + ["export"])
            if code != 0:
                return self.send(500, {"error": err.strip() or "task export failed"})
            changed = []
            for t in json.loads(out or "[]"):
                for a in t.get("annotations", []):
                    text = a.get("description", "")
                    if text.startswith(("Skipped", "Dismissed")):
                        label = "Dismissed" if text.startswith("Dismissed") else "Skipped"
                        a["description"] = f"{label}: {note}" if note else label
                        changed.append(t)
                        break
            if not changed:
                return self.send(400, {"error": "no skip reason found on those tasks"})
            code, out, err = run_task(["import", "-"], json.dumps(changed))
            return self.send(200 if code == 0 else 500, {"ok": code == 0, "error": err.strip()})

        if self.path == "/api/checklist":
            day = str(body.get("date", ""))
            items = body.get("items", {})
            if not DATE_RE.match(day) or not isinstance(items, dict):
                return self.send(400, {"error": "bad checklist"})
            with lock:
                data = load_checklist()
                data[day] = {str(k)[:80]: c for k, v in items.items() if (c := clean_check(v))}
                write_json(CHECKLIST, data)
            return self.send(200, {"ok": True})

        if self.path == "/api/plan":
            plan = body.get("plan")
            if not isinstance(plan, dict) or not PLAN_KEYS.issubset(plan):
                return self.send(400, {"error": "plan is missing sections"})
            with lock:
                write_json(PLAN, plan)
            return self.send(200, {"ok": True})

        if self.path == "/api/chat":
            return self.chat(str(body.get("message", "")).strip())
        if self.path == "/api/chat/new":
            if chat_lock.locked():
                return self.send(409, {"error": "wait for the current answer, or press Stop"})
            provider = str(body.get("provider") or load_chat()["provider"])
            if provider not in assistant.PROVIDERS:
                return self.send(400, {"error": "unknown AI"})
            old = load_chat()
            write_json(CHAT, {"provider": provider, "messages": [], "mcps": old.get("mcps", ["myfitnesspal", "anki"]),
                              "models": old.get("models", {})})
            return self.send(200, {"ok": True})
        if self.path == "/api/chat/model":
            # The model for the current AI ("" = its default). Applies from the next message, same chat.
            model = str(body.get("model", "")).strip()
            if model and not assistant.MODEL_RE.match(model):
                return self.send(400, {"error": "that doesn't look like a model name"})
            if chat_lock.locked():
                return self.send(409, {"error": "wait for the current answer"})
            chat = load_chat()
            chat.setdefault("models", {})[chat["provider"]] = model
            write_json(CHAT, chat)
            return self.send(200, {"ok": True})
        if self.path == "/api/chat/mcps":
            # Which MCP servers the AI gets. Takes effect from the next message.
            mcps = [m for m in body.get("mcps", []) if m in ("myfitnesspal", "anki")]
            if chat_lock.locked():
                return self.send(409, {"error": "wait for the current answer"})
            chat = load_chat()
            chat["mcps"] = mcps
            write_json(CHAT, chat)
            return self.send(200, {"ok": True, "mcps": mcp_list(chat)})

        if self.path == "/api/anki":
            action, params = str(body.get("action", "")), body.get("params") or {}
            if action not in ANKI_ACTIONS or not isinstance(params, dict):
                return self.send(400, {"error": f"action not allowed: {action}"})
            try:
                return self.send(200, {"ok": True, "result": anki(action, **params)})
            except (urllib.error.URLError, OSError) as e:
                return self.send(502, {"ok": False, "error": f"Anki isn't reachable ({e})"})
            except RuntimeError as e:
                return self.send(400, {"ok": False, "error": str(e)})
        if self.path == "/api/anki/open":
            if anki_running():
                return self.send(200, {"ok": True, "started": False})
            if not shutil.which("anki"):
                return self.send(400, {"error": "Anki isn't installed"})
            subprocess.Popen(["anki"], start_new_session=True, stdin=subprocess.DEVNULL,
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            return self.send(200, {"ok": True, "started": True})
        if self.path == "/api/chat/stop":
            global chat_stopped
            if chat_proc and chat_proc.poll() is None:
                chat_stopped = True
                chat_proc.terminate()
            return self.send(200, {"ok": True})

        if self.path == "/api/mfp/refresh":
            cache = sync_mfp()
            return self.send(200, {**cache, "syncing": False})

        if self.path in ("/api/mfp/search", "/api/mfp/entry"):
            # Read-only lookups for the food forms: search results, or one entry's edit options.
            if self.path.endswith("search"):
                q = " ".join(str(body.get("query", "")).split())
                if not 0 < len(q) <= 100:
                    return self.send(400, {"error": "type what to search for"})
                req = {"op": "search", "query": q}
            else:
                if not MFP_ID_RE.match(str(body.get("entry_id", ""))):
                    return self.send(400, {"error": "bad entry"})
                req = {"op": "entry", "entry_id": str(body["entry_id"])}
            data, err = run_mfp(req, timeout=90)
            return self.send(200 if data else 502, data or {"ok": False, "error": err})

        if self.path in ("/api/mfp/add", "/api/mfp/edit", "/api/mfp/delete"):
            op = self.path.rsplit("/", 1)[1]
            req = {"op": op, "date": date.today().isoformat()}
            try:
                q = float(body.get("quantity", 1))
            except (TypeError, ValueError):
                q = -1
            ids = {"add": ("food_id", "weight_id"), "edit": ("entry_id", "weight_id"), "delete": ("entry_id",)}[op]
            if not all(MFP_ID_RE.match(str(body.get(k, ""))) for k in ids):
                return self.send(400, {"error": "bad food or entry"})
            req.update({k: str(body[k]) for k in ids})
            if op in ("add", "edit"):
                if not 0 < q <= 1000:
                    return self.send(400, {"error": "amount must be more than 0"})
                req["quantity"] = q
            if op == "add":
                meal = " ".join(str(body.get("meal", "")).split())
                if not 0 < len(meal) <= 40:
                    return self.send(400, {"error": "pick a meal"})
                req["meal"] = meal
            if op == "edit":
                if not re.match(r"^\d{1,2}$", str(body.get("meal_id", ""))):
                    return self.send(400, {"error": "pick a meal"})
                req["meal_id"] = str(body["meal_id"])
            cache, err = write_mfp(req)
            if err:
                return self.send(502, {"ok": False, "error": err})
            return self.send(200, {**cache, "ok": True, "syncing": False})

        if self.path == "/api/weights":
            day, lb = str(body.get("date", "")), body.get("lb")
            if not DATE_RE.match(day) or not (lb is None or (isinstance(lb, (int, float)) and 50 < lb < 700)):
                return self.send(400, {"error": "bad weight"})
            with lock:
                weights = [w for w in read_json(WEIGHTS, []) if w.get("date") != day]
                if lb is not None:
                    weights.append({"date": day, "lb": round(float(lb), 1)})
                write_json(WEIGHTS, sorted(weights, key=lambda w: w["date"]))
            # Send it to MyFitnessPal too, so both have the same weigh-ins.
            warning = ""
            if lb is not None:
                _, err = write_mfp({"op": "weight", "date": day, "lb": round(float(lb), 1)})
                if err:
                    warning = "Saved here, but not in MyFitnessPal: " + err
            return self.send(200, {"ok": True, "warning": warning})

        self.send(404, {"error": f"unknown endpoint {self.path}"})


def reload_on_change():
    me = Path(__file__).resolve()
    watched = [me, me.with_name("assistant.py")]
    stamp = lambda: [f.stat().st_mtime for f in watched]
    start = stamp()
    while True:
        time.sleep(1)
        try:
            if stamp() != start:
                print("server.py changed, restarting…", flush=True)
                os.execv(sys.executable, [sys.executable, str(me), str(PORT)])
        except FileNotFoundError:
            pass


if __name__ == "__main__":
    for _ in range(20):  # after a self-restart the old socket can take a moment to free up
        try:
            server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
            break
        except OSError as e:
            err = e
            time.sleep(0.25)
    else:
        sys.exit(f"Port {PORT} is busy ({err.strerror}). Is the dashboard already running? Try: ./run.sh <other-port>")
    threading.Thread(target=reload_on_change, daemon=True).start()
    threading.Thread(target=mfp_loop, daemon=True).start()
    seed_docs()
    print(f"Health dashboard: http://localhost:{PORT}  ({date.today()})", flush=True)
    server.serve_forever()
