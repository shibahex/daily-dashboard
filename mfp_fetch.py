#!/usr/bin/env python3
"""MyFitnessPal for the dashboard: read the day (food per meal, goals, weigh-ins) and log,
edit or remove foods. Prints JSON.

Runs inside the myfitnesspal-mcp project's environment (server.py starts it with `uv run`),
so it reuses that project's saved login and its headless-browser session refresh.

Usage: mfp_fetch.py '{"op": "fetch", "date": "YYYY-MM-DD"}'
  ops: fetch · search {query} · entry {entry_id} · add {date, meal, food_id, weight_id, quantity}
       edit {date, entry_id, quantity, weight_id, meal_id} · delete {date, entry_id} · weight {date, lb}
Writes print the refreshed day, like fetch.
"""
import json
import sys
from datetime import date, timedelta
from urllib import parse

from lxml import html as lh
from myfitnesspal_mcp import diary, mfp_client, refresh

# Works with both layouts of the mfp-mcp package: the released 0.4.x (everything in diary.py)
# and newer source with split modules. Only the session/diary basics come from the package;
# the small web calls below are done here so either version is enough.
try:
    from myfitnesspal_mcp.entry_matching import collapse_whitespace, normalize_meal
    from myfitnesspal_mcp.exercise import get_exercise
    from myfitnesspal_mcp.food_search import food_candidates
    from myfitnesspal_mcp.measurements import set_weight
    from myfitnesspal_mcp.mfp_web import api_headers
except ImportError:
    collapse_whitespace, normalize_meal = diary._collapse_whitespace, diary._normalize_meal
    get_exercise, food_candidates, set_weight, api_headers = diary.get_exercise, diary.food_candidates, diary.set_weight, diary.api_headers

ORIGIN = "https://www.myfitnesspal.com"
NUTRIENT_KEYS = ("carbohydrates", "protein", "fat", "saturated_fat", "polyunsaturated_fat", "monounsaturated_fat",
                 "trans_fat", "cholesterol", "sodium", "potassium", "fiber", "sugar", "vitamin_a", "vitamin_c",
                 "calcium", "iron")
# Calories plus every nutrient MFP has a goal for (grams; cholesterol, sodium and
# potassium in mg), so intake and goals line up key for key.
KEYS = ("calories", *NUTRIENT_KEYS)


def site_url(client, path):
    return parse.urljoin(client.BASE_URL_SECURE, path)


def require_status(resp, endpoint, accepted):
    if resp.status_code not in accepted:
        raise RuntimeError(f"MyFitnessPal {endpoint} returned HTTP {resp.status_code}")


def legacy_form_headers(referer):
    return {"Content-Type": "application/x-www-form-urlencoded", "Origin": ORIGIN, "Referer": referer}


def get_document(client, url):
    resp = client.session.get(url, headers=api_headers(client))
    resp.raise_for_status()
    return lh.fromstring(resp.text)


def get_goals(client, day):
    resp = client.session.get(parse.urljoin(client.BASE_API_URL, f"v2/nutrient-goals?date={day.isoformat()}"),
                              headers=api_headers(client, {"Accept": "application/json"}))
    require_status(resp, "/v2/nutrient-goals", (200,))
    items = resp.json().get("items") or []
    if not items:
        raise RuntimeError("MyFitnessPal returned no nutrient goals")
    goal = items[0]["default_goal"]
    return {"calories": goal["energy"]["value"], **{k: goal.get(k) for k in NUTRIENT_KEYS}}
WEIGHT_LOOKBACK = 90


def nutrients(contents):
    out = {"calories": (contents.get("energy") or {}).get("value") or 0}
    out.update({k: contents.get(k) or 0 for k in NUTRIENT_KEYS})
    return out


def add_up(total, part):
    for k in KEYS:
        total[k] = round(total.get(k, 0) + part.get(k, 0), 1)


def number(text):
    try:
        return float(text.strip().replace(",", ""))
    except ValueError:
        return None


def food_entries(client, day):
    """The web app's v2 diary API. Unlike the diary page, which only has the six
    columns picked in MFP's settings, it returns every nutrient per entry."""
    url = parse.urljoin(client.BASE_API_URL, f"v2/diary?entry_date={day.isoformat()}&types=food_entry")
    resp = client.session.get(url, headers=api_headers(client, {"Accept": "application/json"}))
    require_status(resp, "/v2/diary", (200,))
    return resp.json().get("items") or []


def page_rows(doc):
    """Rows of the classic diary page: its entry ids are the ones edit and remove take."""
    rows, meal, position = [], None, -1
    for tr in doc.xpath("//tr"):
        if diary._is_meal_header(tr):
            position += 1
            meal = normalize_meal(diary._header_label(tr, position))
            continue
        anchors = tr.xpath(".//a[@data-food-entry-id]")
        if anchors:
            cells = tr.xpath("./td")
            rows.append({"entry_id": anchors[0].get("data-food-entry-id"), "meal": meal,
                         "name": collapse_whitespace(anchors[0].text_content()),
                         "calories": number(cells[1].text_content()) if len(cells) > 1 else None})
    return rows


def match_row(rows, meal, description, calories):
    """The page names an entry "<description>, <amount>"; calories break ties between copies."""
    desc = collapse_whitespace(description)
    same = [r for r in rows if r["meal"] == normalize_meal(meal) and r["name"].startswith(desc)]
    best = next((r for r in same if r["calories"] is not None and round(r["calories"]) == round(calories)), None)
    row = best or (same[0] if same else None)
    if row:
        rows.remove(row)
    return row


def exercise_calories(client, day):
    total = 0.0
    try:
        for entries in get_exercise(client, day)["exercise"].values():
            for e in entries:
                info = e.get("nutrition_information") or {}
                total += float(info.get("calories burned") or 0)
    except Exception as exc:
        if mfp_client.is_auth_error(exc):
            raise
    return round(total)


def fetch(client, day):
    doc, _ = diary.diary_page(client, day)
    meal_names = diary.meal_headers(doc)
    rows = page_rows(doc)
    meals = {normalize_meal(n): {"name": n, "id": i, "totals": {k: 0 for k in KEYS}, "entries": []}
             for i, n in enumerate(meal_names)}
    totals = {k: 0 for k in KEYS}
    for item in sorted(food_entries(client, day), key=lambda i: (i.get("meal_position", 9), i.get("created_at") or "")):
        food, size = item.get("food") or {}, item.get("serving_size") or {}
        n = nutrients(item.get("nutritional_contents") or {})
        servings = item.get("servings") or 1
        amount = f"{servings * (size.get('value') or 1):g} {size.get('unit') or ''}".strip()
        name = item.get("meal_name") or "Other"
        key = normalize_meal(name)
        meal = meals.setdefault(key, {"name": name, "id": item.get("meal_position"), "totals": {}, "entries": []})
        row = match_row(rows, name, food.get("description") or "", n["calories"])
        meal["entries"].append({"name": food.get("description") or "Food", "amount": amount, "totals": n,
                                "entry_id": row["entry_id"] if row else None})
        add_up(meal["totals"], n)
        add_up(totals, n)
    weights = client.get_measurements("Weight", day - timedelta(days=WEIGHT_LOOKBACK))
    return {
        "day": day.isoformat(),
        "totals": totals,
        "meals": sorted(meals.values(), key=lambda m: (m["id"] is None, m["id"] or 0)),
        "exercise_calories": exercise_calories(client, day),
        "goals": get_goals(client, day),
        "weights": [{"date": d.isoformat(), "lb": round(float(v), 1)} for d, v in sorted(weights.items())],
    }


def search(client, query):
    """Candidates with their serving sizes. Nutrition is per serving with multiplier 1."""
    out = []
    for c in food_candidates(client, query, limit=10):
        out.append({"food_id": c["food_id"], "name": c["name"], "brand": c["brand"], "verified": c["verified"],
                    "nutrition": c["nutrition"], "default_weight_id": c["default_weight_id"],
                    "servings": c["servings"]})
    return {"query": query, "results": out}


def edit_form(client, entry_id):
    """The classic diary's own edit dialog: current amount, serving sizes and meal."""
    doc = get_document(client, site_url(client, f"food/edit_entry/{entry_id}"))
    sel = lambda name: [{"id": o.get("value"), "label": collapse_whitespace(o.text_content()), "selected": o.get("selected") is not None}
                        for o in doc.xpath(f"//select[@name='{name}']/option")]
    qty = doc.xpath("//input[@name='food_entry[quantity]']/@value")
    if not qty:
        raise RuntimeError("couldn't open that entry in MyFitnessPal")
    return {"entry_id": entry_id, "name": collapse_whitespace(" ".join(doc.xpath("//p[@class='food-description']//text()"))),
            "quantity": number(qty[0]), "servings": sel("food_entry[weight_id]"), "meals": sel("food_entry[meal_id]"),
            "token": (doc.xpath("//input[@name='authenticity_token']/@value") or [""])[0]}


def edit(client, day, entry_id, quantity, weight_id, meal_id):
    form = edit_form(client, entry_id)
    referer = diary.food_diary_url(client, day)
    resp = client.session.post(site_url(client, f"food/edit_entry/{entry_id}"), data={
        "authenticity_token": form["token"],
        "food_entry[id]": entry_id,
        "food_entry[date]": day.isoformat(),
        "food_entry[quantity]": f"{quantity:g}",
        "food_entry[weight_id]": weight_id,
        "food_entry[meal_id]": meal_id,
    }, headers=legacy_form_headers(referer))
    require_status(resp, "/food/edit_entry", (200, 204, 302))


def delete(client, day, entry_id):
    _, token = diary.diary_page(client, day)
    resp = client.session.post(site_url(client, f"food/remove/{entry_id}"),
                               data={"_method": "delete", "authenticity_token": token},
                               headers=legacy_form_headers(diary.food_diary_url(client)))
    require_status(resp, "/food/remove", (200, 204))


def run(req):
    client = mfp_client.get_client()
    op = req.get("op", "fetch")
    day = date.fromisoformat(req["date"]) if req.get("date") else date.today()
    if op == "fetch":
        return fetch(client, day)
    if op == "search":
        return search(client, str(req["query"]))
    if op == "entry":
        form = edit_form(client, str(req["entry_id"]))
        form.pop("token")
        return form
    if op == "add":
        diary.push_food(client, day, str(req["meal"]), food_id=str(req["food_id"]), weight_id=str(req["weight_id"]),
                        quantity=float(req["quantity"]))
    elif op == "edit":
        edit(client, day, str(req["entry_id"]), float(req["quantity"]), str(req["weight_id"]), str(req["meal_id"]))
    elif op == "delete":
        delete(client, day, str(req["entry_id"]))
    elif op == "weight":
        set_weight(client, day, float(req["lb"]))
    else:
        raise ValueError(f"unknown op {op}")
    return fetch(client, date.today())


def main():
    req = json.loads(sys.argv[1]) if len(sys.argv) > 1 else {}
    try:
        out = run(req)
    except Exception as exc:
        if not mfp_client.is_auth_error(exc):
            raise
        # Expired session: let the headless browser rotate it, then try once more. Safe for
        # writes too: an auth failure means MyFitnessPal rejected the request.
        refresh.refresh_session()
        out = run(req)
    print(json.dumps(out, default=str))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        msg = str(exc)
        if mfp_client.is_auth_error(exc):
            msg = "MyFitnessPal login expired. Run `mfp-mcp auth` in the myfitnesspal-mcp folder, then refresh."
        print(json.dumps({"error": msg}))
        sys.exit(1)
