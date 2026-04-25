# Contributing to Mortise

Welcome! We're glad you're interested in contributing to Mortise. This
document explains how to get started.

## Code of Conduct

Please read and follow our [Code of Conduct](CODE_OF_CONDUCT.md). We take
it seriously and enforce it consistently.

## How to Contribute

### Reporting Bugs

Use the [Bug Report](https://github.com/mortise-org/mortise/issues/new?template=bug-report.yaml)
issue template. Include:

- What happened vs. what you expected
- Steps to reproduce
- Mortise version (`mortise version` or Helm release)
- Kubernetes version (`kubectl version`)

**Security issues:** Do NOT open a public issue. See [SECURITY.md](SECURITY.md).

### Suggesting Features

Use the [Feature Request](https://github.com/mortise-org/mortise/issues/new?template=feature-request.yaml)
issue template. Explain what you want and why. Implementation ideas are
welcome but not required.

### Your First Pull Request

1. Fork the repo and create a branch from `main`.
2. Read the [development setup](#development-setup) section below.
3. Make your changes. Follow the [coding guidelines](#coding-guidelines).
4. Add or update tests. Every PR must include test coverage.
5. Run `make test` and `make test-charts` locally. Both must pass.
6. Open a PR against `main` using the PR template.

### Pull Request Process

1. Fill out the PR template completely.
2. A maintainer will review your PR. Address feedback promptly.
3. All CI checks must pass: unit tests, chart linting, and Go vet/lint.
4. At least one maintainer must approve via GitHub review.
5. Once approved and green, a maintainer will merge.

**PR expectations:**
- Keep PRs focused. One logical change per PR.
- Rebase on `main` if your branch falls behind.
- Don't mix refactoring with feature work or bug fixes.
- Write a clear description of *what* and *why*, not just *how*.

## Development Setup

### Prerequisites

- Go (version in `go.mod`)
- Node.js 20+ and npm (for the UI)
- Helm 3.x
- Docker
- kubectl
- k3d (for integration tests and local dev)

### Build and Test

```bash
make build              # compile operator + CLI
make test               # unit + envtest (<10s)
make test-charts        # helm lint + template tests (<30s)
make dev-up             # start k3d + tilt live-reload
make test-e2e           # Playwright E2E (requires dev cluster)
make dev-down           # tear down dev cluster
```

### Running Locally

```bash
make dev-up
```

This starts a k3d cluster with Tilt for live-reload development. The UI
is available at `http://localhost:8080`.

## Coding Guidelines

### Architecture Rules

These are non-negotiable. PRs that violate them will be rejected.

- **Controllers never import third-party SDKs.** External calls go through
  interfaces in `internal/`.
- **Couple to standards, not implementations.** Use k8s Ingress (not
  Traefik IngressRoute), OCI Distribution Spec (not registry-specific
  APIs), OIDC (not provider-specific APIs).
- **Everything is an App.** No new CRD kinds for workloads. Source type
  determines behavior.
- **Mortise owns only what it creates.** Check ownership labels before
  modifying or deleting any resource.

See [CLAUDE.md](CLAUDE.md) for the full architecture and conventions reference.

### Style

- Match existing code style. No reformatting of unrelated code.
- No comments explaining *what*. Only comment *why* when non-obvious.
- No unused code, imports, or variables.
- Use `clock.Clock` for time in controllers, never `time.Now()`.

### Tests

- Unit tests beside the code they test (`_test.go`).
- Integration tests in `test/integration/` with the `integration` build tag.
- Test fixtures in `test/fixtures/`. Never hand-write App YAML in tests.
- Copy the nearest similar test and adapt it. Tests should be boring.

## Commit Messages

Write clear, concise commit messages:

```
Short summary of the change (under 72 chars)

Longer description if needed. Explain why, not what. Reference
issues with "Fixes #123" or "Relates to #123".
```

## License

By contributing to Mortise, you agree that your contributions will be
licensed under the [Apache License 2.0](LICENSE).

## Questions?

Open a [Discussion](https://github.com/mortise-org/mortise/discussions) or
file an issue. We're happy to help.
