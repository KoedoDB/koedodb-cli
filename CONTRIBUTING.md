# Contributing

Bug reports and small fixes are welcome. For a new command or a change of how `deploy` behaves, open an issue first: this tool is kept small on purpose (no dependency outside the Go standard library, a few commands, behaviour that is easy to explain).

- `go vet ./... && go test ./...` must pass; the tests need no network and no account.
- Keep the messages of an error saying what to do next: this is read by people and by AI tools.
- A change that touches what is sent to the API, or what is refused before sending, needs a test.
- Do not add anything that prints the token or the upload URL.
