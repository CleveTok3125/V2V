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

## Version coupling

Pre-1.0 releases do not guarantee wire compatibility. **Update the client
and the server together.** Clients query `GET /api/version` before dialing
and warn on a mismatch.

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
   `MAX_*` limits. The file is commented inline.

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

5. Open `http://localhost:10000/web/` for the browser client.

## Verify a download

```sh
sha256sum -c SHA256SUMS --ignore-missing
cosign verify-blob \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp 'https://github.com/CleveTok3125/V2V/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
```
