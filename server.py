#!/usr/bin/env python3
"""Local dashboard for ~/Projects/health: serves the docs, the editable plan and wraps Taskwarrior.

Binds to 127.0.0.1 only. Writes require a JSON body and an X-Dashboard header,
so other websites open in the browser can't trigger them.
Restarts itself when this file changes, so edits never leave a stale server running.
"""
import json
import os
import re
import subprocess
import sys
import threading
import time
from datetime import date, datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 1628
SITE = Path(__file__).resolve().parent
DOCS = SITE.parent
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


def run_task(args):
    # stdin closed: if Taskwarrior ever asks a question, it fails instead of hanging.
    p = subprocess.run(TASK + args, capture_output=True, text=True, timeout=30, stdin=subprocess.DEVNULL)
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
            return self.send(200, (SITE / "index.html").read_bytes(), "text/html; charset=utf-8")
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

        if self.path == "/api/weights":
            day, lb = str(body.get("date", "")), body.get("lb")
            if not DATE_RE.match(day) or not (lb is None or (isinstance(lb, (int, float)) and 50 < lb < 700)):
                return self.send(400, {"error": "bad weight"})
            with lock:
                weights = [w for w in read_json(WEIGHTS, []) if w.get("date") != day]
                if lb is not None:
                    weights.append({"date": day, "lb": round(float(lb), 1)})
                write_json(WEIGHTS, sorted(weights, key=lambda w: w["date"]))
            return self.send(200, {"ok": True})

        self.send(404, {"error": f"unknown endpoint {self.path}"})


def reload_on_change():
    me = Path(__file__).resolve()
    start = me.stat().st_mtime
    while True:
        time.sleep(1)
        try:
            if me.stat().st_mtime != start:
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
    print(f"Health dashboard: http://localhost:{PORT}  ({date.today()})", flush=True)
    server.serve_forever()
