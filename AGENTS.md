# Factory agent instructions

- Keep Factory focused on its three mechanisms: the event wire, task intake,
  and the sequential agent loop.
- Factory is an intentionally unsafe trusted-environment demonstrator. Do not
  add authentication, permissions, policy, routing, migration, or deployment
  lifecycle machinery without explicit product direction.
- Humans retain merge authority for this repository.
- Run `go test ./...`, `go test -race ./...`, `go vet ./...`, and the
  frozen Bun frontend build before publication.
- Deploy only clean, merged `main` commits from
  `~/repos/tomnagengast/factory`.
- Never deploy from the T9 working mirror.
