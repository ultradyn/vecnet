# Hosted vecnet: pricing analysis and candidate models

Analysis date: **2026-11-05**, storage/retention evidence updated **2026-11-07**. Informative only: every vecnet price below is a candidate, not a decision or offer. Self-hosting/local use remains free and is expected to be common. All monthly financial scenarios are **estimates in USD, excluding tax**, unless another currency is explicit. Vendor links were accessed **2026-11-05**; access dates appear with the source tables. No customer data, production measurements, deployment, or cloud resources were used.

| Candidate | Example monthly price | Estimated service cost/account, before payment fees | Surplus at 5 / 20 paying accounts, after $20 fixed overhead | Main condition |
|---|---:|---:|---:|---|
| A. Flat hosted hub | $19 solo / $49 team / $199 heavy | $12 / $24 / $100 | Solo: **$10 / $100**; team: **$95 / $439** | Bounded usage; verified R2 path, explicit retention activation and measured operating costs required |
| B. Human seats, agents free | $9/human, $19 account minimum | $24 for five humans | **$75 / $362** for five-human accounts | Human identity must be billable; machine-heavy solo use needs quotas |
| C. Metered hosted service | $19 minimum + usage; $25 illustrative bill | $12 at solo usage | **$39 / $216** | Bill accepted sends with fan-out bounds, storage byte-hours and download bytes |
| D. Permanent storage add-ons | +$5/50 GB; +$15/250 GB | $1.50 / $6 incremental | 50 GB alone: **−$5 / $40**; 250 GB alone: **$21 / $143** | Attach to a paid base; one-week transfers need separate expiry semantics |
| E. Bring your own hub, pay for network | $9/network account | $2 incremental | **$12 / $108** | Future federation/relay/OAuth gateway; not implemented network revenue |
| F. Supporter / one-off | $5 recurring; illustrative $199 once | $0.50 recurring, no hosting entitlement | Recurring: **$0 / $60** | Sponsorship supports OSS; one-off cannot fund unlimited lifetime hosting |

**Candidate shortlist for discussion:** A for a small managed-hub launch; B for teams; E as a future network business. D can complement A or B. These are alternatives to investigate, not a selected strategy. The few-user surpluses include delivery support at an estimated $60/hour but exclude fixed founder operations, development, acquisition and salary. Adding two fixed operating hours/month raises overhead to $140 and changes the result substantially (§6).

## 1. What the current product actually costs to operate

Grounding: [README](../../README.md), [protocol](../PROTOCOL.md), [deployment](../DEPLOY.md), [connectors](../connectors/), [public-demo plan](../plans/2026-11-05-demo-instance.md), [fly.toml](../../fly.toml), and the code below, inspected at base commit `40124f3` on branch `docs/pricing-analysis`.

- The deployed configuration is one always-on Go process, SQLite in WAL mode, a local blob filesystem, `shared-cpu-1x`/512 MB in **Sydney**, a mounted volume, and 250/300 soft/hard connections. Deployment instructions create a **1 GB** volume; `fly.toml` itself does not specify provisioned GB. Actual invoice, disk usage and regional compute quote were not inspected.
- Claude Code/watch uses streams and hooks; dots use public MCP, built-in OAuth and outbound MCP Events; Grok Bots use routine webhooks and MCP; GitHub ingress produces messages but never posts back. See [Claude Code](../connectors/claude-code.md), [dots](../connectors/dots.md), [Grok](../connectors/grokbot.md), [GitHub](../connectors/github.md). Agent model subscriptions and token inference are customer costs, not hub COGS. Paid relay features would not include model execution.
- The demo plan describes a **separate**, immutable in-memory replay service, without a hub database or attachments. It is neither a free hosted customer hub nor evidence of tenant capacity. Any demo hosting/marketing cost belongs in fixed overhead separately.
- A principal is an authorization/ownership concept within one hub, **not an implemented SaaS tenant boundary**. A hub has one operator administration surface, directory, SQLite store and identity. Sharing the existing hub among unrelated customers needs a security/product design before its economic savings can be claimed.
- [Deployment backups](../DEPLOY.md#backups) now require online SQLite backup or a verified stopped consistent DB/WAL set plus coordinated attachments, external storage and a restore drill. The existing `just fly-backup` main-file copy is not a valid live-WAL backup. Nightly consistent automation and a managed recovery objective remain operating work; include encryption/access controls, backup retention and restore cost.

### 1.1 Message and SQLite measurements

[Schema](../../internal/store/migrations/0001_init.sql), [later migrations](../../internal/store/migrations/), [send pipeline](../../internal/hub/send.go), [event log](../../internal/eventlog/log.go), [spill](../../internal/hub/spill.go), [blob store](../../internal/blob/store.go):

The original inline baseline stores JSON in `messages.body` once and embeds it in one durable delivery event **per recipient**. The 2026-11-07 dual reader supports opt-in references to that immutable body, frozen recipient envelope/attachment overrides and rare legacy snapshots; delivery/index rows still scale with fan-out. Send also writes an idempotency response, audit entry and thread state. Receipts, presence, thread summaries, notifications and MCP outbox work add more rows/traffic. A message is not one tiny text row and one HTTP request.

A local **synthetic, in-memory schema experiment**, using Python SQLite 3.54.0, applied all 17 SQL migrations to a fresh `:memory:` database. For each profile it inserted 1,000 unique messages, F deliveries/events per message, an idempotency response and a send audit row. IDs used the normal prefixes and 26-character suffixes; timestamps were 24 characters; JSON used compact separators. `delta(page_count × page_size)/1000` includes allocated B-tree/index pages. No live DB, Go build or production load test was used.

| UTF-8 text bytes | Recipients F | Synthetic Message JSON bytes | Delivery JSON bytes | Measured SQLite allocation/send | Planning allowance/send |
|---:|---:|---:|---:|---:|---:|
| 2,048 | 2 | 2,479 | 2,795 | 14,651 | 16 KiB |
| 2,048 | 3 | 2,479 | 2,795 | 19,591 | 24 KiB |
| 4,096 | 5 | 4,527 | 4,843 | 35,176 | 48 KiB |

Reproduction recipe: apply migrations in filename order; insert `messages(msg_id,thr_id,seq,sender,kind,created_at,body)`; for each recipient insert `events(owner,type,at,data)` with a nested Message and `deliveries` with a distinct ID, state, hash and timestamps; insert `idempotency` and `audit`; commit; compare the two page-count products. Test bodies used repeated `x` text, `wake_idle`, depth 1/max_depth 32, sender info and a two-participant thread summary. This is a schema allocation probe, not a full protocol-valid workload: it omits thread/peer creation, later lifecycle writes, read markers, real envelope optional fields, WAL/checkpoints and fragmentation. Planning allowances are estimates, not measured capacities or worst-case bounds; large fan-out or metadata requires a separate profile.

Current default inline preview is 4,096 bytes; send text allows 65,536 bytes, with larger previews spilled into a blob; whole request cap is 256 KiB, metadata 8 KiB, attachments at most 16. JSON escaping and attachment metadata can enlarge envelopes. Do not price on a fixed text size without a body-byte and recipient cap.

**Measured modernc update (2026-11-07).** [Task 8 receipt](db-compaction-bench-2026-11-07.md) compares 80 accepted sends per fixture on x-boss with FULL durability, identical seeds and inline/reference lifecycle pairs. Live B/send subtracts each fixture's empty-schema baseline from `(page_count − freelist_count) × page_size`, including tables/indexes. It is not allocated DB file size, cumulative WAL traffic or a production capacity forecast.

| Representative profile | Measured inline live B/send | Measured reference live B/send | Measured reduction |
|---|---:|---:|---:|
| F1, 2048 text, repeated/new thread | 11,059.2 | 7,731.2 | 30.1% |
| F2, 2048 text, repeated/new thread | 16,896.0 | 10,598.4 | 37.3% |
| F5, 4096 text, varied/reused thread | 53,964.8 | 17,766.4 | 67.1% |
| F20, 4096 text, varied/reused thread | 196,915.2 | 72,396.8 | 63.2% |
| F2, 2048 varied/new, hosted attachment | 17,561.6 | 11,929.6 | 32.1% |

These are selected paired cohorts, not replacements for every workload below. Hosted attachment metadata and recipient overrides are included in the SQLite figures. The cohort reuses one synthetic hosted blob: its actual `BlobBytes` are reported separately in the raw receipt, outside this SQLite live-page table. R2 transfer/cost is unmeasured. Callback outbox copies still count, and the full receipt reports one/three-subscription cohorts. Legacy byte variants can retain an additional deduplicated snapshot; the F1 legacy cohort grows. Measured reference JSON grows with frozen participant metadata (roughly 724–1,373 B per reference across normal fan-outs); the plan's R=650 and C/B/I arithmetic remain **logical assumptions**, not measured distributions or page savings.

For the F5/4096 varied/reused reference fixture, live storage after backfill is **2,310,144 B**; idempotency expiry brings live storage to **2,220,032 B**, settled acknowledged-event expiry to **1,777,664 B**, and explicit finite-history cleanup to **929,792 B**. The main file remains **2,310,144 B** through those deletion stages; bounded incremental reclaim finally reduces it to **929,792 B** in this fixture. These are whole-fixture measured lifecycle totals, including its baseline, with pins/tails left visible in the raw receipt. They are not a universal steady-state size or proof that an existing NONE file shrinks.

**Accepted performance cost:** reference hydration raises read p95 from roughly **0.5 ms to 3–13 ms** in representative cohorts, with some profiles/repeats around **14–16 ms**. The receipt investigates immutable-body loading, reconstruction/hash validation and added CPU/allocations; send/push tails also have regressions/variance. Keep those byte guards and explicitly accept the storage/latency tradeoff; a faster-hydration follow-up will be filed. Do not turn these build-host timings into a shared-core Fly SLA or revise compute budgets downward on storage evidence alone.

**Retention is a cost multiplier.** Delivery expiry/loop TTL does not delete history. Defaults keep messages, unacked events and audit indefinitely; acknowledged prefixes have a seven-day age with independent worker enablement and pins, and idempotency uses 24 hours. Finite history/archival controls are implemented, but hosted 90 days requires a separate operator policy, preview/confirmation and activation. The shipped CLI does not expose the internal acknowledged-pruning enable switch; a duration preference alone is not activation. Acknowledged cleanup/archive do not bound lifetime message growth or hosted blob bytes. A **1 GB volume is not an unlimited plan**.

At the **older inline planning allowances**, one month of solo/team/heavy sends adds **0.164 / 2.458 / 49.152 decimal GB**; twelve months adds **1.97 / 29.49 / 589.82 GB**, before extra lifecycle state. These remain arithmetic forecasts, not measured reference growth. The 30-day working sets below are **commercial modeling assumptions**, not approved retention defaults. A hosted 90-day policy would need a new working-set/cost calculation, plus pinned tails, metadata, attachments, WAL headroom and separately retained external backups. Reference savings and free-page reuse do not by themselves justify a lower provisioned volume or unlimited retention.

### 1.2 Price evidence and infrastructure alternatives

Each row's link is the source of the external price. All sources in this and subsequent source tables were accessed **2026-11-05**. Listed allowances are provider/account allowances, never duplicated per vecnet customer. Estimates below deliberately ignore promotional credits and most free allowances.

| Component | Public price evidence and scope | Fit / qualification |
|---|---|---|
| Fly Machines | [Fly resource pricing](https://docs.fly.io/about/pricing), accessed 2026-11-05: always-on `shared-cpu-1x` 256 MB **$2.19**, 512 MB **$3.69**, 1 GB **$6.70**; `shared-cpu-2x` 2 GB **$13.39** monthly; examples use Ashburn | Region changes price; these are **not Sydney quotes**. Model Sydney compute as estimated $5/$10/$40 for solo/team/heavy; benchmark and obtain quote before an offer. Size is a budgeting assumption, not proven capacity. |
| Fly disk/network | [Fly resource pricing](https://docs.fly.io/about/pricing), accessed 2026-11-05: volume **$0.15/provisioned GB-month**; snapshots **$0.08/stored GB-month**, first 10 GB free; internet egress **$0.02/GB NA/EU**, **$0.04 APAC/Oceania**, **$0.12 Africa/India**; ingress free | Volume includes unused capacity. Sydney model uses $0.04. Snapshot allowance is shared; replication traffic and restore time still matter. |
| Hetzner EU VM | [Official June 2026 adjustment](https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/), accessed 2026-11-05: CX23 **€5.49 / $6.49 monthly**, excluding VAT and IPv4. [CX23 specification](https://www.hetzner.com/cloud/cost-optimized/), accessed 2026-11-05: **2 vCPU, 4 GB, 40 GB**, EU **20 TB** included traffic | Page currently marks CX23 unavailable. Treat as a price comparison, not available inventory; EU latency differs from Sydney. Model $8/month server+IP contingency, explicitly an estimate. Backups are [20% of server price, seven slots](https://docs.hetzner.com/cloud/billing/faq/), accessed 2026-11-05; add off-provider DB recovery and operator time. |
| Workers | [Workers pricing](https://developers.cloudflare.com/workers/platform/pricing/), accessed 2026-11-05: **$5/month** paid minimum includes **10M requests + 30M CPU-ms**; overage **$0.30/M requests + $0.02/M CPU-ms** | Public HTTPS gateway/relay candidate. Existing Go binary, SQLite transaction API, filesystem, goroutine workers and durable streaming are not a drop-in Workers deployment. Port/design cost excluded from unit COGS. |
| D1 | [D1 pricing](https://developers.cloudflare.com/d1/platform/pricing/), accessed 2026-11-05: paid includes **25B rows read, 50M written/month, 5 GB**; then **$0.001/M reads, $1/M writes, $0.75/GB-month** | Index updates count as writes; scans cost rows, not returned messages. Example estimate: 1M sends × 30 billed writes = 30M writes, but a full application workload can exceed inclusion. Needs transaction/limit/stream redesign and database partitioning. |
| SQLite backup | [Litestream](https://litestream.io/), accessed 2026-11-05, continuously replicates SQLite to storage as a separate process | No managed-backup subscription assumed. Price backup objects, PUTs, WAL amplification, outbound bytes and restore drills separately; it does not supply automatic multi-writer HA. |
| TLS and domains | [Fly](https://docs.fly.io/about/pricing), accessed 2026-11-05: first 10 single-host certs free, then **$0.10/hostname/month**; wildcard **$1/month**. [Let's Encrypt](https://letsencrypt.org/about/), accessed 2026-11-05: certificates free | Model **$2/month domain allowance (estimate, not a registrar quote)** for a shared service domain. Customer subdomains do not each require a registration. Automated issuer/audience management and renewal still require work. |

For Workers+D1, an estimated 1M sends × 5 requests × 2 CPU-ms = 5M requests/10M CPU-ms fits those paid inclusions, **before** polls, reconnects, retries, callbacks and admin traffic. This is an arithmetic example, not a validated alternative host. At 100M sends those assumptions alone imply $147 request and $19.40 CPU overage plus the base, and 3B row writes imply $2,950 D1 write overage before reads/storage. Free serverless service is not an unlimited high-volume network budget.

### 1.3 Blobs: storage, requests and the actual transfer path

| Storage provider | Storage / request / egress evidence | 50 / 250 GB at full occupancy; transfer implications |
|---|---|---|
| R2 Standard | [R2 pricing](https://developers.cloudflare.com/r2/pricing/), accessed 2026-11-05: **$0.015/GB-month**, **$4.50/M Class A**, **$0.36/M Class B**, internet egress free; free allowance 10 GB-month, 1M A, 10M B; usage rounded up to billing units | **$0.75 / $3.75 storage** before redundancy and operations. Direct authorized delivery avoids per-GB vendor download fees. Do not use Infrequent Access for one-week objects: it has different retrieval/minimum-duration economics. |
| S3 Standard, US reference scenario | [AWS pricing](https://aws.amazon.com/s3/pricing/), accessed 2026-11-05; the dynamic regional rate table was not exposed in the fetched text. Use **estimated reference rates**, not a verified current quote: **$0.023/GB-month**, **$0.005/1k PUT**, **$0.0004/1k GET**, **$0.09/GB outbound**. Historical first-party [storage/PUT example](https://aws.amazon.com/blogs/storage/monitor-data-transfer-costs-related-to-amazon-s3-replication/) and [transfer/request example](https://docs.aws.amazon.com/solutions/latest/data-transfer-hub/cost.html), both accessed 2026-11-05, support the reference values | Estimated **$1.15 / $5.75 storage**. AWS lists 100 GB/month outbound free aggregated across services/regions with exclusions; do not allocate 100 GB to each customer. Region and GiB billing must be re-quoted; US reference is not a Sydney price. |
| Backblaze B2 | [B2 pricing](https://www.backblaze.com/cloud-storage/pricing), accessed 2026-11-05: starts **$6.95/TB/month** = **$0.00695/decimal GB-month** for planning; first 10 GB free; free transactions; egress free to **3× average stored data**, then **$0.01/GB**, with partner exceptions | **$0.3475 / $1.7375 storage**. Cheap retained storage, but high-turnover ephemeral transfers may greatly exceed the 3× allowance. Validate contract/unit interpretation; model uses decimal TB. |
| Fly volumes, present blob implementation | [Fly pricing](https://docs.fly.io/about/pricing), accessed 2026-11-05; use rates in §1.2 | **$7.50 / $37.50 provisioned disk alone**, plus backups and Sydney egress. Allocating a pack, rather than actual occupancy, costs money immediately. Local disk is not independently replicated object storage. |

R2/S3/B2 are **architectural alternatives**: `internal/blob/store.go` currently writes local files and `internal/httpapi/blobs.go` serves authenticated/tokenized reads. Merely moving files to R2 but proxying all downloads through Fly still incurs **Fly outbound**. Need an authorized direct-download mechanism or edge-serving path, preserving attachment access control, hashes, tombstones and revocation expectations. Uploading Fly-received blobs to R2 also incurs Fly outbound; direct-upload authorization or explicitly metered proxy uploads is needed. Do not treat a public unauthenticated bucket as equivalent delivery.

Estimated cost per GB stored-month = provider rate × copies + request allocation + local staging/headroom. Per GB **downloaded to one recipient** = object egress + serving-host outbound + GET allocation. Upload ingress may be free while forwarding/replicating the upload is not. One uploaded 1 GB file downloaded by ten peers is **10 GB transferred**, not 1 GB.

At an illustrative 10 MB/object: 1 GB upload ≈100 PUTs costs **$0.00045 R2**; 1 GB download ≈100 GETs costs **$0.000036 R2**, before shared billing rounding. With 1 KB files there are about 1M operations/GB instead; request charges become material even where egress is free.

**Expiry is not implemented product retention.** `Store.GC` requires **no `blob_refs`** before either its 24-hour orphan grace or TTL can remove a blob. `Pin` keeps message attachments; deduplicated uploads can extend/remove TTL. Seven-day `BlobTokenTTL` is access-token lifetime, not deletion. A strict one-week transfer product needs a retention class, authorization of expiry changes, shared-reference/dedupe rules, actual deletion jobs and backup deletion policy. Permanent means retained while the plan is paid, with a stated cancellation/grace/export policy, not forever.

For uniform uploads U GB/month, seven-day expiry implies average transfer storage `U×7/30`, not U and not a fixed 50 GB. At U=100 GB/month, that is 23.33 GB, estimated R2 storage **$0.35/month**. If downloaded twice, E=200 GB: R2 egress is zero, B2 paid egress is about **$1.30** above 70 GB free, and Fly proxy download egress is **$8**. R2 free allowance/rounding and request charges are excluded here. Spike caps must constrain instantaneous occupied bytes as well as monthly uploads.

## 2. Bottom-up worksheet and usage profiles

All quantities and non-vendor rates here are estimates. A month is 30 days; payload/traffic GB means 10^9 bytes; KiB means 1,024 bytes. Providers may bill GiB or rounded units: convert exact byte counters to invoice units rather than assuming equal units. No FX conversion is needed for the USD reference model; EUR and AUD prices remain labeled.

| Variable / formula | Meaning |
|---|---|
| M, F, W, R | Accepted new messages/month; mean unique recipient fan-out; delivery wire bytes; retry/duplicate multiplier |
| `D = M × row_allowance × history_months / 1e9` | DB working-set GB, before base tables/headroom |
| `V = provisioned GB >= DB + local blobs + WAL + staging + free-space reserve` | Local volume billing quantity; not average used bytes |
| `B = backup GB-month` | Snapshots + retained WAL + retained blob backup, measured independently of D |
| `Emsg = 2 × M × F × W × R / 1e9` | Planning traffic: deliveries plus equal allowance for catch-up/read/control. Real polling/stream frames must be metered; this factor is an estimate |
| `Cinfra = compute + .15V + .015B + object_ops + .04(Emsg + backup_upload_GB + proxied_blob_E) + logging + email` | Sydney/Fly + R2 illustration, using cited rates; add blob occupancy `.015S` separately, avoid double-counting backups and blobs |
| `Cservice = Cinfra + support_hours × $60` | Direct delivery/support cost, estimated labor value |
| `Cpayment = .036P + .30` | Reference US card processing + Billing percentage, one monthly charge; see §2.2 |
| `contribution c = P − Cpayment − Cservice` | Contribution toward fixed overhead before acquisition/development/tax |
| `surplus = N×c − Ffixed`; `Nbreak = ceil(Ffixed/c)` | For positive c; no finite break-even if c≤0. Exact equality is cash break-even |

### 2.1 Three workloads

Agents are **monthly active authenticated peers**, not registered rows or model calls. Per-agent figures are allocation averages, not a proposed billing unit. No load benchmark establishes that the listed machines can sustain peaks.

| Assumption/month | Solo developer | Small team | Heavy automation |
|---|---:|---:|---:|
| Humans / active agents | 1 / 6 (range 3–10) | 5 / 30 | 5 / 150 (100+) |
| M / mean F / W / R | 10k / 2 / 3,000 B / 1.2 | 100k / 3 / 3,000 B / 1.2 | 1M / 5 / 5,000 B / 1.2 |
| Permanent blob occupancy S | 2 GB | 20 GB | 200 GB |
| New blob uploads / downloads | 5 / 10 GB | 50 / 100 GB | 500 / 2,000 GB |
| History assumption | 30 days | 30 days | 30 days |
| DB allowance / resulting D | 16 KiB / 0.164 GB | 24 KiB / 2.458 GB | 48 KiB / 49.152 GB |
| Provisioned DB/WAL/staging volume V, blobs remote | 2 GB | 8 GB | 80 GB |
| Emsg, including catch-up/control allowance | 0.144 GB | 2.16 GB | 60 GB |
| Compute budget | $5 | $10 | $40 |
| Volume `.15V` | $0.30 | $1.20 | $12.00 |
| Backup storage+ops reserve | $0.05 | $0.25 | $2.50 |
| Backup outbound: assume `2D`/month × $.04 | $0.013 | $0.197 | $3.932 |
| Blob occupancy × $.015 | $0.03 | $0.30 | $3.00 |
| Additional blob request reserve | $0.05 | $0.10 | $1.00 |
| Emsg × $.04 | $0.006 | $0.086 | $2.40 |
| Variable logging/monitoring reserve | $0.25 | $0.50 | $2.00 |
| Direct infra subtotal | **$5.70** | **$12.63** | **$66.83** |
| Support, 5 / 10 / 30 minutes at $60/h | $5 | $10 | $30 |
| Rounded service-cost budget v used in margins | **$12** | **$24** | **$100** |
| Fully allocated v/M | $0.0012/message | $0.00024/message | $0.00010/message |
| Fully allocated v/active agent | $2.00 | $0.80 | $0.67 |

Backup reserve uses a working assumption of about two DB copies plus WAL/operation overhead (`.015×2D` gives $0.005/$0.074/$1.475 before that overhead). A busy SQLite workload can rewrite pages far faster than net DB growth. Measure WAL bytes, snapshot turnover and PUT counts; the `2D` outbound assumption is not an upper bound. A million tiny Litestream PUTs alone adds the cited request-unit charge even if byte growth is small.

These are **future remote-blob/direct-transfer + bounded-history estimates**. Present local-volume deployment sensitivity, including all blob ingress forwarded only locally, one additional remote blob backup and its upload, and approximate disk headroom: V=4/32/320 GB; add local blob download egress, replace R2 primary storage with disk, retain one R2 blob backup. The same arithmetic gives approximately **$11.48/$31.03/$220.83** service cost before rounding; budget **$13/$32/$225**. The heavy $199 example would lose money in that architecture. A proxy-to-R2 implementation also needs to add forwarding uploads `.04×U`: **$0.20/$2/$20** per month, plus download egress if proxied.

With twelve months of retained history, D grows about 12× under this worksheet's assumptions. Re-size volume, backups and replication accordingly; no automatic 30-day policy is enabled by default to hold these costs flat. Blob growth can be unbounded too if every referenced attachment remains permanent.

**Marginal message cost differs from v/M.** For solo-sized messages, 16 KiB on Fly is approximately **$0.00000246 per retained message-month**, and the traffic formula costs **$0.000000576/send**. Two R2 backup copies add approximately **$0.000000492/message-month** before WAL amplification/PUTs. Compute cost is initially a VM step, not an observed per-message price. Use v/M for a whole-account allocation and the byte/write model for marginal scenarios; do not infer that a nearly idle dedicated hub costs $0.000003/message in total.

Outbound push/webhook work runs in this Go binary: no Svix/Pusher subscription is needed for the current implementation. Retry attempts, notification routes and multiple subscriptions cost additional bytes/CPU; a 256 KiB callback retried eight times is up to 2 MiB **per recipient** before headers. Long-lived idle connections, empty polls, presence fan-out and reconnect catch-up can cost more than sparse sends; cap and meter them separately. No outbound email is required by built-in link-code OAuth; an optional 1,000 transactional emails/month costs **$0.10 plus data** at [SES à la carte rates](https://aws.amazon.com/ses/pricing/), accessed 2026-11-05, and is a platform cost here. Customer support email setup/deliverability is additional work.

### 2.2 Payments and the fixed-cost floor

The reference spreadsheet uses [Stripe US Payments](https://stripe.com/pricing), accessed 2026-11-05, **2.9% + $0.30** domestic card processing, plus [Stripe Billing](https://stripe.com/billing/pricing), accessed 2026-11-05, **0.7% of Billing volume**: `.036P+.30`. This is a common-currency reference, **not an assumption that an Australian merchant receives US rates**. International cards/FX, refunds, disputes, tax tooling and merchant-of-record services are excluded and must be added for the actual route.

[Stripe Australia](https://stripe.com/au/pricing), accessed 2026-11-05, lists **1.65% + A$0.30 domestic**, **3.5% + A$0.30 international**, plus **2% if currency conversion is required**, with GST included in card fees. Keep the A$0.30 in AUD or convert with a dated settlement FX assumption before combining with USD. No FX rate was guessed here. An extra two percentage points of payment expense reduces contribution by `.02P`.

Estimated lean platform fixed **cash** floor, incremental to per-customer resources/support above:

| Fixed component/month | Estimate |
|---|---:|
| Billing/control/demo-host budget | $10 |
| Shared domain allowance | $2 |
| Shared health/log retention budget | $3 |
| Optional transactional email | $0.10 |
| Backup/rounding/incident contingency | $4.90 |
| **Ffixed, lean cash floor** | **$20** |
| Add two fixed operating hours at $60/h | **+$120 → $140** |

$20 is a constrained founder-operated scenario, not a verified bill or fully staffed operation. No separate managed database, expensive monitoring minimum, paid support contract, dedicated IP per hub or staffed SLA is included. Variable VM/support budgets cover each account separately. Doubling the small fixed floor is less damaging than letting one heavy transfer account consume unlimited bandwidth. An SLA, redundancy, longer retention, many free users, or more demo resources changes Ffixed and/or v.

## 3. Single-tenant and shared hosting economics

**Hub per customer** preserves today's domain and isolation shape: separate DB, keys, blobs, operator and upgrade/restore lifecycle. Dedicated Fly Machines make idle capacity a customer-specific minimum. The solo remote-blob budget has roughly $7 infra/headroom + $5 support = v=$12, even at low message count. A minimum price covering service and fixed cost share is:

`Pmin(N) = (v + .30 + Ffixed/N) / .964`.

| Topology / estimate | v | Pmin at 5 accounts, Ffixed=$20 | Pmin at 20 | Qualification |
|---|---:|---:|---:|---|
| Dedicated Fly hub, future direct object path | $12 | $16.91 | $13.80 | $19 solo has modest cushion; current local-disk budget v=$13 gives $17.95 / $14.83 |
| Separate processes/DBs on pooled Hetzner VM | $7 | $11.72 | $8.61 | Assumed $2 infra share + $5 support at adequate density; not isolation equivalent to separate VMs |
| Shared application with true tenants | $6 | $10.68 | $7.57 | Assumed $1 infra share + $5 support; requires new tenant model and isolation review |
| Dedicated team hub | $24 | $29.36 | $26.24 | A five-human $45 or flat $49 example covers the modeled working set |

Pooled process hosting can save compute **without** rewriting SQLite tables into one multi-tenant DB, but needs filesystem/process/resource isolation, per-hub ingress routing and credential separation. One cheap VM is also one correlated failure domain. At small N, pay for unused capacity: do not divide a VM by 100 hypothetical future customers. For example, estimated $8 Hetzner host + $2 backup/log reserve + 5×$5 support = $35 total before other shared overhead, or $7/account at N=5; at N=1 it is $15/account. This comparison assumes the cited limited inventory is obtainable and excludes migration effort.

True multi-tenancy requires tenant IDs on authorization, queries, cache keys, events, blobs, quota/meter records, OAuth issuer/account routing and audit export. Operator/global directory access must be redesigned. Shared-content dedupe cannot leak another tenant's file existence; restore/export/deletion must remain tenant-scoped. At 1,000 accounts, shard by hub/tenant and bound write contention, DB size and streams; **nothing proves 1,000 accounts fit on one $5 machine**. The financial tables scale per-account capacity budgets linearly, not one VM infinitely.

No topology here provides proven multi-region HA. SQLite single-writer, local volume locality, deployment gaps and restore time need a separate availability design. R2 durability and backups do not turn the hub into a zero-downtime service. A 300-connection limit can be reached by a heavy customer using multiple streams per peer; an active-peer count alone is not a connection budget.

## 4. Pricing options, entitlements and cost controls

All example prices and caps in this section are **proposals for analysis**. Existing limits listed in §5 are distinct from the proposed commercial controls here. Every option preserves free local/self-hosted operation; payment purchases a service, retained bytes or optional network convenience rather than the right to run a hub.

### A. Flat per-hub subscription tiers

| Example tier | Price/month | Illustrative included bounds |
|---|---:|---|
| Solo | $19 | 10 active agents; 25k accepted sends, mean fan-out ≤2; 2 GB permanent + 10 GB transfer uploads/month with seven-day expiry; 20 GB downloads; 30-day history |
| Team | $49 | 50 active agents; 150k sends, mean fan-out ≤3; 20 GB permanent + 100 GB expiring uploads; 200 GB downloads; 30-day history |
| Automation | $199 | 200 active agents; 1M sends, mean fan-out ≤5; 200 GB permanent + 500 GB uploads; 2 TB downloads; 30-day history |

The entitlements are a starting envelope, not a claim that all simultaneous maxima cost the profile average. Full-quota example for Solo: 25k×16 KiB≈0.41 GB history, expiring bytes average 2.33 GB plus permanent 2 GB; volume/headroom and operation reserves must still fit v. Download and upload quotas apply across permanent and ephemeral objects, with a separate instantaneous storage cap. Heavy tier at 2 TB download requires direct serving; current local-disk estimate v=$225 makes the proposed $199 loss-making. A present-code heavy tier would need at least about **$250** merely to cover v=$225 and $4 fixed allocation at N=5, before a target margin.

Fits willingness to pay for managed updates, a stable public HTTPS endpoint, built-in OAuth operation, delivery workers, restoreable backups and convenience over running a machine. Risk: one sender broadcasting to thousands, attachment archives and stream churn erase the flat-price margin. Proposed guards: hard tenant budgets for recipient deliveries/bytes/connections, modest maximum fan-out per send (e.g. 20), prepaid top-ups or refusal when full, bounded retries and explicit burst limits. Avoid an ambiguous unlimited-agent/unlimited-usage promise. Self-hosters can run exactly the core functionality themselves.

### B. Human seats, agents free

Example **$9 per named active human/month, $19 per hub minimum**; five humans pay $45, and agents have no seat charge. Use Solo and Team usage envelopes from A, with additional volume requiring a capacity pack; agents are free **within capacity**, not unlimited load. Larger organizations could pay $12/human for policy/support features, an unmodeled alternative.

Matches who benefits and avoids charging for transient agent identities. Risk: one human runs 150 expensive automations, customers share a login, or principals do not correspond to humans. Specify billable human membership and invitations, disable/refund timing and service-account exclusion. Cap aggregate hub use regardless of seat count; enforce identity access rather than trusting a display name. No requirement to add seat licensing to self-hosted code. At one human the $19 floor has A's solo economics; without the floor a $9 dedicated solo account at v=$12 loses money before fixed costs.

### C. Usage-based hosting with a bounded free tier

Example free shared sandbox: **1,000 sends/month, two recipients/send, 100 MB storage, 1 GB transfer, seven-day history, two streams**, no managed support or uptime promise. This is a proposed product requiring tenant isolation and retention. Keep free self-hosting available without these hosted limits.

Paid example: **$19/month minimum includes 10k sends, 2 GB permanent, 10 GB download**. Overage **$2/10k accepted sends**, **$0.05/GB-month storage**, **$0.10/GB download**, with upload and request-rate caps. A 40k-send solo-shaped account with no other overages pays **$25**. Model v=$12 assumes this remains within the same small VM envelope; verify at full quota. A send includes up to two recipient deliveries; extra recipient deliveries could consume send-equivalent units at two per unit. Define and disclose that before billing; free internal retries/idempotency replays, receipts and health reads should not inflate billable sends.

Risk: confusion over generated messages, replay, failure billing and teams with large fan-out; tiny objects can generate huge API bills despite low GB. Proposed controls: separate recipient-delivery and object-operation budgets, pre-authorized monthly spend ceiling, dashboard estimates/alerts and hard refusal or queueing with clear semantics. Never generate unbounded surprise invoices from agent loops. The free fleet is an explicit budget: at assumed $0.10/free account/month, 100 free accounts add $10 fixed overhead; 1,000 add $100. A 1% conversion rate would be expensive relative to the few-user floor. Free users are **not** assumed costless in the main N scenarios.

### D. Expiring transfer plus permanent storage packs

Transfer could be included with A/B/C and expire seven days after upload, while **+$5/month buys 50 GB permanent** and **+$15 buys 250 GB**. Packs provide capacity while paid, not a one-time perpetual storage purchase. The cost model reserves 1.5× primary R2 occupancy for a partial retained backup/version overhead, plus operations/support: `50×.015×1.5+.375 = $1.50`; `250×.015×1.5+.375 = $6`. A full second copy raises those budgets to **$1.875/$7.875**. Retention/redundancy policy must be explicit; 1.5× is not a promise of two complete independent copies.

Risk: becoming a public file host, hotlinking, tiny-object floods, purge/version accumulation, cross-account dedupe and retained backups undermining expiry. Proposed guards: authenticated access, upload/download/request quotas shared with the base plan, minimum billing units, immediate occupied-byte checks and deletion verification. Permanent packs do not include arbitrary transfer. At the illustrative $0.10/GB extra-download price, direct R2 has a large byte margin but support and per-request work remain; on current Fly storage, 250 GB costs $37.50 disk **before** other costs and cannot be sold sustainably for $15.

Model incremental packs on an existing base account. The stand-alone matrix below pessimistically gives a pack its own payment transaction and full fixed floor; combining it on the base invoice avoids another fixed $0.30 charge and makes its incremental economics better. A base plus packs must not allocate full fixed overhead twice. These allowances are services for convenience; self-hosters can use their own disks/object stores.

### E. Bring your own hub, pay for network features

Future example **$9/month per network account**, up to three registered private hubs, a verified public-directory presence, managed public OAuth endpoint/HTTPS ingress, federation discovery and **10 GB relay transfer**; optional **$0.10/GB** above that with spend caps. A $29 team network package is another testable price point. People can retain private local compute and storage while paying for reachability and delivery.

The network and billing are **future work**. Federation is not a current service; a public OAuth gateway cannot transparently front arbitrary issuer/audience-bound hubs without designing trust, routing, token binding and tenant isolation. A relay is an application-message/attachment relay, not a claim of implemented TURN or an IP VPN. v=$2 estimates $0.50 relay/control infrastructure + $1.50 support (1.5 minutes). If support is five minutes, v becomes $5.50 and the $9 contribution falls to $2.876.

Risk: open-proxy abuse, directory spam, delivery loops across hubs, SSRF and account takeover; BYOH telemetry is untrusted. Meter at the managed ingress/relay boundary, authenticate both hubs, cap relayed bytes/connections/hops and provide abuse suspension and keys. Customers who run their own reachable hubs should still communicate using free/open primitives; the paid value is managed discovery, relay and endpoint convenience rather than a mandatory network toll.

### F. Lifetime, one-off supporter and OSS sponsorship

Recurring example **$5/month supporter**, recognition/updates/community participation, **no hosting or guaranteed support**; direct handling reserve v=$0.50. Organizations could sponsor **$50/$250 per month** for sustaining OSS with a disclosed support scope; do not silently promise an SLA or convert sponsorship into a paid core-use license. Sponsor platform fees, if used, need a separate quote: calculations here assume direct Stripe, not a free sponsorship platform.

One-off example **$199 supporter purchase**, free core remains free, with a finite setup/help budget of $20 and no recurring service entitlement. With Payments only (no recurring Billing percentage), proceeds after `.029×199+.30` and that budget are **$172.929**, a gross contribution margin of **86.9% in the sale period**. Recognized evenly over an illustrative 24-month planning window, contribution is $7.205/account/month; at N=5/20/100/1000 amortized surplus after $20 overhead is **$16.03/$124.11/$700.54/$7,185.38**, with three purchases needed to cover $480 of two-year overhead. This is cohort arithmetic, not MRR or a chosen accounting recognition rule.

Upfront cohort surplus after all 24 months' fixed overhead is **$384.64/$2,978.58/$16,812.90/$172,449.00** at those N. In month 25, absent new sales, there is no renewal revenue; ongoing operations need sponsorship/other revenue. Do not interpret a one-off cohort as a sustainable subscription business. If it included v=$12/month hosted service, $192.929 after processing would fund only **16.1 months**, before overhead/support increases. Unbounded lifetime hosting/storage is therefore refuted by ongoing costs. A one-off commercial license would also conflict with the stated free-core premise if it restricted normal self-hosting; any optional commercial artifact/license requires separate product/legal scope.

## 5. Existing guards versus billing controls still needed

Existing defenses reduce risk, but they do not establish a monthly customer cost ceiling:

| Existing code / behavior | Present bound | Pricing implication / gap |
|---|---|---|
| [hub config](../../internal/hub/hub.go), [loop](../../internal/hub/loop.go) | Default depth 32, 24-hour loop TTL, thread budget 500; explicit fresh/operator expiry ≤7 days; sender chain last 32 | Operators reset depth/TTL; new threads, parallel chains and fan-out still cost money; thread budget can be unlimited. Need tenant-wide monthly budgets independent of loop limits. |
| [peer rate limits](../../internal/hub/ratelimit.go) | Defaults 60 sends/min, 20 threads/hour per peer; configurable | 150 peers ×60/min permits 388.8M sends/30 days if other constraints permit; these are abuse limits, not a 1M/month plan. New identities must not replenish a tenant allowance. |
| [blob store](../../internal/blob/store.go) | 25 MiB per blob, 500 MiB/uploader/UTC day; deduped upload still charged; 24-hour orphan grace; referenced files protected | 150 uploaders can approach 2.2 TiB/month. No tenant download quota or paid-storage pack enforcement. Auto-spill uploads charge uploader `hub`, so attribute spill usage back to its customer rather than billing a global bucket. |
| [push](../../internal/hub/deliveries.go), [notify](../../internal/hub/notify.go), [MCP Events](../../internal/events/worker.go) | Eight ordinary push attempts, backoff ≤1h, capped push/response bodies; notify has three retries and 32 in flight; Events has its own durable outbox/state machine | Replaying dead letters renews ordinary retry budget. Count all actual service attempts/bytes for internal costs; do not charge customers repeatedly for the same logical send by accident. Bound route/subscription creation and replay. |
| [provisioning](../../internal/hub/provision.go) | Principal peer creation default off, allowed kinds, max active peers and hourly creates (default 60) | Useful foundation; operator-created identities and multiple principals still need aggregate account limits. |
| [public middleware](../../internal/httpapi/middleware.go), [Fly configuration](../../fly.toml), [protocol §20](../PROTOCOL.md) | Anonymous bucket 30, refill 30/min; valid credentials bypass this limiter; 300 machine connections; deadlines | Hosted paying/free authenticated accounts need separate read/poll/connection budgets. Anonymous defenses cannot be presented as customer usage controls. |

Apply invoice-safe limits atomically with usage reservation; concurrent uploads/sends must not overshoot spend caps by an unlimited amount. Global limits protect the platform, tenant limits protect cost allocation, and peer limits protect each workflow. Rejecting/pausing a quota-bound operation needs clear replay/idempotency semantics so retry storms do not make the incident worse.

## 6. Margins at 5, 20, 100 and 1,000 paying accounts

N means **paying customer accounts/hubs**, not active agents. For B it means N five-human organizations (25/100/500/5,000 paid humans); human count and organizational support density are not interchangeable. All accounts in a row have that row's profile, one monthly payment, and linear capacity/support budgets. No free customer fleet or growth staffing step is hidden inside v.

`GP=N×(.964P−.30−v)`; true modeled **gross contribution margin GM=GP/(NP)** is constant across N because delivery/support are linear. The four GM figures below explicitly correspond to **5 / 20 / 100 / 1,000** accounts. Fixed platform overhead is deducted separately in the following table; this avoids calling profit after fixed costs “gross margin.” Founder salaries/acquisition/development/tax are excluded throughout.

| Candidate/profile | P | v | Contribution/account c | GM at 5 / 20 / 100 / 1,000 | Break-even accounts at F=$20 |
|---|---:|---:|---:|---|---:|
| A Solo | $19 | $12 | $6.016 | 31.7% / 31.7% / 31.7% / 31.7% | 4 |
| A Team | $49 | $24 | $22.936 | 46.8% / 46.8% / 46.8% / 46.8% | 1 |
| A Heavy | $199 | $100 | $91.536 | 46.0% / 46.0% / 46.0% / 46.0% | 1 |
| B Five-human team | $45 | $24 | $19.080 | 42.4% / 42.4% / 42.4% / 42.4% | 2 |
| C Solo-shaped, 40k sends | $25 | $12 | $11.800 | 47.2% / 47.2% / 47.2% / 47.2% | 2 |
| D 50 GB incremental | $5 | $1.50 | $3.020 | 60.4% / 60.4% / 60.4% / 60.4% | 7 |
| D 250 GB incremental | $15 | $6 | $8.160 | 54.4% / 54.4% / 54.4% / 54.4% | 3 |
| E Network/BYOH, future | $9 | $2 | $6.376 | 70.8% / 70.8% / 70.8% / 70.8% | 4 |
| F Recurring supporter | $5 | $0.50 | $4.020 | 80.4% / 80.4% / 80.4% / 80.4% | 5 |

| Estimated monthly surplus after F=$20 | 5 accounts | 20 accounts | 100 accounts | 1,000 accounts |
|---|---:|---:|---:|---:|
| A Solo | $10.08 | $100.32 | $581.60 | $5,996.00 |
| A Team | $94.68 | $438.72 | $2,273.60 | $22,916.00 |
| A Heavy | $437.68 | $1,810.72 | $9,133.60 | $91,516.00 |
| B Five-human team | $75.40 | $361.60 | $1,888.00 | $19,060.00 |
| C Solo-shaped | $39.00 | $216.00 | $1,160.00 | $11,780.00 |
| D 50 GB incremental | −$4.90 | $40.40 | $282.00 | $3,000.00 |
| D 250 GB incremental | $20.80 | $143.20 | $796.00 | $8,140.00 |
| E Network/BYOH | $11.88 | $107.52 | $617.60 | $6,356.00 |
| F Recurring supporter | $0.10 | $60.40 | $382.00 | $4,000.00 |

Thus **A, B and C can cover the lean floor at 5–20 accounts** in their stated profiles; D's 50 GB pack cannot stand alone at five. E can in a future network, not today. F at five has effectively no cushion. The current local-volume Solo budget v=$13 still gives **$5.08/$80.32** at 5/20; current Team v=$32 at $49 gives **$54.68/$278.72**. Current heavy v=$225 at $199 has **negative $33.464 contribution/account**, no finite break-even, and must not inherit the remote-blob margin claim.

**Fixed founder operations sensitivity:** add $120/month for two hours and subtract $120 from every surplus cell. Then A Solo loses **$109.92/$19.68** at 5/20 (break-even 24 accounts); A Team makes **−$25.32/$318.72** (7 accounts); B makes **−$44.60/$241.60** (8 accounts); C makes **−$81/$96** (12 accounts). A Heavy still makes **$317.68/$1,690.72** if its future cost assumptions hold. E breaks even at 22 accounts; D 50/250 GB at 47/18; F at 35. Few-user profitability depends on the operating promise and labor floor, not cheap storage alone.

Support is another decisive uncertainty: extra ten minutes/account/month costs $10 at the assumed rate. That makes A Solo contribution negative. A $3,000 fixed monthly salary requires about **502 Solo accounts**, **132 Team accounts**, or **159 five-human B accounts**, including the $20 overhead. These are arithmetic thresholds, not adoption forecasts. At 1,000 accounts, taxes, staffing, compliance, incidents, payment mix, acquisition and any HA premium need a new operating plan; the linear table is a scenario, not a staffing or capacity promise.

## 7. Comparable developer infrastructure offers

Public list-price snapshots, **USD/month unless stated**, accessed **2026-11-05**. Different payload accounting, retained history, fan-out, support and billing periods mean these are positioning references, not equivalent vecnet workloads. No competitor is required as an upstream dependency.

| Comparable and source | Observed list-price example | Implication for a free-self-host / paid-convenience service |
|---|---|---|
| [ngrok](https://ngrok.com/pricing) | Pay-as-you-go **$20/month + additional usage**, includes $20 usage credit, 5 GB transfer and 100k HTTP/S requests; three team members included | Reachability and managed public endpoints can justify a minimum; transfer and humans need explicit scope. |
| [Tailscale](https://tailscale.com/pricing) | Personal **$0 up to six users**; Standard **$8/user/month**, Premium **$18** | Human/network pricing is familiar, but machine/resource allowances are distinct. Do not reuse older $6/$18 or three-user snapshots. |
| [Cloudflare Zero Trust](https://www.cloudflare.com/plans/) | Free up to **50 users**, pay-as-you-go **$7/user/month** | Free connectivity sets a low price anchor; vecnet's paid value must extend to managed agent messaging, delivery, history and recovery. |
| [Pusher Channels](https://pusher.com/channels/pricing/) | Free **200k messages/day, 100 concurrent connections**; Startup **$49/month**, 1M/day, 500 connections | Separate volume and concurrent-connection limits; a persistent connection is not a free unlimited message channel. |
| [Ably](https://ably.com/pricing) | Free **6M messages/month, 200 concurrent connections**; Standard **$29/month + usage**; messages **$2.50/M**, channels/connections **$1/M minutes** each | Idle connections and fan-out accounting matter; raw message transport can be much cheaper than a supported managed hub. |
| [Svix](https://www.svix.com/pricing/) | Free **50k messages/month, seven-day payload retention**; Basic **from $20/month**, 30-day retention and 50k free messages/month | Paid retention/reliability is a useful anchor; a base fee is not an unlimited webhook allowance. |
| [Hookdeck](https://hookdeck.com/pricing) | Developer **$0, 10k events, three-day retention**; Team **from $39/month**, seven-day retention, unlimited users plus usage; published additional-event starting rate **$1/100k** | User-free collaboration with event/retention metering is established. |
| [Supabase](https://supabase.com/pricing) | Pro **$25/month**, includes $10 compute credit, 8 GB disk/project, 250 GB bandwidth, daily backups with seven-day retention | A useful managed-project comparison; additional projects/compute and storage are separately priced. |
| [Fly](https://docs.fly.io/about/pricing) | Small 512 MB Ashburn machine **$3.69/month** before storage/traffic | DIY cost anchor, not managed customer price: support, backups and unused dedicated capacity have to be covered. |
| [Upstash Redis](https://upstash.com/pricing/redis) | Pay-as-you-go **$0.20/100k commands**; fixed plan displayed **$10/month** | Predefined billable operations and predictable caps matter; Redis commands are not vecnet logical sends. |
| [Langfuse](https://langfuse.com/pricing) | AI observability Hobby free **50k units/month**, Core **$29/month**, with usage pricing | Relevant agent-infra/OSS analogue: free self-host and a paid managed workflow can coexist; telemetry units are not message units. |

Cloudflare Tunnel is a reachability alternative to operating a public origin ([Tunnel documentation](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/), accessed 2026-11-05); the Zero Trust plan above is not a quote for unlimited vecnet storage or an automatically compatible ChatGPT OAuth service. Similarly, Tailscale private connectivity alone does not supply a publicly reachable custom MCP issuer. Hosted vecnet competes with the user's **whole operational workflow**, not just these products' per-GB prices.

## 8. Candidate shortlist, open questions and billing instrumentation

No model is selected. The shortlist preserves different hypotheses worth testing:

1. **A: bounded managed hubs**, with optional D storage packs. Most directly follows the current binary/DB/hub boundary and can cover a lean fixed floor with five paid accounts. The $19 example has little support cushion; a $25–29 Solo experiment would leave more room. An initial offer needs cumulative storage caps, verified object-store/expiry activation and honest backup/recovery scope; implemented opt-ins do not establish deployed capacity or the modeled 30-day retention policy.
2. **B: human seats with a hub minimum**, if five-human teams value managed coordination enough to sustain $45–49 bills. It avoids per-agent friction while keeping aggregate usage controls; validate human identity semantics and support demand before treating seats as the billing record.
3. **E: BYOH plus optional network services**, as a later network product. It aligns strongly with common self-hosting, but requires federation/relay/OAuth design and actual service usage before the low v assumption is credible. D and sponsorship can complement it without making core operation dependent on payment.

C offers better alignment for automation outliers, but metering/dispute UX and a free fleet add work; it can be tested as paid capacity overages before becoming the principal price label. Lifetime hosting fails the recurring-cost test. This is an informative shortlist, not authorization to implement or change licensing.

### Open questions for Max

- Is the first paid buyer a solo developer, a five-human team, or an automation operator? What reliability/support promise would they pay to avoid operating themselves?
- Is an initial hub dedicated per account, pooled as separate processes, or a redesigned shared tenant? What data residency and recovery-time/recovery-point requirements apply?
- Must history be permanent? May ordinary history expire after 30 days? Which message/blob relationships survive export, cancellation and seven-day transfer expiry?
- Does “50 GB permanent” mean primary occupied bytes, allocated capacity, or primary plus version/backups? Is one independent backup mandatory, and how long must deletion persist in backups?
- Are download bytes charged per recipient? May clients receive short-lived object-store URLs, and what revocation guarantees must those URLs preserve?
- How many streams, reads, callbacks, recipients, routes and burst sends does each real workflow use? Does a heavy account mean 100 active peers or thousands of short-lived identities?
- Are price points advertised in AUD or USD, which merchant entity/payment route applies, and should prices include GST? What monthly support/operations labor must revenue actually cover?
- Is a limited hosted sandbox valuable enough to fund, or should free acquisition focus on self-hosting plus a demo? What free-fleet budget and conversion rate are acceptable?
- In a future network, what remains interoperable without payment, and what optional managed network features create value beyond cheap tunnels/private VPNs?

### Metering points and implementation gaps

| Billing capability | Actual seams to instrument | Required accounting rule |
|---|---|---|
| Accepted messages and deliveries, A/C | `Hub.Send`, `sendTx`/`fanOut` in [send.go](../../internal/hub/send.go); reactions, source ingress and hub-authored messages have separate paths | Transactional outbox/ledger keyed by billing account + stable message ID; count once after successful commit, count unique resolved recipients separately. Do not bill rejected sends or idempotent retries. State how ingress, reactions and hub notices count. |
| Human membership / active agents, A/B | [provision.go](../../internal/hub/provision.go), [directory.go](../../internal/hub/directory.go), [presence.go](../../internal/hub/presence.go), [OAuth](../../internal/oauth/server.go), [MCP auth](../../internal/mcpserver/auth.go) | Introduce stable billing account and human membership; peer kind/principal is not proof of a human. Define monthly activity from authenticated service usage, not just stale presence or peer creation. |
| Occupied/permanent/ephemeral storage, A/C/D | `Store.Put`, `Pin`, `GC`, `Purge` in [blob store](../../internal/blob/store.go); [hub blobs](../../internal/hub/blobs.go), [spill](../../internal/hub/spill.go), [avatars](../../internal/hub/avatar.go) | Reserve bytes before accepting, integrate GB-hours, account for shared references and actual deletion. Attribute auto-spill to the originating account. Decide logical versus physical dedupe billing and whether avatars/versions/backups count. |
| Download/upload/relay transfer, C/D/E | [putBlob/getBlob](../../internal/httpapi/blobs.go), future direct object credentials/CDN logs, future federation relay | Count actual successfully written bytes, including ranges/retries, at trusted serving boundaries; reconcile object provider logs. Decide whether failed transfer bytes are billable. Do not count a URL issuance as a completed download. |
| Push and retry COGS, all hosted | [push worker](../../internal/push/worker.go), [Events worker](../../internal/events/worker.go), [notify](../../internal/hub/notify.go), [event streams](../../internal/httpapi/events.go), [MCP streams](../../internal/mcpserver/sse.go) | Internal counters for attempts, bytes, latency, subscription/route count and connection-seconds; logical billing and internal retry costs remain separate. Include notification fan-out and catch-up. |
| DB/backup/compute capacity | [store](../../internal/store/store.go), [migrations](../../internal/store/migrations/), [tickers](../../internal/hub/tickers.go), backup runner/host metrics to be added | DB/WAL/blob used bytes, provisioned GB, WAL/upload amplification, replication lag, restores, CPU/RAM, writer contention and streams. Per-account attribution on pooled hosts; capacity step alarms. |
| Invoices/spend controls, all paid | New billing-account mapping, entitlements, usage ledger and reconciliation worker | Monotonic, deduplicated usage records with a defined UTC billing period, integer bytes/currency minor units, credit/refund semantics and auditability. Hard monthly caps must survive restart/concurrent requests; verify usage against payment/provider invoices. No message content or credentials in billing telemetry. |
| Network plan E | No production federation/directory/relay billing seam yet; future boundary plus current OAuth issuer/audience checks in [oauth](../../internal/oauth/) and [MCP](../../internal/mcpserver/) | Charge for managed boundary service; never trust BYOH self-reported usage as the only meter. Bind endpoint/hub/account authority and enforce cross-hub hop/byte budgets. |

Minimum evidence before setting commercial quotas: measure distributions of envelope/body bytes and fan-out; sends and lifecycle writes; active streams and empty polls; distinct uploaded/retained/downloaded bytes; object counts/sizes; WAL/backup traffic; retry rates and support minutes. Record percentiles and peak concurrent demand for each workload, not just averages. Re-run this worksheet against the actual Sydney quote, payment route, retention policy and observed support burden. No instrumentation or code change is part of this analysis assignment.

--- SUMMARY ---

- Free self-hosting stays central. Hosted value is convenience, public HTTPS/MCP/OAuth operation, reliable delivery, restoreable backups and managed storage; a future network can add optional reachability and discovery.
- The original synthetic schema probe and logical allowances remain labeled as such; the 2026-11-07 modernc pairs measure 30.1% to 67.1% live-page savings in selected normal cohorts, with snapshot/callback costs and increased read latency disclosed. Indefinite history and pinned bytes still accumulate.
- Small dedicated hubs have low infrastructure bills but support/idle capacity set the floor. Remote blobs/direct serving can change heavy-transfer economics; R2 is not integrated today and Fly proxy egress remains chargeable.
- Strict one-week transfer deletion is new work: existing blob TTL/token expiry does not delete pinned attachments. Permanent 50/250 GB packs need paid-retention and transfer policies.
- Under the estimated $20 fixed cash floor, bounded hosted Solo/Team, seat and metered examples cover costs at 5–20 customers. Two fixed founder hours raise the floor to $140 and remove several five-customer profit claims.
- A managed hub, human seats with a hub minimum, and future BYOH/network services form the discussion shortlist. Storage packs and sponsorship can complement them; unlimited lifetime hosting cannot pay recurring costs indefinitely.
- Prices are candidate scenarios, not decisions. Real usage, retention, support scope, tenant isolation, billing currency and provider quotes remain open; account-level metering and enforceable spend caps precede billing.
