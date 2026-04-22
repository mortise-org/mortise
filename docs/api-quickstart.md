# API Quickstart

This quickstart walks through a complete API-first flow:

1. check setup
2. bootstrap/login
3. create a project
4. create an app
5. configure env vars and secrets
6. trigger deploy
7. inspect runtime state

Assumptions:

- Mortise API is reachable at `http://localhost:8090`
- `jq` is installed for parsing JSON

## 1) Set base variables

```bash
BASE="http://localhost:8090"
EMAIL="admin@local"
PASSWORD="admin123"
PROJECT="quickstart"
APP="web"
ENV="production"
```

## 2) Check whether setup is required

```bash
curl -s "$BASE/api/auth/status" | jq
```

If `setupRequired` is `true`, run setup:

```bash
curl -s -X POST "$BASE/api/auth/setup" \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" | jq
```

If setup already exists, login:

```bash
TOKEN=$(curl -s -X POST "$BASE/api/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" | jq -r .token)
echo "$TOKEN" | cut -c1-24
```

## 3) Create a project

```bash
curl -s -X POST "$BASE/api/projects" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$PROJECT\",\"description\":\"API quickstart project\"}" | jq
```

Verify it exists:

```bash
curl -s "$BASE/api/projects/$PROJECT" \
  -H "Authorization: Bearer $TOKEN" | jq
```

## 4) Create an app (image source)

```bash
curl -s -X POST "$BASE/api/projects/$PROJECT/apps" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{
    \"name\":\"$APP\",
    \"spec\":{
      \"source\":{\"type\":\"image\",\"image\":\"nginx:1.27\"},
      \"network\":{\"public\":true},
      \"environments\":[
        {\"name\":\"$ENV\",\"replicas\":1,\"resources\":{\"cpu\":\"100m\",\"memory\":\"128Mi\"}}
      ]
    }
  }" | jq '{name: .metadata.name, phase: .status.phase, source: .spec.source.type}'
```

## 5) Set env vars and a secret

Set environment variables:

```bash
curl -s -X PATCH "$BASE/api/projects/$PROJECT/apps/$APP/env?environment=$ENV" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"set":{"PORT":"8080","LOG_LEVEL":"info"},"unset":[]}' | jq
```

Create a secret:

```bash
curl -s -X POST "$BASE/api/projects/$PROJECT/apps/$APP/secrets?env=$ENV" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"web-secret","data":{"API_KEY":"supersecret"}}' | jq
```

## 6) Trigger deploy with a new image tag

```bash
curl -s -X POST "$BASE/api/projects/$PROJECT/apps/$APP/deploy" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"environment\":\"$ENV\",\"image\":\"nginx:1.27.1\"}" | jq
```

## 7) Inspect runtime state

List pods:

```bash
curl -s "$BASE/api/projects/$PROJECT/apps/$APP/pods?env=$ENV" \
  -H "Authorization: Bearer $TOKEN" | jq
```

Inspect current metrics:

```bash
curl -s "$BASE/api/projects/$PROJECT/apps/$APP/metrics/current?env=$ENV" \
  -H "Authorization: Bearer $TOKEN" | jq
```

Stream logs (SSE):

```bash
curl -N "$BASE/api/projects/$PROJECT/apps/$APP/logs?env=$ENV&follow=true&token=$TOKEN"
```

## 8) Optional: create deploy token for CI

Create app+environment deploy token:

```bash
DEPLOY_TOKEN=$(curl -s -X POST "$BASE/api/projects/$PROJECT/apps/$APP/tokens" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"ci-$ENV\",\"environment\":\"$ENV\"}" | jq -r .token)

echo "$DEPLOY_TOKEN" | cut -c1-24
```

Use deploy token (no JWT):

```bash
curl -s -X POST "$BASE/api/projects/$PROJECT/apps/$APP/deploy" \
  -H "Authorization: Bearer $DEPLOY_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"environment\":\"$ENV\",\"image\":\"nginx:1.27.2\"}" | jq
```

## 9) Cleanup

Delete the project:

```bash
curl -s -X DELETE "$BASE/api/projects/$PROJECT" \
  -H "Authorization: Bearer $TOKEN" | jq
```

The project controller and Kubernetes garbage collection handle teardown of owned resources.
