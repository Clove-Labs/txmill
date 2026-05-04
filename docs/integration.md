# txmill Integration Guide

Self-contained reference for integrating with txmill. This is the document an SDK or backend integration should be built from.

## What txmill is

A high-throughput EVM transaction relayer. Backend apps submit signed contract calls; txmill broadcasts them on-chain through a pool of pre-generated signer addresses to parallelize around per-account nonce sequencing.

The current production deployment is on **Sonic mainnet (chain id 146)**.

## Mental model

```
1. Operator creates an "app" once       → POST /v1/apps
                                          ↳ returns:
                                            • app_id (UUID)
                                            • bearer_token (one-time secret)
                                            • default_callback_secret (one-time, if webhook URL set)
                                            • treasury_address
                                            • signer_addresses[] (1..1024)

2. Caller's contract whitelists every signer_address (so signer can call the contract).

3. Caller funds treasury_address once. txmill auto-distributes gas to each signer
   using the gas worker (configurable thresholds at app creation).

4. Caller submits relay calls               → POST /v1/relay
                                              ↳ returns:
                                                • request_id (UUID)
                                                • tx_hash
                                                • signer (which pool entry was used)

5. Caller learns final status either way:
     • POLL    GET /v1/relay/:request_id   (always available)
     • WEBHOOK txmill POSTs status changes to a configured callback URL,
               signed with HMAC-SHA256(default_callback_secret, body)
```

`treasury` and `signer` addresses are **internal to txmill** — the caller never holds the private keys. Caller funds the treasury and that's it.

## Authentication

Every request to `/v1/apps/:id/*` and `/v1/relay/*` requires:

```http
Authorization: Bearer tk_<base64url>
```

The token is returned **exactly once**, at app creation. txmill stores only `sha256(token)`. There is no token recovery or rotation endpoint today; lose the token and you must create a new app (and re-onboard signers).

## Base URL

Production deployment: `https://txmill-production.up.railway.app`

All paths in this document are relative to that base.

## Common request/response conventions

- Content type: `application/json` (request and response).
- All `uint256` values (`value`, `balance_wei`, `gas_price`, `effective_gas_price`, all gas thresholds) are sent and returned as **decimal strings** — JSON numbers can't represent uint256.
- `block_number` and `gas_used` fit `int64` and are returned as JSON numbers.
- `gas_limit` is `uint64`, sent as a JSON number.
- Addresses are returned as **EIP-55 checksummed hex** (`"0xAbCd…"`), with `0x` prefix. Inputs accept any case.
- Hex byte fields (`data`) accept either `0x`-prefixed or bare hex (empty `""` and `"0x"` both mean empty calldata).
- Timestamps are ISO 8601 UTC strings.
- `request_id`, `app_id` are RFC 4122 UUIDs (string).

## Error model

Every error response has the shape:

```json
{ "message": "<human-readable reason>" }
```

Status code semantics:

| Code | Meaning |
|---|---|
| `400` | Validation failure: bad JSON, bad field, `chain_id` mismatch, past `deadline`, etc. |
| `401` | Missing or unparseable `Authorization` header / unknown token. |
| `403` | App is disabled. |
| `404` | Resource not found, OR resource belongs to a different app (we hide existence). |
| `502` | Downstream RPC failure (gas estimation, broadcast, etc). |
| `503` | Upstream dependency not configured (rare; configuration bug). |

`502` errors during submit do NOT necessarily mean the call won't reach the chain — they mean the chain RPC rejected/failed our broadcast attempt. The relay request is still persisted; if you want to retry, submit a new `POST /v1/relay` (today there is no idempotency key — see "Footguns").

---

# Endpoints

## App creation

```http
POST /v1/apps
Content-Type: application/json
```

**Authentication: NONE** today (this is the bootstrap endpoint). An admin gate is on the roadmap; integrators should treat this as operator-side, not user-side.

### Request body

```json
{
  "name": "string, required, trimmed",
  "pool_size": 1,
  "default_callback_url": "https://your.app/txmill-cb",
  "signer_min_balance":   "100000000000000000",
  "signer_refill_amount": "1000000000000000000",
  "treasury_min_balance": "5000000000000000000"
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | Human label. NOT unique — duplicate creates ARE allowed (footgun). |
| `pool_size` | int | yes | Number of signer addresses to generate. Range: `1..1024`. |
| `default_callback_url` | string | no | If set, txmill will POST status changes here; the response will include `default_callback_secret`. |
| `signer_min_balance` | uint256-string | no | Refill a signer if its balance drops below this. Wei. |
| `signer_refill_amount` | uint256-string | no | How much to send treasury → signer per refill. Wei. **Must be > `signer_min_balance`** else 400. |
| `treasury_min_balance` | uint256-string | no | When treasury balance drops below this, txmill fires an operator alert (webhook/Telegram if configured). Wei. |

The three gas threshold fields must be **set together or all omitted**. Setting one without the other two → 400.

### Response (201)

```json
{
  "app_id": "fc384b51-…",
  "bearer_token": "tk_<base64url>",
  "default_callback_secret": "whsec_<base64url>",
  "treasury_address": "0x…",
  "signer_addresses": ["0x…", "0x…", "..."]
}
```

`bearer_token` and `default_callback_secret` are **shown exactly once**. Persist immediately. txmill stores only `sha256(bearer_token)` and the plaintext `default_callback_secret` (for HMAC; never returned again).

### Errors

- `400` — `name` is empty, `pool_size` out of range, gas-policy validation failed, bad JSON.

### Example

```bash
curl -X POST https://txmill-production.up.railway.app/v1/apps \
  -H 'Content-Type: application/json' \
  -d '{
    "name":"acme",
    "pool_size":128,
    "default_callback_url":"https://acme.com/txmill-cb",
    "signer_min_balance":   "100000000000000000",
    "signer_refill_amount":"1000000000000000000",
    "treasury_min_balance":"5000000000000000000"
  }'
```

---

## List signers

```http
GET /v1/apps/:id/signers
Authorization: Bearer <token>
```

Returns the current pool of signer addresses for an app. Useful if the original create response was lost.

`:id` MUST equal the bearer token's `app_id`, else `404`.

### Response (200)

```json
{
  "app_id": "fc384b51-…",
  "signer_addresses": ["0x…", "0x…", "..."]
}
```

Order is stable: insertion order (`created_at`, then `address`).

---

## Get balances

```http
GET /v1/apps/:id/balances
Authorization: Bearer <token>
```

Live (uncached) on-chain balance of the treasury + every signer for an app. Fans out one `eth_getBalance` call per address in parallel.

`:id` MUST equal the bearer token's `app_id`, else `404`.

### Response (200)

```json
{
  "treasury": {
    "address":     "0x…",
    "balance_wei": "1988450000000000000"
  },
  "signers": [
    {
      "address":      "0x…",
      "balance_wei":  "1000000000000000000",
      "last_used_at": "2026-05-04T21:41:33.116945Z"
    },
    "..."
  ]
}
```

`last_used_at` is omitted when the signer has never been picked.

### Errors

- `502` — RPC failure on any address aborts the whole response. The call is naturally retryable.

---

## Submit a relay call

```http
POST /v1/relay
Authorization: Bearer <token>
Content-Type: application/json
```

txmill picks an idle signer from the app's pool, signs and broadcasts the tx, then returns immediately. Final status arrives via polling and/or webhook.

### Request body

```json
{
  "chain_id": 146,
  "to": "0xContractAddress",
  "data": "0xabcd…",
  "value": "0",
  "gas_limit": 250000,
  "deadline": 1779000000,
  "callback_url": "https://your.app/per-request-cb",
  "callback_metadata": "any string up to 1024 bytes"
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `chain_id` | int | yes | Must equal the server's configured chain (currently `146`). |
| `to` | hex address | yes | Target contract (or any EOA). |
| `data` | hex bytes | yes | ABI-encoded calldata. `""` or `"0x"` means empty (e.g., a value-only transfer). |
| `value` | uint256-string | no | Default `"0"`. Must be non-negative. |
| `gas_limit` | uint64 | no | If absent, txmill calls `eth_estimateGas` and adds a 20% buffer. |
| `deadline` | unix seconds | no | If past, request rejected with `400`. |
| `callback_url` | string | no | Per-request override of the app's `default_callback_url`. Per-request webhook still uses the **app's** `default_callback_secret` for signing. |
| `callback_metadata` | string | no | Echoed back in the status payload (poll + webhook). Use as a correlation id. Max 1024 bytes. |

### Response (202)

```json
{
  "request_id": "08a6525b-…",
  "tx_hash":    "0xaa0cbb…",
  "signer":     "0x…"
}
```

`tx_hash` is the hash of the signed tx. It MAY change in the future if txmill ever resubmits with bumped gas — today it does not, so the hash is stable.

### Errors

| Code | Reason |
|---|---|
| `400` | Bad JSON, bad address, bad `data` hex, negative `value`, `chain_id` mismatch, past `deadline`, `callback_metadata` too long. |
| `401` | No / invalid token. |
| `502` | Pre-submit estimation failure (e.g., insufficient balance on signer for gas, contract revert during simulation), or the broadcast itself failed. The relay request row is persisted with `status=rejected` (estimation failure) or `status=pending` (broadcast failure). |

When you get a `502`, the caller gets no `request_id` back — but the request DID land in txmill's DB. Reconcile via the operator if needed; or just retry, and accept the orphan.

---

## Get relay status

```http
GET /v1/relay/:request_id
Authorization: Bearer <token>
```

Returns the full lifecycle of a single relay request, including all receipt fields once mined. `:request_id` MUST belong to the bearer's app, else `404`.

### Response (200)

```json
{
  "request_id":          "08a6525b-…",
  "status":              "confirmed",
  "tx_hash":             "0xaa0cbb…",
  "signer":              "0x…",
  "block_number":        69572365,
  "gas_used":            21420,
  "effective_gas_price": "55000000000",
  "revert_reason":       "ERC20: insufficient allowance",
  "logs":                [
    {
      "address":     "0x…",
      "topics":      ["0x…", "..."],
      "data":        "0x…",
      "blockNumber": "0x…",
      "transactionHash": "0x…",
      "transactionIndex":"0x…",
      "blockHash":   "0x…",
      "logIndex":    "0x…",
      "removed":     false
    }
  ],
  "callback_metadata":   "order=42",
  "updated_at":          "2026-05-04T21:41:33.116945Z"
}
```

Fields beyond `request_id`, `status`, `updated_at` are present **only when meaningful**:

- Pre-submit (`pending` / `rejected` with no attempt) → only the three baseline fields.
- Submitted but not yet observed → adds `tx_hash`, `signer`.
- Confirmed/reverted (after the receipt watcher catches up) → adds `block_number`, `gas_used`, `effective_gas_price`, `logs`. `revert_reason` is only present when `status=reverted` AND extractable.

The JSON shape here is **identical** to the webhook payload (see below).

---

# The status state machine

```
┌─────────┐    validate fail / estimate fail
│ pending │ ────────────────────────────────► rejected   (terminal, pre-submit)
└────┬────┘
     │ broadcast accepted
     ▼
┌──────────┐    rare: insert-attempt failure
│submitting│ ─────────────────────────────────► failed   (terminal, broadcast failure)
└────┬─────┘
     │ broadcast acknowledged by RPC
     ▼
┌──────────┐
│submitted │  ← visible to caller via tx_hash
└────┬─────┘
     │ receipt observed by watcher (~1s tick)
     ├──────► confirmed   (receipt.status == 1)
     └──────► reverted    (receipt.status == 0; revert_reason best-effort)
```

Terminal states from caller's perspective: `confirmed`, `reverted`, `rejected`, `failed`.

A request can sit at `submitted` indefinitely if the chain never mines the tx (e.g., underpriced + congested mempool). Today there is no automated rebroadcast/cancel — operator concern.

---

# Webhooks (optional)

Webhooks are **opt-in**. Apps that don't set `default_callback_url` at creation never receive webhooks; they should poll `GET /v1/relay/:request_id` instead.

When opted in, txmill `POST`s to the configured URL on **every status change** of every relay request belonging to the app. The body is identical to the `GET /v1/relay/:request_id` payload.

A successful relay request typically produces **two** deliveries:
1. `status=submitted` (immediately after broadcast)
2. `status=confirmed` or `status=reverted` (after receipt observed)

A pre-submit failure produces **one** delivery (`status=rejected` or `status=failed`).

### Per-request callback override

A `POST /v1/relay` body may set `callback_url`. If present, that URL takes precedence over the app's `default_callback_url` for THIS request only. The HMAC is still computed using the **app's** `default_callback_secret` (set once at app creation).

### Headers on every delivery

```
Content-Type:        application/json
X-Txmill-Signature:  sha256=<hex>
X-Txmill-Delivery:   <delivery-uuid>
X-Txmill-Attempt:    <int>            # 0-indexed; first attempt is "0"
```

`X-Txmill-Signature` is `"sha256=" + hex(HMAC_SHA256(default_callback_secret, request_body))`.

### Verification (Node.js / TypeScript)

```ts
import { createHmac, timingSafeEqual } from "node:crypto";

function verify(req: { headers: Record<string,string>; rawBody: Buffer }, secret: string): boolean {
  const header = req.headers["x-txmill-signature"];
  if (!header?.startsWith("sha256=")) return false;
  const provided = Buffer.from(header.slice("sha256=".length), "hex");
  const expected = createHmac("sha256", secret).update(req.rawBody).digest();
  return provided.length === expected.length && timingSafeEqual(provided, expected);
}
```

**Important**: HMAC must be computed over the **raw request body bytes**, NOT a re-serialization. Reformatting (e.g., re-`JSON.stringify`-ing after parse) WILL break verification. Capture the raw body before any JSON parsing middleware.

### Delivery semantics & retries

| Property | Value |
|---|---|
| Success criterion | Any `2xx` HTTP response. |
| Retry triggers | Any non-2xx, network error, or timeout (10s). |
| Schedule | `5s, 30s, 5m, 30m`. Up to 5 attempts total. After the 5th failure → `dead`, no further retries. |
| Idempotency | Receivers should dedupe on `(request_id, status)` — the same status can land more than once after a transient failure + retry. |
| Ordering | NOT guaranteed across deliveries. The body's `updated_at` is the source of truth for freshness. Receivers should treat older `updated_at` as obsolete. |

A handler MUST respond quickly (< 10s). Slow handlers cause timeouts → marks the delivery as a failure → kicks off the retry schedule.

---

# Address whitelisting on caller's contract

For txmill to call your contract, every signer must be authorized on the contract's side. Common pattern (Solidity):

```solidity
mapping(address => bool) public relayers;

modifier onlyRelayer() {
    require(relayers[msg.sender], "not relayer");
    _;
}

function setRelayers(address[] calldata addrs, bool ok) external onlyOwner {
    for (uint i; i < addrs.length; i++) relayers[addrs[i]] = ok;
}
```

After `POST /v1/apps`, call `setRelayers(signer_addresses, true)` once, batched. Pool sizes up to ~256 fit in a single tx; for 1024-pool apps, batch over multiple txs.

Adding more signers later is **not supported** — pool size is fixed at app creation. To grow capacity, create a new app, whitelist its signers, and migrate.

---

# Footguns / things SDK should handle

These are the rough edges as of today. An SDK that wraps these gracefully will save integrators a lot of pain.

## 1. `bearer_token` and `default_callback_secret` are shown exactly once

There is no recovery / rotation endpoint. If the create response is lost or partially captured, the app is dead — every signer's funds are unrecoverable through the API (the encrypted private keys are still on disk on the server, but with no API path to use them).

**SDK should**: write both values to durable storage IMMEDIATELY upon receipt; loudly fail if storage write fails before returning.

## 2. `POST /v1/apps` is not idempotent

Two POSTs with the same `name` create two **distinct** apps with different `app_id`s, different bearer tokens, different treasuries, different signer pools. The first response is silently lost if not stored.

**SDK should**: surface this clearly to the caller. Consider client-side deduping via a stored `(name, fingerprint)` lookup before issuing a fresh POST.

## 3. `502` from `POST /v1/relay` may have already persisted the request

When the relay handler returns a 502 (gas estimate failed, broadcast errored), the `relay_requests` row was already inserted but the response carries no `request_id`. The orphan can only be reconciled via DB introspection by the operator.

**SDK should**: surface the 502 error message verbatim. Do NOT auto-retry without thinking — depending on the failure mode, a retry may double-spend a nonce or just orphan more rows.

## 4. Webhook secret is per-app, not per-request

A per-request `callback_url` is signed with the app-level `default_callback_secret`. Apps that didn't set `default_callback_url` at creation have no secret → per-request `callback_url` is silently ignored.

**SDK should**: warn/error when caller passes a `callback_url` to `POST /v1/relay` for an app that lacks a secret.

## 5. uint256 are decimal STRINGS, not numbers

`value`, `balance_wei`, `effective_gas_price`, all gas thresholds. JSON numbers can't safely hold values > 2^53. Sending them as numbers risks silent truncation client-side.

**SDK should**: type uint256 fields as `bigint`/`string`, never `number`. Convert at the JSON serialization boundary.

## 6. Status responses use `omitempty`

Fields like `tx_hash`, `block_number`, `gas_used`, `revert_reason` are missing (not null) when not yet known. Receivers must handle absent fields gracefully.

**SDK should**: model the response as a discriminated union over `status`, with present fields per state. Or, more pragmatically, treat all post-baseline fields as `Optional<T>`.

## 7. Pool size is fixed at app creation

No "add more signers" endpoint. Plan capacity at create time.

## 8. Webhook ordering is not guaranteed

Same `request_id`'s `submitted` and `confirmed` deliveries can land out of order under retries. Receivers MUST use `updated_at` as the freshness check.

## 9. `last_used_at` is currently always omitted

The relay submit path doesn't yet update it (TODO server-side). Don't depend on it for "is this signer in use" checks.

---

# Suggested SDK shape (TypeScript)

A minimal, opinionated sketch that handles the rough edges above:

```ts
import { createHmac, timingSafeEqual } from "node:crypto";

export type Address = `0x${string}`;
export type Hex     = `0x${string}` | "";
export type RequestId = string;
export type RelayStatus = "pending" | "submitted" | "confirmed" | "reverted" | "rejected" | "failed";

export interface CreateAppInput {
  name: string;
  poolSize: number;
  defaultCallbackUrl?: string;
  signerMinBalance?:   bigint;
  signerRefillAmount?: bigint;
  treasuryMinBalance?: bigint;
}
export interface CreateAppResult {
  appId: string;
  bearerToken: string;
  defaultCallbackSecret?: string;   // present iff defaultCallbackUrl was set
  treasuryAddress: Address;
  signerAddresses: Address[];
}

export interface SubmitInput {
  chainId: number;
  to: Address;
  data: Hex;
  value?: bigint;
  gasLimit?: number;
  deadline?: number;                // unix seconds
  callbackUrl?: string;
  callbackMetadata?: string;
}
export interface SubmitResult {
  requestId: RequestId;
  txHash: `0x${string}`;
  signer: Address;
}

export interface RelayStatusResponse {
  requestId: RequestId;
  status: RelayStatus;
  txHash?: `0x${string}`;
  signer?: Address;
  blockNumber?: number;
  gasUsed?: number;
  effectiveGasPrice?: bigint;
  revertReason?: string;
  logs?: unknown[];
  callbackMetadata?: string;
  updatedAt: string;
}

export class Txmill {
  constructor(private readonly opts: { baseUrl: string; bearerToken?: string }) {}

  // --- Operator-side ---------------------------------------------------------
  async createApp(input: CreateAppInput): Promise<CreateAppResult> { /* ... */ }

  // --- Authenticated --------------------------------------------------------
  async listSigners(appId: string): Promise<{ appId: string; signerAddresses: Address[] }> { /* ... */ }
  async getBalances(appId: string): Promise<{
    treasury: { address: Address; balanceWei: bigint };
    signers: Array<{ address: Address; balanceWei: bigint; lastUsedAt?: string }>;
  }> { /* ... */ }

  async submit(input: SubmitInput): Promise<SubmitResult> { /* ... */ }
  async getRelayStatus(requestId: RequestId): Promise<RelayStatusResponse> { /* ... */ }

  // Polls until terminal (confirmed/reverted/rejected/failed) or timeout.
  async waitForTerminalStatus(
    requestId: RequestId,
    opts?: { intervalMs?: number; timeoutMs?: number },
  ): Promise<RelayStatusResponse> { /* ... */ }

  // --- Static helpers -------------------------------------------------------
  static verifyWebhook(rawBody: Buffer, signatureHeader: string, secret: string): boolean {
    if (!signatureHeader.startsWith("sha256=")) return false;
    const provided = Buffer.from(signatureHeader.slice("sha256=".length), "hex");
    const expected = createHmac("sha256", secret).update(rawBody).digest();
    return provided.length === expected.length && timingSafeEqual(provided, expected);
  }
}
```

Conversions to handle:

- Send `value`, `signerMinBalance`, etc. as `String(bigint)` in the JSON body.
- Parse `balance_wei`, `effective_gas_price` from response strings into `bigint`.
- Parse `deadline` as a number (unix seconds).
- HEX values pass through as-is.

Recommended polling cadence: **1–2 s on Sonic** (sub-second finality). Default timeout for `waitForTerminalStatus`: 60s.

---

# End-to-end example (curl)

```bash
BASE=https://txmill-production.up.railway.app

# 1. Operator creates the app (one time)
CREATE=$(curl -sS -X POST $BASE/v1/apps -H 'Content-Type: application/json' -d '{
  "name":"acme",
  "pool_size":10,
  "default_callback_url":"https://acme.com/txmill-cb",
  "signer_min_balance":   "100000000000000000",
  "signer_refill_amount":"1000000000000000000",
  "treasury_min_balance":"5000000000000000000"
}')
TOKEN=$(echo "$CREATE" | jq -r .bearer_token)
SECRET=$(echo "$CREATE" | jq -r .default_callback_secret)
APP_ID=$(echo "$CREATE" | jq -r .app_id)
TREASURY=$(echo "$CREATE" | jq -r .treasury_address)

# 2. Operator funds the treasury (manual, one-time)
#    Send native S to $TREASURY on Sonic mainnet.

# 3. Caller (backend service) submits a relay call
SUBMIT=$(curl -sS -X POST $BASE/v1/relay \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{
    \"chain_id\":146,
    \"to\":\"0xYourContract\",
    \"data\":\"0xa9059cbb0000…\",
    \"callback_metadata\":\"order=42\"
  }")
REQ_ID=$(echo "$SUBMIT" | jq -r .request_id)

# 4. Caller polls (or receives webhook) for terminal status
while true; do
  STATUS=$(curl -sS -H "Authorization: Bearer $TOKEN" $BASE/v1/relay/$REQ_ID)
  S=$(echo "$STATUS" | jq -r .status)
  case "$S" in
    confirmed|reverted|rejected|failed) echo "$STATUS" | jq .; break;;
    *) sleep 1;;
  esac
done
```

---

# Operational reference

These aren't part of the API but help an SDK author understand timing.

| Subsystem | Tick | Notes |
|---|---|---|
| Receipt watcher | 1s (default) | Flips `submitted` → `confirmed`/`reverted`. Adjustable via `TXMILL_WATCHER_INTERVAL_MS`. |
| Webhook worker | 1s | Same cadence as receipt watcher. Drains the delivery queue. |
| Gas worker | 30s (prod default) | Replenishes signers from treasury. Adjustable via `TXMILL_GAS_INTERVAL_MS`. |
| Alert throttle | 30 min | Same alert key (e.g., `treasury_low:<app_id>`) won't repeat within this window. |
| Sonic finality | sub-second | A submitted tx is typically `confirmed` within ~2s end-to-end. |
