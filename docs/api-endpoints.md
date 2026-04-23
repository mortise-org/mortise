# Mortise API Endpoints (Implementation Reference)

This is a human-readable guide to the currently implemented API surface.
For machine-readable definitions, use `docs/openapi.yaml`.

Base path: `/api`

## Authentication

- Most endpoints require `Authorization: Bearer <jwt>`
- `POST /api/projects/{project}/apps/{app}/deploy` accepts JWT or deploy token (`mrt_*`)
- SSE endpoints also accept JWT via `?token=` query parameter for EventSource compatibility

Unauthenticated endpoints:

- `GET /auth/status`
- `POST /auth/setup`
- `POST /auth/login`
- `POST /webhooks/{provider}`

## Common error shape

Most error responses use:

```json
{ "error": "message" }
```

## Auth and git connection

- `GET /auth/status` - returns `{ setupRequired: boolean }`
- `POST /auth/setup` - first-user setup; body `{ email, password }`
- `POST /auth/login` - body `{ email, password }`; returns `{ token, user }`
- `POST /auth/git/{provider}/device` - start OAuth device flow
- `POST /auth/git/{provider}/device/poll` - poll status; body `{ device_code }`
- `GET /auth/git/{provider}/status` - returns `{ connected: boolean }`
- `POST /auth/git/{provider}/token` - store PAT; body `{ token, host? }`

## Webhooks

- `POST /webhooks/{provider}`
  - HMAC-verified webhook receiver
  - Handles push and PR events
  - Returns `202 Accepted` on processed or ignored events

## Admin users

- `GET /admin/users`
- `POST /admin/users` body `{ email, password, role }`
- `PATCH /admin/users/{email}` body `{ role }`
- `DELETE /admin/users/{email}`

## Git providers

- `GET /gitproviders`
- `POST /gitproviders` body `{ name, type, host, clientID }`
- `DELETE /gitproviders/{name}`
- `GET /gitproviders/{name}/webhook-secret`

## Projects

- `GET /projects`
- `POST /projects` body `{ name, description? }`
- `GET /projects/{project}`
- `DELETE /projects/{project}`

## Project members

- `GET /projects/{project}/members`
- `POST /projects/{project}/members` body `{ email, role }`
- `PATCH /projects/{project}/members/{email}` body `{ role }`
- `DELETE /projects/{project}/members/{email}`

## Project environments

- `GET /projects/{project}/environments`
- `POST /projects/{project}/environments` body `{ name, displayOrder? }`
- `PATCH /projects/{project}/environments/{name}` body `{ name?, displayOrder? }`
- `DELETE /projects/{project}/environments/{name}`

## Project bindings graph

- `GET /projects/{project}/bindings?environment={env}`

Returns edge list:

```json
[{ "from": "api", "to": "db", "environment": "production" }]
```

## Activity

- `GET /projects/{project}/activity?limit={n}` (newest first; default 100, max 500)
- `GET /activity?limit={n}` (platform-wide, all readable projects)

## Apps

- `GET /projects/{project}/apps`
- `POST /projects/{project}/apps` body `{ name, spec }`
- `GET /projects/{project}/apps/{app}`
- `PUT /projects/{project}/apps/{app}` body is raw `AppSpec`
- `DELETE /projects/{project}/apps/{app}`

## Stack and templates

- `POST /projects/{project}/stacks`
  - body supports `compose` or `template` (mutually exclusive)
  - optional `name`, `vars`, `services`
- `GET /templates`

## Deploy and release operations

- `POST /projects/{project}/apps/{app}/deploy` body `{ environment, image }`
  - JWT or deploy token
- `POST /projects/{project}/apps/{app}/rollback` body `{ environment, index }`
- `POST /projects/{project}/apps/{app}/promote` body `{ from, to }`
- `POST /projects/{project}/apps/{app}/rebuild`
- `POST /projects/{project}/apps/{app}/redeploy`

## Build, pods, logs, metrics, exec, local connect

- `GET /projects/{project}/apps/{app}/build-logs`
- `GET /projects/{project}/apps/{app}/pods?env={env}`
- `GET /projects/{project}/apps/{app}/logs` (SSE)
- `GET /projects/{project}/events` (SSE)
- `GET /projects/{project}/apps/{app}/logs/history?env={env}&start=&end=&limit=&filter=&before=`
- `GET /projects/{project}/apps/{app}/metrics/current?env={env}`
- `GET /projects/{project}/apps/{app}/metrics?env={env}&start=&end=&step=`
- `POST /projects/{project}/apps/{app}/exec?env={env}` body `{ command: string[] }`
- `POST /projects/{project}/apps/{app}/connect?env={env}`
- `POST /projects/{project}/apps/{app}/disconnect`

## Secrets and env vars

Secrets (environment defaults to `production` when not supplied):

- `GET /projects/{project}/apps/{app}/secrets?env={env}`
- `POST /projects/{project}/apps/{app}/secrets?env={env}` body `{ name, data }`
- `DELETE /projects/{project}/apps/{app}/secrets/{secretName}?env={env}`

Environment vars (`env` or `environment` query is required):

- `GET /projects/{project}/apps/{app}/env?environment={env}`
- `PUT /projects/{project}/apps/{app}/env?environment={env}` body `[{ name, value, source? }]`
- `PATCH /projects/{project}/apps/{app}/env?environment={env}` body `{ set: {K:V}, unset: [K] }`
- `POST /projects/{project}/apps/{app}/env/import?environment={env}` with raw `.env` text body

Project shared vars:

- `GET /projects/{project}/shared-vars`
- `PUT /projects/{project}/shared-vars` body `[{ name, value, source? }]`

## Domains

All domain endpoints require `environment` query:

- `GET /projects/{project}/apps/{app}/domains?environment={env}`
- `POST /projects/{project}/apps/{app}/domains?environment={env}` body `{ domain }`
- `DELETE /projects/{project}/apps/{app}/domains/{domain}?environment={env}`

## Deploy tokens

Project-scoped tokens:

- `GET /projects/{project}/tokens`
- `POST /projects/{project}/tokens` body `{ description }`
- `DELETE /projects/{project}/tokens/{tokenName}`

App+environment scoped tokens:

- `GET /projects/{project}/apps/{app}/tokens`
- `POST /projects/{project}/apps/{app}/tokens` body `{ name, environment }`
- `DELETE /projects/{project}/apps/{app}/tokens/{tokenName}`

## Repositories

- `GET /repos?provider={provider}`
- `GET /repos/{owner}/{repo}/branches?provider={provider}`
- `GET /repos/{owner}/{repo}/tree?provider={provider}&branch={branch}&path={path}`

## Platform config

- `GET /platform`
- `PATCH /platform`

Patch request is partial and merges into existing `PlatformConfig`.

## Important behavior notes

- API request body limit is 1 MiB for authenticated `/api` routes
- Webhook body limit is 10 MiB
- `env` query alias: handlers accept `env` first and `environment` as fallback
- Some endpoints default env to `production`; others require explicit env (notably `/env` and domains via `resolveAppEnv`)
- If logs/metrics adapters are unconfigured, history endpoints return `{ "available": false }`

## Curl examples by feature

Use this setup once in your shell:

```bash
BASE="http://localhost:8090"
EMAIL="admin@local"
PASSWORD="admin123"

TOKEN=$(curl -s -X POST "$BASE/api/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" | jq -r .token)
```

Auth and setup:

```bash
curl -s "$BASE/api/auth/status"

curl -s -X POST "$BASE/api/auth/setup" \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@local","password":"admin123"}'
```

Projects and apps:

```bash
curl -s -X POST "$BASE/api/projects" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"demo","description":"Demo project"}'

curl -s -X POST "$BASE/api/projects/demo/apps" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name":"web",
    "spec":{
      "source":{"type":"image","image":"nginx:1.27"},
      "network":{"public":true},
      "environments":[{"name":"production","replicas":1}]
    }
  }'
```

Project environments:

```bash
curl -s -X POST "$BASE/api/projects/demo/environments" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"staging","displayOrder":20}'

curl -s "$BASE/api/projects/demo/environments" \
  -H "Authorization: Bearer $TOKEN"
```

Env vars and shared vars:

```bash
curl -s -X PATCH "$BASE/api/projects/demo/apps/web/env?environment=production" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"set":{"LOG_LEVEL":"info","FEATURE_X":"true"},"unset":[]}'

curl -s -X PUT "$BASE/api/projects/demo/shared-vars" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '[{"name":"SENTRY_ENV","value":"prod"}]'
```

Secrets:

```bash
curl -s -X POST "$BASE/api/projects/demo/apps/web/secrets?env=production" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"web-api-key","data":{"API_KEY":"supersecret"}}'

curl -s "$BASE/api/projects/demo/apps/web/secrets?env=production" \
  -H "Authorization: Bearer $TOKEN"
```

Deploy and rollout actions:

```bash
curl -s -X POST "$BASE/api/projects/demo/apps/web/deploy" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"environment":"production","image":"nginx:1.27.1"}'

curl -s -X POST "$BASE/api/projects/demo/apps/web/redeploy?env=production" \
  -H "Authorization: Bearer $TOKEN"
```

Domains:

```bash
curl -s -X POST "$BASE/api/projects/demo/apps/web/domains?environment=production" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"domain":"web.example.com"}'

curl -s "$BASE/api/projects/demo/apps/web/domains?environment=production" \
  -H "Authorization: Bearer $TOKEN"
```

Deploy tokens (CI path):

```bash
APP_TOKEN=$(curl -s -X POST "$BASE/api/projects/demo/apps/web/tokens" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"gha-prod","environment":"production"}' | jq -r .token)

curl -s -X POST "$BASE/api/projects/demo/apps/web/deploy" \
  -H "Authorization: Bearer $APP_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"environment":"production","image":"nginx:1.27.2"}'
```

Runtime and observability:

```bash
curl -s "$BASE/api/projects/demo/apps/web/pods?env=production" \
  -H "Authorization: Bearer $TOKEN"

curl -s "$BASE/api/projects/demo/apps/web/metrics/current?env=production" \
  -H "Authorization: Bearer $TOKEN"

curl -N "$BASE/api/projects/demo/apps/web/logs?env=production&follow=true&token=$TOKEN"
```

Project events SSE:

```bash
curl -N "$BASE/api/projects/demo/events?token=$TOKEN"
```

Git provider and repos:

```bash
curl -s "$BASE/api/gitproviders" \
  -H "Authorization: Bearer $TOKEN"

curl -s "$BASE/api/repos?provider=github" \
  -H "Authorization: Bearer $TOKEN"
```

Stacks/templates:

```bash
curl -s "$BASE/api/templates" -H "Authorization: Bearer $TOKEN"

curl -s -X POST "$BASE/api/projects/demo/stacks" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"template":"supabase","name":"demo-supabase"}'
```

Platform config:

```bash
curl -s "$BASE/api/platform" -H "Authorization: Bearer $TOKEN"

curl -s -X PATCH "$BASE/api/platform" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"domain":"apps.example.com","tls":{"certManagerClusterIssuer":"letsencrypt-prod"}}'
```

## See also

- `docs/openapi.yaml` - OpenAPI 3.0 spec for this API
- `docs/systems-overview.md` - architecture and reconciliation overview
- `docs/api-quickstart.md` - end-to-end API walkthrough from setup to deploy
