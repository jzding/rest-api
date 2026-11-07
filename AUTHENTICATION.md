# Authentication Configuration for REST API

This guide describes the behavior implemented in [v2/auth.go](v2/auth.go) and
[v2/server.go](v2/server.go). For cluster resources and mounts, see
[OpenShift Authentication](OPENSHIFT_AUTHENTICATION.md).

## Authentication mechanisms

- **mTLS** (`enableMTLS`): the TLS listener verifies presented client
  certificates against `caCertPath`. Protected HTTP handlers require a client
  certificate for non-loopback requests.
- **Bearer tokens** (`enableOAuth`, the existing configuration name): the
  library calls a `TokenValidator` installed by the embedding application with
  `Server.SetTokenValidator`. The library does not issue tokens, run an OAuth
  login flow, parse JWTs, fetch JWKS, or include a Kubernetes client.

Both mechanisms are required when both are enabled. Enabling bearer-token
authentication without installing a validator makes protected non-loopback
requests fail with HTTP 401.

cloud-event-proxy installs a Kubernetes TokenReview validator. A different
embedding application must supply its own validator; TokenReview is not an
automatic default of this library.

## Token issuance versus validation

Projected ServiceAccount tokens are obtained through Kubernetes TokenRequest
and mounted into pods by Kubernetes. They do not require an OpenShift OAuth
login. OpenShift OAuth access tokens are a separate credential type obtained
from the OpenShift OAuth server. TokenReview delegates validation of the
presented credential to the cluster; it can handle either type if the cluster
recognizes it and accepts the requested audiences.

`useOpenShiftOAuth` is retained in the configuration structure and log summary,
but the inspected REST API and cloud-event-proxy authentication code does not
use it to select a validator or obtain tokens. Setting it to true does not
initiate OAuth or change validation.

### cloud-event-proxy TokenReview behavior

The implementation in cloud-event-proxy's
[`pkg/auth/tokenreview.go`](https://github.com/redhat-cne/cloud-event-proxy/blob/main/pkg/auth/tokenreview.go)
uses in-cluster Kubernetes credentials, or `KUBECONFIG` when set:

1. Submit the bearer token and `requiredAudiences` in a TokenReview on a cache miss.
2. Reject API errors, a nonempty review error, or `status.authenticated: false`.
3. When audiences were requested, also reject an empty returned audience list.
   The implementation relies on the API server to return the valid intersection.
4. Return the authenticated username, UID, groups, and audiences.

For projected ServiceAccount JWTs, the cluster validates signature, issuer,
expiry and audience, as well as applicable object bindings. Opaque OAuth tokens
are authenticated through the cluster's configured token authentication path;
they need not be locally verifiable JWTs.

Successful results are cached for 30 seconds, with at most 4096 entries. Cache
keys include the token hash and requested audiences. Revocation or expiration
after a successful review may therefore take up to the remaining cache TTL to
be observed. This cache belongs to cloud-event-proxy, not this library.

`requiredAudiences` expresses acceptable recipients, not OAuth scopes or
permissions. Use a dedicated service audience when isolation is needed; sharing
the Kubernetes API audience does not bind the token uniquely to this service.
The library forwards this setting to the validator rather than checking token
claims locally.

## Request enforcement and health

All paths below are relative to the configured API prefix, normally
`/api/ocloudNotifications/v2`.

| Routes | Non-loopback requests |
| --- | --- |
| Subscription create, read, delete, and status ping | Require each enabled authentication mechanism |
| Publisher create, read, and delete | Require each enabled authentication mechanism |
| CurrentState, create/event, log, dummy, dummy2 | Require each enabled authentication mechanism |
| `GET /health` | No bearer token or client certificate required |

The middleware trusts requests whose remote address is loopback and skips its
authentication checks. That exemption is based on the connection's remote
address, not the Host header. With host networking, loopback represents the
node's network namespace rather than an isolated pod namespace. Account for
that trust boundary when deploying or placing a proxy in front of the server.

### Listener modes

| enableMTLS | enableOAuth | Listener and protected request requirements |
| --- | --- | --- |
| false | false | HTTP; no authentication |
| false | true | HTTP; bearer token |
| true | false | HTTPS; client certificate |
| true | true | HTTPS; client certificate and bearer token |

Bearer-token authentication alone does not enable TLS on this listener.
Provide an appropriately secured transport if using that mode across a network;
also account for the loopback exemption when forwarding traffic.

With mTLS enabled, the TLS listener uses `VerifyClientCertIfGiven`: no client
certificate is needed to reach health, but any certificate that is presented
must still pass TLS verification. Protected handlers enforce the certificate
requirement after the handshake. HTTPS health callers must trust the server
certificate and use a hostname matching it. The internal `EndPointHealthChk`
helper uses a separate localhost HTTPS client that skips server verification;
this is not the behavior of the outbound push client.

## Configuration

The fields below are defined by `AuthConfig` in [v2/server.go](v2/server.go).

| JSON field | Implemented behavior |
| --- | --- |
| `enableMTLS` | Enable the TLS listener and require client certificates on protected non-loopback requests |
| `caCertPath` | PEM trust bundle for inbound client certificates and, with mTLS enabled, outbound callback server certificates |
| `serverCertPath`, `serverKeyPath` | Serving certificate and private key |
| `clientCertPath`, `clientKeyPath` | Optional client keypair loaded by the mTLS outbound HTTP client |
| `enableOAuth` | Require bearer-token validation and enable token-file injection for outbound HTTPS requests |
| `requiredAudiences` | Audience list passed to the installed validator |
| `serviceAccountToken` | Token-file path used for outbound requests; not the inbound validator's Kubernetes client credentials |
| `serviceAccountName` | Descriptive configuration; does not select the pod identity or authorize callers |
| `useOpenShiftOAuth` | Retained flag with no token acquisition or validator selection behavior |
| `useServiceCA` | Retained flag; the library uses the configured file paths and does not provision certificates |
| `tlsMinVersion`, `tlsCipherSuites` | TLS profile values supplied by the embedding application/operator |

The TLS profile helper defaults to TLS 1.2 when no recognized minimum is supplied
and the TLS configuration has no minimum. Unknown cipher names and TLS 1.3
cipher names are ignored by its configurable cipher-suite mapping. The library
does not itself watch cluster TLS profile resources.

Example with certificates and an explicitly projected service token:

```json
{
  "enableMTLS": true,
  "caCertPath": "/etc/cloud-event-proxy/trust/ca-bundle.crt",
  "serverCertPath": "/etc/cloud-event-proxy/server-certs/tls.crt",
  "serverKeyPath": "/etc/cloud-event-proxy/server-certs/tls.key",
  "clientCertPath": "/etc/cloud-event-proxy/client-certs/tls.crt",
  "clientKeyPath": "/etc/cloud-event-proxy/client-certs/tls.key",
  "enableOAuth": true,
  "requiredAudiences": ["cloud-event-proxy"],
  "serviceAccountToken": "/var/run/secrets/cloud-event-proxy/token"
}
```

The audience `cloud-event-proxy` is an example deployment choice. Clients must
request tokens for that audience. For outbound pushes, the receiving callback
must accept the audience of the token in `serviceAccountToken`.

There are no `oauthIssuer`, `oauthJWKSURL`, `requiredScopes`, or singular
`requiredAudience` fields.

## Certificates and trust

The server refuses to start in mTLS mode without a usable CA pool or serving
keypair. A trusted client certificate is required on protected non-loopback
requests, but the middleware does not map its subject to endpoint permissions.

OpenShift Service CA can provide the serving certificate. It does not provide
the separate clientAuth certificate needed by an mTLS client. Provision that
keypair through your client-certificate issuer and include its issuer in the
server's trust bundle. A serving certificate cannot be reused as a client
certificate merely because it is signed by a trusted CA.

For outbound HTTPS with mTLS enabled, `caCertPath` is also the trust store for
callback servers. If client and serving certificates use different issuers,
include both CA chains. The code loads certificates and CA bundles during
initialization/startup; it does not watch those files for rotation. Arrange
reload/restart when certificate material changes.

## Outbound Push Authentication

`Server.HTTPClient` is used for initial subscription notifications and
publisher endpoint validation. Do not assume every event delivery performed by
other packages uses this client; those senders need their own configuration.

- With `enableMTLS`, the client loads `clientCertPath` and `clientKeyPath`
  when provided. A load failure is logged and the request proceeds without a
  client certificate; a callback requiring mTLS will reject it.
- With `enableOAuth` and a nonempty `serviceAccountToken` path, the transport
  reads the token file on each HTTPS request and attaches a Bearer header.
  This also works when mTLS is disabled. A missing/empty token leaves the
  request without that injected header.
- Tokens are not attached to HTTP callbacks. Without mTLS, outbound HTTPS uses
  the transport's default TLS trust rather than `caCertPath`.
- The client rejects redirects and uses a dialer that checks resolved addresses
  before connecting. Link-local, metadata, multicast, and unspecified addresses
  are blocked; private and loopback addresses are allowed intentionally.

For host-networked publishers, check the actual source address seen by the
consumer when designing NetworkPolicy rules. Namespace selectors alone may not
cover host or SNATed traffic.

## Client examples

Run these from a separate client pod with the appropriate certificates and
projected token mounted. Using localhost would exercise the loopback exemption
and may not match the serving certificate's DNS names.

```bash
API=https://ptp-event-publisher-service.openshift-ptp.svc:9043/api/ocloudNotifications/v2

# Protected read: both mechanisms enabled.
curl --cacert service-ca.crt --cert client.crt --key client.key \
  -H "Authorization: Bearer $(cat /var/run/secrets/cloud-event-proxy/token)" \
  "$API/subscriptions"

# Protected write; callback must be reachable and accept the producer's credentials.
curl --cacert service-ca.crt --cert client.crt --key client.key \
  -H "Authorization: Bearer $(cat /var/run/secrets/cloud-event-proxy/token)" \
  -H "Content-Type: application/json" \
  -d '{"EndpointUri":"https://event-consumer.openshift-ptp.svc:9043/callback","ResourceAddress":"/test/resource"}' \
  "$API/subscriptions"

# Health needs server trust, but no client certificate or bearer token.
curl --cacert service-ca.crt "$API/health"
```

Do not enable shell tracing or log token values when running authenticated
commands. With bearer authentication disabled, omit the Authorization header;
with all authentication disabled, the listener uses HTTP.

## Authorization and operation

Authentication confirms a credential is accepted. The REST API middleware
does not enforce OAuth scopes, perform SubjectAccessReview, or apply
per-user/per-ServiceAccount endpoint RBAC. TokenReview permissions authorize the
server to call Kubernetes; they do not restrict which authenticated callers may
use this REST API. Apply any additional authorization in the embedding
application or a deliberately configured access layer.

For TokenReview RBAC, projected-token mounts, and Service CA resources, use the
[OpenShift deployment examples](OPENSHIFT_AUTHENTICATION.md#deployment-resources).
Monitor TokenReview errors, API-server reachability, certificate expiry, and
callback authentication failures. An API-server outage causes validation failures
on cache misses; a cached successful result can remain usable until its TTL ends.
