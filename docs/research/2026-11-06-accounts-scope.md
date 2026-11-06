# Accounts, guests, and scope

_Researched 2026-11-06. Docs only. No hub was contacted, no token was minted, and no secret was read._

**Question.** What does vecnet already treat as an account, and how do comparable systems do a browser guest link, a scoped token, and Office 365 sign-in on a small Go server?

**Verdict.** Vecnet has one operator credential, three credential kinds, and per-peer send scopes. A send scope does not hide the directory, and nothing in the hub is a relying party for a human identity provider. Guest access, team-scoped accounts, and Office 365 sign-in are new. The choices that follow are in [`docs/plans/2026-11-06-accounts-scope.md`](../plans/2026-11-06-accounts-scope.md), each as an option for the chief of staff. This note does not pick them.

Labels: **SOURCE** = read in this checkout at `2574ec4ce7fe0cade2ea4217693e6695fb230191`. **DOCUMENTED** = a public page fetched 2026-11-06, not exercised.

---

## Source key

| Tag | Source | Trust |
|---|---|---|
| [GLOSS] | `GLOSSARY.md` | Canonical terms |
| [SPEC] | `SPEC.md` | Product scope |
| [PROTO] | `docs/PROTOCOL.md` | Normative behaviour |
| [ENV] | `docs/protocol/ENVELOPE.md`, `docs/protocol/envelope.schema.json` | Wire shape |
| [ADR] | `docs/adr/0001-tech-stack.md` | Go, one binary, small host |
| [HUB] | `internal/hub/hub.go`, `internal/hub/admin.go`, `internal/hub/directory.go`, `internal/hub/groups.go`, `internal/hub/send.go`, `internal/hub/threads.go`, `internal/hub/provision.go` | Enrolment, teams, operator powers |
| [AUTH] | `internal/auth/auth.go` | Credentials and send scopes |
| [HTTP] | `internal/httpapi/middleware.go`, `internal/httpapi/admin.go`, `internal/httpapi/peers.go` | Who may call what |
| [OAUTH] | `internal/oauth/server.go`, `internal/oauth/consent.go`, `internal/store/migrations/0006_oauth.sql` | The hub as an OAuth server for agents |
| [ID] | `internal/ids/ids.go`, `internal/hubid/hubid.go`, `internal/blob/transfers.go` | Local ids and the one cross-hub namespace that exists |
| [UI] | `internal/ui/static/app.js`, `internal/ui/static/invite-model.js` | How a human signs in and adds an agent |
| [ONB] | `docs/plans/2026-11-06-agent-onboarding-kinds.md` | Plan merged; package not in this tree |
| [ADM] | `docs/plans/2026-11-05-admin-parity.md` | Plan only; no grant table in Go |
| [SLACK] | <https://slack.com/help/articles/202518103-Understand-guest-roles-in-Slack> | Slack guest roles |
| [NOTION] | <https://www.notion.com/help/sharing-and-permissions> | Page guests and link access |
| [MM] | <https://docs.mattermost.com/administration-guide/onboard/guest-accounts.html> | Self-hosted guest accounts |
| [GH] | <https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens> | Fine-grained personal access tokens |
| [RFC6750] | <https://www.rfc-editor.org/rfc/rfc6750> §2.3, §5.3 | Bearer tokens in URLs |
| [ENTRA] | <https://learn.microsoft.com/en-us/entra/identity-platform/id-token-claims-reference> | `sub`, `oid`, `tid`, `email` |
| [ENTRA-SSO] | <https://learn.microsoft.com/en-us/entra/identity/enterprise-apps/saml-vs-oidc-decision-guide> | OIDC versus SAML for new apps |
| [GOOIDC] | <https://github.com/coreos/go-oidc/blob/v3/README.md> | Go OpenID Connect relying party |
| [SAMLSP] | <https://github.com/crewjam/saml/blob/main/samlsp/middleware.go> | Go SAML service-provider middleware |
| [DEX] | <https://github.com/dexidp/dex/blob/master/README.md> | Separate OIDC portal, including its SAML connector warning |

---

## Verified facts

| Fact | Status | Source |
|---|---|---|
| One hub is one network. A client may know several networks, each through its own profile | SOURCE | [GLOSS] lines 6–9 |
| The operator is the human who enrols peers, and is also a peer of kind `operator` and a principal. Vecnet is closed: no peer exists without enrolment | SOURCE | [GLOSS] lines 11–21; [SPEC] lines 9, 21 |
| v1 product scope excludes multi-operator tenancy, billing, and open registration. SPEC §16 item 4 lists peer id namespacing under the heading "Phase 2 behaviours", and the status note says phase 4 has not started. The map says v1 ids are flat and namespacing depends on enrolment | SOURCE | [SPEC] lines 92–94, 101, 108; `.scratch/vecnet/map.md` line 60 |
| The hub is one Go static binary. The accepted ADR assumes one operator and a small Fly machine (shared-cpu-1x, 512 MB). HTTP is the standard library, with no web framework | SOURCE | [ADR] lines 19–20, 28–29; `go.mod` lines 1–3 (`go 1.26.0`) |
| `principals` is `prn_id`, `name`, `created_at`. `peers` belongs to exactly one principal. `credentials.kind` is only `peer`, `principal`, or `operator` | SOURCE | `internal/store/migrations/0001_init.sql` lines 19–23, 25–44, 47–55 |
| An empty `scopes` table means allow all. A row is `(peer_id, mode allow\|deny, target_id)`. Teams are `team_id`, `name`, and `team_members(team_id, peer_id)`, with no account column | SOURCE | `0001_init.sql` lines 64–83 |
| A principal's provisioning limits default off: no `principal_limits` row means `max_peers` 0. `operator`, `source`, `session`, and `dot` are never provisionable | SOURCE | `internal/store/migrations/0014_principal_peers.sql` lines 4–10; [PROTO] §18.4; `internal/hub/provision.go` lines 21–23 |
| `PeerKinds` is a fixed map: `grokbot`, `dot`, `openclaw`, `pagchat-bot`, `session`, `operator`, `source`, `launcher`. `AddPeerWith` rejects any other kind with 422 `invalid_value` | SOURCE | `internal/hub/hub.go` lines 223–227, 337–340 |
| `EnsureOperator` creates one operator principal, one peer of kind `operator`, and one credential of kind `operator`. A later call returns that peer and an empty secret | SOURCE | `internal/hub/hub.go` lines 231–244, 258 |
| Kind `operator` is accepted only for a principal that already holds an operator credential. The comment says operator powers key on `peers.kind` | SOURCE | `internal/hub/hub.go` lines 366–376 |
| `peer add` with an empty principal id creates an implicit principal named like the peer. Audit actor for that insert is the string `operator` | SOURCE | `internal/hub/hub.go` lines 347–353, 407; [PROTO] §2.12 |
| A bearer resolves by SHA-256 of the secret. Kinds are `peer`, `principal`, and `operator`. The operator caller carries the operator principal and the first enabled operator-kind peer | SOURCE | `internal/auth/auth.go` lines 24–40, 69–70, 111–169 |
| Secrets are `vn_<kind>_` plus 32 random bytes, stored only as SHA-256. Enrolment tokens are the same shape with kind `enrol`, default life 24 h, maximum 7 d | SOURCE | `internal/auth/auth.go` lines 62–81, 172–186; [PROTO] §12 |
| Send scope: a deny row refuses; any allow row means allow-list mode; no rows means allow. `MaySend` is that check. Protocol text says scopes gate sends and group creation only | SOURCE | `internal/auth/auth.go` lines 377–436; `docs/PROTOCOL.md` line 846 |
| `/v1/admin/` requires `Caller.Kind == "operator"`. The admin handlers do not check the caller again. `CreateTeam` uses the same kind check | SOURCE | `internal/httpapi/middleware.go` lines 77–80, 127–129; `internal/httpapi/admin.go` lines 17–19; `internal/hub/groups.go` lines 346–360 |
| `GET /v1/peers` returns every enabled peer's public directory fields to any authenticated caller. The directory query has no caller argument. Entries omit credentials, scopes, and routes. Protocol text: every peer sees every peer | SOURCE | `internal/httpapi/peers.go` lines 92–100; `internal/hub/directory.go` lines 17–19, 68–105; `docs/PROTOCOL.md` line 739 |
| A sender of kind `operator` may post into a thread it has not joined, may add participants to a locked thread, and may reply to a message it cannot otherwise see. Depth reset for kind `operator` is normative | SOURCE | `internal/hub/send.go` lines 287–290, 355–357; `internal/hub/threads.go` lines 158–175; [PROTO] §16.1, §16.3 |
| `resolveSender` loads `peers.kind` for the peer id. An operator-kind peer therefore takes those paths even when the credential kind is `peer`, provided the peer's principal matches the caller | SOURCE | `internal/hub/send.go` lines 762–795 |
| The operator credential reads any thread in full. `GET /v1/peers/me` for an operator returns `kind` and no peer id | SOURCE | [PROTO] §2.8 (the threads-messages paragraph); `internal/httpapi/peers.go` lines 103–117 |
| `vecnet_whoami` returns `kind` of `peer`, `principal`, or `operator` only | SOURCE | `internal/mcpserver/tools.go` lines 952–963, 646–688 |
| The web UI is unauthenticated until the page sends a bearer. Sign-in stores the operator secret in `localStorage` under `vecnet.ui.secret` and sends it as `Authorization` | SOURCE | `internal/httpapi/middleware.go` lines 69–70; `internal/ui/static/app.js` lines 6–7, 29, 264–276 |
| Add-agent kinds in the page are hardcoded: `session`, `launcher`, `openclaw`, `pagchat-bot`, `grokbot`, `dot`. `operator` and `source` are excluded there | SOURCE | `internal/ui/static/invite-model.js` lines 9–12 |
| The hub is an OAuth 2.1 authorization server for `/mcp`: public clients, PKCE S256, scopes `vecnet`, `events:read`, `events:write`, access tokens 1 h, refresh tokens 30 d. It binds a `dot` or `grokbot` peer with a link code. It is not a client of another issuer | SOURCE | `internal/oauth/server.go` lines 1–8, 25–41; `internal/hub/admin.go` lines 149–154, 186–210; [PROTO] §4.5 |
| A link code is 12 characters from a 31-symbol alphabet, shown in groups of four, stored as SHA-256, valid 10 minutes, single use. Twenty refusals in 10 minutes burn every live code | SOURCE | [PROTO] §20.1; `internal/hub/admin.go` lines 144–147, 180–216 |
| The consent cookie is `vecnet_oauth_sub`, path `/oauth/`, sealed, 30 days. Its subject is `oauth:` + client id + random. Onboarding Task 6, not yet in this tree, is the planned change that a supplied link code beats this cookie | SOURCE | `internal/oauth/consent.go` lines 23–24, 138, 513–524; `internal/oauth/server.go` line 31; [ONB] Task 6 |
| Ids other than `blo_` are `<prefix>_` plus a ULID from a process-wide monotonic `crypto/rand` source. The string contains no hub id. `blo_` is 128 random bits. Protocol prefixes are `msg_`, `dly_`, `thr_`, `peer_`, `prn_`, `team_`, `grp_`, `job_`, `apr_`, `src_`, `ntc_` | SOURCE | `internal/ids/ids.go` lines 1–3, 20–32, 48–61; [PROTO] §1.3 |
| Credential ids are minted with prefix `cred`, which is not in that protocol table and not in `ids.known` | SOURCE | `internal/auth/auth.go` line 370; `internal/ids/ids.go` lines 38–41; [PROTO] §1.3 |
| The public hub identity is a separate Ed25519 key. The fingerprint is `SHA256:` plus the unpadded standard base64 of SHA-256 over the 32-byte public key | SOURCE | `internal/hub/identity.go` lines 50–67; `internal/hubid/hubid.go` lines 36–38, 57–64 |
| R2 object keys carry a per-hub namespace: 16 random bytes, lower hex, stored at `meta.r2_namespace`, matched by `^[0-9a-f]{32}$`. Peer ids are not namespaced that way | SOURCE | `internal/blob/transfers.go` lines 88–105; `internal/blob/r2meta.go` lines 22, 68–80 |
| `senderInfo.kind` in the envelope schema is an open string. The schema's `kind` enum is message kinds (`message`, `approval_request`, `approval_response`, `job_status`, `reaction`), not peer kinds | SOURCE | `docs/protocol/envelope.schema.json` lines 102–111, 563–571 |
| Principal-created peer HTTP shapes are frozen with pag-chat. A created peer has no secret of its own | SOURCE | [PROTO] §18.4 |
| Push route kinds are `http` and `grokbot` only. There is no mail route kind | SOURCE | `internal/store/migrations/0007_grokbot_routes.sql` line 11 |
| The highest migration file in this checkout is `0025_r2_blobs.sql`. `0015_reserved.sql` exists so a number that was reserved and not used could stay in the sequence. The onboarding plan says the DB build owns 0026 and that 0027 is only the number it expects if it needs a migration. Admin-parity's build-order note says the directory ends at 0020; that sentence is older than this checkout | SOURCE | `internal/store/migrations/`; `0015_reserved.sql` lines 1–4; [ONB] Global Constraints; `docs/plans/2026-11-05-build-order.md` lines 27–29 |
| `internal/onboarding/` is absent. No Go file mentions `admin:peers`. Both are plans, not code, at this HEAD | SOURCE | this checkout |
| `golang.org/x/oauth2` is an indirect module. `go.mod` does not require `coreos/go-oidc` or `crewjam/saml` | SOURCE | `go.mod` lines 5–17 and line 40 |

---

## 1. What the words mean in this tree

Three records already exist, and they are not accounts in the Office 365 sense.

| Word in the tree | What it is | What it is not |
|---|---|---|
| Principal (`prn_`) | The credential holder. Every peer has one. A principal credential may send `as` its peers and may hold one event stream for all of them | A login, a team membership, or a permission to administer the hub |
| Peer (`peer_`) | The address. Kind, display name, directory entry, send scopes, rate limits | A human account. Display name is not an address |
| Operator | One credential kind, and a peer kind whose kind string is what the send path treats as the human who may break loop and lock rules | A role that can be given to a second person. `EnsureOperator` returns the existing operator |

A team is an operator-managed recipient list (`team_members`). Addressing a team fans out to its members and still checks the sender's scopes per member ([PROTO] §7, §12). Membership is not an account scope: it causes delivery. An ad hoc group (`grp_`) is the peer-created version of the same idea.

The operator web client is the operator secret in the browser (`internal/ui/static/app.js` lines 6–7, 264–276). A guest who was handed that secret would be the operator.

## 2. Guest links elsewhere

Four products, fetched 2026-11-06, split "named person, narrow place" from "anyone with the link".

**Slack** ([SLACK]). Guest roles exist on paid plans. A single-channel guest can access one channel. A multi-channel guest can access the channels an admin names. Owners and admins set an automatic deactivation time, a custom date, or indefinite access. In the channels they can access, a guest sees members, apps, and history, and can direct-message people in those channels. They do not install apps. Slack invites a person into the workspace; the help page describes members and admins managing channels, not an anonymous URL.

**Notion** ([NOTION]). A guest is invited to a page by email and needs a Notion account. Guests are not workspace members: the invitation covers that page and its sub-pages. Separately, general access can be "only people invited", "everyone at the workspace", or "anyone on the web with the link". The link form can be given an expiry. Those are two mechanisms: a named invite, and a capability URL.

**Mattermost** ([MM]), the self-hosted comparison. A system admin enables guest access. By default only system admins invite guests. A guest cannot discover public channels, join open teams, or direct-message people who do not share a channel. Authentication can be the email invitation, AD/LDAP, SAML, or, from Mattermost v11.3, a magic link emailed to the guest. The first magic link expires in 48 hours. A later sign-in emails a new link that expires in 5 minutes. Mattermost's own FAQ says an expiration date on the guest account itself is not available. The server has to be able to send mail.

**GitHub fine-grained personal access tokens** ([GH]) are the scoped-token comparison, not a guest UI. Each token is limited to one user or organization, then to selected repositories, then to named permissions, with an expiry. GitHub recommends them over classic tokens, which carry coarse scopes.

**Vecnet already has capability secrets, and none of them is a guest.** An enrolment token (`vn_enrol_…`) is single-use, hashed, and exchanged for a long-lived peer bearer ([AUTH] `IssueEnrolToken`; [PROTO] §12). A link code is short, hashed, and bound to one existing `dot` or `grokbot` peer ([PROTO] §20.1). A blob read token is scoped to a recipient and a hash for seven days ([PROTO] §11). The consent cookie is sealed and limited to `/oauth/` ([OAUTH]). The operator UI does the opposite with the long-lived operator secret: it keeps it in `localStorage`.

[RFC6750] §2.3 allows an `access_token` query parameter. §5.3 says bearer tokens SHOULD NOT be passed in page URLs, because history, logs, and `Referer` keep them, and SHOULD be passed in a header or body. It also says tokens issued to a browser SHOULD be short-lived (an hour or less is the example) and audience-restricted, and that a bearer stored in a cookie needs a cross-site request defense. `docs/PROTOCOL.md` line 76 says a long-lived secret MUST NOT appear in a URL.

The hub cannot email a magic link. Route kinds are `http` and `grokbot` (`0007_grokbot_routes.sql` line 11). A link Max sends to a friend is copied by Max, the same way an invite block is copied today (`internal/ui/static/app.js` opens the invite dialog; [PROTO] §12 describes the paste block).

## 3. Scoped tokens, and the gap in send scopes

Vecnet send scopes are an allow list and a deny list of peer, team, and group ids on one peer ([AUTH] `Scopes`; [PROTO] §12). Empty means allow all (`0001_init.sql` line 64). They are checked on send and on group membership. They are not checked on `GET /v1/peers` (`internal/httpapi/peers.go` lines 92–100). A peer whose allow list names one agent can still read every other peer's name, kind, presence, reachability, and team ids ([PROTO] §8). Thread history is participants plus the operator ([PROTO] §2.8, §16.3). So "may send only to Alice" and "can see only Alice" are different cuts, and only the first exists.

GitHub's fine-grained token is the other shape: the token names a resource owner, a repository set, and a permission set ([GH]). Slack's single-channel guest is the same idea on a person instead of a token: the channel set is the visibility set ([SLACK]). Mattermost states the discovery rule outright: guests cannot discover channels they were not given ([MM]).

Admin-parity ([ADM] Decisions and security contract 2) designs a second store: `admin:*` capabilities, `own_principal` or `hub` boundaries, ceilings, and an operator bypass. That plan is not implemented (no `admin:peers` string in Go at this HEAD). Its contract still treats the operator credential as the root that does not need a grant. A guest, a colleague, and an instance admin would then be grants, while the operator remained a kind. That is two models.

## 4. Office 365 sign-in on a small Go server

Microsoft Entra ID is the Office 365 directory. It issues both OpenID Connect ID tokens and SAML 2.0 assertions. The decision guide ([ENTRA-SSO]), fetched 2026-11-06, says Entra fully supports both, and says to default to OpenID Connect for a new app. SAML is the choice when a customer, a legacy provider, or a procurement rule requires SAML.

Claims that matter, from the ID token reference ([ENTRA]):

| Claim | Stable? | Use |
|---|---|---|
| `oid` | Immutable object id. The same user receives the same `oid` in two apps in one tenant. A different tenant means a different `oid` | Together with `tid`, the key when more than one app must mean the same person |
| `tid` | Tenant id | Which directory the sign-in was for |
| `sub` | Immutable, and pairwise: two client ids get two different values for the same person | Key for one app registration. Not a cross-app id |
| `email`, `preferred_username`, `name` | Mutable. The page says not to authorize on them | Display |

The same page says a guest homed in another tenant should be treated as a new user in the tenant they are signing in to. Privileges from the home tenant do not carry.

**Relying party, not a second authorization server.** This hub already is an authorization server, for agents (`internal/oauth/server.go` lines 1–8). Operator login is the opposite role: the hub is the client, Entra is the issuer. Replacing `internal/oauth` with a general OAuth framework would rewrite the MCP flow [PROTO] §4.5. The login path is a new client of Entra.

**`github.com/coreos/go-oidc/v3/oidc`** ([GOOIDC]). The README describes a relying-party client: discover the issuer, build an `oauth2.Config` from `provider.Endpoint()`, and verify the ID token's signature, issuer, audience, and expiry. Nonce checks stay with the caller. It lists Microsoft Entra ID as a provider. It sits on `golang.org/x/oauth2`, which this module already imports indirectly (`go.mod` line 40). It does not issue tokens and it does not store users. The v3 line's tag `v3.15.0` records the change that verifies the signature before processing claims (CVE-2025-27144). This note does not pin a release; a build would pin the current v3 module at integration.

**`github.com/crewjam/saml/samlsp`** ([SAMLSP]). The middleware is a SAML service provider. It serves metadata and an assertion-consumer endpoint, tracks the request, and on success issues its own session JWT in a cookie, signed with the service provider's key. It is a library inside the process, not an identity provider. It is the XML stack, which go-oidc is not. It is the piece to add only when a tenant will not register an OIDC application.

**Dex** ([DEX]). Dex is a separate identity service. Apps speak OpenID Connect to dex, and dex speaks LDAP, SAML, or another provider upstream. That is a second process, with its own issuer, beside the single hub binary ([ADR] lines 28–29). The README's connector table marks the SAML 2.0 connector "Unmaintained and likely vulnerable to auth bypasses" and points at discussion 1884. Putting dex in front of the hub to obtain SAML would depend on that connector.

A full identity server (the usual Java or container deployment of that kind) is the same shape as dex: another service to patch, back up, and fit on the 512 MB host the ADR assumes. This checkout did not measure one. The library path above does not add a service.

## 5. Ids across hubs

A `peer_` or `prn_` minted here is a ULID with a type prefix (`internal/ids/ids.go` lines 48–61). Two hub processes do not share the monotonic entropy source, and the id string does not contain the hub fingerprint. The fingerprint exists, separately, as the hash of the Ed25519 identity key (`internal/hubid/hubid.go` lines 57–64). R2 already isolates objects with a random per-hub namespace (`internal/blob/transfers.go` lines 88–105) and does not do that to peer ids.

A bearer does not travel: another hub looks up the SHA-256 and misses (`internal/auth/auth.go` lines 111–126). The collision that matters is a copied or merged id, or an Entra `sub` treated as global. [ENTRA] says `sub` changes when the client id changes, and `oid` changes when the tenant changes. The stable Office 365 key for several hubs that do not share one app registration is `iss` + `tid` + `oid`, stored beside the local `prn_`, not inside it.

[PROTO] §1.3 and §18.4 freeze the prefixed-id wire for a live consumer. [SPEC] lines 101–108 leave "peer id namespacing" unimplemented and describe it as later work, not as an Office 365 subject.

## 6. What would be a protocol change

Adding a string to `PeerKinds` (`internal/hub/hub.go` lines 223–227) is a protocol change: [PROTO] §8 lists the directory kinds, and `AddPeerWith` rejects anything outside the map. `senderInfo.kind` would carry the new string without an envelope enum edit, because that field is an open string (`envelope.schema.json` lines 563–571).

Hiding directory entries from a guest or a team-scoped account is a protocol change even with no new kind. [PROTO] §8 and `GET /v1/peers` (`internal/httpapi/peers.go` lines 92–100) say every authenticated caller sees every enabled peer.

Moving loop reset, locked-thread join, any-thread reply, and full history off `peers.kind == "operator"` is a protocol change to [PROTO] §16.1, §16.3, §2.8, and the operator-view rules. Today those paths read the peer's kind (`internal/hub/send.go` lines 287, 355, 788).

`vecnet_whoami`'s `kind` enum is `peer` | `principal` | `operator` (`internal/mcpserver/tools.go` lines 956–958). A fourth credential kind is a tool-schema change.

The onboarding plan's registry, HTTP routes, consent cookie, MCP tools, and invite page are the files a guest invite would share. That plan's tasks are not implemented in this tree (`internal/onboarding/` is absent). The overlap list is in the plan, so a build does not edit those files while onboarding still owns them.

Migrations in this checkout stop at `0025_r2_blobs.sql`. The onboarding plan has already spoken for 0026 as the DB build's number. This work takes the next free number when it integrates, and does not reserve one. `0015_reserved.sql` is the precedent for a reserved number that had to ship empty.
