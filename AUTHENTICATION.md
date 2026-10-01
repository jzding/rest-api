# Authentication Configuration for REST API

This document describes how to configure mTLS (mutual TLS) and OAuth authentication for the REST API server using OpenShift's built-in Service CA and OAuth server.

## Overview

The REST API supports two authentication mechanisms that can be applied to specific endpoints:

1. **mTLS (Mutual TLS)**: Client certificate-based authentication using OpenShift Service CA
2. **OAuth**: Bearer token-based authentication. Tokens are validated server-side via the Kubernetes **TokenReview** API, which verifies the token's issuer, signature, expiry, and audience. The REST API library itself does not parse JWTs or fetch a JWKS; it delegates verification to the cluster and only enforces the required audiences it is configured with.

Both mechanisms can be enabled independently or together for enhanced security. This unified approach works seamlessly for both single node and multi-node OpenShift clusters, providing enterprise-grade security with minimal complexity.

### Security Guarantees

- **No Authentication Bypass**: When OAuth is enabled, every non-loopback request must present a valid bearer token
- **Server-side Token Validation**: Issuer, signature, expiry, and audience are all validated by the Kubernetes TokenReview API
- **Audience Binding**: When `requiredAudiences` is configured, the token must be bound to one of those audiences
- **Clear Error Messages**: Authentication failures return specific error codes without exposing sensitive information

## Protected vs Public Endpoints

### Protected Endpoints (Require Authentication)

When authentication is enabled, **every** data endpoint requires authentication for non-loopback requests. Only `GET /health` is exempt. (Requests originating from the pod's own loopback interface are treated as a trusted same-pod fast-path and skip authentication.)

#### Subscription Management
- `POST /subscriptions` - Create subscription
- `GET /subscriptions` - List all subscriptions
- `GET /subscriptions/{subscriptionId}` - Get subscription details
- `DELETE /subscriptions/{subscriptionId}` - Delete specific subscription
- `DELETE /subscriptions` - Delete all subscriptions
- `PUT /subscriptions/status/{subscriptionId}` - Ping for subscription status

#### Publisher Management
- `POST /publishers` - Create publisher
- `GET /publishers` - List all publishers
- `GET /publishers/{publisherid}` - Get publisher details
- `DELETE /publishers/{publisherid}` - Delete specific publisher
- `DELETE /publishers` - Delete all publishers

#### Event Management
- `POST /create/event` - Publish event
- `POST /log` - Log event
- `GET /{ResourceAddress}/CurrentState` - Get current state

#### Test Endpoints
- `POST /dummy` - Test endpoint
- `POST /dummy2` - Test endpoint

### Public Endpoints (No Authentication Required)

Only the health endpoint is reachable without authentication:

- `GET /health` - Service health check (see the special mTLS behavior below)

### Health Endpoint Behavior

The `/health` endpoint has special behavior based on authentication configuration:

#### When Authentication is Disabled
- Accessible via HTTP without any authentication
- Simple health check for service availability

#### When mTLS is Enabled
- Accessible via HTTPS only
- **Requires a valid client certificate** for access
- Used by internal services (like PTP daemon) for health checks
- Service CA certificate is required for internal health checks

**Note**: Even though the `/health` endpoint is considered "public" in terms of business logic, when mTLS is enabled, it still requires proper certificate authentication for security reasons.

## Server Architecture

### Single Server with Conditional Authentication

The REST API uses a single server architecture that adapts based on authentication configuration:

1. **No Authentication**: Server runs on HTTP, all endpoints accessible without authentication
2. **mTLS Only**: Server runs on HTTPS with client certificate validation
3. **OAuth Only**: Server runs on HTTP with Bearer token validation
4. **Both mTLS and OAuth**: Server runs on HTTPS with both client certificate and Bearer token validation

### Health Endpoint Implementation

The `/health` endpoint is always included in the main server but behaves differently based on authentication:

- **Without mTLS**: Accessible via HTTP without authentication
- **With mTLS**: Accessible via HTTPS but requires valid client certificate
- **Internal health checks** (like PTP daemon) use the service CA certificate for authentication

This approach ensures:
- Consistent server architecture
- No port conflicts
- Proper security when mTLS is enabled
- Internal services can still perform health checks

## Outbound Push Authentication

The sections above describe authentication the server *enforces on inbound
requests*. The server is also an mTLS/OAuth **client** on one leg: when a
subscription is created it pushes an initial notification (and later state-change
events) to the subscriber's `EndpointUri` callback. If the consumer callback
enforces mTLS and/or OAuth, that push must authenticate too — otherwise the
callback returns 401 and the subscription is never established.

The push client (`Server.HTTPClient`, built in `InitServer`) is wired from the
same `AuthConfig`:

- **Client certificate (mTLS):** when `ClientCertPath`/`ClientKeyPath` are set and
  resolve, the keypair is loaded into the push client's `tls.Config.Certificates`.
  The OpenShift Service CA only mints **serverAuth** certificates, so the serving
  cert cannot be reused here — provide a dedicated **clientAuth** keypair. If the
  paths are empty or unreadable the client is built without a certificate (a log
  line is emitted) and the push proceeds with bearer-token auth only.
- **Bearer token (OAuth):** when `EnableOAuth` is true and `ServiceAccountToken`
  points at a token file, every push carries an `Authorization: Bearer <token>`
  header. This is wired **independently of mTLS** — an OAuth-only configuration
  (no `EnableMTLS`) still authenticates its pushes with the token. The token is
  **re-read from disk per request** so rotated, projected ServiceAccount tokens
  are always current, and it is attached **only to `https` callbacks**: a
  subscriber may register an `http://` `EndpointUri`, and the token is a cluster
  credential that must never be sent over plaintext, so plain-http pushes are sent
  without it (the callback can still enforce its own auth).
- **Server verification (RootCAs):** `CACertPath` is dual-purpose — it is both the
  `ClientCAs` used to verify inbound client certs and the `RootCAs` used to verify
  the *consumer's* server certificate on the outbound push. The consumer's serving
  CA must therefore be present in that bundle.

> **Networking caveat (OpenShift / OVN-Kubernetes):** when the publisher runs with
> `hostNetwork: true` (as the PTP `linuxptp-daemon` does), its push traffic to a
> ClusterIP consumer is SNATed to the cluster's OVN-Kubernetes **join subnet**
> (default `100.64.0.0/16`), not a pod IP in the publisher's namespace. A consumer
> `NetworkPolicy` that only admits the publisher namespace via `namespaceSelector`
> will silently drop the push; add an `ipBlock` for the join subnet on the callback
> port. See the consumer example `network-policy.yaml` in cloud-event-proxy.

## Configuration

### Authentication Configuration Structure

```go
type AuthConfig struct {
    // mTLS configuration - works for both single and multi-node clusters
    EnableMTLS     bool   `json:"enableMTLS"`
    CACertPath     string `json:"caCertPath"`
    ServerCertPath string `json:"serverCertPath"`
    ServerKeyPath  string `json:"serverKeyPath"`

    // Client certificate presented on the OUTBOUND push leg (see "Outbound Push
    // Authentication" below). The serving cert (ServerCertPath) is serverAuth-only
    // and cannot be reused as a client cert, so a dedicated clientAuth keypair is
    // supplied here. Optional: when either path is empty or does not resolve, the
    // push client is built without a client certificate and relies on the bearer
    // token alone.
    ClientCertPath string `json:"clientCertPath"`
    ClientKeyPath  string `json:"clientKeyPath"`

    UseServiceCA   bool   `json:"useServiceCA"` // Use OpenShift Service CA (recommended for all cluster sizes)

    // OAuth 2.0 / bearer-token configuration. Tokens are validated by the
    // TokenValidator installed via Server.SetTokenValidator (cloud-event-proxy
    // uses the Kubernetes TokenReview API), so no issuer/JWKS is configured here.
    EnableOAuth         bool     `json:"enableOAuth"`
    RequiredAudiences   []string `json:"requiredAudiences"`   // Required token audiences (validated by TokenReview)
    ServiceAccountName  string   `json:"serviceAccountName"`  // ServiceAccount used by clients for authentication
    ServiceAccountToken string   `json:"serviceAccountToken"` // ServiceAccount token path (client side)
    UseOpenShiftOAuth   bool     `json:"useOpenShiftOAuth"`   // Client hint: obtain tokens from OpenShift OAuth

    // TLS profile - centrally managed by the cluster's TLSSecurityProfile and
    // propagated by the operator. Nothing is hardcoded in this library.
    TLSMinVersion   string   `json:"tlsMinVersion"`   // e.g. "VersionTLS12", "VersionTLS13"
    TLSCipherSuites []string `json:"tlsCipherSuites"` // IANA cipher suite names
}
```

### Example Configuration

See `openshift-auth-config.json` for a complete configuration example that works for both single node and multi-node clusters:

```json
{
  "enableMTLS": true,
  "useServiceCA": true,
  "caCertPath": "/etc/cloud-event-proxy/ca-bundle/service-ca.crt",
  "serverCertPath": "/etc/cloud-event-proxy/server-certs/tls.crt",
  "serverKeyPath": "/etc/cloud-event-proxy/server-certs/tls.key",
  "clientCertPath": "/etc/cloud-event-proxy/client-certs/tls.crt",
  "clientKeyPath": "/etc/cloud-event-proxy/client-certs/tls.key",
  "enableOAuth": true,
  "useOpenShiftOAuth": true,
  "requiredAudiences": ["https://kubernetes.default.svc"],
  "serviceAccountName": "cloud-event-proxy-sa",
  "serviceAccountToken": "/var/run/secrets/kubernetes.io/serviceaccount/token"
}
```

> **Note:** There is no `oauthIssuer`, `oauthJWKSURL`, `requiredScopes`, or `requiredAudience` (singular) field. Token issuer, signature, and expiry are validated by the Kubernetes TokenReview API. The only OAuth policy this library enforces locally is `requiredAudiences` (an array): the presented token must be bound to one of the listed audiences.

## OpenShift Integration

### Service CA (Recommended)

OpenShift's Service CA provides automatic certificate management for both single node and multi-node clusters:

#### Prerequisites
- OpenShift cluster (single node or multi-node)
- No additional operators required

#### Certificate Resources
- **Service**: Annotated with `service.beta.openshift.io/serving-cert-secret-name` for automatic certificate generation
- **Secret**: Automatically created by Service CA with server certificates

#### Example Service CA Configuration
```yaml
apiVersion: v1
kind: Service
metadata:
  name: ptp-event-publisher-service
  namespace: openshift-ptp
  annotations:
    service.beta.openshift.io/serving-cert-secret-name: cloud-event-proxy-tls
spec:
  selector:
    app: linuxptp-daemon
  ports:
  - port: 9043
    targetPort: 9043
  type: ClusterIP
```

### OpenShift OAuth / TokenReview

The OAuth implementation validates bearer tokens through the Kubernetes TokenReview API:

#### Prerequisites
- OpenShift cluster (single node or multi-node)
- The server's ServiceAccount must be permitted to create `tokenreviews` (via the `system:auth-delegator` ClusterRole or an equivalent binding)
- No additional operators required

#### OAuth Configuration
- **Token Validation**: The server calls the Kubernetes TokenReview API, which verifies issuer, signature, expiry, and audience server-side
- **ServiceAccount Tokens**: Clients present a projected ServiceAccount token bound to the required audience
- **Audience Binding**: `requiredAudiences` enforces that the token was minted for this service
- **RBAC**: The server SA needs `create` on `authentication.k8s.io/tokenreviews`

#### Example ServiceAccount Configuration
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: cloud-event-proxy-client
  namespace: openshift-ptp
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  namespace: openshift-ptp
  name: cloud-event-proxy-oauth
rules:
- apiGroups: [""]
  resources: ["serviceaccounts"]
  verbs: ["get", "list"]
- apiGroups: ["authentication.k8s.io"]
  resources: ["tokenreviews"]
  verbs: ["create"]
- apiGroups: ["authorization.k8s.io"]
  resources: ["subjectaccessreviews"]
  verbs: ["create"]
```

## mTLS Configuration

### Certificate Requirements

1. **CA Certificate** (`caCertPath`): The Certificate Authority certificate used to validate client certificates
2. **Server Certificate** (`serverCertPath`): The server's TLS certificate
3. **Server Private Key** (`serverKeyPath`): The server's private key

### Certificate Generation Example

```bash
# Generate CA private key
openssl genrsa -out ca.key 4096

# Generate CA certificate
openssl req -new -x509 -key ca.key -sha256 -subj "/C=US/ST=CA/O=MyOrg/CN=MyCA" -days 3650 -out ca.crt

# Generate server private key
openssl genrsa -out server.key 4096

# Generate server certificate signing request
openssl req -new -key server.key -out server.csr -subj "/C=US/ST=CA/O=MyOrg/CN=localhost"

# Generate server certificate signed by CA
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out server.crt -days 365 -sha256

# Generate client private key
openssl genrsa -out client.key 4096

# Generate client certificate signing request
openssl req -new -key client.key -out client.csr -subj "/C=US/ST=CA/O=MyOrg/CN=client"

# Generate client certificate signed by CA
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out client.crt -days 365 -sha256
```

## Client Examples

### Protected Endpoint Examples

#### Create Subscription (with both mTLS and OAuth)

```bash
# With both mTLS and OAuth
curl -X POST https://localhost:9043/api/ocloudNotifications/v2/subscriptions \
  --cert client.crt \
  --key client.key \
  --cacert ca.crt \
  -H "Authorization: Bearer valid_your_jwt_token_here" \
  -H "Content-Type: application/json" \
  -d '{"EndpointUri": "http://example.com/callback", "ResourceAddress": "/test/resource"}'

# With only mTLS (if OAuth is disabled)
curl -X POST https://localhost:9043/api/ocloudNotifications/v2/subscriptions \
  --cert client.crt \
  --key client.key \
  --cacert ca.crt \
  -H "Content-Type: application/json" \
  -d '{"EndpointUri": "http://example.com/callback", "ResourceAddress": "/test/resource"}'

# With only OAuth (if mTLS is disabled)
curl -X POST http://localhost:9043/api/ocloudNotifications/v2/subscriptions \
  -H "Authorization: Bearer valid_your_jwt_token_here" \
  -H "Content-Type: application/json" \
  -d '{"EndpointUri": "http://example.com/callback", "ResourceAddress": "/test/resource"}'
```

#### Delete Publisher (with both mTLS and OAuth)

```bash
curl -X DELETE https://localhost:9043/api/ocloudNotifications/v2/publishers/publisher-id \
  --cert client.crt \
  --key client.key \
  --cacert ca.crt \
  -H "Authorization: Bearer valid_your_jwt_token_here"
```

### Read (GET) Endpoint Examples

`GET` endpoints are protected too - when authentication is enabled they require the same mTLS client certificate and/or bearer token as the write endpoints.

#### List Subscriptions

```bash
# With both mTLS and OAuth
curl -X GET https://localhost:9043/api/ocloudNotifications/v2/subscriptions \
  --cert client.crt \
  --key client.key \
  --cacert ca.crt \
  -H "Authorization: Bearer valid_your_token_here"

# When authentication is disabled (plain HTTP, no auth)
curl -X GET http://localhost:9043/api/ocloudNotifications/v2/subscriptions
```

#### Health Check

```bash
# When mTLS is enabled (requires client certificate)
curl -X GET https://localhost:9043/api/ocloudNotifications/v2/health \
  --cert client.crt \
  --key client.key \
  --cacert ca.crt

# When mTLS is disabled (no authentication required)
curl -X GET http://localhost:9043/api/ocloudNotifications/v2/health

# Internal health check (for services like PTP daemon)
curl -X GET https://localhost:9043/api/ocloudNotifications/v2/health \
  --cacert /etc/cloud-event-proxy/ca-bundle/service-ca.crt
```

## OAuth Security Implementation

### Validation via Kubernetes TokenReview

The OAuth implementation delegates all cryptographic token validation to the Kubernetes TokenReview API. The following checks are performed server-side by the cluster:

1. **Issuer & Signature Validation**:
   - The token's issuer and signature are verified by the cluster; tokens the API server does not recognize are rejected.
   - No bypass mechanisms or fallbacks.

2. **Expiration Checking**:
   ```
   Token expired
   ```
   - Expired tokens are rejected by TokenReview.

3. **Audience Validation**:
   ```
   Token audience validation failed
   ```
   - When `requiredAudiences` is set, the token must be bound to one of those audiences.
   - Prevents token misuse across different services.

4. **Missing Token Handling**:
   ```
   Authorization header required
   Bearer token required
   ```
   - Clear error messages for missing or malformed tokens.
   - Proper HTTP status codes (401 Unauthorized).

### Security Properties

- **No local JWT parsing / JWKS fetching**: verification is performed by the Kubernetes API server via TokenReview.
- **Bounded token cache**: validated results are cached briefly with a short TTL to bound TokenReview load.
- **Memory Safety**: tokens are never logged or otherwise exposed.

## Security Considerations

1. **Certificate Management**
   - Implement proper certificate rotation
   - Use secure storage for private keys
   - Use OpenShift Service CA for automated certificate management

2. **OAuth Security**
   - **Server-side Validation**: All tokens are validated by the Kubernetes TokenReview API (issuer, signature, expiry, audience)
   - **No Bypass Mechanisms**: Non-loopback requests without a valid token are rejected with 401
   - **Audience Binding**: Configure `requiredAudiences` so tokens minted for other services are rejected
   - A short-TTL cache bounds TokenReview load without weakening validation

3. **TLS Configuration**
   - Use TLS 1.2 or higher
   - Configure secure cipher suites
   - Enable HTTP/2 when possible

4. **Access Control**
   - Monitor and log authentication failures
   - Implement rate limiting
   - Consider IP whitelisting for sensitive endpoints

5. **Error Handling**
   - Use generic error messages in production
   - Don't expose internal details in error responses
   - Log detailed errors server-side

6. **Health Endpoint Security**
   - When mTLS is enabled, health endpoint requires client certificates
   - Internal services should use service CA certificates for health checks
   - External health checks require proper client certificates
   - Consider network policies to restrict health endpoint access

## Production Recommendations

1. **Authentication Infrastructure**
   - Use a proper OAuth 2.0 server (e.g., Keycloak, Auth0)
   - Implement a certificate management solution
   - Consider using a service mesh for mTLS

2. **Monitoring and Logging**
   - Log all authentication events
   - Monitor authentication failures
   - Set up alerts for suspicious activity

3. **Security Hardening**
   - Use hardware security modules (HSMs) for key storage
   - Implement certificate revocation checking
   - Regular security audits and penetration testing

4. **Performance Optimization**
   - Implement token caching
   - Use connection pooling
   - Configure appropriate timeouts

5. **Operational Considerations**
   - Document certificate rotation procedures
   - Create incident response plans
   - Regular security training for team members
