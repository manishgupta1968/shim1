# How to test Shim 1 yourself

This guide shows you how to check that Shim 1 does what it should.
Follow the tests in order. Each one takes a few minutes.

## What Shim 1 is supposed to do (in one example)

A buyer app searches for "coffee". The request reaches the seller.
Before the seller's own software sees it, Shim 1 checks:

- Is the signature genuine, and from a registered buyer app?
- Did the ONDC Gateway also sign the search?
- Is the message complete, meant for us, and the right shape?
- Is it fresh, and not a repeat?

A good request goes through. A bad request is refused with a **NACK** (a "no") and a
reason. The seller's software never sees the bad request.

## What you need

| Need | Why |
|------|-----|
| A Linux or Mac terminal | The scripts are shell scripts |
| `go` (version 1.21 or newer) | Go downloads the exact version it needs (1.26.1) by itself |
| `gcc`, `unzip`, `curl` | Plugins are compiled with gcc; the build unzips the schemas |
| `git`, `python3` | Only for Test 3 (the ONDC referee) |
| Internet, the first time | To download Go libraries |

You do **not** need Docker, Redis, or an ONDC account. Use a **normal user**, not root
(see "If you run as root" at the end).

## Test 1: run everything with one command

From the top folder of the repository:

```bash
./run-shim1-demo.sh
```

The first run builds ONIX and all plugins. This takes a few minutes. Later runs are
faster if you add `--skip-build`.

**What you will see.** A table with one row per scenario. Here are three rows as an example:

```
#   RESULT  SCENARIO        EXPECTED                          GOT
1   PASS    valid_search    HTTP 200 ACK                      {"message":{"ack":{"status":"ACK"}}}
5   PASS    tampered_body   HTTP 401 NACK 30016 CORE-ERROR    {"message":{"ack":{"status":"NACK"}},"error":{"type":"CORE-ERROR","code":"30016",...
```

- **PASS** means Shim 1 answered exactly as expected.
- A bad request must be refused **and** must not reach the seller backend. The script
  checks both.

At the end you must see this line:

```
RESULT: ALL CHECKS PASSED
```

If you see `RESULT: 1 PART(S) FAILED`, look at the lines marked `FAIL` above it.

## Test 2: the unit tests

Unit tests check small pieces of code on their own. They are fast.

```bash
cd beckn-onix
go test ./...                       # everything (about 4 minutes)
go test ./pkg/plugin/implementation/ondcguard/ ./pkg/plugin/implementation/ondcregistry/ \
        ./pkg/plugin/implementation/signer/ ./pkg/plugin/implementation/reqpreprocessor/ \
        ./pkg/model/ ./core/module/handler/        # only the parts we wrote or changed
go test -race ./pkg/plugin/implementation/ondcguard/   # also look for unsafe parallel access
```

Every line must start with `ok`. Any line starting with `FAIL` is a problem.
(The `go test ./...` run as a normal user: 58 packages `ok`, none failed.)

## Test 3: ONDC's own code as the referee

Our scenarios sign requests using ONIX's code, and Shim 1 checks them with ONIX's code.
If both misunderstood ONDC in the same way, every test would still pass. So we also use
**ONDC's own sample code** (from their public repository) as an independent judge.

```bash
./run-shim1-demo.sh --skip-build --with-referee
```

The first time, it downloads ONDC's repository and sets up a Python environment.
It then checks, in both directions:

| Check | Meaning |
|-------|---------|
| ONDC signs, Shim 1 verifies | A request signed with ONDC's code is accepted |
| Shim 1 signs, ONDC verifies | The `on_search` callback we sign is accepted by ONDC's code |
| Shim 1 signs a registry lookup, ONDC verifies | Only in `http` registry mode (Test 4) |
| `[control]` lines | ONDC's code must **refuse** a changed message and a wrong key. This shows the judge is not simply saying yes to everything |

Expected end: `9 of 9 referee checks passed` (7 of 7 in the default registry mode).

## Test 4: the registry over HTTP

By default the registry is a JSON file. This test uses a small server that behaves like
the real ONDC registry: it answers **only signed lookups**.

```bash
./run-shim1-demo.sh --skip-build --registry-mode http --with-referee
```

You must see `PASS  every registry lookup was signed and accepted`.

## Try things by hand

Start everything and leave it running:

```bash
./run-shim1-demo.sh --skip-build --keep-running
cd beckn-onix
```

**1. See the list of scenarios, and run just one with the full answer.**

```bash
./shim1demo client --list
./shim1demo client --only tampered_body --verbose
```

**2. Send a sample payload.** The samples are in `config/shim1/samples/` (read the `README.md` there).

```bash
./shim1demo client --payload config/shim1/samples/03-typical-retail-1.2.0-search.json --expect ack
./shim1demo client --payload config/shim1/samples/01-ondc-official-signing-sample.json --expect nack
```

The second one is ONDC's own old sample. Shim 1 refuses it and tells you why:
`city Kochi is not served here; core_version 0.9.1 is not supported; ...`

**3. Send your own payload.** Copy a sample, change something, and send it.
For example, change `"city": "std:080"` to `"city": "std:999"` and use `--expect nack`.
The client signs the request for you and refreshes the time and ids.

**4. Change the registry and watch what happens.** Make the buyer app "not yet subscribed":

```bash
# in config/shim1/generated/registry.json, change the buyer entry's "status" to "INITIATED"
pkill -x server
./server --config=config/shim1/generated/shim1.yaml > /tmp/shim1.log 2>&1 &
until curl -fsS localhost:8081/health >/dev/null 2>&1; do sleep 1; done   # wait until Shim 1 is ready
./shim1demo client --only valid_search --verbose
```

The valid search is now **refused**: the answer says `no subscriber found with given credentials`,
because the buyer is not `SUBSCRIBED`. The table shows `FAIL` for this row. That is the
proof: the scenario expects a good search to be accepted, and Shim 1 refused it.
Set the status back to `"SUBSCRIBED"` and restart Shim 1 the same way, to see it pass again.

**5. Read Shim 1's own log.** The script prints `Logs: /tmp/tmp.XXXX`. Open `shim1.log`
there. A refusal shows a line such as `"error":"duplicate request: message_id already processed"`.

**6. Stop everything.**

```bash
pkill -x server; pkill -x shim1demo
```

## If something fails, what does it mean?

| You see | It usually means |
|---------|------------------|
| `tampered_body` or `expired_signature` fails | Signature checking is broken |
| `gateway_*` scenario fails | The Gateway check (`ondcguard`) is broken |
| `sender_*` scenario fails | The Registry lookup or role check is broken |
| `wrong_bpp_id`, `unsupported_*`, `missing_context_field` fails | The policy file `config/shim1/ondc-policy.rego` or its settings changed |
| `stale_request` or `replayed_message_id` fails | The time or repeat check in `ondcguard` is broken |
| `empty_body` or `body_not_json` fails | The `ondc: "true"` setting is missing from the config |
| `non_ondc_request_keeps_old_shape` fails | A change leaked into non-ONDC traffic. This is serious. |
| A referee check fails | Our signing no longer matches ONDC's. This is serious. |
| `Port 8081 is already in use` | An old run is still going: `pkill -x server; pkill -x shim1demo` |
| `Shim 1 did not start` | Read the log file path printed above it |

## What is NOT tested

Be clear about the limits of these tests:

- **The real ONDC Registry.** We never called it. We tested against a stub that behaves as ONDC's
  tools show. The first real call may reveal a difference.
- **The real ONDC Gateway.** The test client plays the Gateway.
- **The official ONDC schemas.** We use small hand-written ones.
- **ONDC's newer 2.0.x layout.** It is refused (see `docs/ondc-preprocessing.md`, point 7).
- **Many requests at once (load).** Only a small concurrency test for the repeat check.

## If you run as root

Three existing ONIX tests fail when run as root. They create a folder that nobody may open
and expect "permission denied". Root can open any folder, so the test fails by itself.
They pass as a normal user. See `docs/ondc-preprocessing.md`, point 10.
