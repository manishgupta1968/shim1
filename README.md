# shim1

## Beckn ONIX

The folder `beckn-onix/` holds the source code of [beckn/beckn-onix](https://github.com/beckn/beckn-onix),
copied from upstream commit `b0a134b`. Our own changes are made directly in this folder.
The copy keeps the upstream `LICENSE` (Apache 2.0).

To build it, run `./install-onix.sh`. It builds the server and all plugins and extracts the
schemas. The script prints the commands to start and check the server.

Requirements: Go (the version named in `beckn-onix/go.mod`), `unzip`, and Redis
(needed only when the `cache` plugin is used).

The build output (`server`, `plugins/`, `schemas/`) is ignored by Git.

## ONDC end-to-end tests

`beckn-onix/test/e2e/ondc` sends realistic ONDC messages through real ONIX processes, for **all 20 Beckn
endpoints** (`search` … `support` and their `on_*` callbacks) in two ONDC domains: **TRV10** (mobility) and
**RET14** (retail). Run them with:

```bash
cd beckn-onix
go test ./test/e2e/ondc/... -count=1 -timeout 15m -v
```

The first run builds the server and plugins (2–4 minutes); later runs take about 20 seconds. See
`beckn-onix/test/e2e/ondc/README.md` for what is tested, the fixtures and the changes this needed in ONIX.
