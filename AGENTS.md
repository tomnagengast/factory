# Factory agent instructions

- Preserve human-only merge authority and exact verified-head deployment gates.
- Route every run's primary repository through allowlisted Linear project
  metadata; runs carry machine-wide authority for coordinated changes across
  admitted repositories.
- Keep repository-specific clones, worktrees, checkpoints, receipts, and
  completion evidence isolated.
- Run `go test ./...`, `go test -race ./...`, `go vet ./...`, and the
  frozen Bun frontend build before publication.
- Deploy only clean, merged `main` commits from
  `~/repos/tomnagengast/factory`.
- Never deploy from the T9 working mirror.
