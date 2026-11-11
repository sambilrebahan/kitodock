# Kitodock

A local PHP/Laravel/Node.js development environment for Windows — nginx,
MySQL, Redis, Mailpit, multi-version PHP and Node.js, HTTPS with a local
CA, an integrated terminal workspace, file editor, Git panel, SSH/SFTP,
and one-click server deployment.

## Quick start

1. Clone this repo
2. `cd desktop/frontend && npm install`
3. From the repo root: `go run ./desktop` (or build with Wails)

The app creates its environment under `C:\kitodock` (override with
`KITODOCK_ROOT`). First run: visit **Downloads**, install the stack
(nginx + MySQL + Mailpit), then create a project.

## Layout

```
cmd/kitodock-deploy/  headless deploy CLI (same engine as the GUI)
desktop/              Wails GUI (controllers per domain, React frontend)
internal/             Go packages (services, installer, php, vhost, ssh,
                      deploy, agents, mcp, workspacefiles, …)
docs/                 planning documents + per-phase progress logs
```

## Documentation

- [PLAN.md](PLAN.md) — master plan and phase tracker
- [docs/03-roadmap-detail.md](docs/03-roadmap-detail.md) — full roadmap
- [docs/progress/](docs/progress/) — per-phase implementation logs

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).
Third-party notices: [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt).
