// Coordinator rulings live in <statusDir>/decisions/all.md (optional shared rules)
// and <statusDir>/decisions/task-NN.md (two-digit task number, task-specific wins).
// Agents read these files if present at runtime; rulings override conflicting
// plan/review guidance. Edit the files, not args/prompts, to preserve the cached
// prefix on resume. Already-cached agents do not reread them until run live.
export const meta = {
  name: 'vecnet-plan-build',
  description: 'Execute the vecnet core-hub plan task by task: implement (TDD), independent review, bounded fix loop, then whole-branch review and a final gate',
  whenToUse: 'Run docs/plans/2026-11-01-core-hub.md in the .worktrees/core-hub worktree; resumable by task range',
  phases: [
    { title: 'Preflight', detail: 'worktree, toolchain, baseline suite, already-passed tasks' },
    { title: 'Task 01', detail: 'module skeleton, prefixed ULIDs', model: 'opus' },
    { title: 'Task 02', detail: 'envelope types, schema validation', model: 'opus' },
    { title: 'Task 03', detail: 'sqlite store, v1 schema', model: 'opus' },
    { title: 'Task 04', detail: 'credentials, enrolment, scopes, audit', model: 'opus' },
    { title: 'Task 05', detail: 'http skeleton, auth middleware, testhub', model: 'opus' },
    { title: 'Task 06', detail: 'event log', model: 'opus' },
    { title: 'Task 07', detail: 'send pipeline', model: 'opus' },
    { title: 'Task 08', detail: 'loop control, budgets, rate limits', model: 'opus' },
    { title: 'Task 09', detail: 'blob store', model: 'opus' },
    { title: 'Task 10', detail: 'attachments, auto-spill', model: 'opus' },
    { title: 'Task 11', detail: '/v1/events SSE + long-poll, /v1/ack', model: 'opus' },
    { title: 'Task 12', detail: 'receipts, presence, directory', model: 'opus' },
    { title: 'Task 13', detail: 'standard webhooks', model: 'opus' },
    { title: 'Task 14', detail: 'push worker, delivery state machine', model: 'opus' },
    { title: 'Task 15', detail: 'threads, history access, groups', model: 'opus' },
    { title: 'Task 16', detail: 'approvals, jobs', model: 'opus' },
    { title: 'Task 17', detail: 'enrolment, admin API, notify, tickers', model: 'opus' },
    { title: 'Task 18', detail: 'vecnet CLI', model: 'opus' },
    { title: 'Task 19', detail: 'operator inbox UI', model: 'opus' },
    { title: 'Task 20', detail: 'docker, fly, smoke test, docs', model: 'opus' },
    { title: 'Branch review', detail: 'spec, security, test-quality lenses in parallel', model: 'opus' },
    { title: 'Branch fix', detail: 'one fix pass over verified findings', model: 'opus' },
    { title: 'Final gate', detail: 'full suite, vet, gofmt, CGO_ENABLED=0 build, ISA check', model: 'opus' },
  ],
}

// ---------------------------------------------------------------- args
const A = args || {}
// runner: prefix for every test/vet/mod command, e.g. "just bb " to run them on a build box (docs/BUILDBOX.md).
// Default "" keeps the local build2 commands. isa-check (local build + readelf) and go list checks stay local.
const RUN = typeof A.runner === 'string' ? A.runner : ''
const REPO = A.repo || '/home/xertrov/src/vecnet4'
const WT = A.worktree || `${REPO}/.worktrees/core-hub`
const BRANCH = A.branch || 'core-hub'
const BASE_REF = A.baseRef || 'master'
const STATUS = A.statusDir || `${REPO}/.scratch/vecnet/build` // gitignored via `build/`; lives in the main checkout so it survives worktree removal
const STAMP = A.runStamp || 'unstamped'
const START = Number.isInteger(A.startTask) ? A.startTask : 1
const END = Number.isInteger(A.endTask) ? A.endTask : (Array.isArray(A.tasks) ? A.tasks.length : 20)
const MAX_FIX_ROUNDS = Number.isInteger(A.maxFixRounds) ? A.maxFixRounds : 2
const OPEN_BROWSER = A.openBrowser !== false
const FINAL = typeof A.finalReview === 'boolean' ? A.finalReview : END === (Array.isArray(A.tasks) ? A.tasks.length : 20)
const PLAN = `${WT}/${A.plan || 'docs/plans/2026-11-01-core-hub.md'}` // repo-relative plan path
const PROTO = `${WT}/docs/PROTOCOL.md`
const MODEL = 'opus' // Max: Claude Code workflows pass opus explicitly
const PLAN_NAME = A.planName || 'core-hub'
const EXTRA_RULES = Array.isArray(A.extraRules) ? A.extraRules : [] // extra lines for every agent's RULES
const MERGE_CHECKS = Array.isArray(A.mergeChecks) ? A.mergeChecks : [] // commands a merge runs after the Go suite
const GATE_CHECKS = Array.isArray(A.gateChecks) ? A.gateChecks : [] // extra numbered checks for the final gate
// Model routing (skill claude-workflow-mostly-codex): taste and UI roles stay on Opus; technical roles go to Codex
// gpt-6.1-sol through a Haiku 5.5 relay and ccc, because Sol is not a native subagent model in this harness.
// {run, wait, dir}: helper paths and the assignment directory. Absent means every role runs on MODEL.
const SOL = (A.solRelay && typeof A.solRelay === 'object') ? A.solRelay : null
const RELAY_MODEL = 'claude-haiku-5-5'
const REVIEW_MODEL = A.reviewModel || MODEL // independent reviewers/lenses/gate may run on a different model (synthesis of the two drafts)

if (START < 1 || START > END) throw new Error(`bad task range ${START}..${END}`)

const nn = n => String(n).padStart(2, '0')

// ---------------------------------------------------------------- per-task data
// refs: PROTOCOL.md sections the reviewer must check against. race: reviewer also runs -race on the task's packages.
// hazards: plan gaps or traps found while drafting this workflow; given to implementer, reviewer and fixer.
const CORE_TASKS = [
  null,
  { title: 'Module skeleton and prefixed ULIDs', refs: '§1.3', race: false,
    hazards: [
      '`go mod init` under the installed Go 1.27 writes `go 1.27.x`; set the directive to exactly `go 1.26` and remove any `toolchain` line.',
      'The plan\'s justfile snippet is shorthand. In just, `build: X` makes X a dependency, so write each recipe name on its own line with an indented body. Keep the existing recipes.',
      'Add `bin/` and `.data/` to .gitignore.',
    ] },
  { title: 'Envelope types and schema validation', refs: '§1.2, §2.2, docs/protocol/envelope.schema.json, docs/protocol/ENVELOPE.md', race: false,
    hazards: [
      'jsonschema/v6 reports `additionalProperties` and `required` violations at the PARENT instance location. SchemaError.Path must be extended with the offending property so it is the JSON pointer of the violating field (tests require "sender" / "priority" in Path; Review Focus 1).',
      'Example files carry `$comment` keys to strip before validation; example 06 is an array; example 07 is .http and skipped.',
      'The `just validate-protocol` schema-diff extension must run even when python jsonschema is missing; it must fail on drift between docs/protocol/envelope.schema.json and internal/envelope/schema.json.',
    ] },
  { title: 'SQLite store and v1 schema', refs: '§1.3 (epoch, cursors), §5.1', race: true,
    hazards: [
      'STRICT tables only accept INT, INTEGER, REAL, TEXT, BLOB, ANY. Every column the plan types as `JSON` must be TEXT (a CHECK(json_valid(col)) is welcome).',
      '`db.BeginTx` issues a deferred BEGIN. BEGIN IMMEDIATE needs modernc\'s `_txlock=immediate` DSN parameter or a pinned *sql.Conn; TestTxImmediateSerialises must prove it.',
      'Seed `meta.hub_key` inside the migration (e.g. lower(hex(randomblob(32)))).',
    ] },
  { title: 'Credentials, enrolment tokens, scopes, audit', refs: '§12, §14', race: false,
    hazards: [
      'Audit "panics in tests via a vet check list" is ambiguous: implement Record so it returns an error when detail contains a forbidden key (text, body, secret); the test asserts the error.',
      'Plaintext secrets are returned once and never logged or stored; only SHA-256 hex is stored.',
    ] },
  { title: 'HTTP server skeleton, error envelope, auth middleware, test fixture, vecnet-hub init', refs: '§2.1, §2.12, §12, §15', race: false,
    hazards: [
      'testhub.NewPeer is specified via the admin API and /v1/enrol, which arrive only in Task 17. Implement it now with hub/auth helpers directly (insert principal + peer, IssueEnrolToken, Enrol) behind the same signature.',
      'Body-limit and auth middleware must wrap the whole mux, so the 413 and 401 tests pass before /v1/messages and /v1/peers routes exist.',
      'runInit(dataDir, name, w) must be a testable function; main only parses flags and calls it.',
    ] },
  { title: 'Event log', refs: '§1.3, §2.3, §2.4', race: true,
    hazards: [
      'Notify must never block: broadcast by closing a per-generation channel or non-blocking sends, never an unbuffered send.',
      'The cursor epoch comes from meta.epoch. Reads across several owners merge by the global seq.',
    ] },
  { title: 'Send pipeline: threads, recipients, fan-out, idempotency', refs: '§2.2, §5.3, §7, §14, §15, §16.3', race: true,
    hazards: [
      'Append the extra reason codes listed in the plan\'s "Self-review notes" to PROTOCOL.md §15 in this task\'s commit (one line each).',
      'TestIdempotencyRace (Review Focus 3): the lookup and insert of the idempotency row must sit inside one IMMEDIATE transaction, or rely on the UNIQUE key and replay on conflict.',
      'Serialise the Message once and embed the same bytes (json.RawMessage) in every delivery Event; never re-marshal per recipient.',
    ] },
  { title: 'Loop control, thread budget, rate limits', refs: '§6, §16.1, §16.2', race: false,
    hazards: [
      'This task introduces 60 sends/min and 20 new threads/hour per peer. Add a testhub helper (e.g. `SetLimits(peerID, sendsPerMin, threadsPerHour)`) now; Task 11\'s 200-send test and others depend on it.',
      'Rate windows and expiry use h.Now (the FakeClock), never time.Now.',
      'Operator replies reset depth to 0 (§16.1); max_depth and expires_at may only be clamped down by replies.',
    ] },
  { title: 'Blob store', refs: '§2.7, §11, §14.6', race: false,
    hazards: [
      'Reject any sha that is not 64 lowercase hex before touching the filesystem (path traversal).',
      'The daily quota is per UTC day via the injected clock. Temp files are removed on every error path, including a cancelled request body.',
    ] },
  { title: 'Attachments and auto-spill in Send', refs: '§2.2, §2.7, §11', race: false,
    hazards: [
      'Review Focus 2: the preview is cut on a rune boundary, stays valid UTF-8 and is at most the cap in bytes.',
      'The stored Message never contains blob_url; blob_url is added per delivery when Event data or a push body is built.',
    ] },
  { title: '/v1/events (SSE + long-poll) and /v1/ack', refs: '§2.3, §2.4, §5.1, §13.1', race: true,
    hazards: [
      'Make the heartbeat and the long-poll wait injectable; use deadlines at least 5x the expected time and never a fixed sleep as synchronisation.',
      'TestSlowSSEConsumerDoesNotBlockSend sends 200 messages: raise the sender\'s rate limit with the Task 8 testhub helper.',
      'The SSE handler exits on r.Context().Done() and never holds a DB transaction while writing to the client (Review Focus 4).',
    ] },
  { title: 'Receipts, presence, directory', refs: '§2.5, §2.6, §2.10, §8', race: false,
    hazards: [
      'OnIdle arrives in Task 14: leave a nil-able hook and call it only when set.',
      'The directory must not expose secrets, hashes or route URLs.',
    ] },
  { title: 'Standard Webhooks signing and verification', refs: '§3.1, §3.5', race: false,
    hazards: [
      'The known-vector test computes the expected HMAC independently with crypto/hmac in the test. Comparison uses hmac.Equal.',
    ] },
  { title: 'Push routes, push worker and delivery state machine', refs: '§3, §5.1, §5.2, §5.4, §5.5, §13.2, §14.7', race: true,
    hazards: [
      'Drive retries with the FakeClock by calling ClaimDue/PushResult or a single worker step; no real 2^n second sleeps.',
      'The http.Client never follows redirects. Body bytes and webhook-id are identical across attempts (Review Focus 5 for Retry-After).',
      'Plan Self-review note: a route added after sends must pick up queued deliveries. Add a test for it.',
    ] },
  { title: 'Threads, history access, groups, teams', refs: '§2.8, §2.9, §7, §16.3', race: false,
    hazards: [
      'CanSeeBlob replaces Task 9\'s interim participant check in internal/httpapi/blobs.go.',
    ] },
  { title: 'Approvals and jobs', refs: '§5.6, §9, §15', race: false,
    hazards: [
      'The hub-authored expiry message has sender "hub", counts against neither budget nor depth, keeps the parent chain, and must validate against the schema.',
      'Implement the job transition table exactly as §5.6 states.',
    ] },
  { title: 'Enrolment, admin API, notification webhooks, tickers', refs: '§2.12, §3.2, §5.5, §12', race: true,
    hazards: [
      'The ticker interval must be injectable so TestTickersExpire completes within about 2 seconds.',
      'Notification webhooks are fire-and-forget with bounded goroutines and never block Send.',
      'If testhub.NewPeer moves to the admin API, keep its signature so earlier tests stay unchanged.',
    ] },
  { title: 'vecnet CLI', refs: '§13.1, §13.7', race: false,
    hazards: [
      'CLI tests must never touch the real ~/.config or ~/.cache. Route config and cache paths through getenv (VECNET_CONFIG, XDG_CACHE_HOME, HOME) pointed at t.TempDir().',
      'Watch tests use a cancellable context with a deadline.',
    ] },
  { title: 'Minimal operator inbox UI', refs: '§2.3, §2.8, §13.6, §16.4', race: false,
    hazards: [
      'Max\'s rule: check port 45752 is free, then start the hub detached (`setsid nohup just run ... --dev-ui-dir internal/ui/static > ' + '<status dir>/task-19.hub.log 2>&1 &`), record its PID in the status file' + (OPEN_BROWSER ? ', then run `xdg-open http://127.0.0.1:45752/ui/` exactly once.' : '. Do NOT run xdg-open in this run.'),
      'Visual work needs visual evidence: capture headless screenshots (headless-browser-screenshots skill) of at least three states to docs/plans/shots/19-inbox-empty.png, 19-inbox-message.png, 19-thread-replied.png.',
      'The CSP forbids inline script, so JS lives in app.js. The page stays under 60 KB with no build step.',
    ] },
  { title: 'Dockerfile, fly.toml, smoke test, docs', refs: 'ADR 0001, §12', race: false,
    hazards: [
      'Never run docker, docker build or fly in this task.',
      'Run the readelf ISA check honestly. If it prints a non-zero count, report it as a deviation; do not edit DEPLOY.md to match a check you did not see pass.',
    ] },
]
// args.tasks lets the same workflow run another plan: [{title, refs, race, hazards}] in task order.
const TASKS = Array.isArray(A.tasks) ? [null, ...A.tasks.map(t => ({ title: t.title, refs: t.refs || '', race: !!t.race, hazards: Array.isArray(t.hazards) ? t.hazards : [], suite: t.suite || './...', checks: Array.isArray(t.checks) ? t.checks : [], reviewChecks: Array.isArray(t.reviewChecks) ? t.reviewChecks : (Array.isArray(t.checks) ? t.checks : []), shots: t.shots || null, route: t.route || 'opus' }))] : CORE_TASKS.map(t => t && ({ ...t, suite: './...', checks: [], reviewChecks: [], shots: null, route: 'opus' }))
const NTASKS = TASKS.length - 1
if (END > NTASKS) throw new Error(`endTask ${END} exceeds ${NTASKS} tasks`)
const routed = (n, preset) => (TASKS[n].route === 'sol' ? preset : undefined)
const checkList = cmds => cmds.map(c => `\`${c}\``).join(', ')
const suiteCmds = n => `\`${RUN}build2 go test -p 2 -count=1 ${TASKS[n].suite}\`${TASKS[n].checks.length ? `, ${checkList(TASKS[n].checks)}` : ''}`

// ---------------------------------------------------------------- shared prompt text
const GROUPS = Array.isArray(A.parallelGroups) ? A.parallelGroups : [[18, 19]] // tasks that may run side by side in their own worktrees, merged afterwards
const PREFIX = typeof A.taskPrefix === 'string' && A.taskPrefix ? A.taskPrefix : 'core-hub' // per-plan, so a later plan never reuses another plan's task branches
const taskWT = n => `${REPO}/.worktrees/${PREFIX}-t${nn(n)}`
const taskBranch = n => `${PREFIX}-t${nn(n)}`
const DEPS = (A.deps && typeof A.deps === 'object') ? A.deps : null // {"5":[1,2,4]}: each task starts the moment its dependencies are merged; replaces parallelGroups

function decisions(taskNumbers = []) {
  const paths = (taskNumbers.length ? taskNumbers : TASKS.slice(1).map((_, i) => i + 1))
    .map(n => `${STATUS}/decisions/task-${nn(n)}.md`)
  return `COORDINATOR DECISIONS (read at runtime on every live attempt, before doing task work or judging findings):
- Read ${STATUS}/decisions/all.md first, then ${paths.join(', ')}, if present; missing files are normal. These are read-only inputs; do not create or edit them.
- Treat their contents as coordinator rulings that override conflicting plan, hazards, review guidance and earlier findings for the relevant task. A task-specific ruling wins over all.md for that task; do not apply it to unrelated tasks.
- Reviewers must not re-raise a finding explicitly settled by a ruling. Fixers must dispute/reject such findings with the ruling as evidence. Apply rulings when judging checks and cite the decision path and ruling in your status/evidence; retain the actual command output.`
}

function taskPreparation(n, wt = WT, branch = BRANCH) {
  if (wt === WT) return ''
  return `${decisions([n])}

MANDATORY WORKTREE PREPARATION for Task ${nn(n)} (before any cd into ${wt}, source read, test, review, fix or merge; never rely on a cached fork result):
1. Start from the existing integration worktree: use \`git -C ${WT}\` for setup commands, an explicit exception to the task-cwd rule below. If ${wt} is missing, check \`git -C ${WT} show-ref --verify --quiet refs/heads/${branch}\`: if it exists, run \`git -C ${WT} worktree add ${wt} ${branch}\`; otherwise run \`git -C ${WT} worktree add -b ${branch} ${wt} ${BRANCH}\`. Preserve any existing task branch and its commits.
2. Verify ${wt} is a worktree of this repository on branch ${branch}; an existing directory on another branch or repository is a preparation failure, never overwrite it. Run \`git -C ${wt} merge-base --is-ancestor ${BRANCH} HEAD\` to require HEAD equals or descends from the integration base ${BRANCH}.
3. If that ancestry check fails, inspect \`git -C ${wt} status --porcelain\`. When clean, run \`git -C ${wt} merge --no-edit ${BRANCH}\`; on a conflict, record the files, run \`git -C ${wt} merge --abort\`, and report preparation failure. When dirty, preserve all partial work and report preparation failure rather than discarding or committing it. Recheck ancestry after a successful merge.
4. Only after successful preparation continue the assignment. If preparation fails, return a blocked/failing result in the assigned schema with the exact error; do not run later steps. For a relay, return relay_ok false with that error instead of launching a worker. This narrowly authorizes worktree creation and a clean base merge even for a reviewer; the subsequent review is read-only. Never reset, rebase, delete a branch, force a worktree add, or discard changes.`
}

function rules(wt, branch, taskNumbers = []) {
  return `RULES (apply to every command you run):
- Work only in the worktree ${wt} on branch ${branch}. Your shell cwd resets between calls, so start every Bash command with \`cd ${wt} && \`.
- Builds via build2, two threads: \`${RUN}build2 go test -p 2 ...\`, \`CGO_ENABLED=0 build2 go build -p 2 ...\`, \`${RUN}build2 go vet ./...\`. Never a bare \`go test\` or \`go build\`.${RUN ? ` Tests, vet and mod checks run on the build box: prefix them with \`${RUN.trim()}\` exactly as written in these instructions (env assignments go after it; use \`${RUN.trim()} bash -c '…'\` for compound commands). Builds for the ISA check, \`go list\` checks, git, gofmt and file reads stay local.` : ''}
- Never use attn. You report only through your return value and the status files.
- Never run destructive git (reset --hard, checkout -- ., restore on others' files, clean, stash drop, rebase, push, branch -D) and never modify the main checkout at ${REPO} except files under ${STATUS}.
- Network: only \`go get\` / \`go mod download\` of modules the plan names. No docker, no fly, no other downloads.
- Commits: \`git -c commit.gpgsign=false commit -m "<msg>" --trailer "Plan-Task: <NN>"\`. Stage files explicitly (no \`git add -A\` of unknown files); after committing, \`git status --porcelain\` must be empty.${EXTRA_RULES.map(r => `\n- ${r}`).join('')}

${decisions(taskNumbers)}`
}
const RULES = rules(WT, BRANCH)

const STATUS_DOC = `STATUS FILES (directory ${STATUS}, create it with mkdir -p if missing):
- Write your step file (named below) with the full JSON you return.
- Then overwrite task-<NN>.json with {"task":<n>,"title":"...","run_stamp":"${STAMP}","status":"<status>","step":"<step file name>","round":<k>,"head_sha":"<git rev-parse HEAD>"}.
Write with a quoted heredoc (cat > FILE <<'EOF' ... EOF) or the Write tool.`

function readTaskCmds(n) {
  return `Read exactly these parts of the plan (do not implement anything outside your task):
- Global Constraints, Review Focus and File structure: \`awk '/^## Global Constraints/{f=1} /^### Task 1:/{exit} f' ${PLAN}\`
- Your task: \`awk '/^### Task ${n}:/{f=1} f&&/^---$/{exit} f' ${PLAN}\`
- The PROTOCOL.md sections your task cites (${TASKS[n].refs}): find them with \`grep -n '^#' ${PROTO}\` and read them with sed -n.
- Existing code from earlier tasks as needed for the interfaces you consume.
- Inspect the plan's complete task list and file ownership to identify any later task that owns rewriting contradictory PROTOCOL/docs text; this inspection does not expand your implementation scope.

PLAN / DOCS PRECEDENCE (after coordinator rulings): if this task's plan contradicts current PROTOCOL.md or other docs AND a later task in this plan owns rewriting that text (verify its task number and file ownership), follow the plan now, leave those docs to the later task, and record the stale text as a deviation naming that later task in your step file and returned evidence. Do not refuse the task or implement the old documented behaviour for this reason. Reviewers must not flag that deliberately deferred stale text as a finding for Task ${n}; fixers must dispute such findings with the later task's ownership as evidence. Otherwise PROTOCOL.md retains precedence and unresolved contradictions are recorded as deviations.`
}

function hazardText(n) {
  return `KNOWN HAZARDS for this task (found when the workflow was drafted and already folded into the plan text; treat as requirements subject to coordinator rulings and PLAN / DOCS PRECEDENCE above):
${TASKS[n].hazards.map(h => `- ${h.replace('<status dir>', STATUS)}`).join('\n')}`
}

// ---------------------------------------------------------------- schemas
const FINDING = {
  type: 'object',
  properties: {
    severity: { type: 'string', enum: ['blocker', 'major', 'minor'] },
    category: { type: 'string', enum: ['spec', 'interface', 'test-quality', 'flaky', 'security', 'correctness', 'constraint', 'other'] },
    file: { type: 'string' },
    line: { type: 'integer' },
    issue: { type: 'string', description: 'one or two sentences: what is wrong' },
    evidence: { type: 'string', description: 'command output, code excerpt or spec quote that proves it' },
    ref: { type: 'string', description: 'PROTOCOL.md section, plan Interfaces item, Review Focus number or Global Constraint' },
    fix: { type: 'string', description: 'concrete suggested fix' },
  },
  required: ['severity', 'category', 'file', 'issue', 'evidence', 'fix'],
}

const IMPL_SCHEMA = {
  type: 'object',
  properties: {
    status: { type: 'string', enum: ['done', 'already_done', 'blocked'] },
    base_sha: { type: 'string', description: 'HEAD before you changed anything (for already_done: the parent of the first Plan-Task commit)' },
    head_sha: { type: 'string' },
    commits: { type: 'array', items: { type: 'string' } },
    red_evidence: { type: 'string', description: 'the failing output observed at Step 2 (one to five lines)' },
    green_evidence: { type: 'string', description: 'the passing output of the full suite' },
    files_changed: { type: 'array', items: { type: 'string' } },
    deviations: { type: 'array', items: { type: 'object', properties: { what: { type: 'string' }, why: { type: 'string' } }, required: ['what', 'why'] } },
    blocker: { type: 'string', description: 'when status is blocked: what stops you and what decision would unblock it' },
  },
  required: ['status', 'base_sha', 'head_sha', 'commits', 'red_evidence', 'green_evidence', 'files_changed', 'deviations'],
}

const REVIEW_SCHEMA = {
  type: 'object',
  properties: {
    verdict: { type: 'string', enum: ['pass', 'fail'] },
    tests_passed: { type: 'boolean', description: 'full suite with -count=1 passed when YOU ran it' },
    repeat_runs_passed: { type: 'boolean', description: 'task packages with -count=3 (and -race when required) passed' },
    vet_clean: { type: 'boolean' },
    gofmt_clean: { type: 'boolean' },
    commands_run: { type: 'array', items: { type: 'string' } },
    test_output_tail: { type: 'string' },
    findings: { type: 'array', items: FINDING },
    prior_resolved: { type: 'array', items: { type: 'object', properties: { issue: { type: 'string' }, resolved: { type: 'boolean' }, note: { type: 'string' } }, required: ['issue', 'resolved'] } },
  },
  required: ['verdict', 'tests_passed', 'repeat_runs_passed', 'vet_clean', 'gofmt_clean', 'commands_run', 'test_output_tail', 'findings'],
}

const FIX_SCHEMA = {
  type: 'object',
  properties: {
    status: { type: 'string', enum: ['fixed', 'partial', 'blocked'] },
    head_sha: { type: 'string' },
    commits: { type: 'array', items: { type: 'string' } },
    addressed: { type: 'array', items: { type: 'object', properties: { issue: { type: 'string' }, action: { type: 'string' } }, required: ['issue', 'action'] } },
    disputed: { type: 'array', items: { type: 'object', properties: { issue: { type: 'string' }, reason: { type: 'string' } }, required: ['issue', 'reason'] } },
    tests_passed: { type: 'boolean' },
  },
  required: ['status', 'head_sha', 'commits', 'addressed', 'disputed', 'tests_passed'],
}

const PREFLIGHT_SCHEMA = {
  type: 'object',
  properties: {
    ok: { type: 'boolean' },
    problems: { type: 'array', items: { type: 'string' } },
    head_sha: { type: 'string' },
    passed_tasks: { type: 'array', items: { type: 'integer' }, description: 'tasks whose status file says passed AND whose Plan-Task commit is on the branch' },
    dirty_files: { type: 'array', items: { type: 'string' } },
    baseline_suite: { type: 'string', enum: ['passed', 'failed', 'no-module-yet'] },
  },
  required: ['ok', 'problems', 'head_sha', 'passed_tasks', 'dirty_files', 'baseline_suite'],
}

const LENS_SCHEMA = {
  type: 'object',
  properties: {
    lens: { type: 'string' },
    summary: { type: 'string' },
    commands_run: { type: 'array', items: { type: 'string' } },
    findings: { type: 'array', items: FINDING },
  },
  required: ['lens', 'summary', 'commands_run', 'findings'],
}

const BRANCH_FIX_SCHEMA = {
  type: 'object',
  properties: {
    head_sha: { type: 'string' },
    commits: { type: 'array', items: { type: 'string' } },
    fixed: { type: 'array', items: { type: 'object', properties: { id: { type: 'string' }, note: { type: 'string' } }, required: ['id', 'note'] } },
    rejected: { type: 'array', items: { type: 'object', properties: { id: { type: 'string' }, reason: { type: 'string' } }, required: ['id', 'reason'] } },
    tests_passed: { type: 'boolean' },
  },
  required: ['head_sha', 'commits', 'fixed', 'rejected', 'tests_passed'],
}

const GATE_SCHEMA = {
  type: 'object',
  properties: {
    pass: { type: 'boolean' },
    head_sha: { type: 'string' },
    checks: { type: 'array', items: { type: 'object', properties: { name: { type: 'string' }, command: { type: 'string' }, ok: { type: 'boolean' }, output_tail: { type: 'string' } }, required: ['name', 'command', 'ok', 'output_tail'] } },
    fix_diff_concerns: { type: 'array', items: { type: 'string' } },
  },
  required: ['pass', 'head_sha', 'checks', 'fix_diff_concerns'],
}

// ---------------------------------------------------------------- helpers
// agent() returns null on transient failure or user skip. Retry once, then give up loudly; never treat null as success.
// RELAY-START
// Relay roles exist because Sol is not a native subagent model here. The relay copies nothing by hand: sol-run.sh reads the
// assignment out of the relay's own transcript, and the result comes back with a checksum this script recomputes.
const canon = v => Array.isArray(v) ? `[${v.map(canon).join(',')}]`
  : (v && typeof v === 'object') ? `{${Object.keys(v).sort().map(k => `${JSON.stringify(k)}:${canon(v[k])}`).join(',')}}`
  : JSON.stringify(v)

function fnv1a(t) {
  let h = 0x811c9dc5
  for (const ch of t) {
    const c = ch.codePointAt(0)
    const bytes = c < 0x80 ? [c] : c < 0x800 ? [0xc0 | c >> 6, 0x80 | c & 63]
      : c < 0x10000 ? [0xe0 | c >> 12, 0x80 | c >> 6 & 63, 0x80 | c & 63]
      : [0xf0 | c >> 18, 0x80 | c >> 12 & 63, 0x80 | c >> 6 & 63, 0x80 | c & 63]
    for (const b of bytes) h = Math.imul(h ^ b, 0x01000193) >>> 0
  }
  return h.toString(16)
}

const COPY_RULE = 'Copy every character of every string exactly as it is in the file, including anything that looks wrong, such as an odd path. Do not shorten, summarise, correct or reorder anything. Your copy is checksummed, and one that differs is thrown away.'

function relayCopyPrompt(base) {
  return `Codex has finished, and your only job is to copy its result. Do no other work. Never use attn.

1. Run with Bash: \`${SOL.wait} ${base}\`. It prints a line starting DONE 0 RESULT_OK <sum>.
2. Read ${base}.slim.json and return {relay_ok: true, detail: "", check: "<sum>", result: <that object>}. ${COPY_RULE}
3. If step 1 prints anything else, return {relay_ok: false, detail: <that line>} with no result field.`
}

function relayPrompt(prompt, schema, cwd, preset, base, prepare) {
  return `You are a thin relay to Codex, not a worker. Do not solve, implement, review or judge anything yourself, do not read the code, and do not copy the assignment anywhere: the helper below reads it from your transcript. Never use attn. ${prepare ? 'Your first action is the mandatory preparation below; then immediately delegate with step 1.' : 'Your first action is step 1.'}

${prepare || ''}

1. Run with Bash: \`${SOL.run} ${preset} ${cwd} ${base} '${SOL.transcripts}'\`. It returns at once. A failure here is a relay failure: go to step 4.
2. Run with Bash, passing timeout 600000: \`${SOL.wait} ${base}\`. It prints RUNNING, or a line starting DONE. Repeat this step while it prints RUNNING. A long wait is normal; some runs take more than an hour. Never start a second run while one is going.
3. If the line is DONE 0 RESULT_OK <sum>: read ${base}.slim.json and return {relay_ok: true, detail: "", check: "<sum>", result: <that object>}. ${COPY_RULE}
4. On any other outcome: repeat the mandatory preparation if supplied, then run step 1 and step 2 once more. If that also fails, return {relay_ok: false, detail: <the DONE line or error, and the last 20 lines of ${base}.log>} with no result field. Never invent or fill in a result. A wrong result is far worse than relay_ok false.

ASSIGNMENT-BEGIN ${base}
${prompt}

RESULT FILE. Your final output is a file, not your reply. Wherever these instructions say to return JSON, write exactly one JSON object satisfying this JSON Schema to ${base}.result.json:
${JSON.stringify(schema)}
Keep each string in that object under 1500 characters: full evidence belongs in the step file, and a coordinator must be able to act on the summary. Write any step and status files these instructions name as well. You are running under a coordinator with no human present: do not ask questions.
ASSIGNMENT-END ${base}`
}

// A schema-forced agent returns something even when it failed, so failure is a field, and the checksum proves the copy.
async function relayed(prompt, opts, cwd, preset, label, prepare) {
  const schema = { type: 'object', properties: { relay_ok: { type: 'boolean' }, detail: { type: 'string' }, check: { type: 'string' }, result: opts.schema }, required: ['relay_ok', 'detail'] }
  const base = `${SOL.dir}/${STAMP}-${slug(label)}`
  // A bad copy retries the copy, not the Codex run. The last attempt is on Opus: it does no work, and it copies reliably.
  for (let copy = 0; copy < 3; copy++) {
    const r = await agent(copy ? relayCopyPrompt(base) : relayPrompt(prompt, opts.schema, cwd, preset, base, prepare),
      { ...opts, label: copy ? `${label} copy ${copy}` : label, schema, model: copy === 2 ? MODEL : RELAY_MODEL, effort: 'low' })
    if (!r || !r.relay_ok || !r.result) { log(`${label}: relay failed: ${r ? r.detail : 'no result'}`); return null }
    if (fnv1a(canon(r.result)) === r.check) return r.result
    log(`${label}: copy ${copy + 1} failed its checksum`)
  }
  return null
}

const slug = t => t.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
// RELAY-END

async function call(prompt, rawOpts) {
  const { sol, cwd, prepare = '', ...opts } = rawOpts
  const assignment = prepare ? `${prepare}\n\n${prompt}` : prompt
  for (let attempt = 1; attempt <= 2; attempt++) {
    const label = attempt === 1 ? opts.label : `${opts.label} (retry)`
    const r = (SOL && sol)
      ? await relayed(assignment, opts, cwd || WT, sol, label, prepare)
      : await agent(assignment, { model: MODEL, ...opts, label })
    if (r) return r
    log(`${opts.label}: no result on attempt ${attempt}`)
  }
  return null
}

const isSerious = f => f && (f.severity === 'blocker' || f.severity === 'major')

// The script decides pass/fail from the evidence fields; the reviewer's verdict alone is not enough.
function reviewPassed(r) {
  return !!r && r.verdict === 'pass' && r.tests_passed && r.repeat_runs_passed && r.vet_clean && r.gofmt_clean &&
    !(r.findings || []).some(isSerious)
}

const halted = []
const deferredMinors = Array.isArray(A.priorMinors) ? A.priorMinors.slice() : []
const results = []

function halt(n, reason, extra) {
  const rec = { task: n, title: TASKS[n].title, reason, statusFile: `${STATUS}/task-${nn(n)}.json`, ...(extra || {}) }
  halted.push(rec)
  log(`HALT at Task ${nn(n)}: ${reason}`)
  return rec
}

// ---------------------------------------------------------------- prompts
function implPrompt(n, wt = WT, branch = BRANCH) {
  const N = nn(n)
  return `You are implementing Task ${n} ("${TASKS[n].title}") of the vecnet ${PLAN_NAME} plan, test-first.

${rules(wt, branch, [n])}

${readTaskCmds(n)}

${hazardText(n)}

PROCEDURE
0. Resume check. Run \`cd ${wt} && git rev-parse --abbrev-ref HEAD && git status --porcelain && git log ${BASE_REF}..HEAD --format='%H %s' --grep='^Plan-Task: ${N}$'\`. Trailer numbers repeat across plans, so only commits in ${BASE_REF}..HEAD count.
   - Branch must be ${branch}; otherwise return status "blocked".
   - If a Plan-Task: ${N} commit already exists and the tree is clean, verify the full suite passes and return status "already_done" with base_sha = parent of the first such commit.
   - If the tree is dirty, it is partial work from an interrupted attempt at THIS task: inspect it and continue from it. Do not discard it.
   - Record base_sha = HEAD before you change anything.
1. Write the tests listed in the task's Step 1. The plan writes many test bodies as /* comments */: turn every one into real assertions of exactly the behaviour the comment states. No t.Skip, no empty bodies, no assertion weaker than the comment. Same test names as the plan.
2. Run the task's package tests with ${RUN}build2 and confirm they FAIL for the reason the plan expects. Record the failing line(s) as red_evidence.
3. Implement the Interfaces block exactly: same package, names, signatures, error values and reason codes. Where a signature is ambiguous, apply coordinator rulings and PLAN / DOCS PRECEDENCE, then pick the reading that matches PROTOCOL.md and record a deviation. Where your task names a later task's hook, leave a nil-able seam; do not build the later task.
4. Run the task's package tests until they pass, then ${suiteCmds(n)}, \`${RUN}build2 go vet ./...\` and \`gofmt -l .\` (must print nothing). Earlier tasks' tests must still pass; the merge runs the full suite.
5. Commit with the plan's commit message for this task and the trailer Plan-Task: ${N}.
6. Step file: task-${N}.impl.json. Status for task-${N}.json: "implemented" (or "blocked").

Resolve contradictions using coordinator rulings and PLAN / DOCS PRECEDENCE first. If neither settles a contradiction safely, follow PROTOCOL.md and record a deviation. Return status "blocked" only when no reasonable reading lets you finish; say exactly what decision unblocks it.

${STATUS_DOC}

Return the JSON described by the output schema.`
}

function reviewPrompt(n, impl, round, history, wt = WT, branch = BRANCH) {
  const N = nn(n)
  const final = round >= MAX_FIX_ROUNDS
  const prior = history.length
    ? `PREVIOUS ROUNDS (verify each serious finding is now resolved and fill prior_resolved; for each item the fixer disputed, decide on the merits and drop the finding if the dispute is right):
${JSON.stringify(history, null, 1)}`
    : 'This is the first review of this task.'
  return `You are an independent reviewer for Task ${n} ("${TASKS[n].title}") of the vecnet ${PLAN_NAME} plan. Review round ${round}. You did not write this code; assume nothing the implementer claimed.

${rules(wt, branch, [n])}
- After mandatory worktree preparation, you are READ-ONLY on the worktree: do not edit, stage, commit or format any file there. You write only your status files.

${readTaskCmds(n)}

${hazardText(n)}

The work under review is \`git -C ${wt} diff ${impl.base_sha}..HEAD\` (commits with trailer Plan-Task: ${N}). The implementer reported these deviations: ${JSON.stringify(impl.deviations || [])}

CHECKS
1. Run, yourself, and record each command in commands_run:
   - \`${RUN}build2 go test -p 2 -count=1 ${TASKS[n].suite}\`${TASKS[n].reviewChecks.length ? `, ${checkList(TASKS[n].reviewChecks)}` : ''} (tests_passed: every one must pass)
${TASKS[n].reviewChecks.length ? `   - Run the last of those commands a second time; a failure on the second run sets repeat_runs_passed false.\n` : ''}   - \`${RUN}build2 go test -p 2 -count=3 <packages with changed _test.go files>\` (if no _test.go changed, skip this command)${TASKS[n].race ? ` and \`${RUN}CGO_ENABLED=1 CC=/usr/bin/gcc build2 go test -race -p 2 <same packages>\` (if cgo is unavailable, say so in test_output_tail and judge repeat_runs_passed on -count=3 alone)` : ''} (repeat_runs_passed)
   - \`${RUN}build2 go vet ./...\` (vet_clean) and \`gofmt -l .\` (gofmt_clean: prints nothing)
2. Interfaces: every item in the task's Interfaces block exists with the stated package, name, signature, status code and reason code. Use \`go doc -all ./internal/<pkg>\` and the source.
3. Tests: every test named in Step 1 exists under that name, is not a stub, and asserts the full behaviour its plan comment describes. Flag sleeps used as synchronisation, real clocks where the FakeClock is meant, fixed ports, writes outside t.TempDir(), and goroutine leaks.
4. Spec: subject to coordinator rulings and PLAN / DOCS PRECEDENCE, behaviour matches the cited PROTOCOL.md sections (${TASKS[n].refs}) and envelope.schema.json; the Global Constraints hold (byte caps, defaults, error envelope, id and time formats, CGO_ENABLED=0, secrets never in logs, URLs or audit).
5. Review Focus items this task pins are genuinely pinned by a test that would fail without the fix.
6. Hygiene: Plan-Task: ${N} trailer present, tree clean, go.mod/go.sum tidy (\`${RUN}build2 go mod tidy -diff\` prints nothing; if your go lacks -diff, skip and say so), no unrelated changes.${TASKS[n].shots ? `\n7. Visual: Read every committed PNG matching docs/plans/shots/oui/${TASKS[n].shots}-*.png with the Read tool and compare it with the prototype shot the task names. A missing or blank state, clipped or overlapping text, a light background, or a shot that does not show the state its name claims is major.` : ''}

SEVERITY: blocker = does not build, a test fails, an Interfaces item is missing or wrong, or a §14 security invariant is broken. major = spec or constraint deviation, a Step 1 test missing or stubbed, a flaky test, or an unpinned Review Focus item. minor = naming, docs, small clarity issues. Only report what you can evidence.

verdict is "pass" only with zero blocker or major findings and every check green.

Step file: task-${N}.review-r${round}.json. Status for task-${N}.json: "passed" when your verdict is pass; otherwise ${final ? '"failed" (this is the final allowed round; the workflow stops here, so make the findings self-contained for a human)' : '"changes_requested"'}.

${STATUS_DOC}

${prior}

Return the JSON described by the output schema.`
}

function fixPrompt(n, impl, round, review, history, wt = WT, branch = BRANCH) {
  const N = nn(n)
  const serious = review.findings.filter(isSerious)
  const minors = review.findings.filter(f => !isSerious(f))
  return `You are fixing review findings for Task ${n} ("${TASKS[n].title}") of the vecnet ${PLAN_NAME} plan. Fix round ${round} of at most ${MAX_FIX_ROUNDS}.

${rules(wt, branch, [n])}

${readTaskCmds(n)}

${hazardText(n)}

The task's work is \`git -C ${wt} diff ${impl.base_sha}..HEAD\`.

MUST ADDRESS (blocker and major):
${JSON.stringify(serious, null, 1)}

REVIEWER CHECK RESULTS: tests_passed=${review.tests_passed} repeat_runs_passed=${review.repeat_runs_passed} vet_clean=${review.vet_clean} gofmt_clean=${review.gofmt_clean}
Tail of the reviewer's test output:
${review.test_output_tail}

OPTIONAL (minor; fix only when cheap and clearly right):
${JSON.stringify(minors, null, 1)}

${history.length ? `Earlier rounds, for context:\n${JSON.stringify(history, null, 1)}\n` : ''}
PROCEDURE
1. For each finding, first confirm it is real. For behaviour bugs, write or tighten a test that fails, then fix. If a finding is wrong, do not change code for it: put it in disputed with evidence (spec quote, command output).
2. Fix every failing check the reviewer reported, even ones not phrased as findings.
3. Run ${suiteCmds(n)}, \`${RUN}build2 go vet ./...\`, \`gofmt -l .\`.
4. Commit "fix(task ${N}): address review round ${round}" with trailer Plan-Task: ${N}.
5. Step file: task-${N}.fix-r${round}.json. Status for task-${N}.json: "fixed_pending_review".

${STATUS_DOC}

Return the JSON described by the output schema.`
}

// ---------------------------------------------------------------- Preflight
phase('Preflight')
const pre = await call(`Preflight check before running Tasks ${START}..${END} of the vecnet ${PLAN_NAME} plan.

${RULES}
- You are READ-ONLY on the worktree. Do not fix anything you find; report it.

Check and report:
1. ${WT} exists, is a git worktree on branch ${BRANCH}, and ${BASE_REF} is an ancestor of HEAD.
2. \`git status --porcelain\` output (dirty_files). A dirty tree is acceptable only when ${STATUS}/task-NN.json for the first not-yet-passed task in range has status "implemented", "fixed_pending_review" or an in-progress state left by an interrupted run; otherwise set ok=false.
3. \`go version\` is at least 1.26; \`command -v build2\` succeeds; ${PLAN} and ${PROTO} exist.
4. For every task number t < ${START}: a commit with trailer \`Plan-Task: NN\` (two-digit) is reachable from HEAD (\`git log HEAD --grep '^Plan-Task: NN$'\`; earlier tasks may already be merged into ${BASE_REF}, so search the whole history, not only ${BASE_REF}..HEAD). A missing one sets ok=false: the base would be incomplete.
5. passed_tasks: every t in ${START}..${END} whose ${STATUS}/task-NN.json has status "passed" AND whose Plan-Task commit is on the branch.
6. Baseline: if go.mod exists, run \`${RUN}build2 go test -p 2 -count=1 ./...\`. Failure sets ok=false (never build on a red base). If go.mod does not exist, report "no-module-yet" (only valid when ${START} == 1).

Write the JSON you return to ${STATUS}/preflight-${STAMP}.json (mkdir -p first).
Return the JSON described by the output schema.`, { label: 'Preflight', phase: 'Preflight', schema: PREFLIGHT_SCHEMA, effort: 'low', sol: 'sol-coder' })

if (!pre) {
  return { status: 'halted', reason: 'preflight returned no result twice', runStamp: STAMP }
}
if (!pre.ok) {
  log(`Preflight failed: ${pre.problems.join('; ')}`)
  return { status: 'halted', reason: 'preflight', problems: pre.problems, dirty: pre.dirty_files, runStamp: STAMP }
}
const passedBefore = new Set((pre.passed_tasks || []).filter(t => t >= START && t <= END))
if (passedBefore.size) log(`Skipping tasks already passed: ${[...passedBefore].sort((a, b) => a - b).join(', ')}`)

// ---------------------------------------------------------------- Tasks
// Tasks chain on each other's interfaces, so they run in order, except the groups in GROUPS (default [[18, 19]]):
// those run side by side, each in its own worktree branched from the main worktree's HEAD, and a merge agent folds them back.
async function runTask(n, wt, branch) {
  const N = nn(n)
  const P = `Task ${N}`
  const prepare = taskPreparation(n, wt, branch)
  const impl = await call(implPrompt(n, wt, branch), { label: `T${N} implement`, phase: P, schema: IMPL_SCHEMA, sol: routed(n, 'sol-coder'), cwd: wt, prepare })
  if (!impl) return halt(n, 'implementer returned no result twice')
  if (impl.status === 'blocked') return halt(n, `implementer blocked: ${impl.blocker || 'no reason given'}`, { deviations: impl.deviations })
  if (impl.status === 'done' && (!impl.commits || impl.commits.length === 0)) return halt(n, 'implementer reported done without a commit')
  if ((impl.deviations || []).length) log(`T${N} deviations: ${impl.deviations.map(d => d.what).join(' | ')}`)

  const history = []
  let review = null
  let passed = false
  for (let round = 0; round <= MAX_FIX_ROUNDS; round++) {
    review = await call(reviewPrompt(n, impl, round, history, wt, branch), {
      label: `T${N} review r${round}`, phase: P, schema: REVIEW_SCHEMA, effort: 'high', model: REVIEW_MODEL, sol: routed(n, 'sol-reviewer'), cwd: wt, prepare,
    })
    if (!review) break
    const serious = review.findings.filter(isSerious)
    log(`T${N} review r${round}: verdict=${review.verdict} tests=${review.tests_passed} repeat=${review.repeat_runs_passed} serious=${serious.length} minor=${review.findings.length - serious.length}`)
    if (reviewPassed(review)) { passed = true; break }
    if (review.verdict === 'pass') log(`T${N} r${round}: reviewer said pass but evidence fields disagree; treating as fail`)
    if (round === MAX_FIX_ROUNDS) break

    const fix = await call(fixPrompt(n, impl, round + 1, review, history, wt, branch), { label: `T${N} fix r${round + 1}`, phase: P, schema: FIX_SCHEMA, sol: routed(n, 'sol-coder'), cwd: wt, prepare })
    if (!fix) { review = null; break }
    if (fix.status === 'blocked') log(`T${N} fixer blocked; one more review will judge the state`)
    history.push({
      round,
      findings: serious,
      checks: { tests_passed: review.tests_passed, repeat_runs_passed: review.repeat_runs_passed, vet_clean: review.vet_clean, gofmt_clean: review.gofmt_clean },
      fixer_addressed: fix.addressed,
      fixer_disputed: fix.disputed,
    })
  }

  if (!review) return halt(n, 'reviewer or fixer returned no result twice; task state unverified')
  if (!passed) return halt(n, `still failing after ${MAX_FIX_ROUNDS} fix rounds`, { findings: review.findings.filter(isSerious), checks: { tests_passed: review.tests_passed, repeat_runs_passed: review.repeat_runs_passed, vet_clean: review.vet_clean, gofmt_clean: review.gofmt_clean } })
  const minors = review.findings.filter(f => !isSerious(f))
  minors.forEach(f => deferredMinors.push({ task: n, ...f }))
  results.push({ task: n, status: 'passed', rounds: history.length, deviations: impl.deviations, minors: minors.length })
  return null
}

const MERGE_SCHEMA = {
  type: 'object',
  properties: {
    ok: { type: 'boolean' },
    head_sha: { type: 'string' },
    merged: { type: 'array', items: { type: 'integer' } },
    conflicts_resolved: { type: 'array', items: { type: 'string' } },
    tests_passed: { type: 'boolean' },
    notes: { type: 'string' },
  },
  required: ['ok', 'head_sha', 'merged', 'conflicts_resolved', 'tests_passed', 'notes'],
}

// Build the ordered list of units: a single task, or a parallel group whose members are all in range and not yet passed.
const units = []
for (let n = START; n <= END; n++) {
  if (passedBefore.has(n)) { results.push({ task: n, status: 'skipped_already_passed' }); continue }
  if (DEPS) continue
  const g = GROUPS.find(g => g.includes(n) && g.every(m => m >= START && m <= END && !passedBefore.has(m)))
  if (g) { if (!units.some(u => Array.isArray(u) && u.includes(n))) units.push([...g].sort((a, b) => a - b)) }
  else units.push(n)
}

async function forkUnit(unit) {
  const label = unit.map(nn).join('+')
  const fork = await call(`Create one worktree per task for the parallel group ${label} of the vecnet ${PLAN_NAME} build.
${RULES}
For each task number NN in [${unit.map(nn).join(', ')}]: if ${REPO}/.worktrees/${PREFIX}-tNN does not exist, run \`git -C ${REPO} worktree add -b ${PREFIX}-tNN ${REPO}/.worktrees/${PREFIX}-tNN ${BRANCH}\` (if the branch already exists from an interrupted run, \`worktree add\` without -b). If a worktree already exists and its HEAD is neither ${BRANCH}'s HEAD nor a descendant of it: commit any uncommitted changes there with \`git -C <path> add -A && git -C <path> -c commit.gpgsign=false commit -m "wip(task NN): interrupted attempt"\` (no Plan-Task trailer), then \`git -C <path> merge --no-edit ${BRANCH}\`; on a conflict run \`git -C <path> merge --abort\` and return ok false naming the files. Confirm each worktree's HEAD equals ${BRANCH}'s HEAD or is a descendant of it. Do not build or test. Return {ok, notes}.`,
    { label: `fork ${label}`, phase: `Task ${nn(unit[0])}`, effort: 'low', model: RELAY_MODEL, schema: { type: 'object', properties: { ok: { type: 'boolean' }, notes: { type: 'string' } }, required: ['ok', 'notes'] } })
  if (!fork || !fork.ok) { halt(unit[0], `could not create worktrees for ${label}: ${fork ? fork.notes : 'no result'}`); return false }
  return true
}

async function mergeUnit(unit) {
  const label = unit.map(nn).join('+')
  const merge = await call(`Merge the parallel group ${label} back into ${BRANCH} for the vecnet ${PLAN_NAME} build.
${rules(WT, BRANCH, unit)}
Branches to merge, in this order: ${unit.map(taskBranch).join(', ')} (worktrees ${unit.map(taskWT).join(', ')}).
Procedure: cd ${WT}; for each branch run \`git merge --no-ff <branch>\`; resolve any conflict by keeping BOTH tasks' behaviour (typical conflicts: route registration in internal/httpapi/server.go, justfile recipes, go.mod/go.sum, README). Never drop a test or an endpoint to make a merge clean. After all merges run \`${RUN}build2 go test -p 2 -count=1 ./...\`, \`${RUN}build2 go vet ./...\`, \`gofmt -l .\`${MERGE_CHECKS.length ? `, ${checkList(MERGE_CHECKS)}` : ''}; fix merge-induced failures with a follow-up commit carrying trailer "Plan-Task: merge". Then, only after \`git -C <path> status --porcelain\` is empty, remove each task worktree with \`git -C ${REPO} worktree remove <path>\`. Leave the task branches in place; the coordinator deletes them.
Return {ok, head_sha, merged:[task numbers], conflicts_resolved:[files], tests_passed, notes}. ok is true only when every branch merged and the suite passed.`,
    { label: `merge ${label}`, phase: `Task ${nn(unit[unit.length - 1])}`, schema: MERGE_SCHEMA, sol: 'sol-coder', prepare: unit.map(n => taskPreparation(n, taskWT(n), taskBranch(n))).join('\n\n') })
  if (!merge || !merge.ok || !merge.tests_passed) { halt(unit[unit.length - 1], `merge of ${label} failed: ${merge ? merge.notes : 'no result'}`); return false }
  log(`merged ${label} at ${merge.head_sha}; conflicts resolved: ${merge.conflicts_resolved.length}`)
  return true
}

if (DEPS) {
  // Dependency-driven: no levels, so no task waits for an unrelated slow sibling. Fork and merge touch the branch, so they run one at a time.
  // A dependency outside START..END counts as merged: earlier chunks and preflight cover it.
  const done = new Set(passedBefore)
  const started = new Set()
  const running = new Map()
  const ready = n => (DEPS[n] || []).every(d => d < START || d > END || done.has(d))
  let lock = Promise.resolve()
  const exclusive = f => { const r = lock.then(f, f); lock = r.then(() => {}, () => {}); return r }
  const one = async n => {
    if (!(await exclusive(() => forkUnit([n])))) return
    if (await runTask(n, taskWT(n), taskBranch(n))) return
    if (await exclusive(() => mergeUnit([n]))) done.add(n)
  }
  for (;;) {
    if (!halted.length) {
      for (let n = START; n <= END; n++) {
        if (done.has(n) || started.has(n) || !ready(n)) continue
        started.add(n)
        running.set(n, one(n).catch(e => { halt(n, `scheduler error: ${e && e.message}`) }).then(() => n))
      }
    }
    if (!running.size) break
    running.delete(await Promise.race(running.values())) // on a halt, in-flight tasks finish and nothing new starts
  }
  const stuck = []
  for (let n = START; n <= END; n++) if (!done.has(n)) stuck.push(n)
  if (stuck.length && !halted.length) halt(stuck[0], `dependencies never satisfied for tasks ${stuck.join(', ')}`)
}

for (const unit of units) {
  if (!Array.isArray(unit)) {
    phase(`Task ${nn(unit)}`)
    if (await runTask(unit, WT, BRANCH)) break
    continue
  }
  phase(`Task ${nn(unit[0])}`)
  if (!(await forkUnit(unit))) break
  const outcomes = await parallel(unit.map(n => () => runTask(n, taskWT(n), taskBranch(n))))
  if (outcomes.some(Boolean) || halted.length) break // a halt record is truthy; the first halt is already recorded
  if (!(await mergeUnit(unit))) break
}

if (halted.length) {
  return { status: 'halted', runStamp: STAMP, halted: halted[0], completed: results, deferredMinors, resume: `re-run with resumeFromRunId, or with args.startTask=${halted[0].task} after a human fix` }
}

if (!FINAL) {
  log(`Tasks ${START}..${END} passed; branch review skipped (finalReview=false or endTask<${NTASKS})`)
  return { status: 'tasks_passed', runStamp: STAMP, completed: results, deferredMinors }
}

// ---------------------------------------------------------------- Branch review (parallel lenses; barrier is needed: the fixer sees all findings at once to dedupe)
const LENS_EXTRA = (A.lensFocus && typeof A.lensFocus === 'object') ? A.lensFocus : {} // {spec, security, tests}: extra focus text per lens for another plan
const LENSES = [
  { key: 'spec', title: 'Spec conformance',
    focus: `Conformance of the whole branch to ${PROTO} (all of sections 1-3, 5-9, 11-12, 13.1, 13.2, 13.6, 13.7, 15, 16) and docs/protocol/envelope.schema.json: endpoints and methods, status codes, reason codes and details fields, defaults and caps, the delivery state machine and receipts, loop control, history_access, presence staleness, push headers and retry rules. Also check each task's Interfaces block in ${PLAN} against the code, and cross-task seams (hooks left nil, testhub.NewPeer, CanSeeBlob, OnIdle) are actually wired. Do not run the test suite (another lens does); read code and use go doc.` },
  { key: 'security', title: 'Security invariants',
    focus: `PROTOCOL.md §14 (all eight invariants), §12 and the plan's Global Constraints, across every handler: sender always from the credential plus as; names never resolved as addresses; vecnet.* metadata rejected; secrets hashed, compared safely, never in URLs, logs (grep every log/slog/fmt.Print call site) or audit rows; blob tokens scoped to one recipient and one sha with expiry; no redirect following in push and notify clients; body limits on every route; authz on every handler (operator-only admin, not_recipient, not_participant, not_your_peer, not_member, scopes); SQL built only with placeholders; blob paths immune to traversal; the operator UI's CSP and secret handling. Note SSRF exposure of push routes as a finding with the right severity for a single-operator hub. Do not run the test suite.` },
  { key: 'tests', title: 'Test quality and flakiness',
    focus: `Test quality across the branch. Run \`${RUN}build2 go test -p 2 -count=2 ./...\` and \`${RUN}CGO_ENABLED=1 CC=/usr/bin/gcc build2 go test -race -p 2 ./...\` (report if cgo is unavailable). Find: plan tests left as stubs or asserting less than their plan comment; sleeps used as synchronisation; real clocks where the FakeClock is meant; fixed ports; writes outside t.TempDir(), especially the CLI touching ~/.config or ~/.cache; goroutine or server leaks; tests that pass vacuously. Confirm Review Focus 1-5 in ${PLAN} are each pinned by a test that would fail if the behaviour regressed (reason it through or mutate locally WITHOUT saving: you are read-only).` },
]
if (Array.isArray(A.extraLenses)) LENSES.push(...A.extraLenses) // {key, title, focus, taste}: taste lenses stay on Opus

phase('Branch review')
const lensResults = (await parallel(LENSES.map(L => () => call(`Whole-branch review of the vecnet ${PLAN_NAME} branch, lens: ${L.title}.

${RULES}
- You are READ-ONLY on the worktree: do not edit, stage or commit anything.

Scope: \`git -C ${WT} diff ${BASE_REF}...HEAD\` (the full ${BRANCH} branch), plus the plan ${PLAN} and ${PROTO}.

FOCUS
${L.focus}${LENS_EXTRA[L.key] ? `\n\nADDITIONAL FOCUS FOR THIS PLAN:\n${LENS_EXTRA[L.key]}` : ''}

SEVERITY: blocker = broken behaviour, failing or flaky test, or a broken security invariant. major = spec deviation, missing authz, unpinned Review Focus item. minor = clarity, naming, docs. Report only what you can evidence; cite file and line.

Write the JSON you return to ${STATUS}/branch-review-${L.key}.json.
Return the JSON described by the output schema.`, { label: `Lens: ${L.title}`, phase: 'Branch review', schema: LENS_SCHEMA, effort: 'high', model: REVIEW_MODEL, sol: L.taste ? undefined : 'sol-reviewer' })
  .then(r => (r ? { key: L.key, ...r } : null))))).filter(Boolean) // tag with the lens key before filtering so ids stay stable when a lens is null

const missingLenses = LENSES.map(L => L.key).filter(k => !lensResults.some(r => r.key === k))
if (missingLenses.length) log(`Branch review: lenses with no result: ${missingLenses.join(', ')}; continuing with the rest`)

const branchFindings = []
lensResults.forEach(r => (r.findings || []).forEach((f, fi) => branchFindings.push({ id: `${r.key}-${fi + 1}`, lens: r.key, ...f })))
const seriousBranch = branchFindings.filter(isSerious)
const minorBranch = branchFindings.filter(f => !isSerious(f))
log(`Branch review: ${seriousBranch.length} serious, ${minorBranch.length} minor, ${deferredMinors.length} deferred minors from task reviews`)

// ---------------------------------------------------------------- Branch fix (one pass)
let branchFix = null
let unaccounted = []
if (seriousBranch.length || minorBranch.length || deferredMinors.length) {
  phase('Branch fix')
  const deferredWithIds = deferredMinors.map((f, i) => ({ id: `task${nn(f.task)}-minor-${i + 1}`, ...f }))
  branchFix = await call(`One fix pass over the whole-branch review of the vecnet ${PLAN_NAME} branch.

${RULES}

Read ${PLAN} (Global Constraints, Review Focus) and the PROTOCOL.md sections each finding cites.

MUST EVALUATE (blocker and major, from three independent lenses; duplicates across lenses are likely, so fix each root cause once and list every id it covers):
${JSON.stringify(seriousBranch, null, 1)}

OPTIONAL (minor, from the lenses and from per-task reviews; fix when cheap and clearly right, otherwise reject with a short reason):
${JSON.stringify(minorBranch.concat(deferredWithIds), null, 1)}

PROCEDURE
1. For each finding, confirm it is real before changing code. For behaviour bugs, write a failing test first, then fix. A finding that is wrong goes in rejected with evidence.
2. Every id above appears exactly once in fixed or rejected.
3. Run \`${RUN}build2 go test -p 2 -count=1 ./...\`, \`${RUN}build2 go vet ./...\`, \`gofmt -l .\`.
4. Commit in small logical commits, each with trailer "Plan-Task: review".
5. Write the JSON you return to ${STATUS}/branch-fix-${STAMP}.json.

Return the JSON described by the output schema.`, { label: 'Branch fix', phase: 'Branch fix', schema: BRANCH_FIX_SCHEMA })
  if (!branchFix) {
    unaccounted = seriousBranch.map(f => f.id)
    log('Branch fix returned no result twice; the gate will still run against the current HEAD')
  } else {
    const covered = new Set([...(branchFix.fixed || []).map(x => x.id), ...(branchFix.rejected || []).map(x => x.id)])
    unaccounted = seriousBranch.map(f => f.id).filter(id => !covered.has(id))
    if (unaccounted.length) log(`Branch fix left serious findings unaccounted: ${unaccounted.join(', ')}`)
  }
}

// ---------------------------------------------------------------- Final gate (one repair attempt)
function gatePrompt(attempt) {
  return `Final gate for the vecnet ${PLAN_NAME} branch (attempt ${attempt}).

${RULES}
- You are READ-ONLY on the worktree: do not edit, stage or commit anything.

Run each check yourself and record it in checks:
1. \`${RUN}build2 go test -p 2 -count=1 ./...\`
2. \`${RUN}build2 go vet ./...\`
3. \`gofmt -l .\` prints nothing
4. \`${RUN}build2 go mod tidy -diff\` prints nothing (skip with a note if unsupported)
5. \`CGO_ENABLED=0 build2 go build -p 2 -o bin/ ./cmd/...\` then \`readelf -n bin/vecnet-hub | grep -c 'x86 ISA needed'\` prints 0
6. \`just validate-protocol\` (if python jsonschema is missing, mark ok only when the schema-drift diff part passed, and say so)
7. \`git status --porcelain\` is empty apart from gitignored files
8. Every task ${START}-${END} in this invocation has verified Plan-Task evidence: read ${STATUS}/task-NN.json for each two-digit task number NN; require its task number to match, status to be \"passed\", run_stamp to be \"${STAMP}\", and head_sha to be a full commit SHA. Run \`git cat-file -e <head_sha>^{commit}\`, \`git merge-base --is-ancestor <head_sha> HEAD\`, and \`git show -s --format=%B <head_sha>\`; require every command to succeed and the recorded commit to carry the exact trailer \"Plan-Task: NN\". Any missing or mismatched evidence must fail this check; list each affected task. Do not infer coverage from a trailer count or a moving comparison against ${BASE_REF}: tasks may already have merged into ${BASE_REF}, and trailer numbers repeat across plans. Tasks outside ${START}-${END} belong to other invocations.
${GATE_CHECKS.map((c, i) => `${9 + i}. ${c}\n`).join('')}Also read the diff of commits with trailer "Plan-Task: review" and list in fix_diff_concerns anything that weakens a test, removes an assertion, or loosens a security check.

pass = every check ok and fix_diff_concerns empty.
Write the JSON you return to ${STATUS}/gate-${STAMP}-a${attempt}.json.
Return the JSON described by the output schema.`
}

phase('Final gate')
let gate = await call(gatePrompt(1), { label: 'Gate', phase: 'Final gate', schema: GATE_SCHEMA, model: REVIEW_MODEL, sol: 'sol-reviewer' })
if (gate && !gate.pass) {
  const failing = gate.checks.filter(c => !c.ok)
  log(`Gate failed: ${failing.map(c => c.name).join(', ')}${gate.fix_diff_concerns.length ? ' + fix-diff concerns' : ''}; one repair attempt`)
  const repair = await call(`Repair the failing final-gate checks on the vecnet ${PLAN_NAME} branch.

${RULES}

Failing checks:
${JSON.stringify(failing, null, 1)}
Concerns about the branch-fix diff:
${JSON.stringify(gate.fix_diff_concerns, null, 1)}

Fix root causes; never weaken or delete a test to make it pass, and never edit docs to match a check you did not see pass. Run the full suite, vet and gofmt, then commit with trailer "Plan-Task: gate". Write a short JSON {commits, notes} to ${STATUS}/gate-repair-${STAMP}.json.
Return the JSON described by the output schema.`, { label: 'Gate repair', phase: 'Final gate', sol: 'sol-coder', schema: { type: 'object', properties: { commits: { type: 'array', items: { type: 'string' } }, notes: { type: 'string' } }, required: ['commits', 'notes'] } })
  if (repair) gate = await call(gatePrompt(2), { label: 'Gate (re-run)', phase: 'Final gate', schema: GATE_SCHEMA, model: REVIEW_MODEL, sol: 'sol-reviewer' })
  else gate = { ...gate, pass: false }
}

const gateOk = !!gate && gate.pass === true
// 'complete' needs a green gate, every lens heard, and every serious finding fixed or rejected with a reason.
const status = !gateOk ? 'gate_failed' : (missingLenses.length || unaccounted.length) ? 'needs_attention' : 'complete'
return {
  status,
  missingLenses,
  unaccountedFindings: seriousBranch.filter(f => unaccounted.includes(f.id)),
  runStamp: STAMP,
  head: gate ? gate.head_sha : null,
  completed: results,
  branchReview: {
    lensesReturned: lensResults.map(r => r.key),
    serious: seriousBranch.length,
    minor: minorBranch.length,
    fixed: branchFix ? branchFix.fixed : null,
    rejected: branchFix ? branchFix.rejected : null,
  },
  gate: gate ? gate.checks.map(c => ({ name: c.name, ok: c.ok })) : null,
  gateConcerns: gate ? gate.fix_diff_concerns : ['gate returned no result'],
  statusDir: STATUS,
}
