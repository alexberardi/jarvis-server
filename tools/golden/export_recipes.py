"""Export the recipes golden fixtures (docs/recipes/00-inventory.md §12) from the legacy code.

    PYTHONDONTWRITEBYTECODE=1 ../jarvis-recipes-server/.venv/bin/python tools/golden/export_recipes.py \
        --recipes ../jarvis-recipes-server --out fixtures/golden/recipes

Writes (all under --out):

- quantity.json        parse_quantity_display (+ the Numeric(10,4) wire string), normalize_fraction_display,
                       normalize_unit_token / is_known_unit, ISO durations, parse_minutes / parse_servings /
                       parse_servings_from_text, parse_job_service._split_qty_unit
- ingredients.json     ingredient_parser.extract_ingredients (strings and dicts) and clean_parsed_ingredients
- normalize_name.json  shopping_list_service.normalize_name over every string in the legacy ingredient tests,
                       the stock ingredient names/synonyms/units and a synthetic corpus
- cart.json            grocery_service._parse_pack_size, _pack_quantity, _amount_display, cart_url, map_key
- ocr.json             ocr_quality.score_quality, ocr_join reading order/combine, the best-reading pick and the
                       P3 trigger predicate from queue_worker._structure_from_readings
- llm_parse.json       llm_client._coerce_recipe_draft, _try_local_json_repair, _strip_invalid_control_chars,
                       extractors.llm._parse_llm_json_content, call_meal_plan_select post-processing (P4 id
                       filter / confidence clamp), match_grocery_items + apply_matches (P5)
- extract.json         schema.org + heuristic extractors over pages; ingestion_service.parse_recipe over webview
                       payloads, and parse_job_service.mark_complete's result_json for each success
- prompts/P*.json      P1, P1r (both schema hints), P2 (1 and 2 readings), P3, P4, P5: the request body the
                       legacy code POSTs to /v1/chat/completions, byte-exact, plus the httpx timeout
- _index.json          legacy SHA, row counts and the SHA-256 of every file

Like the other exporters this lives in jarvis-server because the Python repos are frozen. Nothing
here touches the network or a database: the LLM HTTP client is replaced by a recorder that answers
with canned replies, and the two DB-bound grocery helpers apply_matches calls (list_map,
upsert_mapping) are replaced by in-memory fakes. Everything else is the legacy code itself.
"""

import argparse
import ast
import asyncio
import hashlib
import inspect
import io
import json
import logging
import os
import sys
import tempfile
from decimal import Decimal
from pathlib import Path
from types import SimpleNamespace

LEGACY_SHA = "f8589fbdfd09aa96862d38a04291d2cd914d8774"
INPUTS = Path(__file__).resolve().parent / "inputs"

# Test modules whose string literals are ingredient lines (the normalize_name corpus).
INGREDIENT_TEST_FILES = [
    "test_shopping_list.py",
    "test_shopping_list_grouping.py",
    "test_staples.py",
    "test_grocery_cart.py",
    "test_quantity_parser.py",
    "test_quantity_split.py",
    "test_url_recipe_parser.py",
    "test_ingestion_refactor.py",
    "test_commit_plan.py",
    "test_recipes.py",
]


def err(exc: BaseException) -> dict:
    return {"error": type(exc).__name__, "message": str(exc)}


def jsonable(v):
    if isinstance(v, Decimal):
        return str(v)
    if hasattr(v, "model_dump"):
        return v.model_dump(mode="json")
    if isinstance(v, tuple):
        return [jsonable(x) for x in v]
    if isinstance(v, list):
        return [jsonable(x) for x in v]
    if isinstance(v, dict):
        return {k: jsonable(x) for k, x in v.items()}
    return v


def call(fn, *a, **kw):
    try:
        return {"out": jsonable(fn(*a, **kw))}
    except Exception as exc:  # noqa: BLE001 - the exception IS the golden
        return err(exc)


# ── fake LLM transport ────────────────────────────────────────────────────────


class FakeResponse:
    def __init__(self, status: int, body):
        self.status_code = status
        self._body = body

    def json(self):
        return self._body

    def raise_for_status(self):
        if self.status_code >= 400:
            import httpx

            req = httpx.Request("POST", "http://llm.invalid/v1/chat/completions")
            raise httpx.HTTPStatusError(f"status {self.status_code}", request=req,
                                        response=httpx.Response(self.status_code, request=req))


class Recorder:
    """Stands in for httpx.AsyncClient. Each post() is recorded and answered from `replies`."""

    calls: list = []
    replies: list = []

    def __init__(self, timeout=None, **_):
        self.timeout = timeout

    async def __aenter__(self):
        return self

    async def __aexit__(self, *exc):
        return False

    async def post(self, url, json=None, headers=None):  # noqa: A002 - httpx's name
        t = self.timeout
        Recorder.calls.append({
            "url": url,
            "header_names": sorted((headers or {}).keys()),
            "timeout": None if t is None else {"connect": t.connect, "read": t.read, "write": t.write, "pool": t.pool},
            "body": json,
        })
        reply = Recorder.replies.pop(0) if Recorder.replies else {"content": "{}"}
        if "body" in reply:
            return FakeResponse(reply.get("status", 200), reply["body"])
        return FakeResponse(reply.get("status", 200),
                            {"choices": [{"message": {"role": "assistant", "content": reply["content"]}}]})

    @classmethod
    def reset(cls, replies=None):
        cls.calls = []
        cls.replies = list(replies or [])


class StubSettings:
    """get_settings_service() without a database: every key answers its declared default."""

    def get_str(self, key, default=None):
        return default

    def get_int(self, key, default=None):
        return default


def run(coro):
    return asyncio.new_event_loop().run_until_complete(coro)


# ── corpus helpers ────────────────────────────────────────────────────────────


def harvest_strings(path: Path) -> list[str]:
    """Single-line string literals that sit inside list/tuple/dict literals or call args.

    That is where the legacy tests keep their ingredient lines (parametrize tables and fixture
    lists); docstrings and assert messages are left out.
    """
    tree = ast.parse(path.read_text())
    out = []
    for node in ast.walk(tree):
        if isinstance(node, (ast.List, ast.Tuple, ast.Set)):
            elts = node.elts
        elif isinstance(node, ast.Dict):
            elts = node.values
        elif isinstance(node, ast.Call):
            elts = list(node.args) + [k.value for k in node.keywords]
        else:
            continue
        for e in elts:
            if isinstance(e, ast.Constant) and isinstance(e.value, str):
                s = e.value
                if s and "\n" not in s and len(s) <= 120:
                    out.append(s)
    return out


def dedupe(items):
    seen, out = set(), []
    for s in items:
        if s not in seen:
            seen.add(s)
            out.append(s)
    return out


def html_pages_from_tests(tests: Path) -> list[dict]:
    pages = []
    tree = ast.parse((tests / "test_url_recipe_parser.py").read_text())
    for fn in [n for n in tree.body if isinstance(n, ast.FunctionDef)]:
        n = 0
        for node in ast.walk(fn):
            if isinstance(node, ast.Constant) and isinstance(node.value, str) and "<" in node.value and (
                "<html" in node.value or "<script" in node.value or "<article" in node.value
            ):
                n += 1
                pages.append({"name": f"tests:{fn.name}#{n}", "url": "https://example.com/test", "html": node.value})
    return pages


# ── main ──────────────────────────────────────────────────────────────────────


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--recipes", required=True, help="path to the jarvis-recipes-server checkout")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()
    repo = Path(args.recipes).resolve()
    out = Path(args.out).resolve()
    (out / "prompts").mkdir(parents=True, exist_ok=True)

    # Harmless bootstrap values; nothing is ever contacted. Run from an empty dir so pydantic-settings
    # cannot pick up an .env file.
    os.environ.update({
        "DATABASE_URL": "sqlite://", "JARVIS_APP_ID": "golden-app", "JARVIS_APP_KEY": "golden-key",
        "LLM_BASE_URL": "http://llm.invalid", "JARVIS_CONFIG_URL": "", "TZ": "UTC",
    })
    os.chdir(tempfile.mkdtemp(prefix="recipes-golden-"))
    sys.path.insert(0, str(repo))
    sys.dont_write_bytecode = True
    logging.disable(logging.CRITICAL)  # the legacy code logs every coercion fallback; not part of the golden

    import httpx

    from jarvis_recipes.app.schemas.ingestion_input import IngestionInput
    from jarvis_recipes.app.schemas.recipe import IngredientRead
    from jarvis_recipes.app.services import (
        grocery_service,
        ingestion_service,
        llm_client,
        ocr_join,
        ocr_quality,
        parse_job_service,
        queue_worker,
        shopping_list_service,
    )
    from jarvis_recipes.app.services.quantity_parser import parse_quantity_display
    from jarvis_recipes.app.services.url_parsing import ingredient_parser, parsing_utils
    from jarvis_recipes.app.services.url_parsing.extractors import heuristic, schema_org
    from jarvis_recipes.app.services.url_parsing.extractors import llm as llm_extractor
    from jarvis_recipes.app.services.url_parsing.models import ParsedIngredient

    httpx.AsyncClient = Recorder
    for mod in (llm_client, llm_extractor):
        mod.get_settings_service = StubSettings
        mod._llm_base_url = lambda: "http://llm.invalid"
    llm_client._llm_base_url = lambda: "http://llm.invalid"

    # B24: the URL extractor writes /tmp/llm_raw_<sha1>.log on every call. Shadow `open` in that
    # module so the file is recorded instead of written.
    debug_writes: list[str] = []

    def fake_open(path, *a, **kw):
        debug_writes.append(str(path))
        return io.StringIO()

    llm_extractor.open = fake_open

    s_in = json.loads((INPUTS / "recipes_strings.json").read_text())
    l_in = json.loads((INPUTS / "recipes_llm.json").read_text())
    p_in = json.loads((INPUTS / "recipes_pages.json").read_text())
    counts: dict[str, int] = {}

    def write(rel: str, payload) -> None:
        path = out / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(payload, indent=1, ensure_ascii=False) + "\n")

    # ── quantity.json ──
    def wire_quantity(raw):
        """What GET /recipes/{id} renders: Numeric(10,4) round trip, then pydantic's Decimal → str."""
        d = parse_quantity_display(raw)
        if d is None:
            return None
        try:
            stored = d.quantize(Decimal("0.0001")) if d.is_finite() else d
            return IngredientRead(id=1, text="x", quantity_value=stored).model_dump(mode="json")["quantity_value"]
        except Exception as exc:  # noqa: BLE001 - NaN/Infinity: the response model refuses them
            return err(exc)

    quantity = {
        "parse_quantity_display": [
            {"input": q, **call(parse_quantity_display, q), "wire_numeric_10_4": wire_quantity(q)}
            for q in s_in["quantity_display"]
        ],
        "normalize_fraction_display": [
            {"input": q, **call(parsing_utils.normalize_fraction_display, q)} for q in s_in["fraction_display"]
        ],
        "normalize_unit_token": [
            {"input": u, **call(parsing_utils.normalize_unit_token, u),
             "is_known_unit": parsing_utils.is_known_unit(u)} for u in s_in["unit_tokens"]
        ],
        "parse_iso8601_duration": [
            {"input": d, **call(parsing_utils.parse_iso8601_duration, d)} for d in s_in["durations"]
        ],
        "parse_minutes": [{"input": v, **call(parsing_utils.parse_minutes, v)} for v in s_in["minutes_values"]],
        "parse_servings": [{"input": v, **call(parsing_utils.parse_servings, v)} for v in s_in["servings_values"]],
        "parse_servings_from_text": [
            {"input": v, **call(parsing_utils.parse_servings_from_text, v)} for v in s_in["servings_text"]
        ],
        "split_qty_unit": [
            {"input": v, **call(parse_job_service._split_qty_unit, v)} for v in s_in["split_qty_unit"]
        ],
    }
    write("quantity.json", quantity)
    counts["quantity.json"] = sum(len(v) for v in quantity.values())

    # ── ingredients.json ──
    ingredients = {
        "extract_ingredients_line": [
            {"input": line, **call(ingredient_parser.extract_ingredients, [line])} for line in s_in["ingredient_lines"]
        ],
        "extract_ingredients_dict": [
            {"input": d, **call(ingredient_parser.extract_ingredients, [d])} for d in s_in["ingredient_dicts"]
        ],
        "extract_ingredients_string_arg": [
            {"input": "2 cups rice", **call(ingredient_parser.extract_ingredients, "2 cups rice")},
            {"input": 5, **call(ingredient_parser.extract_ingredients, 5)},
        ],
        "clean_parsed_ingredients": [
            {"input": d, **call(ingredient_parser.clean_parsed_ingredients, [ParsedIngredient(**d)])}
            for d in s_in["clean_parsed"]
        ],
    }
    write("ingredients.json", ingredients)
    counts["ingredients.json"] = sum(len(v) for v in ingredients.values())

    # ── normalize_name.json ──
    tests = repo / "tests"
    harvested = []
    for name in INGREDIENT_TEST_FILES:
        if (tests / name).exists():
            harvested += harvest_strings(tests / name)
    stock_ing = json.loads((repo / "static_data" / "ingredients.json").read_text())
    stock_units = json.loads((repo / "static_data" / "units_of_measure.json").read_text())
    stock = [i["name"] for i in stock_ing] + [s for i in stock_ing for s in (i.get("synonyms") or [])]
    units = [u["name"] for u in stock_units] + [u.get("abbreviation") or "" for u in stock_units]
    sources = [("tests", harvested), ("stock", stock), ("units", units),
               ("synthetic", s_in["normalize_name_synthetic"] + s_in["ingredient_lines"])]
    rows, seen = [], set()
    for src, items in sources:
        for text in items:
            if text in seen:
                continue
            seen.add(text)
            rows.append({"source": src, "input": text, **call(shopping_list_service.normalize_name, text)})
    write("normalize_name.json", rows)
    counts["normalize_name.json"] = len(rows)

    # ── cart.json ──
    Amount, ShoppingItem = shopping_list_service.Amount, shopping_list_service.ShoppingItem

    def item_from(amounts):
        return ShoppingItem(name="x", recipes=[], amounts=[
            Amount(unit=a.get("unit"), quantity=None if a.get("quantity") is None else Decimal(a["quantity"]),
                   unparsed=list(a.get("unparsed") or [])) for a in amounts])

    sizes = dedupe([c["unit_size"] for c in s_in["pack_cases"] if c["unit_size"]] + ["1.5 lb", "12-oz", " 16 OZ ", "abc"])
    cart = {
        "parse_pack_size": [{"input": s, **call(grocery_service._parse_pack_size, s)} for s in sizes],
        "pack_quantity": [
            {"input": c, **call(grocery_service._pack_quantity, item_from(c["amounts"]), c["unit_size"])}
            for c in s_in["pack_cases"]
        ],
        "amount_display": [
            {"input": c, **call(grocery_service._amount_display, item_from(c))} for c in s_in["amount_display_cases"]
        ],
        "cart_url": [
            {"input": c, **call(grocery_service.cart_url, [
                grocery_service.CartItem(ingredient_name="x", sku=i["sku"], quantity=i["quantity"]) for i in c])}
            for c in s_in["cart_url_cases"]
        ],
        "map_key": [{"input": t, **call(grocery_service.map_key, t)} for t in
                    ["90/10 ground beef", "2% milk", "7up", "1 lb ground beef", "Garlic, minced"]],
    }
    write("cart.json", cart)
    counts["cart.json"] = sum(len(v) for v in cart.values())

    # ── ocr.json ──
    sys.path.insert(0, str(tests / "fixtures"))
    import ocr_samples  # noqa: E402 - the legacy fixture module, by path

    by_name = {s["name"]: s for s in p_in["ocr_synthetic"]}

    def sample_text(s):
        t = s["text"]
        return by_name[t[1:]]["text"] if t.startswith("@") else t

    samples = [
        {"name": f"ocr_samples.{k}", "confidence": c, "text": getattr(ocr_samples, k)}
        for k in sorted(n for n in dir(ocr_samples) if n.isupper() and isinstance(getattr(ocr_samples, n), str))
        for c in (None, 90.0)
    ] + [{"name": s["name"], "confidence": s["confidence"], "text": sample_text(s)} for s in p_in["ocr_synthetic"]]
    # Boundary cases built in code: exactly at / one under the 250-char and 10-line floors.
    line = "1 cup flour mixed with the other dry ingredients\n"
    samples += [
        {"name": "boundary_10_lines", "confidence": 90.0, "text": (line * 10)[:-1]},
        {"name": "boundary_9_lines", "confidence": 90.0, "text": (line * 9)[:-1]},
        {"name": "boundary_250_chars", "confidence": 90.0, "text": ("ab\n" * 83 + "a")},
    ]
    quality_rows = [{"name": s["name"], "confidence": s["confidence"], "text": s["text"],
                     **call(ocr_quality.score_quality, s["text"], s["confidence"])} for s in samples]

    # Reading order + combine (ocr_join) and the pick queue_worker makes before the gate.
    readings_cases = [
        [{"provider": "tesseract", "received_at": "2026-01-01T00:00:01", "results": [{"index": 1, "ocr_text": "B"}, {"index": 0, "ocr_text": "A"}]},
         {"provider": "apple_vision", "received_at": "2026-01-01T00:00:05", "results": [{"index": 0, "ocr_text": "apple one two"}]},
         {"provider": "mystery", "received_at": "2026-01-01T00:00:00", "results": [{"index": 0, "ocr_text": "m"}]},
         {"provider": "rapidocr", "received_at": "2026-01-01T00:00:02", "results": [{"index": 0, "ocr_text": ""}, {"index": 1, "ocr_text": "   "}]},
         {"provider": None, "received_at": None, "results": [{"index": 0, "ocr_text": "none provider"}]}],
        [{"provider": "easyocr", "received_at": "b", "results": [{"index": 0, "ocr_text": "a b c d e"}]},
         {"provider": "easyocr", "received_at": "a", "results": [{"index": 0, "ocr_text": "longer-by-chars-but-one-word"}]}],
        [],
    ]
    src = inspect.getsource(queue_worker._structure_from_readings)
    for needle in [
        'texts = [(r.get("provider") or "", ocr_join.combine(r.get("results") or [])) for r in readings]',
        "texts = [(provider, text) for provider, text in texts if text.strip()]",
        "combined_text = max((t for _, t in texts), key=lambda t: len(t.split()))",
        'common_units = ["cup", "cups", "tsp", "teaspoon", "tbsp", "tablespoon", "oz", "ounce", "lb", "pound", "g", "gram", "kg", "ml", "liter", "clove", "cloves"]',
        "if any(unit in name_lower for unit in common_units) and not ing.unit:",
        'if " and " in name_lower or ("," in name_lower and len(name_lower.split(",")) > 1):',
        "if not draft.description:",
    ]:
        if needle not in src:
            sys.exit(f"queue_worker._structure_from_readings drifted; missing: {needle}")

    def pick(readings):
        ing = SimpleNamespace(ocr_readings=readings, ocr_expected=len(readings))
        ordered = ocr_join.readings_for_llm(ing)
        texts = [(r.get("provider") or "", ocr_join.combine(r.get("results") or [])) for r in ordered]
        texts = [(p, t) for p, t in texts if t.strip()]
        best = max((t for _, t in texts), key=lambda t: len(t.split())) if texts else None
        return {"order": [r.get("provider") for r in ordered], "texts": texts, "gate_text": best,
                "describe": ocr_join.describe(ing)}

    def needs_cleaning(draft_dict):
        """queue_worker's P3 trigger, verbatim (checked against the source above)."""
        from jarvis_recipes.app.schemas.ingestion import RecipeDraft

        draft = RecipeDraft.model_validate(draft_dict)
        try:
            draft.validate_minimums()
            validation_passed = True
        except Exception:  # noqa: BLE001
            validation_passed = False
        if not validation_passed:
            return True
        for ing in draft.ingredients:
            name_lower = ing.name.lower() if ing.name else ""
            common_units = ["cup", "cups", "tsp", "teaspoon", "tbsp", "tablespoon", "oz", "ounce", "lb", "pound", "g", "gram", "kg", "ml", "liter", "clove", "cloves"]
            if any(unit in name_lower for unit in common_units) and not ing.unit:
                return True
            if " and " in name_lower or ("," in name_lower and len(name_lower.split(",")) > 1):
                return True
        if not draft.description:
            return True
        return False

    def d(names, units=None, desc="A dish.", steps=("a", "b"), title="Dish"):
        units = units or [None] * len(names)
        return {"title": title, "description": desc, "steps": list(steps), "source": {"type": "ocr"},
                "ingredients": [{"name": n, "unit": u} for n, u in zip(names, units)]}

    p3_cases = [
        ("clean", d(["butter", "eggs", "rice"])),
        ("letter_g_in_name_no_unit", d(["egg", "milk", "rice"])),
        ("letter_g_in_name_with_unit", d(["egg", "milk", "rice"], ["", "cup", "cup"])),
        ("unit_word_with_unit_set", d(["cup of tea", "milk", "rice"], ["cup", None, None])),
        ("unit_substring_no_unit", d(["cupcake", "milk", "rice"])),
        ("and_in_name", d(["salt and pepper", "milk", "rice"], ["x", "x", "x"])),
        ("comma_in_name", d(["onion, chopped", "milk", "rice"], ["x", "x", "x"])),
        ("trailing_comma", d(["onion,", "milk", "rice"], ["x", "x", "x"])),
        ("no_description", d(["butter", "rice", "milk"], desc=None)),
        ("empty_description", d(["butter", "rice", "milk"], desc="")),
        ("fails_minimums", d(["butter", "rice"])),
    ]
    ocr = {
        "score_quality": quality_rows,
        "engine_rank": ocr_join.ENGINE_RANK,
        "readings": [{"input": r, **call(pick, r)} for r in readings_cases],
        "p3_trigger": [{"name": n, "draft": dd, **call(needs_cleaning, dd)} for n, dd in p3_cases],
    }
    write("ocr.json", ocr)
    counts["ocr.json"] = len(quality_rows) + len(readings_cases) + len(p3_cases)

    # ── llm_parse.json ──
    def coerce(obj):
        return llm_client._coerce_recipe_draft(obj, source_type="ocr")

    def meal_select(reply):
        Recorder.reset([reply])
        return run(llm_client.call_meal_plan_select(
            slot={"date": "2026-10-08", "meal_type": "dinner"}, preferences={}, recent_meals=[],
            candidates=[{"id": "1", "title": "A"}, {"id": "2", "title": "B"}, {"id": "3", "title": "C"},
                        {"id": "core_1", "title": "D"}]))

    visible_map = [
        SimpleNamespace(id=1, ingredient_name="ground beef", sku="111", product_name="Ground Beef 1 lb", unit_size="1 lb", source="manual"),
        SimpleNamespace(id=2, ingredient_name="garlic", sku="222", product_name=None, unit_size=None, source="manual"),
    ]
    written: list = []

    def fake_upsert(db, user, **kw):
        written.append(kw)
        return kw

    grocery_service.list_map = lambda db, user, retailer="walmart": list(visible_map)
    grocery_service.upsert_mapping = fake_upsert

    def grocery(reply):
        Recorder.reset([reply])
        written.clear()
        matches = run(llm_client.match_grocery_items([{"role": "user", "content": "x"}]))
        grocery_service.apply_matches(None, SimpleNamespace(id=7, household_id="hh"), matches)
        return {"matches": matches, "upserts": list(written)}

    llm_parse = {
        "coerce_recipe_draft": [{"name": c["name"], "input": c["input"], **call(coerce, c["input"])} for c in l_in["coerce"]],
        "try_local_json_repair": [{"input": s, **call(llm_client._try_local_json_repair, s)} for s in l_in["json_repair"]],
        "strip_invalid_control_chars": [{"input": s, **call(llm_client._strip_invalid_control_chars, s)} for s in l_in["json_repair"]],
        "parse_llm_json_content": [{"input": s, **call(llm_extractor._parse_llm_json_content, s)} for s in l_in["json_repair"]],
        "meal_plan_select": [{"name": r["name"], "reply": r, **call(meal_select, r)} for r in l_in["meal_plan_responses"]],
        "grocery_match": {"visible_map": [vars(v) for v in visible_map],
                          "cases": [{"name": r["name"], "reply": r, **call(grocery, r)} for r in l_in["grocery_responses"]]},
    }
    write("llm_parse.json", llm_parse)
    counts["llm_parse.json"] = (len(l_in["coerce"]) + 3 * len(l_in["json_repair"]) +
                                len(l_in["meal_plan_responses"]) + len(l_in["grocery_responses"]))

    # ── extract.json ──
    pages = html_pages_from_tests(tests)
    for fx in sorted((tests / "fixtures" / "ingestion").glob("*.html")):
        pages.append({"name": f"fixtures:{fx.name}", "url": "https://example.com/fixture", "html": fx.read_text()})
    pages += p_in["pages"]

    webview = list(p_in["webview_payloads"])
    for fx in sorted((tests / "fixtures" / "ingestion").glob("*.json")):
        blocks = json.loads(fx.read_text()).get("jsonld_blocks")
        webview.insert(0, {"name": f"fixtures:{fx.name}", "input": {
            "source_type": "client_webview", "source_url": "https://example.com/fixture", "jsonld_blocks": blocks}})

    def ingest(case):
        Recorder.reset([{"content": case["llm_reply"]}] if "llm_reply" in case else [])
        debug_writes.clear()
        result = run(ingestion_service.parse_recipe(IngestionInput(**case["input"])))
        row = {"parse_result": result.model_dump(mode="json"),
               "llm_calls": len(Recorder.calls), "debug_log_writes_B24": list(debug_writes)}
        if result.success:
            job = SimpleNamespace(id="golden", status="RUNNING", job_type="ingestion", result_json=None)
            db = SimpleNamespace(commit=lambda: None, refresh=lambda _: None, rollback=lambda: None)
            parse_job_service.mark_complete(db, job, result)
            row["mark_complete"] = {"status": job.status, "result_json": job.result_json}
        return row

    extract = {
        "schema_org": [{"name": p["name"], "html": p["html"], **call(schema_org.extract_recipe_from_schema_org, p["html"], p["url"])} for p in pages],
        "heuristic": [{"name": p["name"], "html": p["html"], **call(heuristic.extract_recipe_heuristic, p["html"], p["url"])} for p in pages],
        "ingestion": [{"name": c["name"], "input": c["input"], **call(ingest, c)} for c in webview],
    }
    write("extract.json", extract)
    counts["extract.json"] = 2 * len(pages) + len(webview)

    # ── prompts/ ──
    prompt_files = {}

    def save_prompt(name, inputs, calls):
        payload = {"inputs": inputs, "requests": calls}
        write(f"prompts/{name}.json", payload)
        prompt_files[name] = len(calls)

    # P1: the URL/HTML extractor, short page (padded with up to 200 lines of main text) and long page
    # (capped at 10 000 chars). The reply is unparseable on the long page so P1r follows with the URL hint.
    short_lines = "".join(f"<p>Line {i} of the story about this dish.</p>" for i in range(260))
    short_html = (f"<html><head><title>Short Page</title></head><body><nav>menu</nav><article><h1>Short Stew</h1>"
                  f"<ul><li>1 cup a</li><li>2 cups b</li></ul>{short_lines}</article></body></html>")
    long_items = "".join(f"<li>{i} cups ingredient number {i} finely chopped</li>" for i in range(1, 400))
    long_steps = "".join(f"<li>Step {i}: stir the pot and taste carefully.</li>" for i in range(1, 200))
    long_html = (f"<html><body><main><h1>Long Page</h1><script type=\"application/ld+json\">{{\"@type\":\"WebPage\"}}</script>"
                 f"<ul>{long_items}</ul><h2>Instructions</h2><ol>{long_steps}</ol></main></body></html>")
    reply_ok = {"content": "{\"title\": \"x\", \"ingredients\": [], \"steps\": [], \"notes\": null}"}
    Recorder.reset([reply_ok])
    run(llm_extractor.extract_recipe_via_llm(short_html, "https://example.com/short"))
    p1_short = list(Recorder.calls)
    Recorder.reset([{"content": "not json"}, {"content": "{\"title\": \"x\"}"}])
    run(llm_extractor.extract_recipe_via_llm(long_html, "https://example.com/long"))
    p1_long = list(Recorder.calls)
    save_prompt("P1_url_extract", {"short": {"url": "https://example.com/short", "html": short_html},
                                    "long": {"url": "https://example.com/long", "html": long_html}},
                {"short": p1_short[0], "long": p1_long[0]})
    save_prompt("P1r_json_repair_url", {"broken": "not json"}, [p1_long[1]])

    # P1r with the RecipeDraft hint (_parse_with_repair), reached when coercion and local repair fail.
    Recorder.reset([{"content": "{\"title\": \"Fixed\", \"ingredients\": [{\"name\": \"a\"}, {\"name\": \"b\"}, {\"name\": \"c\"}], \"steps\": [\"x\", \"y\"]}"}])
    run(llm_client._parse_with_repair("totally {not json", "ocr"))
    save_prompt("P1r_json_repair_draft", {"broken": "totally {not json"}, list(Recorder.calls))

    # P2: OCR → RecipeDraft, one reading and two readings (ensemble rules + labelled blocks).
    good = ("{\"title\": \"Crepes\", \"description\": \"Thin.\", \"ingredients\": [{\"name\": \"flour\", \"quantity\": \"1\", \"unit\": \"cup\"}, "
            "{\"name\": \"eggs\", \"quantity\": \"3\"}, {\"name\": \"milk\", \"quantity\": \"2\", \"unit\": \"cups\"}], \"steps\": [\"Beat\", \"Cook\"]}")
    one = [("apple_vision", ocr_samples.CREPE_CARD_APPLE_VISION)]
    two = [("apple_vision", ocr_samples.CREPE_CARD_APPLE_VISION), ("rapidocr", ocr_samples.CREPE_CARD_RAPIDOCR)]
    Recorder.reset([{"content": good}])
    r1 = run(llm_client.call_text_structuring(one[0][1], "live", readings=one))
    c1 = list(Recorder.calls)
    Recorder.reset([{"content": good}])
    run(llm_client.call_text_structuring(one[0][1], "", readings=None))
    c1_default = list(Recorder.calls)
    Recorder.reset([{"content": good}])
    run(llm_client.call_text_structuring(two[0][1], "live", readings=two))
    c2 = list(Recorder.calls)
    Recorder.reset([{"content": good}])
    run(llm_client.call_text_structuring("", "live", readings=[("", "a"), ("", "b")]))
    c2_unlabelled = list(Recorder.calls)
    save_prompt("P2_ocr_structuring", {"one": one, "two": two, "unlabelled": [["", "a"], ["", "b"]]},
                {"one_reading": c1[0], "one_reading_empty_model_name": c1_default[0], "two_readings": c2[0],
                 "two_unlabelled_readings": c2_unlabelled[0], "draft_from_reply": r1.model_dump(mode="json")})

    # P3: draft cleanup.
    Recorder.reset([{"content": good}])
    run(llm_client.clean_and_validate_draft(r1, "live"))
    c3 = list(Recorder.calls)
    Recorder.reset([{"content": good}])
    run(llm_client.clean_and_validate_draft(r1, ""))
    c3_default = list(Recorder.calls)
    save_prompt("P3_draft_cleanup", {"draft": r1.model_dump(mode="json")},
                {"model_live": c3[0], "empty_model_name": c3_default[0]})

    # P4: meal-plan select, 30 candidates (cut to 25), descriptions over 100 chars, None, core ids.
    candidates = []
    for i in range(1, 31):
        candidates.append({
            "id": str(i) if i % 7 else f"core_{i}", "source": "user" if i % 7 else "core",
            "title": f"Recipe {i} — “quoted” & <b>", "tags": ["dinner", "quick"] if i % 2 else [],
            "description": None if i % 5 == 0 else ("Long description " * 10 if i % 3 == 0 else f"Short {i}"),
            "prep_time_minutes": 0, "cook_time_minutes": None if i % 4 == 0 else i * 5,
        })
    slot = {"date": "2026-10-08", "meal_type": "dinner", "servings": 4, "tags": ["chicken"],
            "notes": "something easy", "is_meal_prep": False}
    prefs = {"diet": None, "excluded_ingredients": ["peanut"], "max_prep_minutes": None, "max_cook_minutes": 45}
    recent = [{"date": "2026-10-07", "meal_type": "dinner", "recipe_id": "3", "title": "Ünïcode", "tags": []}]
    Recorder.reset([{"content": "{\"ranked_recipes\": []}"}])
    run(llm_client.call_meal_plan_select(slot, prefs, recent, candidates))
    save_prompt("P4_meal_plan_select", {"slot": slot, "preferences": prefs, "recent_meals": recent,
                                        "candidates": candidates}, list(Recorder.calls))

    # P5: grocery SKU match, 65 candidates (cut to 60) and 30 ingredients (cut to 25).
    p5_candidates = [SimpleNamespace(id=i, ingredient_name=f"item {i}", product_name=(f"Product {i} (12 oz)" if i % 2 else None))
                     for i in range(1, 66)]
    p5_unmatched = [f"ingredient {i}" for i in range(1, 31)]
    messages = grocery_service.build_match_prompt(p5_candidates, p5_unmatched)
    Recorder.reset([{"content": "{\"matches\": []}"}])
    run(llm_client.match_grocery_items(messages))
    save_prompt("P5_grocery_match", {"candidates": [vars(c) for c in p5_candidates], "unmatched": p5_unmatched},
                list(Recorder.calls))

    counts.update({f"prompts/{k}.json": v for k, v in prompt_files.items()})

    # ── _index.json ──
    files = sorted(p for p in out.rglob("*.json") if p.name != "_index.json")
    index = {
        "legacy_repo": "jarvis-recipes-server",
        "legacy_sha": LEGACY_SHA,
        "generator": "tools/golden/export_recipes.py",
        "rows": counts,
        "sha256": {str(p.relative_to(out)): hashlib.sha256(p.read_bytes()).hexdigest() for p in files},
    }
    write("_index.json", index)


if __name__ == "__main__":
    main()
