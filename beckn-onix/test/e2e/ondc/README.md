# ONDC end-to-end tests

These tests send **realistic ONDC messages through real ONIX processes** and check what comes out the other side.
They cover **all 20 Beckn endpoints** — the 10 requests `search`, `select`, `init`, `confirm`, `status`, `track`,
`update`, `cancel`, `rating`, `support` and their 10 callbacks `on_search` … `on_support` — for two ONDC domains:

| Domain | Context | Source of the messages and schemas |
| --- | --- | --- |
| **ONDC:TRV10** (mobility, ride hailing) | `version: 2.0.0` | schemas shipped in `schemas.zip` (`ondc_trv10/v2.0.0`) |
| **ONDC:RET14** (retail, electronics) | `core_version: 1.2.5` | official ONDC sample logs and the ONDC log validator (`ondc_ret14/v1.2.5`) |

## Run them

```bash
cd beckn-onix
go mod download                      # once
go test ./test/e2e/ondc/... -count=1 -timeout 15m -v
```

You need Go (the version in `go.mod`), `CGO_ENABLED=1` and a C compiler (Go plugins), and nothing else: Redis is
replaced by an in-process one. The first run builds the server and 8 plugins, which takes **2–4 minutes**; later runs
take about **20 seconds**.

| Setting | Effect |
| --- | --- |
| `-short` | skip the end-to-end tests, run only the fixture self-checks (no build, about a second) |
| `ONIX_E2E_BIN_DIR=/path` | reuse a directory that holds `server` and `plugins/*.so` instead of building |
| `ONIX_E2E_KEEP=1` | keep the temp directory with the rendered configs and the ONIX logs |

When a test fails it prints the tail of both ONIX logs.

## What happens in a test

```
buyer app ──▶ BAP ONIX /bap/caller/<action> ──signs──▶ BPP ONIX /bpp/receiver/<action> ──verifies──▶ seller backend
                           (search goes through the gateway, which adds its own signature)
seller app ─▶ BPP ONIX /bpp/caller/on_<action> ─signs─▶ BAP ONIX /bap/receiver/on_<action> ─verifies─▶ buyer backend
```

There are **two real ONIX processes per domain**: one acts as the buyer's network participant (BAP), one as the
seller's (BPP). Each runs the real `server` and the real plugins from YAML (`testdata/config/`). Around them, inside
the test process: Redis (miniredis), the registry (`POST /lookup`), the gateway, and a recording buyer app and seller
app. Keys are generated for every run; nothing secret is committed.

| Test | What it proves |
| --- | --- |
| `TestRequestLegs` | each of the 10 requests, sent unsigned to the buyer's ONIX, is schema-checked, signed, routed and arrives at the seller app **byte for byte**, with a signature that an independent verifier accepts. `search` goes through the gateway and carries `X-Gateway-Authorization` too. |
| `TestCallbackLegs` | the same for the 10 callbacks, seller → buyer, signed by the seller. |
| `TestInteropIndependentSigner` | ONIX's receivers accept messages signed by the test's own ONDC signer (`ondcsign_test.go`, no ONIX code): ONIX speaks the ONDC signature format in both directions. |
| `TestJourney` | one whole order in one transaction; every callback has the request's `transaction_id` and `message_id`; unsolicited `on_status` / `on_update` work too. |
| `TestNegativeSignatures` | tampered body, missing header, wrong key, unknown / unauthorised / expired / unsubscribed signer, expired or future signature, bad gateway signature → HTTP 401 and an ONDC NACK; nothing reaches the app. |
| `TestNegativeSchemas` | 25 messages that break an ONDC rule (wrong enum, missing field, extra context key …) → HTTP 400 and an ONDC NACK, at the caller and (for some) at the receiver. |
| `TestNegativeRouting` | unknown domain, version or endpoint, empty body, body that is not JSON (all answered with an ONDC NACK). |
| `TestFixturesAreONDCValid` | every fixture passes the ONDC schemas ONIX ships, the context is right, one order id and provider id run through the journey, and every quote adds up. Needs no ONIX. |
| `TestSchemasRejectBrokenFixtures` | a broken fixture really fails that validation (so the check is not vacuous). |
| `TestEveryEndpointIsCovered` | fixtures, routing and schemas all list the same 20 endpoints. Add an endpoint to one and this fails until you add it to the others. |

ONDC answers are checked as ONDC defines them: a request is acknowledged by the receiving app with
`{"message":{"ack":{"status":"ACK"}}}`; a refusal by ONIX is
`{"message":{"ack":{"status":"NACK"}},"error":{"type":…,"code":…,"message":…}}` with ONDC's numeric code.

## Fixtures

`testdata/fixtures/<domain>/<action>.json`, one journey per domain with consistent ids. Placeholders
`{{TRANSACTION_ID}}`, `{{MESSAGE_ID}}`, `{{TIMESTAMP}}`, `{{BAP_URI}}`, `{{BPP_URI}}` are filled in per test.

* **TRV10** — written from the shipped ONDC TRV10 schemas (Bengaluru rides, `AUTO_RICKSHAW` / `CAB`, INR fares,
  `SETTLEMENT_TERMS` / `BUYER_FINDER_FEES` payment tags).
* **RET14** — `search`, `on_search`, `select`, `on_select`, `init`, `on_init`, `confirm`, `on_confirm`, `cancel` are
  adapted from the official ONDC sample logs
  ([ONDC-Official/retail-v1.2.5-logs](https://github.com/ONDC-Official/retail-v1.2.5-logs), Pramaan run) with every
  real seller, buyer, brand and address replaced by fictional ones, and the catalogue cut to one provider with two
  items. `status`, `on_status`, `track`, `on_track`, `update`, `on_update`, `on_cancel` are written to satisfy the ONDC
  validator schemas. `rating`, `on_rating`, `support`, `on_support` have **no official ONDC validator schema**; they
  follow the Beckn core definitions and are the least certain fixtures.
* All identities use `example.com` names.

## Schemas (`schemas.zip`)

The tests use the schemas ONIX ships. They were changed as follows (rebuild with `tools/rebuild_schemas_zip.py`):

* Fixed `core/v1.1.0` `status` and `track`: `order_id` / `ref_id` were an Order *object*; Beckn core 1.1.0 (and ONDC
  TRV10) define strings, so `/status` could never validate.
* Fixed `ondc_trv10/v2.0.0` `on_confirm`: it forbade `order.id` (a `$ref` copied from `confirm`), but `on_confirm` is
  where the seller assigns the order id.
* Added `ondc_ret14/v1.2.5` (20 schemas). 16 are converted from the ISC-licensed
  [ONDC log-validation-utility](https://github.com/ONDC-Official/log-validation-utility) (branch `1.2.5`,
  `schema/Retail`) by `tools/ts2json.py`, a parser that reads the TypeScript as data and **never runs it**. Three
  upstream quirks are normalised so the schemas work outside the ONDC tool: JavaScript-only regular expressions are
  rewritten for Go, a `required` list written inside `properties` is dropped (the ONDC tool ignores it too), and
  `oneOf` made of `if/then` branches is read as the "if A then B" it means. Of the 20 official log files for these
  actions, 16 pass; `search` (the 1.2.5 `bap_features` tag) and `on_status` (older rules) are rejected because the
  validator's schemas lag behind its own 1.2.5 logs, so the fixtures avoid those two constructs.

## What ONIX does for ONDC

ONDC support in ONIX comes from two pieces of work that are merged here.

**From Shim 1** (`docs/ondc-preprocessing.md`):

* ONDC Retail sends its protocol version as `context.core_version`; ONIX reads it (router, schema validator, handler)
  and treats such a request as ONDC.
* For ONDC requests ONIX answers in the Beckn 1.x / ONDC format: `{"message":{"ack":{"status":"ACK"}}}` and, for a
  refusal, `{"message":{"ack":{"status":"NACK"}},"error":{"type":…,"code":…,"message":…}}` with ONDC's numeric codes
  (`30016` when a sender cannot be verified, `30000` for an invalid message, `31001` for an internal error).
* A module can mark all its traffic as ONDC with `ondc: "true"` on its `reqpreprocessor`, so even a body that is empty
  or not JSON is refused in the ONDC shape.

**Added with these tests:**

* **ONDC TRV10 needs `ondc: "true"`.** TRV10 has `context.version 2.0.0` and no `core_version`, so without the setting
  ONIX takes it for Beckn v2 and answers in the Beckn v2 format and signs ACKs. With the setting, ONDC requests are
  never treated as Beckn v2, whatever their version (`StepContext.IsBecknV2`). `testdata/config/adapter.yaml.tmpl`
  shows the setting; the tests prove both domains work with it.
* The schemas (see above).

Because ONIX gives every refusal to an unverifiable sender the one code `30016`, the negative tests tell the cases
apart by the error message (for example "expired", "not yet valid", "identity").

## Not covered

* Other retail domains (RET10, RET11 …): copy `ondc_ret14` to the new domain name in `schemas/` and add the domain to
  the routing templates and `domains` in `world_test.go`.
* The sample configs in `config/onix*` are stale (`routingType`, no `track`/`update`/`rating`/`support`).
  `testdata/config` is a working, complete ONDC example.
* CI: the repository's workflows are not changed.
