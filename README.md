# domieface-server

Go implementation of the social API in [HANDOFF.md](HANDOFF.md). Postgres for data,
S3-compatible object storage for images, JWT access tokens with rotating refresh tokens.

Nothing here uses a web framework — routing is `net/http`'s own method-and-pattern mux.

## Running it

`make` on its own lists every target. The common ones:

| | |
|---|---|
| `make dev` | Start Postgres and MinIO, then run the server |
| `make test` | Run the tests (no infrastructure needed) |
| `make check` | Everything CI runs: fmt, vet, build, test |
| `make up` | Start the whole stack in Docker, API included |
| `make docs` | Check the spec matches the routes, and print the docs URLs |
| `make db-reset` | Empty every table, keeping the schema |

Machine-specific settings go in `.env` (gitignored). See
[if something already owns port 5432](#if-something-already-owns-port-5432).

Targets work from PowerShell, cmd and Git Bash alike. On Windows they need Git for
Windows installed, which is what supplies the `sh` that GNU Make hands recipes to.

**Dev loop (recommended).** Dependencies in Docker, the server on your machine:

```bash
make dev
```

which is:

```bash
docker compose up -d postgres minio minio-init
go run .
```

The API is on `http://localhost:8080`, MinIO's console on `http://localhost:9001`
(`minioadmin` / `minioadmin`). Migrations run automatically at startup.

### If something already owns port 5432

A native Postgres service is the usual culprit, and the symptom is misleading:

```
password authentication failed for user "domieface"
```

That is not a wrong password. The connection reached *that* server instead of the
container, and it has no `domieface` role. Publish the container somewhere else:

```bash
make dev POSTGRES_PORT=55433
```

To avoid retyping it, put it in `.env` — gitignored, read by both the Makefile and
docker compose, so one line configures both:

```
POSTGRES_PORT=55433
```

`DATABASE_URL` is derived from `POSTGRES_PORT`, so the port is the only thing to
change. Copy [.env.example](.env.example) for everything else that can go there;
anything set in `.env` is passed through to the server by `make run`.

**Everything in Docker:**

```bash
make up
```

One caveat: a presigned URL is signed *for a specific host*, and the signature covers
that host, so it cannot be rewritten afterwards. In this mode URLs are signed for
`minio:9000`, which your machine cannot resolve. Add this to your hosts file:

```
127.0.0.1  minio
```

(`/etc/hosts`, or `C:\Windows\System32\drivers\etc\hosts`.) The dev loop above avoids
this entirely, which is why it is the recommended one.

**Tests** need neither Postgres nor MinIO — they run against an in-memory store and a
stub presigner:

```bash
make test
```

## API documentation

With the server running:

| | |
|---|---|
| `http://localhost:8080/docs` | Swagger UI, with Try It Out wired to the running server |
| `http://localhost:8080/openapi.yaml` | The raw OpenAPI 3.1 spec |

The spec is hand-written at [internal/api/openapi.yaml](internal/api/openapi.yaml) and
embedded in the binary, so a built image serves its own reference with no files
alongside it. It is not generated from annotations: the contract came first, and
annotations scattered through handlers drift from it quietly.

What stops *this* file drifting is a test. `Server.Routes()` is the one routing table
the mux is built from, and `TestOpenAPIMatchesRoutingTable` fails the build if a route
is added without being documented, or documented without existing.
`TestOpenAPISecurityMatchesTheRoutingTable` does the same for which endpoints are
public — a spec that lies about authentication is worse than no spec, because clients
believe it.

Two notes:

- The `servers` entry is `/`, resolved against whatever host and port served the page.
  Try It Out therefore works on any port, and there is no hardcoded URL to go stale.
- Swagger UI's own JavaScript and CSS come from a CDN, so the **page** needs internet
  access. The **spec** is served from the binary and always works offline.

Set `DOCS_ENABLED=false` to serve neither.

## Health endpoints

Both return the same body shape, unversioned and unauthenticated so an
orchestrator can reach them.

```
GET /healthz   liveness   always 200
GET /readyz    readiness  200, or 503 when a dependency is down
```

```json
{
  "status": "ok",
  "service": "domieface-server",
  "version": "1.4.0",
  "revision": "9f2c1ab4de70",
  "goVersion": "1.26.0",
  "environment": "production",
  "time": "2026-09-04T11:23:00.000Z",
  "uptimeSeconds": 86400,
  "checks": [{ "name": "database", "status": "ok", "latencyMs": 3 }]
}
```

These are the fields you want during an incident without shelling into the box:
which build is serving traffic, and how long it has been up — an `uptimeSeconds`
that keeps resetting means the process is crash-looping. `version` is stamped at
build time (`make build VERSION=1.4.0`), falling back to the VCS revision, then
`dev`. `revision` is omitted when the binary was not built from a repository.

**The two endpoints answer different questions, and conflating them causes
outages.** Liveness touches no dependency: it answers "is this process wedged,
should you restart it". Failing it during a database blip would have the
orchestrator kill healthy instances and turn a recoverable outage into a restart
storm. Readiness is where dependencies are checked, because 503 there removes the
instance from rotation without killing it.

`checks` appears on readiness only, with a per-dependency verdict and its
latency. The latency is worth watching on its own: a check that passes in 1900 ms
is a dependency about to start failing. A failing check adds `error`, which says
what failed rather than where — these endpoints are unauthenticated.

Object storage is deliberately not checked. Uploads need it, but the feed and
every read path do not, so a storage blip should not pull the whole instance out
of rotation. `checkDatabase` in
[internal/api/health.go](internal/api/health.go) is where you would add it.

## Keep-alive

The server requests its own `/healthz` every 10 minutes (`SELF_PING_INTERVAL`), which
keeps the process and its Postgres connection pool warm rather than letting idle
connections get dropped and reconnected on the first real request.

On Render the target defaults to `RENDER_EXTERNAL_URL`, so the request leaves and
re-enters through the platform router, which is the only version of this that can
affect idling. Even so, treat it as best-effort: if staying awake matters, drive it
from an external cron rather than from the instance that is trying not to sleep.

Be clear about what this does not do: **pinging the loopback address will not stop a
platform-as-a-service idling the instance.** Those platforms decide based on traffic
arriving at their edge router, and a request the container makes to itself never gets
there. To keep such a host awake, point `SELF_PING_URL` at the public health URL so the
request goes out and back:

```
SELF_PING_URL=https://your-service.example.com/healthz
```

`SELF_PING_ENABLED=false` turns it off.

## Deploying

[render.yaml](render.yaml) is a Render blueprint covering the whole stack. Deploying
anywhere else needs the same variables.

**`ENVIRONMENT` defaults to `production`, and only the exact string `development`
unlocks the local fallbacks.** That direction is deliberate. The fallbacks include a
JWT signing key committed to this repository, so defaulting to development would let a
deployment that forgot one variable sign real tokens with a published secret — anyone
could then forge a token for any account. A missing variable stops the boot instead,
naming everything that is missing at once:

```
missing required configuration (ENVIRONMENT=production):
  DATABASE_URL           Postgres connection string, e.g. postgres://user:pass@host:5432/db?sslmode=require
  JWT_SECRET             at least 32 characters; generate with: openssl rand -base64 48
  ...
```

Two settings are easy to get wrong and do not announce themselves:

- **`TRUST_PROXY_HEADER=true` on any PaaS.** Every request arrives through the
  platform's proxy, so with this off the rate limiter sees a single client address for
  the entire internet and throttles all your users against one shared budget. It is
  off by default because trusting `X-Forwarded-For` when the server is directly
  reachable lets any caller forge an address and walk past the limiter.
- **`S3_PUBLIC_BASE_URL` must be reachable from the phone**, not just from the server.
  It is baked into every `avatarUrl` and `imageUrl`.

Render has no object storage, so the `S3_*` variables point at S3, Cloudflare R2,
Backblaze B2 or similar. The server refuses to start without them rather than
accepting uploads and failing on the first presign.

Set the health check path to `/healthz`, not `/readyz`. Liveness touches no dependency,
so a database blip will not have the platform recycle otherwise-healthy instances.

On the free plan: the web service sleeps after ~15 minutes without inbound traffic, and
the Postgres instance is deleted after 30 days.

## Configuration

Every value is an environment variable with a local-development default; see
[.env.example](.env.example). Two matter more than the rest:

| Variable | Why it matters |
|---|---|
| `JWT_SECRET` | Required when `ENVIRONMENT=production`, and must be ≥32 characters there. Startup fails otherwise rather than silently signing tokens with a known key. |
| `S3_PUBLIC_BASE_URL` | Baked into every `avatarUrl` and `imageUrl`. It must be reachable **from the phone**, not just from the server. |

## Layout

```
Makefile                     every command you need; run `make` for the list
main.go                      wiring, HTTP server, graceful shutdown
migrations/                  embedded SQL, applied at startup
internal/
  api/                       routes, handlers, wire DTOs, openapi.yaml
  auth/                      bcrypt, JWT minting, refresh token lifecycle
  config/                    environment loading
  httpx/                     error envelope, JSON codec, middleware, rate limiter
  buildinfo/                 version and VCS revision of this binary
  janitor/                   24h upload GC, expired token sweep
  keepalive/                 the self-ping
  jsontime/                  the one timestamp format the contract allows
  store/                     domain models and persistence interfaces
    postgres/                the real implementation
    memory/                  test double only — not a production store
  uploads/                   S3/MinIO presigning and the per-purpose limits
  validate/                  the field rules from contract §6
```

Handlers never see SQL, and the store never sees HTTP.

## Decisions worth knowing about

**Refresh tokens are stored hashed.** Only a SHA-256 digest is persisted, so a database
leak does not hand over live sessions. SHA-256 rather than bcrypt because the input is
256 bits we generated ourselves — it is not guessable, and lookup has to be an indexed
equality search.

**Replaying a consumed refresh token revokes the whole family.** Every exchange marks the
presented token used and issues a successor sharing a `family_id`. Presenting a consumed
token means it leaked — either the client failed to persist its successor or somebody
copied it — and we cannot tell which, so everyone in that family signs in again. This is
why the contract insists the client persists the new refresh token immediately.

**Upload keys are claimed, not trusted.** `POST /uploads/presign` records the key against
the caller and the purpose. Attaching it later checks all three: it must be your key,
presigned for that purpose, and not already attached. So you cannot post with someone
else's upload, use a 5 MB avatar key as a 10 MB post image, or fan one upload out across
many posts.

**Content-Type and Content-Length are part of the presigned signature.** Storage rejects
any PUT whose headers differ from what was signed. That is the only enforcement point
available, because the bytes never reach this server. It does mean the client must know
the exact byte length before asking for a URL — compress first, then measure, then
presign.

**The feed pages by keyset on `(created_at, id)`, both descending.** The ID tie-breaks
posts created in the same millisecond; without it a page boundary could skip or repeat a
post, which is the exact failure offset paging was rejected to avoid. A composite
descending index makes this an index scan rather than a sort.

**`PATCH /users/me` distinguishes absent from null.** `httpx.Optional[T]` carries three
states, because a plain pointer cannot tell "leave the avatar alone" from
`"avatarKey": null`, and the contract gives those two different meanings.

**Login does not reveal whether an account exists.** An unknown email spends comparable
time on a throwaway bcrypt comparison and returns the same `INVALID_CREDENTIALS` as a
wrong password.

**`TOKEN_EXPIRED` and `TOKEN_INVALID` are kept strictly apart.** Expired means refresh and
retry once; invalid means log the user out. Returning the wrong one either strands a user
with a recoverable session or spins the client through a refresh that can never succeed.

## Gaps in the contract I had to decide

Raising these as the handoff asks. All are implemented one way; say the word if you want
another.

1. **`User`, `PublicUser`, `Post` and `AuthResponse` are named but never defined.** I went
   with: `User` = `id, email, username, displayName, bio, avatarUrl, createdAt`;
   `PublicUser` = the same without `email`; `Post` = `id, author, imageUrl, caption,
   createdAt`; `AuthResponse` = `accessToken, refreshToken, accessTokenExpiresAt, user`.
   Including the user on `AuthResponse` saves a round trip after every login, and the
   expiry lets the client refresh *before* a request fails rather than after.

2. **Storage keys go in, URLs come out.** The contract has the client send `avatarKey` and
   `imageKey`, but never says what comes back. Responses carry `avatarUrl` and `imageUrl`,
   not the keys — the key is an internal handle, and moving storage hosts later should not
   be a client change.

3. **Minimum image dimensions cannot be enforced here.** The contract sets 200×200 for
   avatars and 320×320 for post images, but the bytes go straight to object storage, so
   this server never sees the image. Size and MIME type *are* enforced, via the presigned
   signature. Dimensions are client-side only. If they need to be guaranteed, the options
   are a storage event that inspects and rejects after the fact, or routing uploads
   through the API — which the contract explicitly rules out.

4. **`POST /auth/logout` is protected but takes the refresh token in the body.** Implemented
   as written. It revokes the whole family, and is idempotent: an unknown or empty token
   still returns 204, since the contract tells the client to clear its tokens regardless.

5. **The bucket is world-readable.** Every `imageUrl` is a plain URL, which only works if
   objects are publicly readable. If images should be private, they need presigned GETs
   too, and that changes the response shape — feed items would carry expiring URLs.

6. **Rate limiting is per-process.** `RATE_LIMITED` and `Retry-After` are implemented with
   an in-memory token bucket. Behind more than one replica each instance keeps its own
   budget, so a client effectively gets N times the limit. Move it to Redis before scaling
   out — do not just lower the numbers.

## What is deliberately absent

Everything in contract §9: likes, comments, follows, DMs, notifications, post editing,
multiple images, search, blocking. No scaffolding for them either — no unused columns, no
commented-out routes.

## Endpoints

All under `/v1`. Everything except register, login and refresh needs
`Authorization: Bearer <accessToken>`.

| Method | Path | |
|---|---|---|
| `POST` | `/auth/register` | → 201 `AuthResponse` |
| `POST` | `/auth/login` | → 200 `AuthResponse` |
| `POST` | `/auth/refresh` | → 200 `AuthResponse`, rotates the refresh token |
| `POST` | `/auth/logout` | → 204 |
| `GET` | `/users/me` | → 200 `User` |
| `PATCH` | `/users/me` | → 200 `User` |
| `GET` | `/users/{username}` | → 200 `PublicUser` |
| `GET` | `/users/{username}/posts` | → 200 `Paginated<Post>` |
| `POST` | `/uploads/presign` | → 200 `{ uploadUrl, key, expiresAt }` |
| `GET` | `/posts` | → 200 `Paginated<Post>` |
| `POST` | `/posts` | → 201 `Post` |
| `GET` | `/posts/{id}` | → 200 `Post` |
| `DELETE` | `/posts/{id}` | → 204, or 403 if it is not yours |

Plus `GET /healthz` and `GET /readyz` (see [Health endpoints](#health-endpoints)), and
`GET /docs` and `GET /openapi.yaml` for the reference.

This table is a summary; [the spec](internal/api/openapi.yaml) is the authority, and it
is the one a test keeps honest.
