# Recover admin access

When nobody can log in — a lost admin password, or every saved CLI token has
expired — there is no API-side recovery by construction. The recovery
credential is cluster access to `mortise-system`.

## Why a hand-minted token does not work

Tokens are signed with `mortise-system/mortise-jwt-key` **and** carry the
user's password generation (`pwd_gen`). The API checks that claim against the
`password_gen` field of the user's Secret on every request, so a token minted
outside the API without the current generation reads as `invalid token` even
with the right key. Do not mint tokens by hand; reset the password instead.

## Reset a password

```bash
mortise admin reset-password you@example.com
# or, from a script (first line of stdin is the password):
printf '%s\n' "$NEW_PASSWORD" | mortise admin reset-password you@example.com --password-stdin
```

This writes a new bcrypt hash and bumps the password generation, which
invalidates every previously issued token for that user. Then:

```bash
mortise login https://mortise.example.com
```

`--kubeconfig` and `--context` select the cluster, as for `mortise diff`.

## Create a user without logging in

```bash
mortise admin create-user ops@example.com --role admin
```

Roles are `admin` or `member`. For day-to-day user management use the UI or
`/api/admin/users`; the cluster-side verbs exist for recovery and for
bootstrapping automation accounts.

## Long-lived tokens for automation

Once logged in, `mortise token create` issues a deploy token that does not
depend on the interactive session. Rotating that user's password invalidates
those tokens too, by design.
