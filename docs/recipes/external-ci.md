# External CI: Build Images Outside Mortise

Mortise's built-in BuildKit is optional. If your team already uses GitHub
Actions, GitLab CI, or any other CI system, you can build images there and
tell Mortise to deploy them.

## How it works

1. CI builds and pushes an OCI image to any registry Mortise can pull from.
2. CI calls the Mortise deploy webhook with the image reference.
3. Mortise updates the App and rolls out the new image.

## Prerequisites

- A **deploy token** created via the Mortise API or CLI:
  ```bash
  mortise deploy-token create my-ci-token --project my-saas --app web
  ```
- The token is returned once. Store it as a CI secret.

## Webhook endpoint

```
POST https://<mortise-domain>/api/v1/projects/{project}/apps/{app}/deploy
Authorization: Bearer <deploy-token>
Content-Type: application/json

{
  "image": "ghcr.io/myorg/myapp:sha-abc1234"
}
```

A successful response returns `200 OK` with the new deployment revision.

## GitHub Actions example

```yaml
name: Build and Deploy
on:
  push:
    branches: [main]

jobs:
  build-and-deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Log in to GHCR
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and push
        uses: docker/build-push-action@v6
        with:
          push: true
          tags: ghcr.io/${{ github.repository }}:sha-${{ github.sha }}

      - name: Deploy to Mortise
        run: |
          curl -sf -X POST \
            -H "Authorization: Bearer ${{ secrets.MORTISE_DEPLOY_TOKEN }}" \
            -H "Content-Type: application/json" \
            -d '{"image": "ghcr.io/${{ github.repository }}:sha-${{ github.sha }}"}' \
            "${{ vars.MORTISE_URL }}/api/v1/projects/${{ vars.PROJECT }}/apps/${{ vars.APP }}/deploy"
```

## GitLab CI example

```yaml
stages:
  - build
  - deploy

build:
  stage: build
  image: docker:27
  services:
    - docker:27-dind
  script:
    - docker login -u $CI_REGISTRY_USER -p $CI_REGISTRY_PASSWORD $CI_REGISTRY
    - docker build -t $CI_REGISTRY_IMAGE:$CI_COMMIT_SHORT_SHA .
    - docker push $CI_REGISTRY_IMAGE:$CI_COMMIT_SHORT_SHA

deploy:
  stage: deploy
  image: curlimages/curl:8.11.0
  script:
    - |
      curl -sf -X POST \
        -H "Authorization: Bearer $MORTISE_DEPLOY_TOKEN" \
        -H "Content-Type: application/json" \
        -d "{\"image\": \"$CI_REGISTRY_IMAGE:$CI_COMMIT_SHORT_SHA\"}" \
        "$MORTISE_URL/api/v1/projects/$PROJECT/apps/$APP/deploy"
```

## Any CI system

The pattern is always the same:

1. `docker build && docker push` (or buildah, kaniko, etc.)
2. `curl -X POST` the deploy webhook with the image ref and deploy token.

No Mortise-specific tooling required.

## Further reading

- [Mortise deploy tokens](../api/deploy-tokens.md)
- [App source types](../../SPEC.md) -- `source.type: image`
