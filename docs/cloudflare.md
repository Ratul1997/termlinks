# Default Cloudflare deployment

Cloudflare is Termlinks' included public relay adapter, not a required hosted service. Every user deploys it into their own Cloudflare account and receives their own URLs and secrets.

The default path has four pieces:

```text
phone browser → Cloudflare Pages Function → Worker/Durable Object ← outbound local connector
```

The browser talks only to the owner's Pages hostname. Its Pages Function forwards `/ws/bridge` to the configured relay Worker, while a Durable Object carries opaque ciphertext between that browser and one computer. No inbound router port, custom domain, or DNS record is required.

The browser and local connector independently derive an AES-256-GCM key from the random portal token. API requests, session metadata, terminal output, keystrokes, window frames, and control input remain application-encrypted across Pages, the Worker, and the Durable Object. Cloudflare can observe ordinary connection metadata such as IP addresses, timing, and packet sizes, but it cannot decrypt those payloads. The portal token never crosses the network and is not stored by the browser.

As with any hosted web E2E application, the JavaScript is delivered by the hosting provider. A compromised Cloudflare account or modified deployment could serve code that captures a token as it is entered. Protect the account with MFA and review deployments; application-layer encryption protects relay data, not a malicious frontend build.

## Requirements

- A Cloudflare account
- Node.js 20+, npm, Go 1.22+, and Wrangler
- Two globally/account-unique project names of your choice

Authenticate interactively with `npx wrangler login`, or set `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` in the shell without committing them.

Choose names for your deployment:

```sh
export TERMLINKS_RELAY_NAME=my-termlinks-relay
export TERMLINKS_PAGES_PROJECT=my-termlinks-portal
```

## First deployment

Build and test the repository:

```sh
npm ci
npm test
npm run build
```

Deploy the relay Worker and its SQLite-backed Durable Object:

```sh
npx wrangler deploy \
  --config apps/relay/wrangler.jsonc \
  --name "$TERMLINKS_RELAY_NAME"
```

Save the `https://<worker-name>.<account-subdomain>.workers.dev` URL printed by Wrangler:

```sh
export TERMLINKS_RELAY_URL=https://<worker-name>.<account-subdomain>.workers.dev
```

Generate one connector secret. Install the same value in the Worker and the local private Termlinks configuration; a temporary file keeps it out of shell history:

```sh
TERMLINKS_CONNECTOR_SECRET_FILE=$(mktemp)
chmod 600 "$TERMLINKS_CONNECTOR_SECRET_FILE"
openssl rand -base64 48 > "$TERMLINKS_CONNECTOR_SECRET_FILE"
npx wrangler secret put CONNECTOR_TOKEN \
  --config apps/relay/wrangler.jsonc \
  --name "$TERMLINKS_RELAY_NAME" \
  < "$TERMLINKS_CONNECTOR_SECRET_FILE"
termlinks cloud configure \
  --url "$TERMLINKS_RELAY_URL" \
  --token-stdin \
  < "$TERMLINKS_CONNECTOR_SECRET_FILE"
unlink "$TERMLINKS_CONNECTOR_SECRET_FILE"
```

Create the Pages project, configure the relay origin used by its Function, and deploy from `apps/web` so Wrangler includes the `functions/` directory:

```sh
npx wrangler pages project create "$TERMLINKS_PAGES_PROJECT" \
  --production-branch main
printf '%s' "$TERMLINKS_RELAY_URL" | \
  npx wrangler pages secret put RELAY_ORIGIN \
    --project-name "$TERMLINKS_PAGES_PROJECT"
npm run build:web
(cd apps/web && npx wrangler pages deploy dist \
  --project-name "$TERMLINKS_PAGES_PROJECT" \
  --branch main)
```

`RELAY_ORIGIN` is a deployment setting rather than source code so forks never need to contain somebody else's hostname. Cloudflare supports Pages environment variables through project settings; the secret command is used here because it is easy to automate and prevents accidental logging. Redeploy Pages after changing the setting.

Finally, start the local outbound connector and open the Pages URL printed by Wrangler:

```sh
termlinks cloud start
termlinks cloud status
termlinks token
```

The Pages URL is the portal URL. The token from `termlinks token` is the browser login. Do not use the connector secret as the browser token and do not share either one.

## Everyday commands

```sh
termlinks cloud start
termlinks cloud status
termlinks cloud stop
```

`cloud stop` disconnects public access but does not stop the Termlinks daemon or its managed terminal sessions.

One deployment currently represents one computer. Do not configure another computer with the same connector secret: there is no device identity or selector, so connectors would replace each other. Give each computer its own Worker/Pages deployment and private secrets until multi-device pairing and routing are implemented.

## Rotate the connector secret

Rotation temporarily disconnects the public portal but does not stop terminal sessions:

```sh
termlinks cloud stop
TERMLINKS_CONNECTOR_SECRET_FILE=$(mktemp)
chmod 600 "$TERMLINKS_CONNECTOR_SECRET_FILE"
openssl rand -base64 48 > "$TERMLINKS_CONNECTOR_SECRET_FILE"
npx wrangler secret put CONNECTOR_TOKEN \
  --config apps/relay/wrangler.jsonc \
  --name "$TERMLINKS_RELAY_NAME" \
  < "$TERMLINKS_CONNECTOR_SECRET_FILE"
termlinks cloud configure \
  --url "$TERMLINKS_RELAY_URL" \
  --token-stdin \
  < "$TERMLINKS_CONNECTOR_SECRET_FILE"
unlink "$TERMLINKS_CONNECTOR_SECRET_FILE"
termlinks cloud start
```

The local connector credential is stored in the private state directory shown by `termlinks doctor` and is forced to mode `0600`.

## Public end-to-end smoke tests

The opt-in JavaScript test imports the exact Web Crypto module used by the frontend, logs in, lists sessions, opens a terminal stream, and reads output. It sends no terminal input unless the optional input variables are provided:

```sh
export TERMLINKS_PORTAL_URL=https://<pages-project>.pages.dev
TERMLINKS_E2E_PORTAL="$TERMLINKS_PORTAL_URL" \
TERMLINKS_E2E_TOKEN="$(termlinks token 2>/dev/null)" \
npm run test:e2e:web
```

An independent Go protocol client provides a second interoperability check:

```sh
TERMLINKS_E2E_PORTAL="$TERMLINKS_PORTAL_URL" \
TERMLINKS_E2E_TOKEN="$(termlinks token 2>/dev/null)" \
go test -C apps/backend -ldflags=-linkmode=external \
  ./internal/cloud -run TestCloudPortalEndToEnd -v
```

To verify keyboard input, target only a deliberately safe test session with `TERMLINKS_E2E_SESSION_NAME` and `TERMLINKS_E2E_SEND`. Do not target a coding-agent or production shell session.

To create and clean up an isolated interactive shell automatically:

```sh
TERMLINKS_E2E_PORTAL="$TERMLINKS_PORTAL_URL" \
TERMLINKS_E2E_TOKEN="$(termlinks token 2>/dev/null)" \
TERMLINKS_E2E_CREATE_SHELL=1 \
npm run test:e2e:web
```

## Other providers

The local daemon is provider-independent. Any reverse tunnel or HTTPS proxy that preserves WebSocket upgrades can expose `http://127.0.0.1:8787` directly for terminal access. In that mode, use the provider's authentication/MFA layer as defense in depth and understand that TLS normally terminates at that provider.

To retain Termlinks' application-layer E2E bridge and remote-desktop/window protocol on a different host, implement the small channel relay described in [architecture.md](architecture.md): one authenticated outbound connector, browser channels, opaque bounded ciphertext forwarding, and channel-close notifications. The existing Cloudflare Worker is the reference adapter.
