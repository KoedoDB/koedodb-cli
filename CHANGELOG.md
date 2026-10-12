# Changelog

## Unreleased

- `koedodb deploy [dir]`: packs a build output (the same files make the same bytes), asks the API for a presigned upload URL, uploads, and publishes. Refuses a directory that looks like a project, or has `.env` files, a symbolic link, no `index.html`, or is over the limits (100 MiB packed, 500 MiB unpacked, 20,000 files).
- `--json`, exit status 0 to 5, the token from `KOEDODB_TOKEN`.
