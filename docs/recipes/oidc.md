# OIDC Authentication

Mortise supports any OIDC-compliant identity provider. This recipe covers
setup for the most common providers.

## Overview

1. Create an OIDC client (application) in your identity provider.
2. Set the Mortise callback URL.
3. Configure `PlatformConfig` with the OIDC client details.
4. Users log in via the standard OIDC flow.

## Callback URL

All providers need to know the redirect URI:

```
https://<mortise-domain>/api/auth/oidc/callback
```

## PlatformConfig

```yaml
apiVersion: mortise.dev/v1alpha1
kind: PlatformConfig
metadata:
  name: platform
spec:
  auth:
    mode: oidc
    oidc:
      issuerURL: https://idp.example.com/realms/mortise
      clientID: mortise
      clientSecretRef:
        name: oidc-client-secret
        key: client-secret
      # Optional: restrict to specific email domains
      allowedDomains:
        - example.com
```

Create the client secret:

```bash
kubectl create secret generic oidc-client-secret \
  -n mortise-system \
  --from-literal=client-secret=<your-client-secret>
```

## Provider-specific setup

### Authentik

1. **Admin > Applications > Create.**
2. Name: `Mortise`, Slug: `mortise`.
3. Create a new OAuth2/OIDC Provider:
   - Client type: Confidential
   - Redirect URIs: `https://<mortise-domain>/api/auth/oidc/callback`
   - Scopes: `openid email profile`
4. Copy the Client ID and Client Secret into PlatformConfig.
5. Issuer URL: `https://authentik.example.com/application/o/mortise/`

### Keycloak

1. Create realm (or use existing).
2. **Clients > Create client.**
   - Client ID: `mortise`
   - Client authentication: On
   - Valid redirect URIs: `https://<mortise-domain>/api/auth/oidc/callback`
3. Copy credentials from the **Credentials** tab.
4. Issuer URL: `https://keycloak.example.com/realms/<realm>`

### Okta

1. **Applications > Create App Integration > OIDC - Web Application.**
2. Sign-in redirect URI: `https://<mortise-domain>/api/auth/oidc/callback`
3. Assignments: assign users or groups.
4. Copy Client ID and Client Secret.
5. Issuer URL: `https://<your-org>.okta.com`

### Google

1. **Google Cloud Console > APIs & Services > Credentials > Create OAuth client ID.**
2. Application type: Web application.
3. Authorized redirect URIs: `https://<mortise-domain>/api/auth/oidc/callback`
4. Copy Client ID and Client Secret.
5. Issuer URL: `https://accounts.google.com`

## Testing

After applying the PlatformConfig change, visit the Mortise UI. The login
page should show an "SSO Login" button that redirects to your IdP.

## Notes

- Mortise auto-creates a local user account on first OIDC login. The email
  from the `email` claim becomes the username.
- The `allowedDomains` field restricts which email domains can sign up. Omit
  it to allow any authenticated user.
- Admin users can still log in with local credentials if `auth.mode` is set
  to `oidc+local` (fallback mode).

## Further reading

- [OpenID Connect specification](https://openid.net/connect/)
- [Authentik docs](https://docs.goauthentik.io/)
- [Keycloak docs](https://www.keycloak.org/documentation)
