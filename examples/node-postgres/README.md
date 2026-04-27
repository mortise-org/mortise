# Node + Postgres Example

Deploys a Node.js web app backed by a Postgres database within a single Mortise project.

## What it creates

- `postgres` - A private Postgres 16 instance with 1Gi persistent storage
- `web` - A public Node.js service (port 3000) bound to the postgres app

## Usage

```bash
# Create a project first, then apply both apps into its control namespace
kubectl apply -f postgres.yaml -n pj-<your-project>
kubectl apply -f web.yaml -n pj-<your-project>
```

The web app receives database credentials via the binding to `postgres`.
Expect the web pods to restart once or twice while postgres initializes.
