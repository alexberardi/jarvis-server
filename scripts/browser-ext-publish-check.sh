#!/usr/bin/env bash
# Check of browser-ext-publish.sh without the stores: a fake Chrome Web Store API and OAuth token
# endpoint (Node), and a stand-in npx for web-ext. It checks the service-account token (the JWT
# verifies against the key's public half), a version the store already has (no upload), a new
# version (upload of the store zip, polled until processed, then publish), a failed upload (no
# publish), and AMO's answers: submitted, "already exists" (a skip) and another error (a failure).
#
#   scripts/browser-ext-publish-check.sh
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT=$HERE/browser-ext-publish.sh
fail() { echo "browser-ext-publish-check: $*" >&2; exit 1; }
has() { grep -qF -- "$2" "$1" || { echo "--- $1:" >&2; cat "$1" >&2; fail "$1 lacks: $2"; }; }
lacks() { if grep -qF -- "$2" "$1"; then echo "--- $1:" >&2; cat "$1" >&2; fail "$1 has: $2"; fi; }

T=$(mktemp -d)
trap 'kill "$SRV" 2>/dev/null || true; rm -rf "$T"' EXIT
VERSION=$(node -p 'require(process.argv[1]).version' "$HERE/../internal/agent/browser/extension/manifest.json")

# The fake store. $T/state.json is what fetchStatus returns ({{V}} = the source version) and how
# uploads end; every request is appended to $T/requests.
node - "$T" <<'EOF' &
const http = require("http"), fs = require("fs"), crypto = require("crypto");
const dir = process.argv[2];
const st = () => JSON.parse(fs.readFileSync(dir + "/state.json", "utf8"));
let polls = 0;
http.createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    const body = Buffer.concat(chunks);
    fs.appendFileSync(dir + "/requests", `${req.method} ${req.url} ${req.headers.authorization ?? ""} ${req.headers["content-type"] ?? ""} ${body.length}\n`);
    const json = (o, code = 200) => { res.writeHead(code, { "Content-Type": "application/json" }); res.end(JSON.stringify(o)); };
    const s = st();
    if (req.url === "/token") {
      const assertion = new URLSearchParams(body.toString()).get("assertion");
      const [h, p, sig] = assertion.split(".");
      const ok = crypto.verify("RSA-SHA256", Buffer.from(h + "." + p), fs.readFileSync(dir + "/pub.pem"), Buffer.from(sig, "base64url"));
      const claims = JSON.parse(Buffer.from(p, "base64url"));
      if (!ok || claims.scope !== "https://www.googleapis.com/auth/chromewebstore" || claims.iss !== "ci@example.iam.gserviceaccount.com")
        return json({ error: "invalid_grant" }, 400);
      return json({ access_token: "minted-token" });
    }
    if (req.headers.authorization !== "Bearer " + s.token) return json({ error: "unauthenticated" }, 401);
    if (req.url.endsWith(":fetchStatus")) {
      polls++;
      return json({ ...JSON.parse(JSON.stringify(s.status).replaceAll("{{V}}", s.version)),
        lastAsyncUploadState: polls > 1 ? s.uploadEnds : "IN_PROGRESS" });
    }
    if (req.url.includes(":upload")) {
      fs.writeFileSync(dir + "/uploaded.zip", body);
      polls = 1;
      return json({ uploadState: "IN_PROGRESS" });
    }
    if (req.url.endsWith(":publish")) return json({ state: "PENDING_REVIEW", warningInfo: { warnings: [{ reason: "R", description: "a warning" }] } });
    json({ error: "not found" }, 404);
  });
}).listen(0, "127.0.0.1", function () { fs.writeFileSync(dir + "/port", String(this.address().port)); });
EOF
SRV=$!
for _ in $(seq 100); do [ -s "$T/port" ] && break; sleep 0.05; done
[ -s "$T/port" ] || fail "fake store didn't start"
API=http://127.0.0.1:$(cat "$T/port")

# Stand-in npx: web-ext lint passes; web-ext sign answers $FAKE_SIGN.
mkdir -p "$T/bin"
cat >"$T/bin/npx" <<'EOF'
#!/bin/sh
case "$*" in
  *" sign "*) echo "sign $*" >>"$FAKE_LOG"
    case "$FAKE_SIGN" in
      ok) echo "Your add-on has been submitted for review."; exit 0 ;;
      exists) echo 'WebExtError: Submission failed (2): 409 {"version": ["Version 0.3.9 already exists."]}'; exit 1 ;;
      *) echo "WebExtError: 401 Unauthorized"; exit 1 ;;
    esac ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$T/bin/npx"

state() { # state PUBLISHED_VERSION UPLOAD_ENDS
  node -e '
    const [v, have, ends] = process.argv.slice(1);
    const rev = (x) => ({ state: "PUBLISHED", distributionChannels: [{ deployPercentage: 100, crxVersion: x }] });
    console.log(JSON.stringify({ token: "minted-token", version: v, uploadEnds: ends,
      status: { itemId: "abkbjmelelgelkhneobebgoilejlgbob", publishedItemRevisionStatus: rev(have) } }));
  ' "$VERSION" "$1" "$2" >"$T/state.json"
  : >"$T/requests"
  rm -f "$T/uploaded.zip"
}

openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$T/key.pem" 2>/dev/null
openssl pkey -in "$T/key.pem" -pubout -out "$T/pub.pem"
KEY=$(node -e 'console.log(JSON.stringify({ client_email: "ci@example.iam.gserviceaccount.com", private_key: require("fs").readFileSync(process.argv[1], "utf8") }))' "$T/key.pem")
chrome() {
  CWS_API=$API CWS_TOKEN_URL=$API/token CWS_SERVICE_ACCOUNT_KEY=$KEY CWS_PUBLISHER_ID=pub1 \
    "$SCRIPT" chrome >"$T/out" 2>&1
}

# 1. The store already has this version: no upload.
state "$VERSION" SUCCEEDED
chrome || { cat "$T/out" >&2; fail "chrome, version already published: failed"; }
has "$T/out" "already has $VERSION"
has "$T/requests" "POST /token"
has "$T/requests" "GET /v2/publishers/pub1/items/abkbjmelelgelkhneobebgoilejlgbob:fetchStatus Bearer minted-token"
lacks "$T/requests" ":upload"

# 2. An older version in the store: upload the zip, poll, publish.
state 0.0.1 SUCCEEDED
chrome || { cat "$T/out" >&2; fail "chrome, new version: failed"; }
has "$T/requests" "POST /upload/v2/publishers/pub1/items/abkbjmelelgelkhneobebgoilejlgbob:upload?uploadType=media Bearer minted-token application/zip"
has "$T/requests" "POST /v2/publishers/pub1/items/abkbjmelelgelkhneobebgoilejlgbob:publish"
has "$T/out" "$VERSION submitted (PENDING_REVIEW)"
has "$T/out" "warning: R: a warning"
unzip -p "$T/uploaded.zip" manifest.json | grep -qF "\"version\": \"$VERSION\"" || fail "uploaded zip isn't the $VERSION store build"
if unzip -p "$T/uploaded.zip" manifest.json | grep -q '"key"'; then fail "uploaded zip kept the manifest key"; fi

# 3. The upload fails: no publish.
state 0.0.1 FAILED
if chrome; then fail "chrome, failed upload: succeeded"; fi
has "$T/out" "ended as 'FAILED'"
lacks "$T/requests" ":publish"

# 4. AMO.
export FAKE_LOG=$T/sign.log
firefox() { PATH=$T/bin:$PATH AMO_JWT_ISSUER=user:1:2 AMO_JWT_SECRET=s FAKE_SIGN=$1 "$SCRIPT" firefox >"$T/out" 2>&1; }
firefox ok || { cat "$T/out" >&2; fail "firefox, submitted: failed"; }
has "$T/out" "$VERSION submitted to the listed channel"
has "$FAKE_LOG" "--channel=listed --approval-timeout=0"
firefox exists || { cat "$T/out" >&2; fail "firefox, already exists: failed"; }
has "$T/out" "AMO already has $VERSION"
if firefox other; then fail "firefox, other error: succeeded"; fi
has "$T/out" "401 Unauthorized"

echo "browser-ext-publish-check: ok"
