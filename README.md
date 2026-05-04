# txmill

> **A mill for transactions.** Pour in calls, get back hashes. Built for backends that need to push a lot of EVM writes and would rather not babysit nonces.

EVM nonces are sequential per address. That's a hard cap on per-account throughput, and it ruins your day the first time you try to fan out workloads. txmill takes the other side of the bargain: you give it one address to fund, it gives you back **N** signers to pull from, and it relays your contract calls through whichever one is idle. You stop thinking about gas, nonces, mempool race conditions, and "wait, did this one land?"

It is the gas worker, the receipt watcher, the webhook emitter, the tiny pool of disposable signing keys, and the encrypted thing on disk that holds them — all in one Go binary, ~42 MB.

```
                                    ┌──────────────┐
   you ──── POST /v1/relay ─────►   │              │ ───► Sonic mainnet
                                    │   txmill     │
   you ◄─── GET  /v1/relay/:id ───  │              │ ◄─── receipts (~1s)
   you ◄─── webhook (HMAC) ───────  │              │
                                    └──────┬───────┘
                                           │
                              ┌────────────┴────────────┐
                              │  treasury  → signer #0  │
                              │            → signer #1  │   each goroutine
                              │            → signer #2  │   parallel + serialized
                              │            → ...        │   per signer
                              │            → signer #N  │
                              └─────────────────────────┘
```

## What it does

- **Pool-based parallelism.** One app gets N pre-generated signer addresses (1 to 1024). N concurrent in-flight transactions, no nonce contention.
- **Auto gas distribution.** Fund one treasury, the gas worker tops signers up when they dip below threshold. Set the policy at app creation; forget it after.
- **Encrypted keystore.** Argon2id + AES-256-GCM, password from env. Private keys never touch the wire and never leave the box in plaintext.
- **Two ways to learn the answer.** Poll `GET /v1/relay/:request_id` like a peasant, or set a callback URL and we'll HMAC-sign POSTs to your endpoint like Stripe taught us.
- **Restart-safe.** In-flight transactions resume across reboots. Nothing orphaned, nothing double-spent, nothing mysterious.
- **Operator-friendly.** Webhook + Telegram alerts when the treasury runs dry. Throttled so you don't get paged 720 times an hour.

## What it isn't

- Not a wallet. End users don't sign with txmill — your backend does, on behalf of itself.
- Not multi-tenant in the SaaS sense. One txmill instance = one chain. Run more for more chains.
- Not a meta-transaction relayer (yet). The signer is `from`, not the original user.
- Not a place to store life savings. The keystore is OK; the threat model assumes the host is trusted.

## Quick start (caller)

```bash
# 1. Get an app
CREATE=$(curl -sS -X POST https://txmill-production.up.railway.app/v1/apps \
  -H 'Content-Type: application/json' \
  -d '{"name":"acme","pool_size":10,
       "signer_min_balance":   "100000000000000000",
       "signer_refill_amount":"1000000000000000000",
       "treasury_min_balance":"5000000000000000000"}')

# 2. Save these. We will only show them once.
TOKEN=$(echo $CREATE | jq -r .bearer_token)            # tk_…
TREASURY=$(echo $CREATE | jq -r .treasury_address)     # 0x…

# 3. Fund the treasury once.  Your contract whitelists every signer in $CREATE.signer_addresses.

# 4. Send transactions.
curl -sS -X POST https://txmill-production.up.railway.app/v1/relay \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"chain_id":146,"to":"0xYourContract","data":"0xa9059cbb...","callback_metadata":"order=42"}'
# → 202 { request_id, tx_hash, signer }

# 5. Watch it land (or set up a webhook and don't watch anything).
curl -sS -H "Authorization: Bearer $TOKEN" \
  https://txmill-production.up.railway.app/v1/relay/<request_id>
```

For everything an SDK or backend integration needs, see **[`docs/integration.md`](docs/integration.md)** — auth, every endpoint, the status state machine, webhook verification, the nine footguns to handle, and a TS SDK shape.

## Architecture, abridged

| Subsystem | Purpose | Tick |
|---|---|---|
| Echo HTTP server | Public API on `:8080` | — |
| Relay service | Validate → checkout signer → estimate → sign → broadcast → persist | per-request |
| Receipt watcher | Polls submitted txs, captures status + logs + revert reason | 1s |
| Webhook worker | Drains delivery queue, retries with backoff (5s → 30s → 5m → 30m) | 1s |
| Gas worker | Treasury → signer top-ups when below threshold | 30s (configurable) |
| Signer pool | Per-app in-memory checkout/release with atomic per-signer nonce | — |
| Keystore | Argon2id + AES-256-GCM file-per-key on `TXMILL_KEYSTORE_DIR` | — |
| Alert transports | Webhook + Telegram, deduped within a throttle window | per-event |

State lives in Postgres (`apps`, `signers`, `relay_requests`, `tx_attempts`, `gas_attempts`, `webhook_deliveries`). Migrations via embedded goose; runs as a separate `migrate` binary at deploy time.

## Local dev

```bash
make dev-up                # docker compose: Postgres on :5432
export TXMILL_DB_URL='postgres://postgres:postgres@localhost:5432/txmill?sslmode=disable'
make migrate-up            # apply schema
make run                   # start txmill on :8080
```

A `.env.example` lists every knob. For end-to-end smoke tests against live Sonic, see `scripts/live/` (gitignored, copy in your own `.env.local` with an RPC URL).

## Production

The current production deployment runs on Railway, fronted by `https://txmill-production.up.railway.app`, with a managed Postgres and a 5 GB volume mounted at `/data/keys` for the encrypted keystore. Deploy config is in `railway.json` (Dockerfile build, `migrate up` as pre-deploy, `/health` checks). Image is multi-stage Go → distroless static, ~42 MB.

## Where it stands

Built and live: app onboarding, bearer auth, signer pool, relay submit, receipt tracking, status polling, signed webhooks, treasury → signer gas distribution, operator alerts, balances API, restart recovery.

Not built: token rotation, idempotency keys on app create, admin auth on app create, bulk status, multi-chain in one process, gas-attempt receipt tracking, automated rebroadcast for stuck txs.

If something on that "not built" list bites you in production, an issue is welcome.

## License

(TBD)
