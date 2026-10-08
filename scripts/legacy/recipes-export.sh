#!/usr/bin/env bash
# Export the legacy recipes data for `jarvisd import-recipes` (docs/recipes/00-inventory.md §13).
#
# STRICTLY READ-ONLY on the legacy host: it runs `docker ps`, `docker exec … printenv`,
# `docker exec … psql` with default_transaction_read_only=on and SELECTs only, and
# `docker cp CONTAINER:/app/media/<name> -` (works on a stopped container too). Nothing is written
# there, not even a temp file: every byte streams back to this machine. Only the bundle is
# written, locally, mode 0600.
#
#   scripts/legacy/recipes-export.sh [--ssh USER@HOST] [--out FILE.tar.gz] [options]
#
# Without --ssh it runs the docker commands locally (on the legacy host itself).
#
# Options:
#   --ssh USER@HOST            run the docker commands over ssh
#   --out FILE                 bundle path (default ./recipes-export-<UTC time>.tar.gz); must not exist
#   --pg-container NAME        Postgres container (default jarvis-postgres)
#   --pg-user USER             Postgres role (default: the container's POSTGRES_USER, else postgres)
#   --recipes-db NAME          legacy recipes database (default jarvis_recipes)
#   --auth-db NAME             legacy auth database (default jarvis_auth)
#   --recipes-container NAME   recipes API container holding /app/media (default: auto-detect
#                              jarvis-recipes-server or jarvis-recipes-server-recipes-api-1)
#   --media-dir PATH           media directory inside it (default /app/media)
#
# The bundle holds: one JSON array per table (recipes, ingredients, steps, tags, recipe_tags,
# meal_plans, meal_plan_items, staples, grocery_sku_map), the legacy auth users' id + email
# (nothing else), households (id, name) and memberships (household_id, user_id, role), the
# editor photos the recipes refer to, and manifest.json with a sha256 per file. It contains
# emails and household data: keep it private and delete it after the import.
set -euo pipefail

SSH="" OUT="" PG=jarvis-postgres PGUSER_="" RDB=jarvis_recipes ADB=jarvis_auth RC="" MEDIA=/app/media
HEAD=e1f2a3b4c5d6
umask 077
while [ $# -gt 0 ]; do
  case $1 in
    --ssh) SSH=$2; shift 2 ;;
    --out) OUT=$2; shift 2 ;;
    --pg-container) PG=$2; shift 2 ;;
    --pg-user) PGUSER_=$2; shift 2 ;;
    --recipes-db) RDB=$2; shift 2 ;;
    --auth-db) ADB=$2; shift 2 ;;
    --recipes-container) RC=$2; shift 2 ;;
    --media-dir) MEDIA=$2; shift 2 ;;
    -h|--help) sed -n '2,31p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "recipes-export: unknown argument $1" >&2; exit 2 ;;
  esac
done

say() { echo "recipes-export: $*" >&2; }
die() { say "$*"; exit 1; }

OUT=${OUT:-./recipes-export-$(date -u +%Y%m%dT%H%M%SZ).tar.gz}
[ -e "$OUT" ] && die "$OUT exists; pick another --out"

# R runs a command on the legacy host (over ssh when --ssh is set). stdout streams back.
R() {
  if [ -n "$SSH" ]; then
    local q
    q=$(printf '%q ' "$@")
    ssh -o BatchMode=yes "$SSH" "PATH=\"\$PATH:/usr/local/bin:/opt/homebrew/bin\"; $q"
  else
    "$@"
  fi
}

if [ -z "$PGUSER_" ]; then
  PGUSER_=$(R docker exec "$PG" printenv POSTGRES_USER 2>/dev/null | tr -d '\r' || true)
  PGUSER_=${PGUSER_:-postgres}
fi

# psql_ro DB SQL: one read-only query, unaligned, tuples only.
psql_ro() {
  R docker exec -e PGOPTIONS=-cdefault_transaction_read_only=on "$PG" \
    psql -X -q -v ON_ERROR_STOP=1 -U "$PGUSER_" -d "$1" -At -c "$2"
}

WORK=$(mktemp -d "${TMPDIR:-/tmp}/recipes-export.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
chmod 700 "$WORK"
mkdir "$WORK/media"

head_now=$(psql_ro "$RDB" "SELECT version_num FROM alembic_version" | tr -d '\r')
[ "$head_now" = "$HEAD" ] || die "legacy recipes schema is at '$head_now', not $HEAD: this exporter and jarvisd import-recipes read $HEAD only"
say "source ${SSH:-local}, $PG ($PGUSER_), recipes schema $head_now"

# table FILE DB SELECT…: the rows as one JSON array, in the SELECT's order.
table() {
  local file=$1 db=$2 sel=$3
  psql_ro "$db" "SELECT coalesce(json_agg(t), '[]'::json) FROM ($sel) t" > "$WORK/$file"
  [ -s "$WORK/$file" ] || die "empty output for $file"
}

table recipes.json "$RDB" "SELECT id, user_id, household_id, title, description, image_url, source_type, source_url, servings, total_time_minutes, created_at, updated_at FROM recipes ORDER BY id"
table ingredients.json "$RDB" "SELECT id, recipe_id, text, quantity_display, quantity_value, unit FROM ingredients ORDER BY id"
table steps.json "$RDB" "SELECT id, recipe_id, step_number, text FROM steps ORDER BY id"
table tags.json "$RDB" "SELECT id, name FROM tags ORDER BY id"
# A recipe's tags come back in attach (physical) order on legacy: keep it.
table recipe_tags.json "$RDB" "SELECT recipe_id, tag_id FROM recipe_tags ORDER BY ctid"
table meal_plans.json "$RDB" "SELECT id, user_id, household_id, name, start_date, created_at FROM meal_plans ORDER BY id"
table meal_plan_items.json "$RDB" "SELECT id, meal_plan_id, recipe_id, date, meal_type FROM meal_plan_items ORDER BY id"
table staples.json "$RDB" "SELECT id, user_id, household_id, name, created_at FROM staples ORDER BY id"
table grocery_sku_map.json "$RDB" "SELECT id, user_id, household_id, retailer, ingredient_name, sku, product_name, unit_size, source, created_at, updated_at FROM grocery_sku_map ORDER BY id"
table auth_users.json "$ADB" "SELECT id, email FROM users ORDER BY id"
table auth_households.json "$ADB" "SELECT id, name FROM households ORDER BY id"
table auth_household_memberships.json "$ADB" "SELECT household_id, user_id, role FROM household_memberships ORDER BY id"

# The survey the cutover runbook asks for: how recipes point at their photos.
say "image_url: $(psql_ro "$RDB" "SELECT 'none=' || count(*) FILTER (WHERE coalesce(image_url, '') = '') || ' media=' || count(*) FILTER (WHERE image_url LIKE '/media/%') || ' absolute=' || count(*) FILTER (WHERE image_url ~ '^https?://') || ' other=' || count(*) FILTER (WHERE image_url <> '' AND image_url NOT LIKE '/media/%' AND image_url !~ '^https?://') FROM recipes" | tr -d '\r')"

# Editor photos: every /media/<name> a recipe refers to (relative, or absolute to any host;
# the import decides which absolute ones are the legacy server's).
names=$(psql_ro "$RDB" "SELECT DISTINCT substring(image_url FROM '/media/([^/?#]+)\$') FROM recipes WHERE image_url ~ '/media/[^/?#]+\$' ORDER BY 1" | tr -d '\r')
found=0 missing=0
if [ -n "$names" ]; then
  if [ -z "$RC" ]; then
    RC=$(R docker ps -a --format '{{.Names}}' | tr -d '\r' | grep -E '^jarvis-recipes-server(-recipes-api-1)?$' | head -n1 || true)
    [ -n "$RC" ] || die "recipes refer to /media photos but no recipes container was found; pass --recipes-container"
  fi
  say "photos from $RC:$MEDIA"
  while IFS= read -r n; do
    [ -n "$n" ] || continue
    if ! printf '%s' "$n" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]*$'; then
      say "  skipping unsafe photo name: $n"; missing=$((missing + 1)); continue
    fi
    if R docker cp "$RC:$MEDIA/$n" - 2>/dev/null | tar -xf - -C "$WORK/media" 2>/dev/null && [ -f "$WORK/media/$n" ]; then
      found=$((found + 1))
    else
      say "  missing on the host: $n (the import drops that image_url)"; missing=$((missing + 1))
    fi
  done <<< "$names"
fi
say "photos: $found copied, $missing missing"

# manifest.json with a sha256 per file.
if command -v sha256sum >/dev/null; then SUM=(sha256sum); else SUM=(shasum -a 256); fi
{
  printf '{\n  "format": "jarvis-recipes-export/1",\n'
  printf '  "exported_at": "%s",\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf '  "source": "%s",\n' "${SSH:-$(hostname)} $PG/$RDB"
  printf '  "recipes_alembic_head": "%s",\n  "files": {\n' "$head_now"
  first=1
  while IFS= read -r f; do
    [ $first = 1 ] || printf ',\n'
    first=0
    printf '    "%s": "%s"' "$f" "$(cd "$WORK" && "${SUM[@]}" "$f" | cut -d' ' -f1)"
  done < <(cd "$WORK" && find . -type f ! -name manifest.json | sed 's#^\./##' | LC_ALL=C sort)
  printf '\n  }\n}\n'
} > "$WORK/manifest.json"

for f in recipes meal_plans meal_plan_items staples grocery_sku_map auth_users; do
  say "  $f: $(grep -o '"id"' "$WORK/$f.json" | wc -l | tr -d ' ') rows"
done

tar -czf "$OUT" -C "$WORK" .
chmod 600 "$OUT"
say "wrote $OUT (contains emails: keep it private, delete it after the import)"
