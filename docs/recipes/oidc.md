# Authentication status

Mortise currently implements **native authentication only**:

- first-user setup: `POST /api/auth/setup`
- login: `POST /api/auth/login`
- bearer JWT for protected API routes

There is currently **no OIDC login flow** in the API server, and no
`PlatformConfig.spec.auth` fields in the CRD.

## What this means in practice

- You cannot configure an OIDC callback URL in Mortise today.
- You cannot switch auth mode via `PlatformConfig`.
- User management is done through Mortise native users/roles.

## Recommended current pattern

- Expose Mortise behind your ingress/controller as usual.
- Use native Mortise auth for platform access.
- If you need SSO behavior today, front-door it outside Mortise (for
  example with an auth gateway/proxy), understanding this is not a
  first-class Mortise auth mode.

## API reference

- See `docs/api-endpoints.md` for implemented auth endpoints.
- See `docs/openapi.yaml` for the machine-readable contract.
