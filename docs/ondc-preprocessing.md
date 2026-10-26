# Shim 1: ONDC pre-processing of incoming requests

## What this document is about

A company that wants to be a Network Participant (NP) on ONDC must do the same
"gate-keeping" work for every request it receives, before its own business logic
runs. Shim 1 does this work once, so each company does not have to build it again.

Shim 1 is built on **Beckn ONIX**. We reuse ONIX wherever it already does the job.
We add only what ONDC needs and ONIX lacks. We stub the parts of the ONDC network
that we cannot reach (the Registry and the Gateway).

## The journey of one request

Take a buyer app that searches for "coffee". The request travels like this:

```
Buyer app --> ONDC Gateway --> Shim 1 --> seller's business logic
                                 |
                                 +-- checks 1 to 11 happen here
```

Shim 1 runs these checks **in this order**. The first failure stops the request and
a NACK (a "no") goes back. The business logic never sees a bad request.

| # | Check | Example of a failure | Who does it |
|---|-------|----------------------|-------------|
| | **Step `validateSign`: who sent it, and is it genuine?** | | |
| 1 | The request has a signature header, and we can read it | No `Authorization` header | ONIX, as it is |
| 2 | The signer is in the Registry, `SUBSCRIBED`, and inside its validity dates | Unknown sender, or registration expired | **New** `ondcregistry` (stub), called by ONIX |
| 3 | The signature is genuine and has not expired | Someone changed the message after it was signed | ONIX, as it is |
| 4 | The signer is who `bap_id` says it is | `bap_id` says A, but B signed | ONIX, as it is |
| | **Step `checkPolicy`: is it complete, and is it for us?** | | |
| 5 | All mandatory context fields are present | `transaction_id` missing | **New** policy file, run by ONIX |
| 6 | The request is meant for us: our `bpp_id`, our domain, city, country and version | A request for city `std:999`, which we do not serve | **New** policy file, run by ONIX |
| | **Step `validateSchema`: is the JSON the right shape?** | | |
| 7 | The message follows the schema for this action | `/search` without `message.intent` | ONIX `schemavalidator` (stub schemas) |
| | **Step `ondcguard`: ONDC network rules** | | |
| 8 | For `/search`, the Gateway has also signed it, and the Gateway is registered as a Gateway | `X-Gateway-Authorization` is missing | **New** `ondcguard` |
| 9 | The sender is registered as a buyer app for this domain and city | A seller app pretending to be a buyer app | **New** `ondcguard` |
| 10 | The request is not stale: `timestamp` is within `ttl` | A request 5 minutes old with `ttl` of 30 seconds | **New** `ondcguard` |
| 11 | The request is not a repeat of one already handled | The same `message_id` sent twice | **New** `ondcguard` (in memory) |
| | **Hand-over** | | |
| 12 | Send it to the business logic | | ONIX `addRoute` and proxy |
| 13 | Business logic answers later with `on_search`, which is signed and sent to the buyer | | ONIX caller module (`sign`) |

The answer to the caller is always an **ACK** (yes) or a **NACK** (no) with an error
code, for example:

```json
{"message":{"ack":{"status":"NACK"}},
 "error":{"type":"CORE-ERROR","code":"30016","message":"Signature Validation Error: signature verification failed"}}
```

### Why this order?

- The signature is checked **before** anything else, because it covers the exact
  bytes that were sent. We must not change the message before checking it.
- The role checks (8 and 9) come **after** the signature is proven. We only trust
  who the sender claims to be once the signature has proved it.
- The "repeat" check comes **last**. If it came first, a bad request could "use up"
  a `message_id`, and the correct request would then be refused.

## What came from ONIX and what is new

**Used as it is from ONIX:** reading the signature header, the BLAKE-512 digest and
ed25519 check (this is the same method ONDC uses), the `created` and `expires` time
check, matching the signer with `bap_id`, JSON schema checks, the policy engine
(OPA), routing, forwarding, and signing of outgoing callbacks.

**Small changes to ONIX.** Most apply only to requests with `core_version`, or to a module
that is switched to ONDC mode, so other Beckn traffic behaves as before. The last test in the
scenario table proves this.

| File | Change | Why |
|------|--------|-----|
| `core/module/handler/step.go` | Read `core_version` and `message_id` as well as `version` and `messageId` | ONDC retail 1.2.x uses the first spelling |
| `pkg/plugin/implementation/router/router.go` | Same, for routing | Same |
| `pkg/plugin/implementation/schemavalidator/schemavalidator.go` | Same, for schema choice | Same |
| `pkg/model/error.go`, `pkg/model/ondc.go` | Add error `type`; map ONIX error codes to ONDC codes | ONDC errors have a type and numeric codes |
| `core/module/handler/responsestep.go`, `stdHandler.go` | For ONDC requests, put `error` next to `message` in the NACK | This is the Beckn 1.x / ONDC reply shape |
| `pkg/plugin/implementation/reqpreprocessor` | New setting `ondc: "true"`. It marks every request on that module as ONDC, so even an empty or non-JSON body gets an ONDC-shaped NACK | Without it, ONIX only learns a request is ONDC from `core_version` inside the body, which cannot be read from a broken body. Found by testing. |
| `pkg/plugin/implementation/signer/signer.go` | Accept ONDC's 64-byte private key form (seed + public key) as well as the 32-byte seed | **Found by checking against ONDC's own sample code.** ONDC's Python and Go samples store the key as 64 bytes. ONIX accepted only 32 and said "invalid seed length". |
| `reqpreprocessor_test.go` (existing test) | One character: `%s` to `%v` in an error message | Adding a field to the settings made Go's vet check reject the old format. |

**New code:**

| Folder | What it is |
|--------|------------|
| `pkg/plugin/implementation/ondcregistry` | Looks up a participant the way the ONDC Registry does. Only `SUBSCRIBED` entries inside their dates are returned. Has `stub` mode (a JSON file) and `http` mode. In `http` mode it sends ONDC's own request fields (`ukid`, `country`, ...), **signs the lookup with our key** (the real registry requires this), and keeps answers for one minute. |
| `pkg/plugin/implementation/ondcguard` | A pipeline step for checks 8, 9, 10 and 11 |
| `config/shim1/ondc-policy.rego` | Policy for checks 5 and 6 |
| `config/shim1/` (other files) | Configuration, routing and stub schemas |
| `cmd/shim1demo` | The stubs (seller backend, buyer app, HTTP registry) and the test client |
| `config/shim1/samples/` | Four sample `/search` payloads, one of them ONDC's own file |
| `tests/ondc-referee/` | Checks our signing with ONDC's own sample code |

## What is stubbed, and how to replace it

| Stub | Stands in for | To go live |
|------|---------------|------------|
| `config/shim1/generated/registry.json` (or `shim1demo registry`) | ONDC Registry | Set `mode: http` and `url:` for both `ondcregistry` and `ondcguard`, and give them our own `callerId`, `callerKeyId` and `callerPrivateKey`. The code signs the lookup like ONDC's tool does. **It has been tested against our stub registry only, never against the real ONDC registry.** |
| The test client signing `X-Gateway-Authorization` | ONDC Gateway | Not needed. The real Gateway sends it. |
| `config/shim1/schemas/ondc_ret10/v1.2.0/*.json` | Official ONDC retail schemas | Generate the schemas from the ONDC retail API contract, and replace these files. |
| `shim1demo backend` | The seller's own business logic | Point `routing-receiver.yaml` to the real backend. |
| `shim1demo bap` | A buyer app | Not needed. Real buyer apps receive the callbacks. |
| Demo keys (made fresh on every run) | The company's real keys | Put real keys in the key manager settings. |

## How to run the demo

```bash
./run-shim1-demo.sh                          # first time: builds everything (a few minutes)
./run-shim1-demo.sh --skip-build             # later runs
./run-shim1-demo.sh --registry-mode http     # registry over HTTP, lookups must be signed
./run-shim1-demo.sh --with-referee           # also check signing with ONDC's own code
```

The script ends with `RESULT: ALL CHECKS PASSED` only when every part passed.
The full step-by-step guide, with things to try by hand, is in
[how-to-test-shim1.md](how-to-test-shim1.md).

What it checks:

1. **26 scenarios.** Four good requests (`search`, `select`, `init`, `confirm`) and 22 bad ones.
   Each bad one must be refused with the right status and error, **and must not reach the
   seller backend**. For the good `/search` it also confirms that the signed `on_search`
   reached the buyer app with a valid signature.
2. **Four sample payloads**, one of them ONDC's own sample file.
3. In `http` mode: that every registry lookup was signed and accepted.
4. With `--with-referee`: that ONDC's own code accepts what we sign, and that we accept
   what ONDC's code signs.

## What testing found (and fixed)

| Finding | How we found it | Fix |
|---------|-----------------|-----|
| ONIX refused ONDC's 64-byte private key form | Reading ONDC's own sample code | `signer.go` now accepts both forms, with a test |
| A broken body (empty, or not JSON) got a non-ONDC reply | Running the new scenarios | `ondc: "true"` setting on the request pre-processor |
| The registry request used `ukId`; ONDC's tool sends `ukid`, and also `country` | Reading ONDC's lookup tool | Fixed, with a test |
| The real registry needs a signed lookup, and we had no signing | Reading ONDC's lookup tool | Built, and checked with ONDC's own verifier |
| Registry answers were not cached, so every request would call the network | Review | One-minute cache, with tests |

## Things to confirm before going live

These are the points where we took a reasonable decision but have not seen an
official ONDC statement.

1. **The error `type` text.** The ONDC error-code document lists codes but not the
   `type` values for the reply. We use `CORE-ERROR`, `CONTEXT-ERROR`, `POLICY-ERROR`
   and `JSON-SCHEMA-ERROR`. If ONDC wants other text, change it in one place:
   `ToONDCError` in `pkg/model/ondc.go`.
2. **Which code for which failure.** We use 30016 (invalid signature) for every sender
   problem, 30022 (stale request) and 30000 (invalid request) for the rest. A repeat
   request has no ONDC code, so it uses 30000.
3. **Registry lookup.** The signed lookup is built and works against our stub registry, and
   ONDC's own code can verify its signature. It has **never been tried against the real ONDC
   registry**, so the first real call may reveal a difference (for example in the response).
4. **Repeat check is in memory.** It is lost when Shim 1 restarts, and it is not
   shared between two Shim 1 servers. For production, use a shared store such as Redis.
5. **Only retail 1.2.0 for `ONDC:RET10` is configured**, and only `search` has a
   meaningful schema. The other actions have a very light schema.
6. **The Gateway header rule.** We require `X-Gateway-Authorization` on `search`
   only. Please check with the ONDC network rules if other actions need it.
7. **ONDC's newer layout is not supported.** ONDC's logistics utility shows a context with
   `version: "2.0.1"` and the city and country inside `location`. Shim 1 serves the retail
   1.2.x layout (`core_version`, flat `city` and `country`). A 2.0.x request is refused, and
   because ONIX treats any version 2 or higher as Beckn 2, the refusal uses the Beckn 2 shape,
   not the ONDC shape.
8. **The wording of two messages comes from ONIX.** A missing `Authorization` header says
   "WWW-Authenticate missing", and a wrong Gateway signature does not say which of the two
   signatures failed. The refusal status and error code are right. We did not change the text.
9. **The sample registry response in our unit test** was written from the field list in our
   research notes. We could not fetch the original ONDC page, and ONDC's tools show no full
   sample. Please compare it with a real response when you have one.
10. **Three existing ONIX tests fail when run as the root user, and pass as a normal
   user.** The tests are `TestUnzipFailure/file_creation_failure`,
   `TestPluginsFailure/permission_denied` (both in `pkg/plugin/manager_test.go`) and
   `TestValidator_Initialise/Failed_to_read_directory` (in the schemavalidator package).
   Each one makes a folder that nobody may open, and expects "permission denied".
   The root user can open any folder, so no error comes and the test fails.
   We checked this by running the same compiled test files as root (3 failures) and as
   the normal user `ubuntu` (all pass). The code those tests check is not code we changed
   (we made one small, unrelated edit in `schemavalidator.go`). We also ran the **whole**
   suite as the normal user: 58 packages passed and none failed. If you run the suite as
   root, expect these three failures.
