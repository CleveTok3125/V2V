# Installing and Deploying V2V

This document covers deploying the V2V server. Clients (`V2V-*`) and the
admin tool (`V2Vctl-*`) are plain executables; download the file for your
platform and run it.

## Release artifacts

| Artifact                                   | Purpose                                                        |
| ------------------------------------------ | -------------------------------------------------------------- |
| `V2V-<os>-<arch>[.exe]`                      | Chat client                                                    |
| `V2Vctl-<os>-<arch>[.exe]`                   | Admin tool (roles, keys, enrollment, instance management)      |
| `V2V-server-<os>-<arch>.tar.gz` / `.zip`       | Self-contained server bundle (see below)                       |
| `V2V-webterm-<version>.zip`                   | Standalone browser client (wasm + `serve.py`)                  |
| `ghcr.io/clevetok3125/v2v:<version>`         | Multi-arch container image (`linux/amd64`, `linux/arm64`)      |
| `SHA256SUMS`, `SHA256SUMS.sigstore.json`        | Checksums and a cosign keyless signature bundle of the checksums |

The server bundle contains:

```
server               # the server binary (server.exe on Windows)
v2vctl               # matching admin tool for the bundle's platform
webterm/              # wasm web client served at /web/
template/             # bootstrap config tree
v2v-template.json     # manifest used by `v2vctl config sync`
docker-compose.yml    # container deployment
```

## Platform support

| Component         | Windows      | Linux        | macOS        | Android         | Web (WASM)         |
| ----------------- | ------------ | ------------ | ------------ | --------------- | ------------------ |
| Client `V2V`        | amd64, arm64 | amd64, arm64 | amd64, arm64 | arm64 (aarch64) | any modern browser |
| `V2Vctl`            | amd64, arm64 | amd64, arm64 | amd64, arm64 | arm64 (aarch64) | —                  |
| Server            | amd64, arm64 | amd64, arm64 | amd64, arm64 | —               | —                  |
| Container image   | —            | amd64, arm64 | —            | —               | —                  |
| `serve.py` bundle | any (Python) | any (Python) | any (Python) | any (Python)    | —                  |

The table lists officially released artifacts. On Android some components
ship no artifact but still run, with or without small changes. The WASM
client runs in any modern browser (Android and iOS included), and the
`serve.py` bundle is OS-independent, needing only Python 3. Client and
`V2Vctl` ship as bare binaries; the server ships as `.tar.gz`, or `.zip`
on Windows.

## Version coupling

Pre-1.0 releases do not guarantee wire compatibility. **Update the client
and the server together.** Clients query `GET /api/version` before dialing
and warn on a mismatch. The WASM client is served by the same server
build, so it is already paired; a standalone `V2V-webterm` bundle must be
kept on the same commit as the server it connects to.

## Standalone browser client

The `V2V-webterm-<version>.zip` artifact ships the WASM client with
`serve.py`, a small stdlib-only Python server that reproduces the V2V
static behavior (mount at `/web/`, `application/wasm`, the pre-compressed
`.br`/`.gz` variants). It needs no chat server build and gives the client
the browser sandbox.

```sh
unzip V2V-webterm-<version>.zip
python3 serve.py            # http://127.0.0.1:8080/web/
python3 serve.py --port 9000 --bind 0.0.0.0
```

Open the printed URL and enter the chat server address in the form. On
the server side, list the page's origin in `ALLOWED_ORIGINS` (the loopback
defaults cover `localhost` and `127.0.0.1`, which browsers treat as
different origins). The server also has to allow `/web/`
(`WEB_ENABLED=true`, the default). To serve it behind a real web server
instead, point that server at the extracted directory with the same
`/web/` prefix; the assets reference `/web/...` paths.

Passkey is bound to one `WEBAUTHN_RPID` and one `WEBAUTHN_ORIGIN`
(`RPOrigins` holds a single value), so a page served from a different host
cannot run the ceremony. Serving this bundle from `127.0.0.1`/`localhost`
over plain HTTP therefore disables passkey login; guest, tripcode and
ed25519 key logins still work. Using passkey here means serving the page
from the exact `WEBAUTHN_ORIGIN` hostname over HTTPS, which cannot also
serve a different production origin at the same time.

## Docker (recommended)

Prerequisites: Docker Engine with the Compose plugin, and the `v2vctl`
binary from the bundle.

1. Extract the bundle and enter its directory.

2. Bootstrap an instance. This writes `instances/<name>/.env`,
   `instances/<name>/config/` and an empty `instances/<name>/data/`:

   ```sh
   ./v2vctl instance init prod --port 10000
   ```

3. Edit `instances/prod/.env` before first boot. At minimum set `PORT`,
   `ALLOWED_ORIGINS`, `PROXY_PROVIDER`, and review `REQUIRE_TLS` and the
   `MAX_*` limits. The file is commented inline. Check it without booting:

   ```sh
   ./v2vctl config validate --to instances/prod
   ```

   It reads the same loader the server uses, so every fail-closed check
   (required vars, number/duration formats, proxy chain and trust files,
   onion hosts, `roles.json`) reports here instead of at first start.

4. Pull the published image and start the instance:

   ```sh
   export IMAGE_NAME=ghcr.io/clevetok3125/v2v:vX.Y.Z
   docker compose -p v2v-prod --env-file instances/prod/.env pull
   ./v2vctl instance up --build=false prod
   ```

   `IMAGE_NAME` selects the published image; without it Compose would
   build from source. The container runs read-only, drops all
   capabilities except the three the entrypoint needs, and binds to
   loopback by default (`BIND_ADDR`).

5. Check health and logs:

   ```sh
   ./v2vctl instance status prod
   ./v2vctl instance logs prod
   curl http://127.0.0.1:10000/api/version
   ```

### Upgrade and rollback

Pin `IMAGE_NAME` to an explicit version tag (never rely on `latest` in
production):

```sh
export IMAGE_NAME=ghcr.io/clevetok3125/v2v:vX.Y.Z
docker compose -p v2v-prod --env-file instances/prod/.env pull
./v2vctl instance up --build=false prod
```

To roll back, set `IMAGE_NAME` to the previous version and repeat. Data
lives in `instances/prod/data/` and is untouched by image changes.

## Bare metal

1. Extract the bundle and enter its directory.

2. Bootstrap the default instance:

   ```sh
   ./v2vctl config sync --dir . --to instances/default
   ```

3. Edit `instances/default/.env`.

4. Run the server. The web assets are resolved next to the executable,
   so the working directory does not matter:

   ```sh
   V2V_ROOT=instances/default ./server
   ```

   On Windows the binary is `server.exe`. Set `WEBTERM_DIR` to serve the
   web bundle from a different directory.

5. Open `http://localhost:10000/web/` for the browser client. The WASM
   client is served locally by the same binary, so a browser deployment
   gets the browser sandbox without any separate web server.

   Two flags gate it: `WEB_ENABLED` (default `true`) is the master switch
   for every request, and `ONION_ALLOW_WEB` (default `false`) adds the
   onion restriction, so an onion request needs both. Set `WEB_ENABLED=false`
   to stop serving `/web/` entirely. From source, build the assets with
   `make web` first; keep the client and server on the same commit, since
   the pre-dial version check is skipped for the WASM client.

## Verify a download

```sh
sha256sum -c SHA256SUMS --ignore-missing
cosign verify-blob \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp 'https://github.com/CleveTok3125/V2V/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
```
