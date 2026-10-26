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
