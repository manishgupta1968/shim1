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

**Small changes to ONIX** (they only apply to requests with `core_version`, so
other Beckn traffic behaves as before):

| File | Change | Why |
|------|--------|-----|
| `core/module/handler/step.go` | Read `core_version` and `message_id` as well as `version` and `messageId` | ONDC retail 1.2.x uses the first spelling |
| `pkg/plugin/implementation/router/router.go` | Same, for routing | Same |
| `pkg/plugin/implementation/schemavalidator/schemavalidator.go` | Same, for schema choice | Same |
| `pkg/model/error.go`, `pkg/model/ondc.go` | Add error `type`; map ONIX error codes to ONDC codes | ONDC errors have a type and numeric codes |
| `core/module/handler/responsestep.go`, `stdHandler.go` | For ONDC requests, put `error` next to `message` in the NACK | This is the Beckn 1.x / ONDC reply shape |

**New code:**

| Folder | What it is |
|--------|------------|
| `pkg/plugin/implementation/ondcregistry` | Looks up a participant the way the ONDC Registry does (`ukId`, `subscriber_url`). Only `SUBSCRIBED` entries inside their dates are returned. Has `stub` mode (a JSON file) and `http` mode (a real registry). |
| `pkg/plugin/implementation/ondcguard` | A pipeline step for checks 8, 9, 10 and 11 |
| `config/shim1/ondc-policy.rego` | Policy for checks 5 and 6 |
| `config/shim1/` (other files) | Configuration, routing and stub schemas |
| `cmd/shim1demo` | The stubs and the test client |

## What is stubbed, and how to replace it

| Stub | Stands in for | To go live |
|------|---------------|------------|
| `config/shim1/generated/registry.json` | ONDC Registry | In `shim1.yaml.tmpl` set `mode: http` and `url:` for both `ondcregistry` and `ondcguard`. The real `/v2.0/lookup` needs a signed request. **That signing is not built yet.** |
| The test client signing `X-Gateway-Authorization` | ONDC Gateway | Not needed. The real Gateway sends it. |
| `config/shim1/schemas/ondc_ret10/v1.2.0/*.json` | Official ONDC retail schemas | Generate the schemas from the ONDC retail API contract, and replace these files. |
| `shim1demo backend` | The seller's own business logic | Point `routing-receiver.yaml` to the real backend. |
| `shim1demo bap` | A buyer app | Not needed. Real buyer apps receive the callbacks. |
| Demo keys (made fresh on every run) | The company's real keys | Put real keys in the key manager settings. |

## How to run the demo

```bash
./run-shim1-demo.sh               # first time: builds everything (a few minutes)
./run-shim1-demo.sh --skip-build  # later runs
```

The script starts the stub seller backend, the stub buyer app and Shim 1. Then it
sends 17 requests. The first is good and 16 are bad in different ways. Each bad one
must be refused with the right error, **and must not reach the seller backend**.

For the good request the script also confirms that the seller backend received it
and that the signed `on_search` reached the buyer app with a valid signature.

To see the full answers, run the stack and use
`./shim1demo client --verbose` from the `beckn-onix` folder.

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
3. **Registry lookup.** The real ONDC Registry needs a signed lookup call. Not built.
4. **Repeat check is in memory.** It is lost when Shim 1 restarts, and it is not
   shared between two Shim 1 servers. For production, use a shared store such as Redis.
5. **Only retail 1.2.0 for `ONDC:RET10` is configured**, and only `search` has a
   meaningful schema. The other actions have a very light schema.
6. **The Gateway header rule.** We require `X-Gateway-Authorization` on `search`
   only. Please check with the ONDC network rules if other actions need it.
7. **Three existing ONIX tests fail when run as the root user, and pass as a normal
   user.** The tests are `TestUnzipFailure/file_creation_failure`,
   `TestPluginsFailure/permission_denied` (both in `pkg/plugin/manager_test.go`) and
   `TestValidator_Initialise/Failed_to_read_directory` (in the schemavalidator package).
   Each one makes a folder that nobody may open, and expects "permission denied".
   The root user can open any folder, so no error comes and the test fails.
   We checked this by running the same compiled test files as root (3 failures) and as
   the normal user `ubuntu` (all pass). The code under test is not code we changed.
   If you run the whole test suite in a root-only sandbox, expect these three failures.
