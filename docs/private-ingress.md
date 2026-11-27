# Opening private developer apps

Only the developer who owns the private app can open it. Drop signs a five-minute credential scoped to the app ID. Open the app from Drop again when the credential expires. The guest can also require its own sign-in.

## Node and ingress configuration

The updated agent renders a private app's only hostname together with
NibRunner's JWT access policy (Ed25519, issuer `drop-private-app`, audience
equal to the NibRunner app ID). The guest runs behind that policy even if it
has no sign-in of its own. The control plane keeps the source hostname out
of the older public-hostnames field. Nodes must report the private-ingress
capability before placement.

For browser access, put `drop private-ingress` between the TLS-terminating
wildcard app proxy and NibRunner's loopback HTTP listener on each serving
node. Keep the control plane's own hostname on its existing backend. Use a
separate domain for the control plane and the apps. Preserve the app's HTTP
Host header. Keep the wildcard gateway and runner bound to loopback and
disable edge caching for private app traffic and the handoff endpoint.
All app hostnames must reach the node holding the corresponding deployment.
The gateway has a fixed upstream and needs no credentials, database, AWS
permissions or access to the node's secret files.

For example, for a runner on port 80 and a wildcard TLS proxy on the same
host, install the provided `deploy/drop-ax41/drop-private-ingress.service`
on that host and supply `/etc/drop/private-ingress.env`:

```ini
DROP_PRIVATE_LISTEN=127.0.0.1:8110
DROP_PRIVATE_UPSTREAM=127.0.0.1:80
DROP_APP_DOMAIN=apps.example.test
DROP_CONTROL_ORIGIN=https://drop.example.test
```

Route the wildcard app origin through port 8110, then advertise private ingress
on the node agent with `DROP_AGENT_PRIVATE_INGRESS=true`. Existing public apps use the same hostname and their normal
login. Keep the NibRunner binary at a version supporting the JWT access policy.
Test an unauthorized request directly against the runner and through the
gateway before admitting private apps. Both must reject it. Do not configure
a separate raw TCP/UDP port or route directly to a guest.

## Browser handoff and HTTP clients

The signed-in reader obtains `private.access` for the immutable app ID, then
hands the app-specific credential to the gateway's
`/.well-known/drop/app-session` page using a popup message locked to the
exact expected origin and window. The token stays out of URLs and referrers.
The isolated handoff page sends the token to its own origin, installs a
host-only, Secure, HttpOnly cookie, clears the opener, then opens the app.
The gateway strips that cookie from upstream requests and supplies the
`X-Drop-App-Access` header for NibRunner to verify before contacting the
guest. Guest cookies and the guest's own Authorization header remain available
for its own authentication. The `/.well-known/drop/app-session` path and
`__Host-drop-runtime` cookie name are reserved by this gateway.

Authenticated HTTP clients can call `private.access` directly and send the
returned token in the named header to the returned HTTPS origin. Keep the
credential private and omit it from logs and command-line arguments. The
operation authorizes only the developer who owns the app and
returns the credential once. A repeat using the same idempotency key does
not recover it.

Drop rotates the verification key when the owner loses developer access or the control plane is restored. Nodes reject earlier credentials as soon as they apply the new key. A disconnected node can accept its last key until the token expires. An established WebSocket can remain open until it disconnects.
