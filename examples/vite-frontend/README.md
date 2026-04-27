# Vite Frontend Example

Deploys a Vite-based frontend from a git repository with build-time arguments.

## What it creates

- `frontend` - A public web app built from source with custom `VITE_*` build args

## Usage

```bash
# Update spec.source.repo to point to your Vite app repository, then apply
kubectl apply -f app.yaml -n pj-<your-project>
```

Build args are injected as Docker build arguments during the image build step.
