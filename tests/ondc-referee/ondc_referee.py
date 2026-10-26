"""Cross-check Shim 1 against ONDC's OWN sample code (not ours).

ONDC code used, unmodified: reference-implementations/utilities/signing_and_verification/python/cryptic_utils.py
  create_authorisation_header()  -> ONDC signs a request
  verify_authorisation_header()  -> ONDC verifies a signed message

Direction 1: ONDC signs /search (buyer + gateway)  -> Shim 1 must accept it.
Direction 2: Shim 1 signs on_search                -> ONDC's code must verify it.
"""
import json, os, sys, time, uuid, urllib.request, urllib.error
from datetime import datetime, timezone

# Where ONDC's own sample code is. tests/ondc-referee/run.sh clones it for you.
ONDC_DIR = os.environ.get("ONDC_PY_DIR", "")
if not os.path.isfile(os.path.join(ONDC_DIR, "cryptic_utils.py")):
    sys.exit("Set ONDC_PY_DIR to ONDC's signing_and_verification/python folder (run tests/ondc-referee/run.sh).")
os.chdir(ONDC_DIR)
os.environ["REQUEST_BODY_PATH"] = os.path.join(ONDC_DIR, "request_body_raw_text.txt")
sys.path.insert(0, ONDC_DIR)
import cryptic_utils as ondc  # ONDC's own file

SHIM1 = "http://localhost:8081"
BAP = "http://localhost:9002"
BACKEND = "http://localhost:9001"
KEYS_FILE = os.environ.get("SHIM1_KEYS", os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "beckn-onix", "config", "shim1", "generated", "keys.json"))
KEYS = json.load(open(KEYS_FILE))
import base64
if len(base64.b64decode(KEYS["buyer"]["private"])) != 64:
    sys.exit("keys.json holds 32-byte seeds. ONDC's sample code needs its own 64-byte key form: run `shim1demo keys -keyformat ondc` (run-shim1-demo.sh does this).")

results = []


def check(name, ok, detail=""):
    results.append(ok)
    print(f"{'PASS' if ok else 'FAIL'}  {name}  {detail}")


def ondc_header(who, body_text, created, expires):
    p = KEYS[who]
    os.environ["PRIVATE_KEY"] = p["private"]
    os.environ["SUBSCRIBER_ID"] = p["id"]
    os.environ["UNIQUE_KEY_ID"] = p["keyId"]
    h = ondc.create_authorisation_header(request_body=body_text, created=created, expires=expires)
    # ONDC's sample wraps the whole header in one extra pair of quotes; remove it.
    return h[1:-1] if h.startswith('"') and h.endswith('"') else h


def search_body():
    ctx = {
        "domain": "ONDC:RET10", "country": "IND", "city": "std:080", "action": "search",
        "core_version": "1.2.0", "bap_id": KEYS["buyer"]["id"], "bap_uri": "http://localhost:9002/bap",
        "transaction_id": uuid.uuid4().hex, "message_id": uuid.uuid4().hex,
        "timestamp": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"), "ttl": "PT30S",
    }
    msg = {"intent": {"item": {"descriptor": {"name": "coffee"}}}}
    # ONDC's sample minifies the JSON, then signs that text. We send exactly that text.
    return ctx, json.dumps({"context": ctx, "message": msg}, separators=(",", ":"))


def post(path, body_text, headers):
    req = urllib.request.Request(SHIM1 + path, data=body_text.encode(), headers={"Content-Type": "application/json", **headers})
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def get_json(url):
    with urllib.request.urlopen(url, timeout=10) as r:
        return json.load(r)


def wait_callback(message_id):
    for _ in range(50):
        for it in get_json(BAP + "/bap/received"):
            if it["message_id"] == message_id and it["action"] == "on_search":
                return it
        time.sleep(0.1)
    return None


def send_search(variant_name, tweak_header=None):
    ctx, body = search_body()
    now = int(time.time())
    created, expires = now, now + 300
    buyer = ondc_header("buyer", body, created, expires)
    gateway = ondc_header("gateway", body, created, expires)
    if tweak_header:
        buyer, gateway = tweak_header(buyer), tweak_header(gateway)
    status, answer = post("/bpp/receiver/search", body, {"Authorization": buyer, "X-Gateway-Authorization": gateway})
    ok = status == 200 and '"ACK"' in answer
    check(f"[ONDC signs -> Shim 1 verifies] {variant_name}", ok, f"HTTP {status} {answer[:90]}")
    return ctx, ok


# ---- Direction 1: ONDC signs, Shim 1 verifies
ctx1, ok1 = send_search("header exactly as ONDC's sample writes it")
ctx2, ok2 = send_search('header with headers="(created)(expires)digest" (no spaces, as in ONDC text)',
                        lambda h: h.replace('headers="(created) (expires) digest"', 'headers="(created)(expires)digest"'))

# negative control: ONDC-signed, then body changed -> Shim 1 must refuse
ctx, body = search_body()
now = int(time.time())
b, g = ondc_header("buyer", body, now, now + 300), ondc_header("gateway", body, now, now + 300)
status, answer = post("/bpp/receiver/search", body.replace("coffee", "tea"), {"Authorization": b, "X-Gateway-Authorization": g})
check("[control] ONDC-signed but body changed -> Shim 1 refuses", status == 401 and "30016" in answer, f"HTTP {status}")

# ---- Direction 2: Shim 1 signs, ONDC verifies
seller_pub = KEYS["seller"]["public"]
for label, ctx in (("first search", ctx1), ("second search", ctx2)):
    cb = wait_callback(ctx["message_id"])
    if cb is None:
        check(f"[Shim 1 signs -> ONDC verifies] on_search for {label}", False, "no callback arrived")
        continue
    good = ondc.verify_authorisation_header(cb["authorization"], request_body_str=cb["raw_body"], public_key=seller_pub)
    check(f"[Shim 1 signs -> ONDC verifies] on_search for {label}", good is True, f"header: {cb['authorization'][:70]}...")
    if label == "first search":
        # negative control: the referee must say NO to a changed body and to a wrong key
        changed = ondc.verify_authorisation_header(cb["authorization"], request_body_str=cb["raw_body"].replace("coffee", "tea"), public_key=seller_pub)
        wrongkey = ondc.verify_authorisation_header(cb["authorization"], request_body_str=cb["raw_body"], public_key=KEYS["buyer"]["public"])
        check("[control] ONDC's verifier rejects a changed callback body", changed is False)
        check("[control] ONDC's verifier rejects the wrong public key", wrongkey is False)

# ---- Direction 3 (only when Shim 1 talks to the HTTP registry stub): Shim 1 signs a registry
# lookup request -> ONDC's code must verify it, using the seller's public key.
try:
    stats = get_json("http://localhost:9003/registry/stats")
except Exception:
    stats = None
if stats and stats.get("last_authorization"):
    good = ondc.verify_authorisation_header(stats["last_authorization"], request_body_str=stats["last_body"], public_key=KEYS["seller"]["public"])
    check("[Shim 1 signs a registry lookup -> ONDC verifies]", good is True, "body: " + stats["last_body"][:70])
    bad = ondc.verify_authorisation_header(stats["last_authorization"], request_body_str=stats["last_body"] + " ", public_key=KEYS["seller"]["public"])
    check("[control] ONDC's verifier rejects a changed lookup body", bad is False)
else:
    print("(skipped: no HTTP registry stub on :9003)")

print(f"\n{sum(results)} of {len(results)} referee checks passed")
sys.exit(0 if all(results) else 1)
