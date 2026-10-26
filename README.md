# shim1

## Installing Beckn ONIX

Run `./install-onix.sh`. It clones [beckn/beckn-onix](https://github.com/beckn/beckn-onix)
into `beckn-onix/`, builds the server and all plugins, and extracts the schemas.
The script prints the commands to start and check the server.

Requirements: Git, Go (the script uses the version the repository asks for), `unzip`,
and Redis (needed only when the `cache` plugin is used).

The `beckn-onix/` folder holds build output, so it is ignored by Git.
