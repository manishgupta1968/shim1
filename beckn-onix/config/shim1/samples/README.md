# Sample /search payloads

Try them with `./shim1demo client --payload <file> --expect <ack|nack>` while Shim 1 is running
(see docs/how-to-test-shim1.md). The client signs them as the demo buyer app and Gateway, and
refreshes `timestamp`, `transaction_id` and `message_id`.

| File | Where it comes from | What Shim 1 should do |
|------|---------------------|-----------------------|
| `01-ondc-official-signing-sample.json` | ONDC's own sample body in `reference-implementations/utilities/signing_and_verification` (copied unchanged). It is old: `core_version` 0.9.1, domain `nic2004:60212`, city `Kochi`, `ttl` `P1M`. | **Refuse** (NACK). Shim 1 serves ONDC retail 1.2.0 for `ONDC:RET10` only. |
| `02-ondc-sample-adapted-to-RET10.json` | The same ONDC message, with only the context moved to `ONDC:RET10`, `std:080`, `1.2.0`, `PT30S`. | **Accept** (ACK). |
| `03-typical-retail-1.2.0-search.json` | Written by us in the usual ONDC retail 1.2.0 shape. It is NOT an official file. | **Accept** (ACK). |
| `04-ondc-2.0.1-location-style-search.json` | The newer ONDC layout shown in ONDC's logistics utility README (`version` 2.0.1, `city` and `country` inside `location`). | **Refuse** (NACK). This layout is not supported yet. |

File `01` is copied unchanged from ONDC's `reference-implementations` repository
(https://github.com/ONDC-Official/reference-implementations, MIT License, Copyright (c) 2022 ONDC).
