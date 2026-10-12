# Security

koedodb is what you run on your own machine, or in your CI, with a deploy token. We take its security seriously.

## Reporting a vulnerability

Please do not open a public issue. Write to **security@koedodb.com**, or use GitHub's private vulnerability reporting (the Security tab of this repository). Say what you found and how to see it; we answer within a few days, and tell you when it is fixed and from which version.

## What the CLI does with what you give it

- The deploy token is read from the environment (`KOEDODB_TOKEN`), never from a flag. It is sent to the KoedoDB API and to nothing else: the upload URL it gets back is for the object store and carries its own signature, so the token is not sent there.
- Neither the token nor the upload URL is printed, in a message or in an error.
- It packs only plain files, refuses a directory that looks like a project (a `package.json`, a `.git`, a `node_modules`) or has a `.env` file, and makes no network call until the directory is accepted.
- The API address must be https (a loopback address is allowed, for tests).

## The releases

A release is made by a GitHub Actions workflow from a tag: the binaries, `SHA256SUMS`, and a signature of `SHA256SUMS` made by Sigstore with the identity of that workflow (there is no signing key to steal). How to check them is in the README.
