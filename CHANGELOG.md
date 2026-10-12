# Changelog

## Unreleased

- `koedodb deploy [dir]`: packs a build output (the same files make the same bytes), asks the API for a presigned upload URL, uploads, and publishes. Refuses a directory that looks like a project, or has `.env` files, a symbolic link, no `index.html`, or is over the limits (100 MiB packed, 500 MiB unpacked, 20,000 files).
- `--json`, exit status 0 to 5, the token from `KOEDODB_TOKEN`.
- Files are read through the directory (`os.Root`) and each opened file is checked to be the plain file the scan saw: a file or directory swapped for a symbolic link after the scan can no longer bring a file from outside the directory into the archive.
