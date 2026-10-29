# AccessServiceToken

Namespaced. Manages a Zero Trust Access **service token** for machine-to-machine access and stores
its credentials in a Secret. Reference it from policies with `serviceTokens: [{name: <token name>}]`
and a `non_identity` decision.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: AccessServiceToken
metadata:
  name: ci
  namespace: default
spec:
  accountRef:
    name: main
  name: ci-runner             # defaults to metadata.name
  duration: 8760h             # token lifetime, hours only; default one year
  refreshBefore: 720h         # extend the token when it expires within this window
  secretName: ci-access-credentials   # default <name>-access-token
  deletionPolicy: Delete
```

## Secret

| Key | Value |
|-----|-------|
| `CF_ACCESS_CLIENT_ID` | Client ID (`<id>.access`) |
| `CF_ACCESS_CLIENT_SECRET` | Client secret |

Clients send them as the `CF-Access-Client-Id` and `CF-Access-Client-Secret` headers.

## Status

| Field | Description |
|-------|-------------|
| `tokenID` | Service token ID. |
| `clientID` | Client ID. |
| `expiresAt` | Current expiry. |
| `secretName` | The Secret holding the credentials. |

## Behaviour

- The client secret is only returned at creation and rotation. If the Secret is deleted, the token
  is **rotated** (new secret, same client ID) and the Secret recreated. Clients using the old secret
  stop working.
- An existing token with the same name is adopted; because its secret is unknown it is rotated once
  during adoption.
- When the token expires within `refreshBefore` (capped at half its lifetime) it is **refreshed**,
  which extends the expiry without changing the secret.
