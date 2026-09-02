# Cloudflare deployment

The personal cloud path uses two default Cloudflare domains:

- Portal: `https://termlinks.pages.dev`
- Relay: `https://termlinks-relay.ratulbhowmick66.workers.dev`

The browser talks only to Pages. A Pages Function forwards the single `/ws/bridge` connection to the relay Worker, and a Durable Object carries opaque ciphertext to the computer's outbound connector. No inbound port, custom domain, or DNS record is required.

The browser and local connector independently derive an AES-256-GCM key from the random portal token. API requests, session metadata, terminal output, keystrokes, and local authentication remain application-encrypted across Pages, the Worker, and the Durable Object. Cloudflare can observe connection metadata such as IPs, timing, and packet sizes, but it cannot decrypt those payloads. The portal token never crosses the network and is not stored by the browser.

As with any hosted web E2E application, the JavaScript is delivered by the hosting provider. A compromised Cloudflare account or modified deployment could serve code that captures a token as it is entered. Protect the account with MFA and review deployments; application-layer encryption protects relay data, not a malicious frontend build.

## Everyday commands

```sh
termlinks cloud start
termlinks cloud status
termlinks cloud stop
```

`cloud stop` disconnects public access but does not stop the Termlinks daemon or its managed terminal sessions.

The current deployment is a single-computer personal relay. Do not configure a second person's computer with the same connector secret: there is no device identity or selector, so connectors would conflict. A friend needs their own Worker/Pages deployment and private secrets until account login, short-lived pairing codes, per-device keys, and device selection are implemented.

Display the separate browser login token:

```sh
termlinks token
```

## First deployment

Set `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` in the shell without committing them. Then:

```sh
npm test
npm run build
npm run deploy:relay
npx wrangler pages project create termlinks --production-branch main
npm run deploy:pages
```

Generate one connector secret and install the same value in Cloudflare and the local private configuration. A temporary file prevents the secret from appearing in shell history:

```sh
CONNECTOR_SECRET_FILE=$(mktemp)
chmod 600 "$CONNECTOR_SECRET_FILE"
openssl rand -base64 48 > "$CONNECTOR_SECRET_FILE"
npx wrangler secret put CONNECTOR_TOKEN --config apps/relay/wrangler.jsonc < "$CONNECTOR_SECRET_FILE"
termlinks cloud configure --url https://termlinks-relay.ratulbhowmick66.workers.dev --token-stdin < "$CONNECTOR_SECRET_FILE"
unlink "$CONNECTOR_SECRET_FILE"
termlinks cloud start
```

The local configuration file is stored in the private state directory shown by `termlinks doctor` and is forced to mode `0600`.

## Rotate the connector secret

Rotation temporarily disconnects the public portal but does not stop terminal sessions:

```sh
termlinks cloud stop
CONNECTOR_SECRET_FILE=$(mktemp)
chmod 600 "$CONNECTOR_SECRET_FILE"
openssl rand -base64 48 > "$CONNECTOR_SECRET_FILE"
npx wrangler secret put CONNECTOR_TOKEN --config apps/relay/wrangler.jsonc < "$CONNECTOR_SECRET_FILE"
termlinks cloud configure --url https://termlinks-relay.ratulbhowmick66.workers.dev --token-stdin < "$CONNECTOR_SECRET_FILE"
unlink "$CONNECTOR_SECRET_FILE"
termlinks cloud start
```

## Public end-to-end smoke test

The opt-in JavaScript test imports the exact Web Crypto module used by the frontend, logs in, lists sessions, opens a terminal stream, and reads output. It sends no terminal input unless both optional variables are provided.

```sh
TERMLINKS_E2E_PORTAL=https://termlinks.pages.dev \
TERMLINKS_E2E_TOKEN="$(termlinks token 2>/dev/null)" \
npm run test:e2e:web
```

An independent Go protocol client provides a second interoperability check:

```sh
TERMLINKS_E2E_PORTAL=https://termlinks.pages.dev \
TERMLINKS_E2E_TOKEN="$(termlinks token 2>/dev/null)" \
go test -C apps/backend -ldflags=-linkmode=external ./internal/cloud -run TestCloudPortalEndToEnd -v
```

To verify keyboard input against a deliberately safe echo session, set `TERMLINKS_E2E_SESSION_NAME` and `TERMLINKS_E2E_SEND` as well. Do not target an interactive coding-agent session for this test.

To create an isolated interactive shell, run `cd /tmp`, verify its resulting working directory, and stop it without touching an existing session:

```sh
TERMLINKS_E2E_PORTAL=https://termlinks.pages.dev \
TERMLINKS_E2E_TOKEN="$(termlinks token 2>/dev/null)" \
TERMLINKS_E2E_CREATE_SHELL=1 \
npm run test:e2e:web
```
