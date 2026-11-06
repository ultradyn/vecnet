# Accounts and scope

> **For agentic workers:** This document is a design. It does not authorize implementation, a migration, or a hub change by itself. CoS recorded D1–D8 on 2026-11-06 in "CoS's record", including amendments 1–6 in the tasks below. Later rulings the same day amend that record: Q1 (`msg_01M6QZHT65EJH8195GG0NJYC5T`), the invite flow (`msg_01M6QZNG1SWQHXFQTS07PMZ76Z`), and Q5 (`msg_01M6R1CP`). Max's answers 1–3 and the invite-flow note are pasted verbatim under "Max's answers". Question 4 is skipped. Question 5 is the Q5 ruling, written into D3, amendment 1, Task 3, and Task 6a. Ask questions 6–10 one at a time, in order, and paste each answer verbatim. If a later answer conflicts with a ruling, flag to CoS before editing tasks. Once these docs merge, Tasks 1–3 wait for the DB build to land (`internal/store/migrations`). Task 4 (browser guest) stays deferred until Max asks for a human-only browser guest. Tasks 5, 5b, 6, 8, and 7 follow the dependency map. Task 6a (Access step in Add agent and Add guest) follows Tasks 1–3 and onboarding Task 9. It does not block onboarding Task 9: that task ships without the step, because grants do not exist until accounts Tasks 1–3. Task 6 is specified in full and does not merge until CoS confirms the MCP enrol-material exception with Max. Nothing is built until the Max answers that block that task are in. The backlog section is queued and is not a build task. Steps use checkbox (`- [ ]`) syntax. A `---` ends every task.

**Goal:** One permission model for the operator, instance admins, colleagues, peers, and a guest, so Max can hand a friend's agent a short prompt that enrols as kind `guest` against one agent or one thread, send and receive files on the threads that guest can see, scope an account to a team or to the whole hub, and let operators and colleagues sign in with Office 365.

**Architecture:** Recorded by CoS on 2026-11-06, and amended the same day by the Q1, invite-flow, and Q5 rulings. A principal stays the account, a peer stays the address, and a grant is the only authority. The scope wire stays frozen as a cache of those grants. Q5 (`msg_01M6R1CP`) names the default Global: hub `send` and hub `directory`, which is today's behaviour and the default for agents. Team-scoped is `send` and `directory` on a team resource, expanded at check time, and that account sees no one outside the team. Operators and other special accounts are global. The Add agent and Add guest Access step that chooses Global or a team is Task 6a. A guest is the friend's agent that pasted the prompt: a principal plus one peer of kind `guest` (a protocol change), created by the same invite registry as agents (`guest_link`, after onboarding Tasks 1, 4, 7, and 9). The browser cookie session and guest web shell are deferred Task 4. Office 365 is a relying-party login for colleagues and admins, using `github.com/coreos/go-oidc/v3/oidc`, with SAML only if Max's question 9 says SAML is the only door. A guest does not use that login. Local ids stay prefixed ULIDs; the external subject is stored on the principal, keyed by the hub fingerprint plus `iss`/`tid`/`oid`. One bootstrap peer keeps kind `operator`. Existing operator-kind peers keep that kind and receive the bootstrap grant set. Everyone else holds grants.

**Tech Stack:** The hub's existing Go module (`go 1.26.0` in `go.mod`), SQLite, standard-library HTTP, the existing seal helper, and one new direct dependency for OpenID Connect verification. No second identity server. Builds through `just bb` per `docs/BUILDBOX.md`.

**Spec:** Chief of staff constraints for this design (2026-11-06): absorb admin-parity into one model; cover principals, accounts, peers, guests, team versus global, multi-operator scopes, agent-as-guest, and OIDC/SAML mapped onto principals; keep the hub light; ids safe across hubs; architecture stays an explicit choice; UX questions go to Max one at a time. CoS recorded D1–D8 on 2026-11-06, then the Q1 ruling (`msg_01M6QZHT65EJH8195GG0NJYC5T`), the invite-flow ruling (`msg_01M6QZNG1SWQHXFQTS07PMZ76Z`), and the Q5 ruling (`msg_01M6R1CP`). Facts: [`docs/research/2026-11-06-accounts-scope.md`](../research/2026-11-06-accounts-scope.md).

**Starts from:** `master` `2574ec4ce7fe0cade2ea4217693e6695fb230191`. `internal/onboarding/` is absent. No Go file contains `admin:peers`. Inventory is this checkout, not a live hub.

## Global Constraints

- Docs for this assignment are the research note and this plan. Implementation tasks stay unchecked.
- One authority. Do not ship [admin-parity](2026-11-05-admin-parity.md) grants as a second system beside send scopes and the operator credential. CoS recorded D2 C on 2026-11-06. Admin-parity is amended to that ruling, or it is not built.
- `PeerKinds` in `internal/hub/hub.go` is a fixed set. Adding `guest` is a protocol change to `docs/PROTOCOL.md` §8 and to every caller that lists kinds. CoS recorded D1 A on 2026-11-06, so kind `guest` is in scope.
- A caller-scoped directory is a protocol change to §8 and to `GET /v1/peers` even when the kind set stays as it is. "Send scope names one peer" does not do this today.
- Operator loop, lock, history, and reply powers key off `peers.kind` today. Leaving them there means any peer of kind `operator` has them. Recorded D2 C moves them onto grants. Amendment 4 copies the bootstrap grant set onto existing operator-kind peers before that check changes, and lists them so Max can revoke. Powers are not stripped silently.
- Ids on the wire stay the prefixed ULIDs in PROTOCOL §1.3. Pag-chat's principal-created peer shapes in §18.4 stay frozen. Cross-hub safety is a stored subject and the hub fingerprint, not a new id prefix.
- The MCP OAuth server in `internal/oauth` stays the agent authorization server. Human login is a new relying-party package. It does not reuse the consent cookie `vecnet_oauth_sub`.
- Agent enrol tokens stay on the human side of the onboarding boundary: MCP results for an agent kind do not contain them. A guest invite is the recorded exception. MCP `vecnet_guest_invite` returns the same prompt as Add guest (invite-flow ruling). The redeem code appears once, in that create result only. Task 6 is built as specified and does not merge until CoS confirms this exception with Max.
- Migration number: this checkout's highest file is `0025_r2_blobs.sql`. The onboarding plan says its DB build owns 0026. Take the next free number at integration. Do not reserve one, and do not edit a shipped migration. `0015_reserved.sql` is why.
- Do not edit an onboarding-owned file while that task is in progress. The overlap table says which task. Re-inventory after that work merges.
- No secret, link, cookie sample, redeem code, or real Office 365 tenant id in git. Tests use generated bytes.
- UI lives at `internal/ui/static/`. There is no top-level `ui/`. The guest web shell (Task 4) is deferred until Max asks for a human-only browser guest. The operator cookie sign-in (Task 5b) is UI work for Claude/Opus and waits until onboarding Task 9 has merged. Amendments 2 and 3 apply to that operator cookie (Task 5 owns the session row and the Origin check; Task 5b is the page). Cookie-authenticated POST, PUT, and DELETE check `Origin` against the hub's public origin in the shared HTTP middleware. Bearer calls do not.
- Feature B (machine accounts / drop to disk) stays held. A virtual file bucket is not in this build. Resource kind `bucket` is reserved in the closed list and nothing implements it.
- The invite prompt stays small. Its budget is 600 characters. Each of MCP, the CLI, and raw HTTPS has an early entrypoint that explains the rest of that path, from one hub-owned guide source (Task 8).
- Default account scope is Global: hub `send` and hub `directory`, with no friction (Q5, `msg_01M6R1CP`). A team is a choice on Task 6a's Access step, not a warning and not a nag. Onboarding Task 9 ships without that step. Task 6a adds it after Tasks 1–3 and onboarding Task 9. The hint line is "Use a team for work or sensitive projects."

## Verified facts this design relies on

The research note holds the full table. The facts a task will trip on:

- One operator credential, created once (`internal/hub/hub.go` lines 231–244). Admin routes admit `Caller.Kind == "operator"` (`internal/httpapi/middleware.go` lines 127–129) and the handlers do not check again (`internal/httpapi/admin.go` lines 17–19).
- Send scopes do not filter `GET /v1/peers` (`internal/httpapi/peers.go` lines 92–100). Empty scopes mean allow all (`internal/store/migrations/0001_init.sql` line 64; `internal/auth/auth.go` lines 389–396).
- A team is a recipient list (`internal/store/migrations/0001_init.sql` lines 72–83). `CreateTeam` is operator-only (`internal/hub/groups.go` lines 346–360). Membership fans out (`docs/PROTOCOL.md` line 846).
- The web sign-in stores the operator secret in `localStorage` (`internal/ui/static/app.js` lines 6–7, 29, 272). `/ui/` is a public path (`internal/httpapi/middleware.go` lines 69–70).
- Push route kinds are `http` and `grokbot` only (`internal/store/migrations/0007_grokbot_routes.sql` line 11). There is no mail route.
- Entra `sub` is pairwise per application. `oid` + `tid` is the cross-app key in one tenant. Email is mutable. Sources are in the research note.
- `github.com/coreos/go-oidc/v3/oidc` verifies ID tokens and does not issue them. Dex is a separate process, and its README marks the SAML connector unmaintained and likely vulnerable. `go.mod` line 40 already has `golang.org/x/oauth2` as an indirect require.

## Decisions for CoS

CoS recorded one option for each decision on 2026-11-06. The option tables stay so the rejected choices remain visible. The recorded letter is in "CoS's record". Amendments 1–6 are part of that record and are written into the named tasks. The Q1 ruling changes what recorded D4 A ships, and adds the guide task, grant expiry, and file attachments through existing message blobs. The invite-flow ruling confirms D8 A, skips question 4, adds resource kind `thread`, reserves resource kind `bucket`, and specifies `vecnet_guest_invite`. The Q5 ruling (`msg_01M6R1CP`) fits recorded D3 B and amendment 1, and adds Task 6a. Those rulings are written into the named tasks. A later answer from Max that conflicts with a recorded option is flagged to CoS before any task text changes.

### D1. What a guest is

A friend's agent pastes a short prompt and talks to one agent, or in one existing thread. That agent needs a peer id to reply to. Today every authenticated caller also sees the whole directory. The Q1 ruling: the guest is the friend's agent that pasted the prompt. A human browser sign-in is deferred Task 4.

| Option | What ships | Cost |
|---|---|---|
| A. New peer kind `guest` | One principal, one peer of kind `guest`, one grant that names the agent. `sender_info.kind` is `guest`, so an agent can tell a friend from a coding session | Protocol change: `PeerKinds`, PROTOCOL §8, the onboarding kind list. `senderInfo.kind` needs no schema enum edit (it is an open string). Serialize with onboarding Tasks 1 and 9 |
| B. Reuse kind `session` | No edit to `PeerKinds`. The directory shows a coding session | Agents and the 15-minute session presence TTL (PROTOCOL §18.2) treat the friend as a coding session |
| C. No new peer | The browser posts as a peer the operator already created | The friend has no directory identity of their own. The link is a second credential on someone else's peer |
| D. Send as `"hub"` | No peer row | PROTOCOL §1.3 reserves sender `"hub"` for messages the hub authors. A guest would impersonate the hub |

**Recorded: A.** Kind stays `guest`. Kind is how agents already branch, and the onboarding registry is already keyed by kind. Unifying "add a guest" with "add an agent" means the guest is one more registered submit action, beside Add agent. Q1 ruling (`msg_01M6QZHT65EJH8195GG0NJYC5T`): the principal's peer is the friend's agent after it redeems the prompt. D1 A is unchanged by that ruling.

Directory hiding is required for A, B, and C. It is decision D3's mechanism, and it is a protocol change of its own. Question 2 fits this record: an agent-scoped guest's directory is that one agent. Its history grant is the threads it participates in, which is the default.

### D2. One permission model

Admin-parity specifies `admin:*` grants, `own_principal` / `hub` boundaries, and an operator credential that bypasses grants. Send scopes are a different table. Operator message powers key off `peers.kind`.

| Option | Authority |
|---|---|
| A. Grants replace both | One `Authorize`. Send allow/deny, admin routes, and the kind-based operator powers all read grants. The `scopes` table is dropped. PROTOCOL §12 and §18.4 change for a live consumer |
| B. Two systems | Build admin-parity as written. Guests use send scopes. The operator kind stays a bypass |
| C. One authority, frozen wire | Grants are the authority. `PUT /v1/admin/peers/{id}/scopes` and principal default scopes remain the wire, filled from grants, so pag-chat's shapes stay. Operator message powers (depth reset, locked join, any-thread reply, full history, operator views) require a grant, not `peers.kind`. The bootstrap operator principal holds the root grant. `EnsureOperator` still creates that one principal |

**Recorded: C.** B is the parallel system this design was told to absorb. A breaks a frozen wire. C lets admin-parity's capability names survive as grant names, and deletes the operator bypass those tasks assumed. Amend admin-parity before it is built; do not migrate a grant table that has not shipped.

Recorded grant: subject is a principal or a peer; capability is from a closed list (`send`, `directory`, `history`, `admin:<domain>` matching admin-parity's domains, `invite`); resource is one peer id, one team id, one thread id, the subject's own principal, or `hub`; access is `read` or `write`. Resource kind `bucket` is reserved in that closed list. This build writes no `bucket` grant and implements no virtual file bucket. A write grant includes read of that capability. Empty resource set denies. There is no implicit allow-all. After the migration, "no rows" is not allow-all. `invite` is absent from amendment 1's defaults. The bootstrap root grant includes `invite`. Any other caller holds `invite` only after a holder of `invite` grants it.

Grants carry an optional `expires_at` (Q1 ruling). A one-off invite sets `expires_at` to the invite lifetime: one timestamp, the validity end chosen when the invite is created. CoS asked for the simpler of invite lifetime and first-thread-quiet. Invite lifetime is the one recorded. First-thread-quiet would need a definition of quiet and a watcher, so it is not the build. Until Max sets a one-off duration, that lifetime is 24 hours from creation, the same bound as the unredeemed redeem code, and redeeming does not extend it. An ongoing invite leaves `expires_at` empty and lasts until revoked. An agent-created invite lasts at most 7 days unless an operator raises that cap for the caller. The cap covers both a one-off and an until-revoked invite an agent creates, so an agent's until-revoked invite is stored with `expires_at` seven days out. An operator's until-revoked invite leaves `expires_at` empty. An expired grant denies the way a missing grant denies. The redeem code has its own clock, below in D4, including for an ongoing invite.

Amendment 1, default grants. The backfill gives every existing peer with no scope rows hub `send` and hub `directory`, not `send` alone. A peer created through the operator's invite or onboarding gets hub `send` and hub `directory` unless the invite names a narrower scope. A principal-provisioned peer (PROTOCOL §18.4) inherits its principal's default grants, filled from the frozen default-scopes wire. Only guest and colleague principals start empty. Without those defaults, every agent enrolled after Task 1 would see an empty directory and could send nowhere, which breaks onboarding and pag-chat. Q5 names that default Global. Task 6a's team choice is the narrower scope: `send` and `directory` on that team resource, not hub. Omitting the choice, and every create path that has no Access step yet, stays Global. Operators and other special accounts stay on that global default.

### D3. Team scope, global scope, and the front door

`teams` / `team_members` are who a message fans out to (`internal/hub/groups.go` `CreateTeam`). Putting a colleague in `team_members` delivers every team message to them.

| Option | Meaning of "scoped to a team" |
|---|---|
| A. Reuse `team_members` | The account is a member. They receive fan-out and, if D1's directory filter expands members, they see those peers |
| B. A grant whose resource is a team id | At check time the grant expands to current members. Fan-out membership stays a separate operator action. A global account holds resource `hub`. A front door is one extra grant whose resource is one peer id, outside the colleague's own peers |
| C. Send-scope allow list only | No directory change. PROTOCOL §8 stays. A guest still lists every peer |

**Recorded: B.** A team grant expands at check time. Fan-out membership stays a separate operator action. A makes "on the project" and "receives every project message" the same switch. C does not meet "only talk to one agent" or "only this team", because `GET /v1/peers` is unfiltered. The front door is a peer id Max names, not a kind and not a team.

Own agents: peers whose `prn_id` is the colleague's principal, created under existing provisioning (default off until Max turns it on). The grant resource `own principal` covers those peers without listing them.

Q1 ruling on question 2, which fits D3: an agent-scoped guest's directory is that one agent, and its threads are only threads with that agent. Its history grant is the threads it participates in. That participation default is `from_join` on the invite form (invite-flow ruling). Full history is operator-only. A thread-scoped guest is a participant of that one thread only. A locked thread accepts an invite only from the operator.

Q5 ruling (`msg_01M6R1CP`, 2026-11-06) fits D3 B and amendment 1. Global is hub `send` and hub `directory`: today's behaviour, and the default for agents. Team-scoped is `send` and `directory` grants on a team resource, expanded at check time. That account sees no one outside the team. Operators and other special accounts are global. The default is Global, with no friction. Add agent and Add guest gain one Access step: Global (default) or Team (pick an existing team or create one), plus one line: "Use a team for work or sensitive projects." That line is a hint. It is never a warning and never a nag. The step is Task 6a. It does not block onboarding Task 9. Onboarding Task 9 ships now without the step, because grants do not exist until accounts Tasks 1–3. Changing an existing peer between global and team is an operator action on the peer page, under Task 6a, and it is audited. Questions 6–8 still govern who sees every project, the front door, and whether a colleague adds their own agents.

### D4. The browser link

Max will copy a URL. The hub cannot send mail. PROTOCOL §2 forbids a long-lived secret in a URL. RFC 6750 §5.3 says bearer tokens should not be query parameters.

| Option | Mechanics |
|---|---|
| A. Fragment, single use | The secret is in the URL fragment. The page posts it once. The hub stores only the SHA-256 of the link and spends it. A server-side session row holds the hashed session id, principal, peer, created, expires, and `revoked_at`. The cookie carries only that session id, sealed with the existing hub seal (`HttpOnly`, `Secure`, `SameSite=Lax`), on a path that is not `/oauth/`. Cookie name is not `vecnet_oauth_sub`. Grants are re-read on each call. Cookie-authenticated POST, PUT, and DELETE require `Origin` equal to the hub's public origin and refuse a missing `Origin`. The cookie is not the operator secret and is not written to `localStorage` |
| B. Query string | The redeem request's access log and `Referer` see the secret |
| C. Typed code | Same shape as a link code (12 characters, hash, lockout). Nothing bearer-like is in the URL. The friend types the code |

**Recorded: A, as the Q1 ruling restates it** (`msg_01M6QZHT65EJH8195GG0NJYC5T`). A is an agent invite. The prompt contains the hub URL, a one-time redeem code, and one line that points at the self-describing entrypoint (Task 8). The code is single use, stored only as a hash, and expires 24 hours after issue until Max sets another code lifetime. Guessing uses the existing link-code lockout (PROTOCOL §20.1). The prompt carries no long-lived secret (PROTOCOL §2). The rendered prompt is at most 600 characters (Task 8 tests the budget).

The same redeem exchange runs on three paths that already exist, and each yields a scoped peer bearer for a peer of kind `guest` that holds only its grants:

- MCP: the hosted OAuth link-code exchange.
- CLI: `vecnet enrol <code>`, the existing enrol command given the redeem code.
- HTTPS: `POST /v1/enrol` with the code.

The code's 24-hour life is independent of grant `expires_at`. An ongoing invite still uses a code that expires. A one-off invite's grant lifetime defaults to that same 24 hours (D2).

The fragment, the browser session cookie, and the guest web shell in option A above are deferred until Max asks for a human-only browser guest (Task 4). Amendments 2 and 3 move with that deferred task. They still apply to the operator cookie sign-in: Task 5 owns the server-side session row and the Origin check, and Task 5b is the operator page that sends the cookie. Until Max sets other bounds, an operator session lasts 7 days. Bearer-authenticated calls, including the guest's peer bearer, do not take the Origin check.

### D5. Office 365 library

The hub is already an OAuth authorization server for agents. Human login is a relying party.

| Option | Dependency |
|---|---|
| A. `github.com/coreos/go-oidc/v3/oidc` plus `golang.org/x/oauth2` as a direct require | Authorization code and PKCE against one configured issuer. Verify signature, `iss`, `aud`, `exp`, and nonce. Map `iss` + `tid` + `oid` to a principal. Refuse to authorize on `email` or `preferred_username`. Pin the module at integration; do not float it |
| B. A plus `github.com/crewjam/saml/samlsp` | Same principal mapping from a configured persistent name identifier, not from an email attribute. Add this only after Max says the tenant will not register an OIDC app |
| C. Dex, or any full identity server, beside the hub | A second process. Dex's SAML connector is documented as unmaintained and likely vulnerable. The ADR's host is one small binary |

**Recorded: A. B only if Max's question 9 says SAML is the only door.** Unchanged by the Q1 ruling. Office 365 is for colleagues and admins. A guest does not sign in with it. Do not route agent MCP OAuth through this login. A signed-in human receives a hub session for their principal (Task 5's session row). Their agents still enrol with the existing bearer or the existing link code. Allow one issuer and one tenant id in configuration; a token whose `tid` is anything else is refused. Several hubs that do not share an app registration must key on `oid`, not `sub`.

### D6. Ids across hubs

| Option | Cross-hub rule |
|---|---|
| A. Keep prefixed ULIDs | Store `(hub fingerprint, prn_id, issuer, tid, oid)` on the principal. Peer ids stay `peer_` + ULID. A bearer remains a local hash and fails closed on any other hub |
| B. Embed the hub id in new ids | `peer_<fingerprint>_<ulid>` or similar. Every consumer of PROTOCOL §1.3 changes, including pag-chat |
| C. Wait for SPEC phase 4 "peer id namespacing" | That item is unimplemented and was scoped as per-computer namespacing, not as an Entra subject |

**Recorded: A.** Keep prefixed ULIDs. The external subject is stored on the principal. B is a wire break for a collision window the ULID's random source already makes small, and it still would not make Entra `sub` stable. C blocks accounts on an unrelated naming project. New account rows use the existing `prn_` mint. Do not add a prefix to `ids.known` for an external subject.

### D7. Several operators

`EnsureOperator` creates one operator credential. A second peer of kind `operator` under that principal is already legal (`AddPeerWith`), and `resolveSender` would give that peer the kind-based powers.

| Option | Who administers |
|---|---|
| A. More credentials of kind `operator` | Each one passes `/v1/admin/` as written. Scopes inside handlers do not exist today |
| B. One bootstrap root, everyone else is a grant | Instance admin is a principal with hub-boundary admin grants. A colleague is a principal with own-principal grants, plus the optional front-door grant from D3. Kind `operator` stays the one bootstrap peer. Further `peer add --kind operator` is refused |
| C. Many peers of kind `operator` | Kind keeps meaning "has operator powers". A colleague peer of that kind can reset depth and read every thread |

**Recorded: B**, with amendment 4. A cannot express "this colleague, not the whole hub". C gives a colleague the loop and history powers PROTOCOL §16 attaches to the operator. The bootstrap secret remains the break-glass credential `EnsureOperator` already prints once. Instance admins sign in through D5 and never receive that secret. Before `--kind operator` is refused, the migration finds every operator-kind peer other than the bootstrap peer, gives each the bootstrap grant set so today's behaviour is preserved, and lists those peers in the Task 7 receipt so Max can revoke. It does not strip powers silently.

### D8. One invite

Onboarding defines `KindSpec` and submit actions `invite`, `oauth_link`, and `grokbot_setup`, owned by tasks that are not in this tree yet.

| Option | Invite |
|---|---|
| A. One registry action `guest_link` | Same kinds endpoint, same human-output rule, a browser result instead of an enrol token. Agent kinds keep their actions |
| B. A separate guests page | Two invite implementations. The add-agent screen does not gain a person |
| C. A guest is onboarding kind `session` with a flag | Overloads the session spec the registry is about to freeze |

**Recorded: A**, confirmed by the invite-flow ruling (`msg_01M6QZNG1SWQHXFQTS07PMZ76Z`). Question 4 is skipped. Add guest is registry action `guest_link` beside Add agent, after onboarding Tasks 1, 4, 7, and 9 have merged. Until then this work does not edit `internal/onboarding/`, `internal/ui/static/invite.js`, `invite-model.js`, or `internal/mcpserver/tools.go`.

The form fields are scope, access, and validity. Scope is one agent or one existing thread. Access is read/reply plus attach and receive files, which is a `send` grant on what the guest can see: attachments ride the existing message blobs (R2), and a guest with `send` attaches and receives files in threads it can see. Validity is one-off or until revoked, stored as D2's `expires_at`. Submit returns the D4 agent-invite prompt.

MCP `vecnet_guest_invite` is in this decision. It returns that same prompt text, which the caller can forward. This is the exception to onboarding's rule that MCP never carries enrol material. CoS is confirming the exception with Max. Build it as specified. Do not merge Task 6 until CoS confirms. Constraints: the caller holds an `invite` grant, which nobody has by default except the bootstrap root; guest grants are a subset of the caller's grants; an agent-created invite lasts at most 7 days unless an operator raises that cap; the create writes an audit row; the redeem code appears once, in the create result only. Status and replay do not return it.

A virtual file bucket is not in this first build. Resource kind `bucket` stays reserved. Question 5 is ruled. Questions 6–10 stay open, asked one at a time. Question 4 stays skipped. The Global-or-team Access step is Task 6a. It is not this form's access-rights field (read/reply and files).

### CoS's record

| Decision | Recorded choice | Date |
|---|---|---|
| D1 Guest | A. New peer kind `guest`. Q1 ruling: the guest is the friend's agent that pasted the prompt. Kind stays `guest` | 2026-11-06 |
| D2 Authority | C. Grants are the one authority. The scope wire stays frozen as a cache. Amendment 1: backfill and new agent peers get hub `send` and hub `directory`; only guest and colleague principals start empty. Invite-flow ruling: resource kinds include `thread`. `bucket` is reserved and not implemented. Q1 ruling: optional `expires_at`; one-off uses the invite lifetime; ongoing is empty until revoked | 2026-11-06 |
| D3 Team, global, front door | B. A team grant expands at check time. Fan-out membership stays separate. Q2 fits: agent-scoped guest directory is that one agent; history is threads it participates in (default `from_join`). Q5 (`msg_01M6R1CP`): Global is hub `send` and hub `directory` (default for agents, today's behaviour). Team-scoped is `send` and `directory` on a team resource and sees no one outside the team. Operators and other special accounts are global. Default is Global, with no friction. Task 6a is the Access step. Onboarding Task 9 does not wait for it | 2026-11-06 |
| D4 Link mechanics | A. Agent invite (Q1 ruling `msg_01M6QZHT65EJH8195GG0NJYC5T`). Prompt is hub URL + one-time redeem code + one guide line. Redeem reuses the enrol exchange on MCP link-code, `vecnet enrol <code>`, and `POST /v1/enrol`. Browser fragment, cookie session, and guest shell are deferred Task 4. Amendments 2 and 3 still apply to the operator cookie (Tasks 5 and 5b) | 2026-11-06 |
| D5 IdP library | A. `github.com/coreos/go-oidc/v3/oidc`. B only if Max's question 9 says SAML is the only door. Unchanged. Office 365 is for colleagues and admins, not guests | 2026-11-06 |
| D6 Ids | A. Keep prefixed ULIDs. The external subject is stored on the principal | 2026-11-06 |
| D7 Operators | B. One bootstrap operator peer. Everyone else holds grants. Amendment 4: existing operator-kind peers receive the bootstrap grant set and are listed for Max to revoke | 2026-11-06 |
| D8 Invite | A. `guest_link` beside Add agent, confirmed (`msg_01M6QZNG1SWQHXFQTS07PMZ76Z`). Question 4 skipped. Form: scope (one agent or one thread), access (read/reply and files), validity (one-off or until revoked). MCP `vecnet_guest_invite` returns the same prompt; Task 6 does not merge until CoS confirms that exception with Max | 2026-11-06 |

### Rulings on Max's answers

The Q1, invite-flow, and Q5 rulings are 2026-11-06. The verbatim answers are under "Max's answers". Question 5's record there is the CoS ruling, because this fold was not given a separate Max paste. D2, D3, D6, D7, D8's letter, and amendments 1–6 stand, except where a ruling below moves amendments 2 and 3 onto the operator cookie and off the deferred guest shell. Q5 fits D3 B and amendment 1 and does not replace them.

**Q1 ruling, `msg_01M6QZHT65EJH8195GG0NJYC5T`.**

- The guest is the friend's agent that pasted the prompt. It is not a human browser sign-in.
- D1 stays A (kind `guest`).
- D4 A is the agent invite. The prompt is the hub URL, a one-time redeem code (single use, hashed at rest, 24-hour default expiry until Max sets one, lockout on guessing), and one line to the self-describing entrypoint. The prompt holds no long-lived secret (PROTOCOL §2).
- Redeem is the same exchange on three existing paths: MCP hosted OAuth link-code; CLI `vecnet enrol <code>`; HTTPS `POST /v1/enrol` with the code. The result is a scoped peer bearer. The peer's kind is `guest` and it holds only its grants.
- Task 4's browser cookie session and guest web shell are deferred until Max asks for a human-only browser guest. Amendments 2 (server-side sessions) and 3 (Origin check) move with that deferred task. They still apply to Task 5b's operator cookie sign-in. Task 5 owns the row and the check so the operator path does not wait on Task 4.
- D5 is unchanged. Office 365 is for colleagues and admins, not guests.
- Task 8: self-discoverable guide entrypoints from one hub-owned source, and a test that they agree. MCP `vecnet_guide` (guest-aware topic). CLI `vecnet guide [topic]`. HTTPS `GET /v1/guide` (no bearer: how to redeem; bearer: that caller's send, inbox, thread, and blob use, plus its limits). Invite prompt size budget is 600 characters, with a test.
- Files both ways are in A's scope through existing message attachments (R2 blobs). A guest with `send` can attach and receive files in threads it can see. Per-guest blob limits (count, bytes, expiry) default lower than an agent's. Feature B (machine accounts / drop to disk) stays held.
- Grants gain optional `expires_at`. One-off means the grant expires at the invite lifetime (chosen above as the simpler rule). Ongoing means no expiry until revoked. An expired grant denies like a missing grant. Tests cover that.
- The rest of the plan stands. Question 2 fits: the guest directory is that one agent, and the history grant is the threads it participates in (the default).

**Invite-flow ruling, `msg_01M6QZNG1SWQHXFQTS07PMZ76Z`.**

- D8 A is confirmed. Question 4 is skipped.
- Add guest is registry action `guest_link` beside Add agent. The form asks for scope (one agent, or one existing thread), access (read/reply, plus attach and receive files), and validity (one-off, or until revoked). It returns the invite prompt from the Q1 ruling.
- D2's resource kinds include `thread`. A thread-scoped guest is a participant of that thread only. The form's history policy defaults to `from_join`. Full history is operator-only. An agent-scoped guest's directory is that agent, and its threads are only threads with that agent. On a locked thread, only the operator may invite.
- MCP `vecnet_guest_invite` is in A and returns the same prompt. This is an exception to onboarding's "MCP never carries enrol material". CoS is confirming it with Max. Build Task 6 as specified. Do not merge Task 6 until CoS confirms. The caller needs an `invite` grant, which is none by default (the bootstrap root holds it). Guest grants are a subset of the caller's grants. An agent-created invite is at most 7 days unless an operator raises that cap. The create writes an audit row. The code appears once, in the create result only.
- A virtual file bucket is not in A's first build. Reserve grant resource kind `bucket` in the closed list. Implement nothing for it.
- Keep asking Max questions 5–10, one at a time. Question 4 stays skipped.

The invite-flow sentence above is the ruling as recorded. Q5, later the same day, closes question 5 only. Questions 6–10 stay open.

**Q5 ruling, `msg_01M6R1CP`.**

- Fits D3 B and amendment 1. It does not replace either.
- Global means hub `send` and hub `directory`. That is today's behaviour, and it is the default for agents.
- Team-scoped means `send` and `directory` grants on a team resource. The grant expands at check time. The account sees no one outside the team.
- Operators and other special accounts are global. This ruling does not name a further list, and this plan does not invent one.
- The default is Global, with no friction. Add agent and Add guest get one Access step: Global (default), or Team (pick an existing team or create one). One line of hint: "Use a team for work or sensitive projects." Never a warning. Never a nag.
- Do not block onboarding Task 9. Onboarding Task 9 ships now without the step. Grants do not exist until accounts Tasks 1–3.
- New accounts Task 6a, "Access step in Add agent and Add guest", after Tasks 1–3 and onboarding Task 9. The step lives on the registry form. It is a hub-owned field, not hardcoded in the UI. The CLI flag is `--team <id>` or `--global`. The MCP field is the same choice.
- Changing an existing peer between global and team is an operator action on the peer page, under Task 6a, and it is audited.
- Questions 6–10 remain open. Ask them one at a time. Question 4 stays skipped.

## Questions for Max

The conductor asks these one at a time, in order, and pastes each answer into "Max's answers" verbatim. Questions 1–3 are answered. Question 4 is skipped by the invite-flow ruling. Question 5 is ruled (`msg_01M6R1CP`). Questions 6–10 are outstanding. A later question is not asked in the same message. Nothing is built while an answer that blocks that task is still blank. If an answer conflicts with CoS's record, flag to CoS before editing the task. Docs merge does not wait on these answers. Question 5 does not block Task 6a.

1. The friend who should reach one agent: do they open a link you send them, with no account of their own, or do they sign in with an email or an Office 365 account you name in advance? **Answered.**
2. Once they are in, what do they see of that agent: only the thread between the two of them, or that agent's other conversations as well? **Answered.**
3. Can you read that conversation in your existing inbox, and can you reply in it? **Answered.**
4. When you add someone, is it one screen with a choice of person or agent, or do people and agents stay on different screens? **Skipped.** D8 A is confirmed. Do not ask.
5. A team on your hub today is a list of agents a message can be sent to, and everyone can still see the whole directory. When an account is "scoped to a team", should that person stop seeing agents outside the team? **Answered.** Ruled by CoS (`msg_01M6R1CP`). Yes for a team-scoped account: it sees no one outside the team. Global stays the default and still sees the whole directory.
6. Who, besides you, should see every project on the hub?
7. A colleague's starting scope: only the agents they add, or those agents plus one front-door agent that other colleagues can reach? If there is a front door, do you name that agent when you invite them?
8. Does the colleague add their own agents, or do you add those agents for them?
9. Office 365 sign-in: can you register an application in your tenant (OpenID Connect), or will the tenant only allow a SAML enterprise app?
10. Besides you, who should be able to administer the hub (enrol people, revoke a guest, read the audit log) from the start: nobody yet, or specific people?

## Max's answers

Paste each answer under its question number, verbatim. Question 5 is the Q5 ruling as this fold received it (`msg_01M6R1CP`). No separate Max paste was in the assignment, so none is invented. Questions 6–10 are still blank. Ask them one at a time. They do not block a docs merge. The invite-flow note is Max's, and it is not an answer to question 4.

1. 2026-11-06 19:27 AEDT.

> they get a link/prompt which contains instructions and auth. ranges from MCP, vecnet cli, to raw HTTPS calls if required. The service/feature in general should allow also like sending/recieving files (either direction possible), a one itme thing or opening comms, etc. So the https raw methods are there as fallback if they can't add mcp and can't use cli. Also note that all 3 of those options need to be self discoverable, ie each has an early entrypoint that explains the rest of it's API/usage/etc. The entire point of that is to make the link/prompt that's sent as small and selfcontained as possible.

2.

> only the thread with that one agent

3.

> yes, i can read it and reply in it

4. Skipped. No answer. CoS confirmed D8 A (`msg_01M6QZNG1SWQHXFQTS07PMZ76Z`).

Invite flow, 2026-11-06 19:29 AEDT. Verbatim. Ruled with question 4 skipped; this note is not a stand-in for question 4.

> re friend access: so i like do something on server (similar to adding a bot, but like adding a guest) -> select scope, token/session validity time, access rights for a specific thread or maybe like (virtual) file bucket, erc -> get prompt. Similarly, agents should be able to do that via MCP and get the same text back (which they then forward through you or wahtever).

5. 2026-11-06. CoS ruling `msg_01M6R1CP`. This is the ruling text, not a reconstructed quote from Max.

> Fits D3 B and amendment 1. Global = hub send and directory grants (today's behaviour, default for agents). Team-scoped = send and directory grants on a team resource, expanded at check time; sees no one outside the team. Operators and other special accounts are global. Default is Global, with no friction. Add-agent/add-guest flow gets one Access step: Global (default) or Team: pick or create, plus one-line hint: "Use a team for work or sensitive projects." Never a warning or a nag. Do not block onboarding Task 9. T9 ships now without the step (grants do not exist until accounts Tasks 1–3). Add new accounts Task 6a: "Access step in Add agent and Add guest", after Tasks 1–3 and onboarding T9. Step lives on the registry form (hub-owned field, not hardcoded in the UI), with the same CLI flag (`--team <id>` or `--global`) and MCP field. Changing an existing peer between global and team is an operator action on the peer page under the same task, audited.

6.
7.
8.
9.
10.

## Protocol changes and shared files

A build touches these. This assignment touches none of them.

| Change | Protocol? | Files |
|---|---|---|
| Peer kind `guest` (D1 A) | Yes. `PeerKinds` and PROTOCOL §8. Envelope `senderInfo.kind` is already an open string | `internal/hub/hub.go`, `docs/PROTOCOL.md`, `docs/protocol/envelope.schema.json` only if a fixture enum is added (the schema does not enum peer kinds today) |
| Caller-scoped directory | Yes. PROTOCOL §8 and §2.10. Required for a guest and for a team-scoped account | `internal/hub/directory.go`, `internal/httpapi/peers.go`, `internal/mcpserver` peers tool |
| Operator powers move off `peers.kind` (D2 C) | Yes. PROTOCOL §16.1, §16.3, §2.8, §2.13, §17.1 | `internal/hub/send.go`, `threads.go`, `reactions.go`, `presence.go`, `admin.go`, `groups.go` |
| Grants behind the existing scope wire | Behaviour change. The wire in §12 and §18.4 stays | `internal/auth/auth.go`, `internal/hub/provision.go`, `internal/store/migrations/` next free number |
| Human relying-party login | New routes. Not an envelope change. Document in PROTOCOL §12 | New package `internal/login/`. Do not put it in `internal/oauth/` |
| Guest redeem code | Reuse the existing enrol exchange. The prompt is the hub URL, the one-time code, and one guide line. No long-lived secret (PROTOCOL §2) | MCP hosted OAuth link-code, CLI `vecnet enrol`, `POST /v1/enrol` |
| Browser guest session | Deferred with Task 4 until Max asks for a human-only browser guest. Reuses Task 5's session row | guest redeem handler, `internal/ui/static/guest.js`, when that task starts |
| Operator session cookie | Amendments 2 and 3, on Tasks 5 and 5b. Cookie-authenticated POST, PUT, and DELETE require `Origin` equal to the hub's public origin and refuse a missing `Origin`. Bearer calls are unchanged | `internal/login/`, `internal/httpapi/middleware.go`, `internal/ui/static/app.js` |
| Guide entrypoints | `GET /v1/guide` is new HTTP. `vecnet_guide` gains a guest-aware topic. `vecnet guide` gains an optional topic. One hub-owned source; the three agree | Task 8. Today the copies are `internal/mcpserver/guide.go` and `internal/cli/guide.md` |
| Grant resource `thread`, reserved `bucket`, optional `expires_at` | `thread` is a resource kind. `bucket` is reserved and not implemented. Expired denies like missing | Task 1 migration |
| `vecnet_guest_invite` | MCP create result contains the invite prompt once. Exception to the enrol-material boundary. Task 6 does not merge until CoS confirms with Max | `internal/mcpserver` |
| Per-guest blob limits | Existing message attachments (R2). Guest ceilings on count, bytes, and expiry are lower than the agent ceilings in PROTOCOL §2.7. Feature B stays held | blob quota check on send |
| Invite registry action | After onboarding. Not a second kind list. Add guest is `guest_link` beside Add agent | `internal/onboarding/`, `internal/httpapi/admin.go`, `internal/ui/static/invite*.js`, `app.js`, `api.js` |
| Access step, Global or team (Q5, Task 6a) | No new envelope field. Directory hiding is the PROTOCOL §8 change already assigned to Task 7: a team-scoped caller does not see peers outside the team. The choice is a hub-owned registry field. CLI `--team <id>` or `--global`. MCP carries the same field. An operator change on the peer page is audited. Creating a team here does not insert `team_members` | Registry field in `internal/onboarding/`; `vecnet agent add` and the `guest_link` CLI adapter; `vecnet_agent_add` and `vecnet_guest_invite`; the operator peer page under `internal/ui/static/` |
| whoami | Tool schema if a new credential kind appears. Recommendation: whoami stays `peer` / `principal` / `operator`; a guest is a peer of kind `guest` | `internal/mcpserver/tools.go` |
| Glossary and product scope | SPEC §15 currently excludes multi-operator tenancy. Amending that sentence is part of the doc task, not a silent edit | `GLOSSARY.md`, `SPEC.md`, `docs/OPERATOR.md`, `CHANGELOG.md` |

`internal/mcpserver` tool registration, `docs/PROTOCOL.md`, and `internal/ui/static/` are shared with onboarding. `internal/hub/admin.go`, `internal/httpapi/admin.go`, `internal/httpapi/middleware.go`, `internal/auth/`, and `internal/oauth/` are shared with admin-parity and with onboarding Tasks 2, 4, and 6.

## Overlaps with agent onboarding

Onboarding is in progress from `docs/plans/2026-11-06-agent-onboarding-kinds.md`. Its package is not in this checkout. Do not start the overlapping task until the named onboarding task has merged, then re-read the file.

| Onboarding task | Files it owns | This plan's task that must wait |
|---|---|---|
| 1 Kind registry | `internal/onboarding/` | Task 6 (`guest_link` spec) and Task 6a (the Access-step field) |
| 2 Hub service | `internal/hub/hub.go`, `internal/hub/admin.go`, `internal/hub/onboarding.go` | Tasks 2 and 3 |
| 4 HTTP | `internal/httpapi/admin.go` | Tasks 4 and 6 |
| 6 Consent cookie | `internal/oauth/consent.go`, `consent.html` | Task 4 must not edit these. Guest cookie is a different name and path |
| 7 MCP | `internal/mcpserver/tools.go` | Task 6 and Task 6a (the MCP field) |
| 9 Add-agent UI | `internal/ui/static/app.js`, `api.js`, `invite.js`, `invite-model.js`, invite CSS and tests | Deferred Task 4 guest shell, Task 5b, Task 6, and Task 6a. Onboarding Task 9 does not wait on Task 6a and ships without the Access step |
| 11 Docs | `docs/PROTOCOL.md`, guides | Tasks 7 and 8 |

Admin-parity Tasks 2–8 assume an operator bypass and a new grant migration. They should be rewritten against recorded D2 C, not implemented in parallel and merged under this work.

Cookie-authenticated state changes share `internal/httpapi/middleware.go` with onboarding Tasks 2, 4, and 6. The Origin check (amendment 3) lives on that middleware. It applies when the caller authenticated with the session cookie. Bearer-authenticated calls skip it.

## Dependency map

```json
{"0":[],"1":[0],"2":[1],"3":[2],"5":[2],"5b":[5],"6":[3,"5b"],"6a":[3],"8":[6],"7":[8],"4":["5b"]}
```

Task 0 holds CoS's record, filled on 2026-11-06 and amended the same day by the Q1, invite-flow, and Q5 rulings. Max's answers 1–3 and the invite-flow note are pasted. Question 5 is the Q5 ruling. Questions 6–10 are still empty. Once these docs merge, Tasks 1–3 wait for the DB build to land (`internal/store/migrations`), then follow this map. Task 4 is deferred: it is not a predecessor of Tasks 5, 5b, 6, 6a, 8, or 7. It reuses Task 5's session row if Max later asks for a human-only browser guest. Task 5b still comes before Task 6 because both edit `internal/ui/static/app.js`. Task 5b and Task 6 also wait for onboarding Task 9. Task 6a depends on Task 3, so it follows Tasks 1–3, and it also waits for onboarding Task 9. Onboarding Task 9 is not a node in the JSON. Task 6a does not depend on Tasks 4, 5, 5b, 6, 8, or 7, and it is not their predecessor. Onboarding Task 9 does not wait for Task 6a. Task 6 does not merge until CoS confirms the MCP enrol-material exception with Max. Nothing is built until the Max answers that block that task are in. Question 5 does not block Task 6a. Questions 6–10 do not block Task 6a. A conflicting answer is flagged to CoS before the task text changes. The backlog items are not in this map.

---

### Task 0: Record the decisions

**Files:** this plan only (`docs/plans/2026-11-06-accounts-scope.md`).

**Interfaces:** "CoS's record" is filled, including the Q1, invite-flow, and Q5 rulings. "Max's answers" holds questions 1–3, the invite-flow note, and the Q5 ruling text. Questions 6–10 fill as the conductor receives them. Question 4 stays skipped.

- [x] CoS recorded one option letter for D1–D8 on 2026-11-06, including amendments 1–6 in the tasks below.
- [x] Questions 1–3 and the invite-flow note are pasted verbatim. Question 4 is skipped. Question 5 is the Q5 ruling (`msg_01M6R1CP`), recorded because this fold had no separate Max paste. The Q1 ruling, the invite-flow ruling, and the Q5 ruling are in "CoS's record" and in the tasks they change. Task 6a is in the dependency map after Task 3, and the prose also waits on onboarding Task 9.
- [ ] The conductor asks question 6, waits, and pastes the answer verbatim. Continues in order through question 10. A question whose answer cannot be known yet stays blank, and any task that answer blocks stays blocked.
- [x] Recorded options match the rulings, and amendments 1–6 are in the tasks. D4 A's shipped shape is the agent invite. The browser session remains written under deferred Task 4. Q5 fits D3 B and amendment 1. If a later Max answer conflicts with a ruling, flag to CoS before editing tasks. Do not leave a conflicting interface in place.

---

### Task 1: Principals gain an external subject, and grants exist

**Implements:** D2 C and D6 A, recorded 2026-11-06, plus the Q1 grant-expiry rule and the invite-flow resource kinds. Amendment 1 is part of this task. Wait for the DB build to land (`internal/store/migrations`) before editing a migration.

**Files:** Create the next free `internal/store/migrations/NNNN_account_grants.sql` after rebase (do not use a number this plan names). Modify `internal/auth/auth.go`, `internal/hub/provision.go`, and their tests. No HTTP route. No change to `PeerKinds`. The `AddPeerWith` call site is Task 2, so this task does not edit `internal/hub/hub.go` while onboarding Task 2 owns it.

**Interfaces:** Produces `Grant` `{id, subject_kind, subject_id, capability, resource_kind, resource_id, access, expires_at}` and `ExternalSubject` `{principal_id, issuer, tenant, object_id}` unique on `(issuer, tenant, object_id)`. `expires_at` is optional. Empty means until revoked. A timestamp in the past denies like a missing grant. `resource_kind` is the closed list `peer`, `team`, `thread`, `principal`, `hub`, and reserved `bucket`. A write with `bucket` is rejected. This task does not create a bucket table or a bucket handler. `Authorize(ctx, caller, capability, resource) (Decision, error)` reads grants inside the caller's transaction and ignores an expired row. Existing `MaySend` stays, and calls `Authorize` for capability `send`. `scopes` rows remain the frozen wire cache: a grant write updates them in the same transaction. After this migration, "no rows" is not allow-all. `invite` is not written by amendment 1. Task 2's bootstrap root grant includes it.

Amendment 1. Three create paths, and only two principal kinds start empty:
- Backfill: every existing peer with no scope rows receives hub `send` and hub `directory`. Not `send` alone. Peers that already have scope rows are not widened.
- Invite and onboarding: the writer in this task gives that new peer hub `send` and hub `directory` unless the invite names a narrower scope. Task 6a's team choice is that narrower scope (`send` and `directory` on the team resource). A missing Access step stays Global. Task 2's edit of `AddPeerWith` calls this writer. Onboarding Task 9 has no Access step, so peers it creates stay on this Global default until Task 6a.
- PROTOCOL §18.4: `provision.go` gives the new peer its principal's default grants, filled from the frozen default-scopes wire. Pag-chat's shapes stay.
- Guest and colleague principals are created with no grants. They deny. Every other new peer uses one of the three paths above, so an agent enrolled after this task can still send and still read the directory.

- [ ] Tests cover all three paths: backfill writes hub `send` and hub `directory`, not `send` alone, and a peer that already had scope rows is unchanged; an invite or onboarding create writes hub `send` and hub `directory` unless the invite names a narrower scope, in which case only that scope is written; a §18.4 principal-provisioned peer inherits its principal's default grants through the frozen default-scopes wire. A guest principal and a colleague principal get no grants and `Authorize` denies. Deny still wins. A team resource expands to current members and does not insert `team_members`. `oid` collision on the same issuer and tenant returns the existing principal; a different tenant does not.
- [ ] Expiry tests: a grant with empty `expires_at` allows; a grant with `expires_at` in the future allows; a grant with `expires_at` in the past denies the way a missing grant denies, including when the bearer is still presented. A `bucket` resource is rejected and creates no row.
- [ ] `Authorize` does not read `peers.kind`. Operator powers are unchanged in this task (Task 2 moves them).
- [ ] Run the auth and hub tests through `just bb`. Commit the migration and the authority. No protocol edit yet.

---

### Task 2: Operator powers become grants

**Implements:** D2 C and D7 B, recorded 2026-11-06. Amendment 4 is part of this task. Depends on Task 1. Wait until onboarding Task 2 has merged if it has touched `internal/hub/hub.go` or `admin.go`. Wait for the DB build to land before editing a migration.

**Files:** Modify `internal/hub/send.go`, `internal/hub/threads.go`, `internal/hub/reactions.go`, `internal/hub/presence.go`, `internal/hub/admin.go`, `internal/hub/groups.go`, `internal/hub/hub.go` (`AddPeerWith`'s operator-kind check and the amendment 1 default-grant call), `internal/httpapi/middleware.go`, `internal/httpapi/admin.go`. Tests beside each.

**Interfaces:** Depth reset, posting into a thread the sender has not joined, adding to a locked thread, replying to a message the sender cannot see, full-history reads, and `/v1/admin/` require a grant, not `peers.kind` and not `Caller.Kind == "operator"` by itself. The bootstrap principal from `EnsureOperator` receives hub-boundary grants for those capabilities, including `invite`, in the same migration as Task 1 or a following next-free migration if Task 1 has already shipped. Amendment 1's defaults still omit `invite`. `AddPeerWith` calls Task 1's invite default (hub `send` and hub `directory`, unless the caller passes a narrower scope). Q5 keeps the bootstrap operator, and other special accounts, on global hub grants. This task does not team-scope them. New `peer add --kind operator` returns 422. Existing operator-kind peers keep their kind. Handlers take the caller; deleting the middleware check without the handler check is a failure. Full history stays an operator grant. A guest history grant does not include it (Task 6 defaults the form to `from_join`).

Amendment 4. Before `--kind operator` is refused, the migration finds every operator-kind peer other than the bootstrap peer. Each of those peers, as subject, receives the bootstrap grant set, so depth reset, inbox, and admin keep working for them. The subject is the peer, not every peer under its principal. The migration records those peer ids for the Task 7 receipt. Max can revoke. The migration does not delete their kind and does not drop the grants it just wrote.

- [ ] Tests: an operator-kind peer that existed before the migration, other than the bootstrap peer, still resets depth and can open an admin route, because it holds the copied grant set, and its id is in the receipt list. After those grants are removed, kind `operator` alone does not reset depth and does not read an unrelated thread. A principal with a hub admin grant can call one admin route without holding kind `operator`. A peer credential cannot. `peer add --kind operator` is 422. Existing bootstrap behaviour (depth reset, inbox, admin) still passes, and the bootstrap grant set includes `invite`. A peer created through `AddPeerWith` without a narrower scope holds hub `send` and hub `directory`, and does not hold `invite`.
- [ ] Audit actor is the caller principal id, not the hardcoded string `operator`, on the paths this task edits.
- [ ] Run hub and httpapi tests through `just bb`.

---

### Task 3: Directory and send respect the grant

**Implements:** D3 B, recorded 2026-11-06, plus the Q1 and invite-flow visibility rules. Amendment 1's directory consequence is part of this task. Depends on Task 2. Protocol change to §8 lands in Task 7 together with the code, not before. Wait for the DB build to land before this task starts, because Tasks 1–3 share that wait. Question 2 is answered: the guest sees only the thread with that one agent. CoS recorded the fit as directory = that one agent, and history = the threads it participates in.

**Files:** Modify `internal/hub/directory.go`, `internal/httpapi/peers.go`, the history read in `internal/hub/threads.go`, the MCP peers projection in `internal/mcpserver` (re-inventory the file; onboarding Task 8 may own list assembly), and the existing blob quota on the attachment path (`internal/blob`, PROTOCOL §2.7). Tests.

**Interfaces:** `Directory(ctx, caller)` returns peers the caller's `directory` grant expands to, plus the caller itself. A hub `directory` grant returns today's full list. `GET /v1/peers` passes the caller. Send to a peer outside the grant is 403 `scope_denied` as today. Team fan-out is unchanged: a grant on a team does not add `team_members`.

Q5 (`msg_01M6R1CP`): a team-scoped account holds `send` and `directory` on that team resource. Expansion is current members only. The directory is those members and the caller itself. A peer outside the team is absent, including another peer of the same caller. A Global account holds hub `send` and hub `directory` and sees today's full list. Operators and other special accounts are global, so they are not team-scoped by default. Task 6a is the control that writes the team grants. This task implements the check, not the form.

An agent-scoped guest holds `directory` and `send` on that one peer. Its directory is that peer and itself. Its threads are only threads with that peer. History returns the threads it participates in. The default policy is `from_join` (PROTOCOL §16.3): messages from before it joined are hidden, and a blob is readable only when a visible message references it. A thread-scoped guest is a participant of that one thread. Its directory is the peers on that thread. Full history remains the operator grant from Task 2.

Files both ways use the existing message attachments (R2 blobs). A guest with `send` may attach a blob on a thread it can see, and may receive a blob that a visible message references. Per-guest limits on count, on bytes, and on expiry default lower than the agent limits. The agent limits today are 25 MiB per blob and 500 MiB daily (PROTOCOL §2.7). The agent path has no separate count ceiling. The guest path has a finite count ceiling, and its byte ceiling and its expiry ceiling are each strictly lower than the agent's. The build writes the guest numbers next to the agent numbers. This plan does not pick those integers. Feature B (machine accounts / drop to disk) is not started. Resource kind `bucket` stays rejected in Task 1.

No `directory` grant returns an empty directory, not the full one. That empty result is the guest before Task 6 writes its grants, the colleague, and a narrowed invite. It is not the result for an agent enrolled with amendment 1's defaults: that peer holds hub `directory` and sees today's full list. Reading "no grant means empty" as the create path would hide the directory from onboarding and from pag-chat.

Question 3 is yes. The operator reads the guest's thread and replies in it, by the operator grants from Task 2. This task does not remove that.

- [ ] Tests: a caller granted one peer id receives a directory of that peer and itself. A team grant follows a membership change. A team-scoped caller sees the current members and itself, and a peer outside the team is absent. A hub `directory` grant matches the previous full directory. An invite-default peer, which is Global, sees that full directory. An operator and any other special account stay on that full directory. A narrowed invite sees only the named peers and itself. A guest principal and a colleague principal, with no grant, see an empty directory. An agent-scoped guest sees that one agent and itself, and a history read returns only threads it participates in with that agent. A thread-scoped guest reads that thread under `from_join`, its directory is the peers on that thread, and it does not read an earlier message or another thread. Full history still requires the operator grant.
- [ ] Attachment tests: a guest with `send` puts a blob and sends it on a visible thread, and reads a blob a visible message references. A blob on a thread it cannot see is denied. A guest upload at the agent ceiling is refused, and the same upload under an agent credential is still accepted. The guest count ceiling is finite. The test names the guest count, byte, and expiry ceilings and asserts each is strictly lower than the agent's.
- [ ] Disabled peers stay hidden to non-operators, matching `Directory` today.
- [ ] Run hub, httpapi, and mcpserver tests through `just bb`.

---

### Task 4: Guest browser session

**Status: deferred.** Q1 ruling (`msg_01M6QZHT65EJH8195GG0NJYC5T`). Do not start this task until Max asks for a human-only browser guest. The guest in the current build is the friend's agent. It redeems through Task 6's three enrol paths and receives a scoped peer bearer. This task is not a predecessor of Tasks 5, 5b, 6, 8, or 7.

**Implements, when Max asks:** a human browser session for kind `guest`. Amendments 2, 3, and 6 were written here. Amendments 2 and 3 move with this deferred task and still apply to the operator cookie: Task 5 owns the session row and the Origin check, and Task 5b is the operator page. When this task starts, it reuses that row. It does not add a second session table. Depends on Task 5b for that row, and on onboarding Task 9 for the shell. Does not edit `internal/oauth/consent.go`. Question 1 is answered. It did not ask for Office 365, and it did not ask for this browser path.

**Go files, when this task starts:** The session row and the Origin check already exist from Task 5. Do not add a second session table. Add the guest redeem handler, kept out of `internal/oauth`, and mark `POST /v1/sessions/redeem` and `GET /ui/guest` public in `internal/httpapi/middleware.go`. Every other new path stays authenticated.

**UI owner: Claude/Opus (amendment 6).** The guest shell is UI work, not part of the Go slice. Create `internal/ui/static/guest.js` and a mount in `app.js` only for the guest shell, not the invite dialog (Task 6 owns the dialog). Do not start those files until onboarding Task 9 has merged. Screenshots of the link-redeem, chat, and expired-link states are required before this task is done. The operator `localStorage` key is not consulted.

**Interfaces (deferred):** `POST /v1/guest-sessions` is not this task. The operator-facing create is Task 6's `guest_link`, which returns the agent-invite prompt. This task, when started, implements `POST /v1/sessions/redeem` with a one-time token in the JSON body, spends the hash, inserts the Task 5 session row, and sets the cookie. `GET /ui/guest` reads the cookie and can load only the directory from Task 3 and send as the guest peer.

Amendment 2, built for the operator in Task 5 and reused here if this task starts. The redeem token is stored as SHA-256 and is single use. An unredeemed browser link expires in 24 hours. On redeem the hub inserts a session row: hashed session id, principal, peer, created, expires, `revoked_at`. The cookie carries only the sealed session id (`HttpOnly`, `Secure`, `SameSite=Lax`, path not `/oauth/`, name not `vecnet_oauth_sub`). It is not a stateless copy of the grants. Each call loads the row, rejects a missing, expired, or revoked row, and re-reads grants. Revoking the grant, or setting `revoked_at`, makes the next call fail. Until Max sets other bounds, the session expires 7 days after redeem. The agent-invite redeem code in Task 6 is a different object: it exchanges for a peer bearer, not for this cookie.

Amendment 3. In the shared middleware, a state-changing request (POST, PUT, DELETE) authenticated by this cookie must present `Origin` equal to the hub's public origin. A missing `Origin` is refused. A different `Origin` is refused. `SameSite=Lax` stays and is not sufficient. Bearer-authenticated calls are unchanged, including a bearer POST with no `Origin`. GET is not refused for a missing `Origin`.

- [ ] Tests: query-string redeem is rejected; fragment is not logged by the handler because the handler never receives it (the test posts JSON); replay of a spent token is 401; an unredeemed link is dead after 24 hours on an injected clock; the cookie is `HttpOnly`, carries only a sealed session id, and its path is not `/oauth/`; the session row stores the hash, principal, peer, created, expires, and a null `revoked_at`; a second call re-reads grants; revoking the grant returns 401 on the next call; setting `revoked_at` does too; the default session expiry is 7 days; a guest session cannot call `/v1/admin/`; the directory is the one agent.
- [ ] CSRF test: cookie POST with `Origin` equal to the hub public origin is allowed; cookie POST, PUT, and DELETE with a missing `Origin` are refused; a different `Origin` is refused; a bearer POST with no `Origin` still succeeds; a cookie GET with no `Origin` is not refused for that reason.
- [ ] No token value in logs, errors, or `localStorage`.
- [ ] Guest shell (Claude/Opus), after onboarding Task 9: screenshots of link-redeem, chat, and expired-link.
- [ ] Run httpapi tests and the guest UI model test through `just bb`.

---

### Task 5: Office 365 relying party

**Implements:** D5 A, recorded 2026-11-06 and unchanged by the Q1 ruling. Office 365 is for colleagues and admins. A guest does not sign in here. Add D5 B only if Max's question 9 says SAML is the only door. Depends on Task 2. This task creates the server-side session row (amendment 2) and the Origin check (amendment 3) for the operator cookie. It does not wait on deferred Task 4. Question 9 blocks the SAML file. It does not block the OIDC path unless the answer says SAML is the only door, in which case B is added and A remains the OIDC half.

**Files:** Create `internal/login/oidc.go`, `internal/login/session.go`, and, only for D5 B, `internal/login/saml.go`. Session rows take the next free migration at integration. Do not name a number and do not edit a shipped migration. Modify `internal/httpapi/middleware.go` so the OIDC redirect paths that must be public are explicit, and so the Origin check below runs for cookie-authenticated state changes. Modify `go.mod` / `go.sum` to require `github.com/coreos/go-oidc/v3/oidc` and `golang.org/x/oauth2` directly. Configuration is flags or environment for issuer, client id, redirect URL, and allowed tenant id, read the same way the hub reads `VECNET_PUBLIC_URL`. Tests with a local token signer, no network. This task does not edit `internal/ui/static/`. The operator UI still reads only the `localStorage` secret until Task 5b, so an instance admin with a cookie and no secret cannot use the inbox yet. Keep the session code out of `internal/oauth`.

**Interfaces:** Authorization code with PKCE. Verify the ID token. Upsert `ExternalSubject` and insert the session row for that principal. The row holds the hashed session id, principal, peer, created, expires, and `revoked_at`. The cookie carries only the sealed session id (`HttpOnly`, `Secure`, `SameSite=Lax`, path not `/oauth/`, name not `vecnet_oauth_sub`). It is not a stateless copy of the grants. Each call loads the row, rejects a missing, expired, or revoked row, and re-reads grants. Revoking the grant, or setting `revoked_at`, makes the next call fail. Until Max sets other bounds, the operator session expires 7 days after login. Grants are re-read on each call. Unknown `oid` with a valid tenant does not become an instance admin; it becomes a principal with no grants until an admin invites it (Task 6). `email` is stored as display only if present, and is not the lookup key. SAML, if built, maps a configured persistent identifier into the same `ExternalSubject` columns and refuses an email-shaped name id as the key.

Amendment 3. In the shared middleware, a state-changing request (POST, PUT, DELETE) authenticated by this cookie must present `Origin` equal to the hub's public origin. A missing `Origin` is refused. A different `Origin` is refused. `SameSite=Lax` stays and is not sufficient. Bearer-authenticated calls are unchanged, including a guest peer bearer and a bearer POST with no `Origin`. GET is not refused for a missing `Origin`.

- [ ] Tests: wrong `aud`, wrong `iss`, wrong `tid`, expired token, and missing nonce fail closed. Two apps' different `sub` values with the same `oid` resolve one principal. A second tenant's `oid` does not. A successful login leaves a session row whose next call re-reads grants. The cookie is `HttpOnly`, carries only a sealed session id, and its path is not `/oauth/`. Revoking the grant returns 401 on the next call. Setting `revoked_at` does too. The default session expiry is 7 days.
- [ ] CSRF test: cookie POST with `Origin` equal to the hub public origin is allowed; cookie POST, PUT, and DELETE with a missing `Origin` are refused; a different `Origin` is refused; a bearer POST with no `Origin` still succeeds; a cookie GET with no `Origin` is not refused for that reason.
- [ ] The MCP authorization server's tests still pass without using this package. `internal/oauth` imports nothing from `internal/login`.
- [ ] Run `go list -deps` for the hub and confirm the new modules are the pinned versions, through `just bb`.

---

### Task 5b: Operator UI accepts the session cookie

**UI owner: Claude/Opus (amendment 5).** Depends on Task 5. Do not edit `internal/ui/static/` until onboarding Task 9 has merged. Task 6 also edits these files and waits for this task.

**Files:** Modify `internal/ui/static/app.js`, `api.js`, and their tests. No new Go.

**Interfaces:** The operator inbox and admin pages send the Task 5 session cookie and treat a valid cookie as signed in. Amendment 3 already covers that cookie in middleware. An instance admin who signed in through Task 5 reaches the inbox with no secret in `localStorage`. The `localStorage` key `vecnet.ui.secret` stays only for the break-glass bootstrap login `EnsureOperator` prints once. Clearing the cookie signs the admin out. Deferred Task 4's guest shell, if Max later asks for it, does not read this key.

- [ ] Tests: a cookie session loads the inbox; clearing the cookie signs out; the `localStorage` secret still authenticates the bootstrap operator; an instance admin with only the cookie and an empty `localStorage` reaches the inbox.
- [ ] Run the UI tests through `just bb`.

---

### Task 6: One invite for a guest and for an agent

**Implements:** D8 A, confirmed by the invite-flow ruling (`msg_01M6QZNG1SWQHXFQTS07PMZ76Z`), and D4's agent-invite redeem, and D3's front door. Depends on Tasks 3, 5, and 5b, and on onboarding Tasks 1, 4, 7, and 9 having merged. It does not depend on deferred Task 4 or on Task 6a. Re-inventory before editing. Questions 7 and 8 block the colleague form fields. The guest form does not wait on them. A conflicting answer is flagged to CoS before this task's interface changes. The Global or team Access step is Task 6a. This task does not add that control and does not hardcode it. Until Task 6a, an agent kind stays on amendment 1's Global default. Task 6a is the only writer of that registry field. When `guest_link` exists, it reads the field. It does not grow a second copy.

**Merge gate:** MCP `vecnet_guest_invite` returns the invite prompt, redeem code included. That is the exception to onboarding's rule that MCP never carries enrol material. CoS is confirming the exception with Max. Build this task as specified, including the tool. Do not merge this task until CoS confirms.

**Files:** Modify `internal/onboarding/` (one new `KindSpec` and submit action `guest_link`), `internal/httpapi/admin.go` or the onboarding HTTP file that task created, the existing enrol handler and the OAuth link-code exchange (re-inventory the files), `internal/mcpserver` (`vecnet_guest_invite`), `internal/ui/static/invite.js`, `invite-model.js`, `app.js`, `api.js`, and their tests. The CLI already calls enrol. This task does not add a second enrol command. The hub accepts the guest redeem code on the existing exchange.

**Interfaces:** Add guest is `guest_link` beside Add agent. The form fields are:

- Scope: one agent, or one existing thread.
- Access: read/reply, plus attach and receive files. Read is the `history` grant, and on an agent scope it includes `directory` on that peer. Reply, attach, and receive are the `send` grant on the scoped resource. Files ride Task 3's attachment path. The form does not add a file capability.
- Validity: one-off, or until revoked. One-off sets `expires_at` to the invite lifetime (D2): 24 hours from creation until Max sets another one-off duration. Until revoked leaves `expires_at` empty when the operator creates it. An agent-created invite, either choice, lasts at most 7 days unless an operator has raised that cap for the caller. An agent's until-revoked invite is stored with `expires_at` seven days out.
- History policy, on a thread scope: default `from_join`. Full history is operator-only.

Submit creates the principal, the guest peer of kind `guest`, the grants, and a one-time redeem code. The code is single use, stored as a hash, expires 24 hours after issue until Max sets another code lifetime, and locks out on guessing the way PROTOCOL §20.1 locks out a link code. The response, operator HTTP and MCP, returns one prompt: the hub URL, the redeem code, and one line pointing at the self-describing entrypoint (Task 8). The prompt holds no long-lived secret (PROTOCOL §2). Task 8 tests that the rendered prompt is at most 600 characters. The guide, not the prompt, carries the enrol command's flags. Status and replay do not return the code. The code appears once, in the create result only. The create writes an audit row.

Redeem is one exchange on three existing paths. Each spends the hash and yields a scoped peer bearer for that guest peer, which holds only its grants:

- MCP hosted OAuth link-code.
- CLI `vecnet enrol <code>` (the existing `vecnet enrol --hub <url> --token -` exchange, given this code).
- HTTPS `POST /v1/enrol` with the code.

Agent scope writes `directory` and `send` on that peer. History covers threads the guest participates in with that peer. A send to that agent, or an add onto a thread with that agent, is what joins it. This task does not join the guest to the agent's existing threads. Thread scope adds the guest as a participant of that one existing thread and writes the thread grants. A locked thread accepts the invite only from the operator (the locked-join grant from Task 2). An agent that holds `invite` and does not hold that grant is refused.

MCP `vecnet_guest_invite` returns the same prompt text as the operator form. The caller must hold `invite`. Amendment 1 does not grant it. The bootstrap root holds it (Task 2). Guest grants must be a subset of the caller's grants. A caller without `invite`, or a grant outside the caller's grants, creates nothing. Agent kinds keep their own submit actions. `vecnet_agent_add` for an agent kind is unchanged, including onboarding's grokbot rule that MCP returns `needs_human` and creates nothing. That rule does not apply to `vecnet_guest_invite`.

A guest principal and a colleague principal start with no default hub `send` or hub `directory` (amendment 1). The guest then receives only the grants this form wrote. A colleague grant set is own-principal plus, if the form names one, a single front-door peer id. Questions 7 and 8 still block those colleague fields. An agent kind enrolled here gets hub `send` and hub `directory` unless the invite names a narrower scope. An instance-admin grant set is the hub-boundary admin grants from Task 2, and only a holder of that grant may issue it. The operator pages this task touches already accept the session cookie from Task 5b.

A virtual file bucket is not built. The form has no bucket field. Resource kind `bucket` stays reserved.

- [ ] Tests: operator `guest_link` and MCP `vecnet_guest_invite` return the same prompt once, containing the hub URL, the redeem code, and one guide line, and no long-lived secret. Status and a second read do not return the code. The code is stored as a hash. Replay of a spent code is refused. An unredeemed code is dead after 24 hours on an injected clock. Guessing locks out. `POST /v1/enrol`, `vecnet enrol`, and the OAuth link-code exchange each yield a guest peer bearer that holds only those grants.
- [ ] Scope tests: an agent-scoped guest's directory is the named agent and itself. A thread-scoped guest is a participant of that thread only, history defaults to `from_join`, and full history from a non-operator caller is refused. A locked thread refuses an invite from a caller who holds `invite` and lacks the operator locked-join grant.
- [ ] Authority tests: a caller without `invite` creates nothing. A guest grant that is not a subset of the caller's grants creates nothing. An agent-created invite longer than 7 days is refused unless an operator raised that caller's cap. The create writes an audit row. The code is absent from logs and from every result except the create result.
- [ ] A colleague without the front-door field cannot send to other colleagues' agents; adding the front door does not insert a team membership. Those fields stay blocked on questions 7 and 8 until the answers are pasted.
- [ ] The six existing onboarding kinds keep their tests. `vecnet_agent_add` for those kinds still returns no enrol material.
- [ ] UI test: one add flow offers the guest kind beside the agent kinds, using the registry, with no second hardcoded list. The guest form posts scope, access (read/reply and files), and validity, and shows the prompt once. The Global or team Access step is Task 6a and is not this access-rights field.
- [ ] Run onboarding, httpapi, mcpserver, and UI tests through `just bb`. Do not merge until CoS confirms the MCP exception with Max.

---

### Task 6a: Access step in Add agent and Add guest

**Implements:** Q5 ruling (`msg_01M6R1CP`, 2026-11-06). Fits D3 B and amendment 1. Depends on Task 3, and on onboarding Task 9 having merged. Does not depend on Tasks 4, 5, 5b, 6, 8, or 7. Does not block onboarding Task 9. Onboarding Task 9 ships without this step, because grants do not exist until accounts Tasks 1–3. This task is not a predecessor of Tasks 4, 5, 5b, 6, 8, or 7. Question 5 does not block it. Questions 6–10 do not block it.

**Files:** The hub-owned field on the registry form in `internal/onboarding/`. The CLI flags `--team <id>` and `--global` on `vecnet agent add` (onboarding's add command) and on the `guest_link` CLI adapter. The same choice on MCP `vecnet_agent_add` and `vecnet_guest_invite`. The operator peer page, where an existing peer moves between global and team; re-inventory it under `internal/ui/static/`. The UI renders the registry field. It does not hardcode Global or Team. Do not add a second enrol command. `vecnet enrol` stays the redeem path from Task 6. Do not edit these files until accounts Task 3 has landed and onboarding Task 9 has merged. The registry edit also waits until onboarding Task 1 has merged. The MCP edit also waits until onboarding Task 7 has merged. Re-inventory before editing. If the peer page is `internal/ui/static/app.js`, serialize with Task 5b and Task 6, who also edit that file. This task is the only writer of the Access-step field. Task 6's `guest_link` reads it when that action exists. This task does not create a second guest form.

**Interfaces:** One Access step on Add agent and on Add guest. This is the account's Global or team choice. It is not Task 6's access-rights field (read/reply and files).

- Choices: Global, preselected, or Team. Team means pick an existing team or create one.
- Hint, one line: "Use a team for work or sensitive projects." The hint is not a warning and not a nag. There is no extra confirmation and no second screen. Submitting without touching the step is Global.
- Global writes hub `send` and hub `directory` (amendment 1, today's behaviour).
- Team writes `send` and `directory` on that team resource. Check-time expansion is Task 3. The peer sees no one outside the team. Creating a team uses the existing operator `CreateTeam`. It does not insert `team_members`. Fan-out membership stays the separate operator action from D3 B.
- Operators and other special accounts stay global. This step does not put them on a team by default, and it does not narrow the bootstrap operator. The ruling does not list the other special accounts. Do not invent that list in the build. A later Max answer that names it is flagged to CoS before the set changes.
- The field is hub-owned on the registry form. CLI: `--team <id>` selects that existing team; `--global` selects Global. Omitting both is Global. `--team` does not take a display name and does not create a team. Create stays on the form. MCP carries the same choice: global, or a team id.
- Changing an existing peer between global and team is an operator action on the peer page, in this task, and it writes an audit row. Global to team replaces that peer's hub `send` and hub `directory` with the team grants. Team to global replaces the team grants with hub `send` and hub `directory`. It does not insert or remove `team_members`.

- [ ] Tests: omitting the field, `--global`, and the MCP global value each write hub `send` and hub `directory` and do not write a team resource. `--team <id>` and the MCP team id write `send` and `directory` on that team only. A team-scoped peer's directory is the current members and itself, and a peer outside the team is absent. An operator peer stays on hub grants. A missing team id is refused and creates no peer.
- [ ] The hint string is the one line above. The form has no warning copy and no nag copy. Global is the preselected choice. A submit that does not touch the step is Global.
- [ ] UI test: Add agent and Add guest render the choice from the registry field. The page has no hardcoded Global/Team list. The form's create-team action does not insert `team_members`.
- [ ] Operator change on the peer page from global to team, and from team to global, writes the grants and an audit row, and does not insert or remove `team_members`.
- [ ] Run onboarding, CLI, mcpserver, and UI tests through `just bb`.

---

### Task 8: Self-describing guide entrypoints

**Implements:** the Q1 ruling's guide task (`msg_01M6QZHT65EJH8195GG0NJYC5T`). Depends on Task 6, because the prompt Task 6 returns points at this entrypoint and this task tests the 600-character budget. One hub-owned source. The three renderers agree.

**Files:** One module in the hub that holds the guide text. Re-inventory first. Today MCP text lives in `internal/mcpserver/guide.go` (`vecnet_guide`, topics `start`, `events`, `replying`, `etiquette`) and CLI text lives in `internal/cli/guide.md` (`vecnet guide` takes no topic). `GET /v1/guide` does not exist. Both copies stop being sources. Modify `internal/mcpserver` so `vecnet_guide` reads the hub source, including a guest-aware topic. Modify the CLI so `vecnet guide [topic]` reads the same text. Add `GET /v1/guide` on the hub. Tests.

**Interfaces:** `GET /v1/guide` with no bearer returns how to redeem: the three paths (MCP link-code, `vecnet enrol`, `POST /v1/enrol`), the rule that the code is single use, and where the rest of each API lives. `GET /v1/guide` with a bearer returns that caller's send, inbox, thread, and blob use, plus that caller's limits. A guest bearer gets the guest topic: the one agent or the one thread, `from_join`, attachment send and receive, and the lower blob ceilings from Task 3. `vecnet_guide` with the guest-aware topic returns that same guest text for a guest caller. `vecnet guide [topic]` prints the same text. The CLI embed of the unauthenticated redeem topic is generated from that hub source, so `vecnet guide` works before a profile exists, and the test shows the embed equals `GET /v1/guide` with no bearer. The invite prompt's one line names this entrypoint. The rendered prompt Task 6 builds is at most 600 characters.

- [ ] Tests: for one caller and one topic, MCP `vecnet_guide`, CLI `vecnet guide`, and `GET /v1/guide` return the same body. Unauthenticated `GET /v1/guide` explains redeem and does not require a bearer. A guest bearer receives the guest topic, including its limits. An agent bearer does not receive the guest ceilings. The prompt fixture from Task 6 is at most 600 characters and contains the hub URL, the code, and one guide line.
- [ ] Run mcpserver, CLI, and httpapi tests through `just bb`.

---

### Task 7: Protocol, glossary, and acceptance

**Depends:** Task 8, which depends on Task 6. Deferred Task 4 is not a predecessor. If CoS has not confirmed the MCP enrol-material exception, Task 6 has not merged, and this task waits with it.

**Files:** Modify `docs/PROTOCOL.md` (§8 kinds and directory visibility, §12 auth including `POST /v1/enrol` for a guest code and `GET /v1/guide`, §16 operator powers, §2.10 and §2.12 as built), `docs/OPERATOR.md`, `GLOSSARY.md` (account, guest, grant; team stays the recipient list), `SPEC.md` §15 (multi-operator tenancy moves from "out of scope for v1" to a pointer at this plan, only for what actually shipped), `CHANGELOG.md`. Create `docs/plans/receipts/2026-11-06-accounts-scope.md` with commands and results, no secrets.

**Interfaces:** The protocol text matches the recorded decisions and the code. D1 A shipped kind `guest`. D4 A shipped the agent invite. The browser guest session is named as deferred. whoami's enum matches the code. The receipt lists the migration number actually used, the operator-kind peers amendment 4 preserved, and the guest blob ceilings Task 3 chose. Q5 is in §8: a team-scoped directory is that team, and Global stays the full directory. The Access step is Task 6a and is not a new envelope field.

- [ ] `just validate-protocol` and the focused Go and UI suites, through `just bb`.
- [ ] Acceptance on fixtures: a guest redeem on each of the three paths sees one agent and can send to that agent; a second send to another peer is `scope_denied`; a thread-scoped guest sees that thread under `from_join`; the guest attaches a file and receives a file on a visible thread; an expired grant denies like a missing grant; the operator reads that thread and replies in it (question 3 is yes); a colleague principal lists only its grants; an Office 365 test uses the local signer from Task 5, not a real tenant; revoking the grant ends the operator session's next call, because the call re-reads grants on the server-side session row (amendment 2), and setting `revoked_at` ends it too. A stateless sealed cookie is not the design under test. The guest's credential under test is the peer bearer from Task 6, not a browser cookie.
- [ ] Task 8's agreement test and the 600-character prompt test are part of this acceptance.
- [ ] The receipt lists every operator-kind peer, other than the bootstrap peer, that Task 2's migration gave the bootstrap grant set, so Max can revoke (amendment 4). It names the migration number actually used. It names the guest blob ceilings. It contains no secrets, links, codes, or cookie samples.
- [ ] Commit docs and the receipt. Deployment stays separately authorized.

---

## Backlog / deferred

CoS approved these and queued them. They are absent from the dependency map. They are sequenced notes, not active build tasks. Do not build them from this plan, and do not give them task numbers.

**Sub-agent links. QUEUED.** Lands on the `guest_link` form on accounts feature A (D8 A, Task 6). This note does not add a form field, a grant, or a task. Not build now.

**Session-query.** Max called this "ask an old session". Plan it after [`docs/plans/2026-11-04-codex-appserver.md`](2026-11-04-codex-appserver.md). Build it after accounts Tasks 1–3 and before feature C. No build now. Not in the dependency map.

**Peer-cleanup. QUEUED.** Lifecycle is archived. There is no hard delete. Sequenced after Task 9+#67. This note does not add an archive column, a delete path, or a task. Not build now.

## Review focus

1. An agent-scoped guest directory contains that one agent. A thread-scoped guest is a participant of that thread only, with history default `from_join`. `GET /v1/peers` for that guest does not return the operator's other peers. Question 2 is the verbatim line under "Max's answers"; this is the recorded fit.
2. The invite prompt holds the hub URL, a one-time redeem code, and one guide line. It holds no long-lived secret. The code appears once, in the create result. MCP `vecnet_guest_invite` returns that prompt. Task 6 does not merge until CoS confirms the exception with Max. The guest does not use `localStorage`. The browser cookie path is deferred Task 4.
3. `peers.kind == "operator"` is not sufficient for depth reset or `/v1/admin/` after Task 2. CoS recorded D2 C and D7 B. Existing operator-kind peers other than the bootstrap peer hold the copied bootstrap grant set and are listed in the receipt. New `--kind operator` is refused. Powers are not stripped silently. The bootstrap root grant includes `invite`. Amendment 1 defaults do not.
4. Team scope does not add `team_members`. CoS recorded D3 B. Q5 (`msg_01M6R1CP`) fits that record: Global is hub `send` and hub `directory` and is the default; a team-scoped account holds `send` and `directory` on the team and sees no one outside the team; operators and other special accounts stay global. Task 6a is the Access step. Onboarding Task 9 ships without it.
5. Entra lookup uses `iss` + `tid` + `oid`. Email is not the key. D5 is unchanged. Guests do not sign in with Office 365.
6. `internal/oauth` is unchanged except where onboarding Task 6 already changed the consent cookie, and except the existing link-code exchange accepting a guest redeem code. The deferred guest cookie cannot satisfy the consent cookie. The operator session cookie is a different name and path.
7. The migration number in the receipt is the number that was free at integration, not a number from this plan.
8. Onboarding's six kinds still enrol as they do after that plan lands. An agent enrolled by invite or onboarding holds hub `send` and hub `directory` unless the invite named a narrower scope. A §18.4 peer inherits its principal's default grants through the frozen default-scopes wire. Only guest and colleague principals start empty. `vecnet_agent_add` for those kinds still returns no enrol material.
9. The operator session is a server row (hashed id, principal, peer, created, expires, `revoked_at`), owned by Task 5. The cookie is only the sealed session id. Grants are re-read on each call. Default operator-session expiry is 7 days. The guest redeem code expires in 24 hours, is single use, and is hashed. A one-off grant expires at the invite lifetime. An ongoing operator-created grant has empty `expires_at` until revoked. An expired grant denies like a missing grant.
10. Cookie-authenticated POST, PUT, and DELETE require `Origin` equal to the hub's public origin and refuse a missing `Origin`. That check applies to the operator cookie (Task 5) and, if Max asks later, to the deferred guest cookie. Bearer calls, including the guest peer bearer, do not take that check.
11. The guest web shell (Task 4) is deferred until Max asks for a human-only browser guest. The operator cookie sign-in (Task 5b) is Claude/Opus, after onboarding Task 9. `localStorage` remains the break-glass bootstrap login only.
12. `vecnet_guide`, `vecnet guide [topic]`, and `GET /v1/guide` render one hub-owned source. The invite prompt is at most 600 characters.
13. A guest with `send` attaches and receives files through existing message blobs, in threads it can see. Guest blob ceilings are lower than the agent's on count, bytes, and expiry. Feature B stays held. Resource kind `bucket` is reserved and not implemented.
14. Question 3 is yes: the operator reads the guest thread and replies in it. Question 5 is ruled (`msg_01M6R1CP`). Questions 6–10 are still open, one at a time. Question 4 stays skipped.
15. Task 6a puts one Access step on Add agent and Add guest: Global (default) or Team (pick or create), with the hint "Use a team for work or sensitive projects." The hint is not a warning and not a nag. The field is hub-owned on the registry. CLI is `--team <id>` or `--global`. MCP carries the same field. Changing an existing peer between global and team is an operator action on the peer page, audited, and it does not touch `team_members`. The backlog items (sub-agent links, session-query, peer-cleanup) are queued and are not build tasks.

## Self-review

- D1–D8 are recorded on 2026-11-06. Amendments 1–6 are in the tasks. The Q1 ruling (`msg_01M6QZHT65EJH8195GG0NJYC5T`), the invite-flow ruling (`msg_01M6QZNG1SWQHXFQTS07PMZ76Z`), and the Q5 ruling (`msg_01M6R1CP`) are in "CoS's record". Q5 is in D3, amendment 1, Task 3, and Task 6a.
- Questions 1–3 and the invite-flow note are pasted verbatim. Question 4 is skipped. Question 5 is the Q5 ruling text, labeled as CoS's, because this fold had no separate Max paste. Questions 6–10 stay blank and are asked one at a time. A conflicting answer is flagged to CoS before a task changes. This docs merge does not wait on Max. Nothing is built until the answers that block that task are in. Tasks 1–3 then wait for the DB build. Task 4 stays deferred. Tasks 5, 5b, 6, 6a, 8, and 7 follow the dependency map. Task 6a follows Tasks 1–3 and onboarding Task 9, and it does not block Task 9. Task 6 does not merge until CoS confirms the MCP exception. Sub-agent links, session-query, and peer-cleanup stay in "Backlog / deferred".
- Guest kind, directory filtering, thread-scoped history, team-scoped directory hiding, and moving operator powers are marked as protocol changes. The Access step is not an envelope field. Envelope peer-kind enum is not claimed, because `senderInfo.kind` is an open string.
- Shared files are named per task. Onboarding Tasks 1, 2, 4, 6, 7, 9, and 11 are the overlap set. Deferred Task 4, Task 5b, Task 6, and Task 6a wait on onboarding Task 9. Onboarding Task 9 does not wait on Task 6a. No migration number is reserved. No virtual bucket is implemented.
- No sample token, redeem code, cookie, or tenant id. One-off grant expiry is the invite lifetime. The 24-hour and 7-day bounds are the ones CoS already recorded. The Access-step hint is the one line Q5 named.
