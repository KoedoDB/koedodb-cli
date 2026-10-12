# koedodb-cli

The command line of [KoedoDB](https://koedodb.com). For now it does one thing: **deploy a static site**.

```sh
export KOEDODB_TOKEN=...        # a deploy token, made for the project in the KoedoDB console
koedodb deploy ./dist           # the directory your build writes the site to
```

This is not an official tool of any other company, and KoedoDB is not affiliated with the projects it builds on. MIT license.

## Install

Download the archive for your system from the [Releases](https://github.com/KoedoDB/koedodb-cli/releases), unpack it, and put `koedodb` somewhere on your `PATH`. Linux, macOS and Windows, on amd64 and arm64.

Or, if you have Go: `go install github.com/koedodb/koedodb-cli/cmd/koedodb@latest`.

### Check what you downloaded

Every release has `SHA256SUMS` and a signature of it (`SHA256SUMS.sigstore.json`), made by the release workflow of this repository with [Sigstore](https://www.sigstore.dev/): there is no signing key that could be stolen, and the signature says which workflow, in which repository, made it.

```sh
# the signature of the checksums: made by this repository's release workflow
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github.com/(?i:koedodb)/koedodb-cli/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# the file you downloaded is the one the checksums name
sha256sum --check --ignore-missing SHA256SUMS      # on macOS: shasum -a 256 --check --ignore-missing SHA256SUMS
```

The builds are reproducible: the same tag, built again with the same Go version (`go.mod`) and the flags in `.github/workflows/release.yml`, makes the same bytes.

## What `deploy` does

1. **Packs** the directory into one `tar.gz`: the files only, in order, with no owner, no time and fixed modes, so the same files always make the same bytes (and the same SHA-256).
2. **Asks KoedoDB** where to put it (`POST /site/uploads` with the SHA-256 and the size). KoedoDB answers with a presigned URL of its object store, good for ten minutes, for exactly that size.
3. **Uploads** the archive to that URL. The bytes go straight to the store, not through KoedoDB's API, and the token is not sent there.
4. **Tells KoedoDB it is done** (`POST /site/uploads/{id}/complete`). KoedoDB checks the object is there, the size, and that it was written after you asked; then the site is published. The server that serves it downloads the archive, checks the SHA-256 again, and unpacks it.

Everything in the directory is published on the internet.

## What it refuses (before anything is uploaded)

A person or an AI tool most easily deploys the **whole project** instead of its build output. So `deploy` refuses a directory that:

- has `package.json`, `.git`, `node_modules`, `go.mod`, `pyproject.toml`, `requirements.txt`, `Cargo.toml` or a `Dockerfile` at its top, or a `node_modules` anywhere in it;
- has a file named `.env` or `.env.*`;
- has a symbolic link (a site is plain files);
- has no `index.html` at its top;
- is over the limits: 100 MiB packed, 500 MiB unpacked, 20,000 files, 200 MiB for a file, 32 directories deep.

`--force` deploys a directory that looks like a project or has `.env` files. `--allow-no-index` deploys one without `index.html`. The limits cannot be forced: the server checks them too.

## For scripts, CI and AI tools

- It never asks a question. The token is read from the environment (`KOEDODB_TOKEN`), not from a flag, so it is not in the process list or the shell history.
- `--json` prints one JSON object and nothing else, on success (`files`, `bytes`, `unpacked_bytes`, `sha256`, `url`, `deployed_at`) and on failure (`{"error": {"code": "...", "message": "..."}}`).
- Exit status: `0` done, `1` anything else, `2` used wrongly, `3` no token or not accepted, `4` the directory cannot be deployed (the message says why), `5` KoedoDB or the network did not answer as it should (run it again).
- With no directory given, it takes the first of `dist`, `build`, `out` that exists.
- It prints neither the token nor the upload URL, in messages or errors.

## Settings

| | |
|---|---|
| `KOEDODB_TOKEN` | the deploy token (required) |
| `KOEDODB_API_URL` or `--api` | the API (default `https://api.koedodb.com/v1`; https only, apart from `localhost` for tests) |

## The API it uses

With a deploy token (the project is the token's):

| | |
|---|---|
| `POST /site/uploads` `{"sha256", "bytes"}` | `201 {"id", "method": "PUT", "url", "expires_at"}` |
| `PUT <url>` | the archive, `Content-Length` exactly the size that was asked for |
| `POST /site/uploads/{id}/complete` | `200 {"published", "bytes", "sha256", "url", "updated_at"}` |

A project may ask for 20 uploads an hour and 2 GiB a day (more is `429`, `upload_rate_limited`); an archive is 100 MiB at most. A token expires (90 days by default), and a project may have ten at a time.

Errors are `{"error": "<code>"}` with the status; the codes are stable.

## Build and test

```sh
go build -o koedodb ./cmd/koedodb      # -ldflags "-X main.version=v1.2.3" sets the version
go test ./...
```

No dependencies outside the Go standard library.
