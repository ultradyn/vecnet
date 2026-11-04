import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { directoryEntry, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { DirectoryEntry } from '../../hub-client/src/wire.ts';
import type { LaunchPermission } from '../src/backend.ts';
import type { LaunchRequest } from '../src/contract/launch-input.ts';
import { parseConfig } from '../src/config.ts';
import type { LauncherConfig } from '../src/config.ts';
import { createFakeBackend } from './fake-backend.ts';
import { createDirectory } from '../src/directory.ts';
import { policyKeyFor, ceilingFor, gateLaunch, classifyPermission, routeApproval } from '../src/policy.ts';

const peer = 'peer_00000000000000000000000001';
const sibling = 'peer_00000000000000000000000002';
const principal = 'prn_00000000000000000000000001';
const now = Date.parse('2026-11-01T00:00:00Z');
function config(): LauncherConfig {
  return parseConfig({host:'test',hub:{url:'http://localhost',credential_file:'/fake/credential'},state_dir:'/fake/state',
    backends:[{id:'t3',module:'./backends/t3/index.ts',config:{}}],
    projects:{vecnet4:{repo_root:'/fake/vecnet4',base_ref:'master'}},policies:{default:{projects:['vecnet4']}}});
}
async function setup(peers: DirectoryEntry[] = [directoryEntry({peer_id:peer,created_by:principal})]) {
  const dir = createDirectory({hub:{async peers() {return peers;}},log:memoryLog()});
  await dir.refresh();
  const caps = await createFakeBackend({id:'t3',caps:{backgroundTasks:'opaque',presets:[{id:'sol',provider:'fake',model:'fake'}]}}).capabilities();
  return {dir,cfg:config(),sender:peer,now,launchesLastHour:0,
    req:{jobId:'job_test',threadId:'thr_test',messageId:'msg_test',requester:peer,instructions:'untrusted',
      expiresAt:'2026-11-08T00:00:00Z',input:{backend:'t3',project:'vecnet4',preset:'sol',permission_mode:'ask'}} as LaunchRequest,
    counts:{unparked:0,open:0,byKey:{} as Record<string,{unparked:number;open:number}>},
    backends:new Map([['t3',{caps,health:{ok:true,version:'test'}}]])};
}
function permission(patch: Partial<LaunchPermission> = {}): LaunchPermission {
  const base = JSON.parse(readFileSync(new URL('./fixtures/permission/valid/edit.json',import.meta.url),'utf8')) as LaunchPermission;
  return {...base,...patch};
}
function denied(reason: string) { return {ok:false,error:'policy_denied',reason}; }

test('policyKeyFor: peer entry beats principal entry; principal entry matches a new peer by created_by with no config change; default otherwise', async () => {
  const peers = [directoryEntry({peer_id:peer,created_by:principal})];
  const a = await setup(peers);
  a.cfg.policies[principal] = {max_sessions:2};
  a.cfg.policies[peer] = {max_sessions:1};
  assert.deepEqual(policyKeyFor(peer,a.dir,a.cfg),{key:peer,source:'peer'});
  peers.push(directoryEntry({peer_id:sibling,created_by:principal}));
  assert.equal(await a.dir.ensureKnown(sibling),true);
  assert.deepEqual(policyKeyFor(sibling,a.dir,a.cfg),{key:principal,source:'principal'});
  delete a.cfg.policies[peer];
  assert.deepEqual(policyKeyFor(peer,a.dir,a.cfg),{key:principal,source:'principal'});
  assert.deepEqual(policyKeyFor('peer_unknown',a.dir,a.cfg),{key:'default',source:'default'});
  delete a.cfg.policies[principal];
  assert.deepEqual(policyKeyFor(sibling,a.dir,a.cfg),{key:'default',source:'default'});
});

test('ceilingFor merges an entry over default field by field', () => {
  const cfg = config();
  cfg.policies[principal] = {max_sessions:0,projects:[],permission_modes:['read-only']};
  const before = structuredClone(cfg);
  assert.deepEqual(ceilingFor({key:principal,source:'principal'},cfg),{
    backends:['t3','paseo'],permission_modes:['read-only'],projects:[],max_sessions:0,max_open_sessions:12,launches_per_hour:30});
  assert.deepEqual(ceilingFor({key:'default',source:'default'},cfg),cfg.policies.default);
  assert.deepEqual(cfg,before);
});

test('gateLaunch refusal order and reasons (one case per step), never a downgrade', async () => {
  // All later checks are invalid too; restoring each earlier condition exposes the next refusal.
  const a = await setup();
  a.sender = 'peer_unknown';
  a.req.input = {backend:'missing',preset:'bad',permission_mode:'full-access',project:'/etc',idle:{wrapup_after:'9h'}};
  a.req.expiresAt = '2026-11-01T00:01:00Z';
  a.counts = {unparked:100,open:100,byKey:{default:{unparked:100,open:100}}};
  assert.deepEqual(gateLaunch(a),denied('sender_kind'));
  for (const kind of ['source','launched']) {
    const b = await setup([directoryEntry({peer_id:peer,kind})]);
    assert.deepEqual(gateLaunch(b),denied('sender_kind'));
  }
  a.sender = peer;
  assert.deepEqual(gateLaunch(a),denied('backend'));
  a.req.input.backend = 't3';
  a.cfg.policies.default.backends = ['paseo'];
  assert.deepEqual(gateLaunch(a),denied('backend'));
  a.cfg.policies.default.backends = ['t3'];
  a.backends.get('t3')!.health.ok = false;
  const unhealthy = gateLaunch(a);
  assert.equal(unhealthy.ok,false);
  if (!unhealthy.ok) { assert.equal(unhealthy.error,'backend_unhealthy'); assert.ok('reason' in unhealthy && unhealthy.reason.length > 0); }
  a.backends.get('t3')!.health.ok = true;
  assert.deepEqual(gateLaunch(a),denied('preset'));
  a.req.input.preset = 'sol';
  a.cfg.policies.default.permission_modes = ['ask'];
  assert.deepEqual(gateLaunch(a),denied('permission_mode'));
  a.cfg.policies.default.permission_modes = ['full-access'];
  a.backends.get('t3')!.caps.permissionModes = ['ask'];
  assert.deepEqual(gateLaunch(a),denied('permission_mode'));
  a.backends.get('t3')!.caps.permissionModes = ['full-access'];
  assert.deepEqual(gateLaunch(a),denied('project'));
  a.req.input.project = 'vecnet4';
  a.cfg.policies.default.projects = [];
  assert.deepEqual(gateLaunch(a),denied('project'));
  a.cfg.policies.default.projects = ['*'];
  a.cfg.projects.vecnet4.backends = ['paseo'];
  assert.deepEqual(gateLaunch(a),denied('project'));
  a.cfg.projects.vecnet4.backends = ['t3'];
  assert.deepEqual(gateLaunch(a),denied('idle'));
  a.req.input.idle = {wrapup_after:'2h'};
  assert.deepEqual(gateLaunch(a),{...denied('expires_too_soon'),details:{expires_at:a.req.expiresAt,needed_ms:7800000}});
  a.req.expiresAt = '2026-11-08T00:00:00Z';
  assert.deepEqual(gateLaunch(a),{ok:false,error:'cap_reached',details:{limit:'limits.max_sessions',running:100,open:100}});
  const b = await setup();
  b.backends.clear();
  assert.deepEqual(gateLaunch(b),denied('backend'));
  const c = await setup();
  c.cfg.backends = [];
  assert.deepEqual(gateLaunch(c),denied('backend'));
  assert.equal(a.req.input.permission_mode,'full-access','a denied mode must not be replaced');
  assert.equal(a.req.input.backend,'t3');
  assert.equal(a.req.input.preset,'sol');
  assert.equal(a.req.input.project,'vecnet4');
});

test('gateLaunch: expires_too_soon when expires_at leaves less than settle + 10 min; deadlineAt = min(expires_at, now+72h)', async () => {
  for (const [backgroundTasks,settleMs,expiresAt] of [
    ['opaque',7200000,'2026-11-01T02:10:00Z'],
    ['none',7200000,'2026-11-01T02:10:00Z'],
    ['observable',86400000,'2026-11-02T00:10:00Z'],
  ] as const) {
    const a = await setup();
    a.backends.get('t3')!.caps.backgroundTasks = backgroundTasks;
    a.req.expiresAt = new Date(Date.parse(expiresAt)-1).toISOString();
    assert.deepEqual(gateLaunch(a),{...denied('expires_too_soon'),details:{expires_at:a.req.expiresAt,needed_ms:settleMs+600000}});
    a.req.expiresAt = expiresAt;
    const result = gateLaunch(a);
    assert.ok(result.ok);
    assert.equal(result.deadlineAt,Date.parse(expiresAt));
    assert.equal(result.settleMs,settleMs);
    a.req.expiresAt = '2026-11-08T00:00:00Z';
    const capped = gateLaunch(a);
    assert.ok(capped.ok);
    assert.equal(capped.deadlineAt,Date.parse('2026-11-04T00:00:00Z'));
    a.req.input.idle = {wrapup_after:'30m'};
    const custom = gateLaunch(a);
    assert.ok(custom.ok); assert.equal(custom.settleMs,1800000);
    a.req.input.idle.wrapup_after = 3600000;
    const numeric = gateLaunch(a);
    assert.ok(numeric.ok); assert.equal(numeric.settleMs,3600000);
    a.cfg.limits.max_session_lifetime_ms = 3600000;
    assert.deepEqual(gateLaunch(a),{...denied('expires_too_soon'),details:{expires_at:a.req.expiresAt,needed_ms:4200000}});
  }
});

test('gateLaunch: D1 any requester may use full-access by default', async () => {
  for (const kind of ['session','grokbot','pagchat-bot','launcher','dot','operator','openclaw']) {
    const a = await setup([directoryEntry({peer_id:peer,kind,name:'untrusted name',capabilities:['deny-all']})]);
    a.req.requester = 'peer_body_spoof';
    a.req.input.permission_mode = 'full-access';
    a.req.input.base_ref = 'topic';
    const original = structuredClone(a.req);
    assert.deepEqual(gateLaunch(a),{ok:true,key:{key:'default',source:'default'},ceiling:a.cfg.policies.default,
      project:{alias:'vecnet4',repoRoot:'/fake/vecnet4',baseRef:'topic'},deadlineAt:Date.parse('2026-11-04T00:00:00Z'),settleMs:7200000});
    assert.deepEqual(a.req,original);
    delete a.req.input.base_ref;
    const defaultRef = gateLaunch(a);
    assert.ok(defaultRef.ok); assert.equal(defaultRef.project.baseRef,'master');
  }
});

test("gateLaunch caps: launcher-wide first, then the key's; a principal key caps all its peers together; details.limit names the cap", async () => {
  const a = await setup([directoryEntry({peer_id:peer,created_by:principal}),directoryEntry({peer_id:sibling,created_by:principal})]);
  a.cfg.policies[principal] = {};
  a.counts = {unparked:6,open:24,byKey:{[principal]:{unparked:3,open:12}}};
  a.launchesLastHour = 30;
  const expectCap = (limit: string,running:number,open:number) => assert.deepEqual(gateLaunch(a),{ok:false,error:'cap_reached',details:{limit,running,open}});
  expectCap('limits.max_sessions',6,24);
  a.counts.unparked = 5;
  expectCap('limits.max_open_sessions',5,24);
  a.counts.open = 23;
  expectCap('max_sessions',3,12);
  a.sender = sibling;
  expectCap('max_sessions',3,12);
  a.counts.byKey[principal].unparked = 2;
  expectCap('max_open_sessions',2,12);
  a.counts.byKey[principal].open = 11;
  expectCap('launches_per_hour',2,11);
  a.launchesLastHour = 29;
  assert.ok(gateLaunch(a).ok);
  a.counts.byKey = {unrelated:{unparked:100,open:100}};
  assert.ok(gateLaunch(a).ok,'other keys do not spend this key\'s cap');
  a.cfg.policies[principal].max_sessions = 0;
  expectCap('max_sessions',0,0);
});

test('classifyPermission: sudo after && is privileged; ~/.ssh write is privileged; reported ACP kind is unverifiable', () => {
  for (const word of ['sudo','su','doas','pkexec','/usr/bin/sudo']) {
    for (const line of [word+' true','echo ok && '+word+' true','echo ok; '+word+' true','true || '+word+' true','echo ok | '+word+' true','true\n'+word+' true']) {
      assert.ok(classifyPermission(permission({kind:'exec',command:{line,shell_operators:true}}),'/home/test').includes('privileged_exec'),line);
    }
    assert.ok(classifyPermission(permission({command:{argv:[word,'true'],shell_operators:false}}),'/home/test').includes('privileged_exec'));
  }
  // Fail closed: a privileged name anywhere in `line` counts, even as data.
  for (const line of ['echo sudo','echo "sudo"','echo "safe && sudo no"','echo /usr/bin/sudo']) {
    assert.ok(classifyPermission(permission({command:{line,shell_operators:false}}),'/home/test').includes('privileged_exec'),line);
  }
  for (const line of ['echo sudoer','echo pseudo','ls -la /tmp','echo summary','git status','cat super.txt']) {
    assert.equal(classifyPermission(permission({command:{line,shell_operators:false}}),'/home/test').includes('privileged_exec'),false,line);
  }
  for (const path of ['/etc','/etc/passwd','/usr/bin/file','/home/test/.ssh/key','~/.ssh/key','/home/test/.config/app']) {
    assert.ok(classifyPermission(permission({paths:[{path,access:'write',in_workspace:false}]}),'/home/test').includes('privileged_exec'),path);
  }
  for (const path of ['/etcetera/file','/usr2/file','/home/test/.ssh-backup/key','/home/other/.ssh/key']) {
    assert.equal(classifyPermission(permission({paths:[{path,access:'write',in_workspace:false}]}),'/home/test').includes('privileged_exec'),false,path);
  }
  for (const access of ['write','delete','move'] as const) {
    assert.ok(classifyPermission(permission({outside_workspace:true,paths:[{path:'/outside/file',access,in_workspace:false}]}),'/home/test').includes('outside_workspace_writes'));
  }
  for (const outside_workspace of [false,'unknown'] as const) {
    assert.equal(classifyPermission(permission({outside_workspace}),'/home/test').includes('outside_workspace_writes'),false);
  }
  assert.equal(classifyPermission(permission({outside_workspace:true,paths:[{path:'/outside',access:'read',in_workspace:false}]}),'/home/test').includes('outside_workspace_writes'),false);
  assert.deepEqual(classifyPermission(permission({fidelity:{kind:'reported',command:'none',paths:'reported',host:'none'}}),'/home/test'),['edit','unverifiable']);
  assert.deepEqual(classifyPermission(permission({fidelity:{kind:'enforced',command:'none',paths:'none',host:'none'}}),'/home/test'),['edit','unverifiable']);
  assert.deepEqual(classifyPermission(permission(),'/home/test'),['edit']);
});

const substitutionCases = [
  {line:'echo "$(sudo id)"',privileged:true},
  {line:'echo `sudo id`',privileged:true},
  {line:'echo $(su user)',privileged:true},
  {line:'echo "`doas id`"',privileged:true},
  {line:'echo "$(echo "$(pkexec id)")"',privileged:true},
  {line:'echo "$(echo `/usr/bin/sudo id`)"',privileged:true},
  {line:'echo `echo $(sudo id)`',privileged:true},
  {line:'echo `echo \\`sudo id\\``',privileged:true},
  {line:'echo "$(printf \'%s\' \')\'; sudo id)"',privileged:true},
  {line:'echo "$( (echo safe); sudo id)"',privileged:true},
  {line:'echo "$(echo safe)" && sudo id',privileged:true},
  {line:'echo "$(echo safe)$(sudo id)"',privileged:true},
  {line:'echo \'$(sudo id)\'',privileged:true},
  {line:'echo \'`sudo id`\'',privileged:true},
  {line:'echo "\\$(sudo id)"',privileged:true},
  {line:'echo \\`sudo id\\`',privileged:true},
  {line:'echo "\\`sudo id\\`"',privileged:true},
  {line:'echo "$(echo sudo)"',privileged:true},
  {line:'echo `echo sudo`',privileged:true},
  {line:'echo `echo \'$(sudo id)\'`',privileged:true},
  {line:'echo "$(echo \'$(sudo id)\')"',privileged:true},
  {line:'echo "$(echo \'`sudo id`\')"',privileged:true},
  {line:'echo "$(echo \'safe && sudo id\')"',privileged:true},
  {line:'echo "$(echo safe)" sudo',privileged:true},
];

// Exercise the same shell words at both classification and routing boundaries.
const prefixCases = [
  ...[
    'FLAG=1', 'FLAG=', 'FLAG+=one', 'FLAG="two words"', "FLAG='safe && sudo no'", 'FLAG=one\\ two',
    'FLAG=1 OTHER="two words"', 'FLAG=$(echo safe)', 'FLAG=`echo safe`',
    '>/dev/null', '> /dev/null', '2>/dev/null', '2>> /dev/null', '< /dev/null',
    '2>&1', '0<&3', '2>"two words"', '<<< safe', '<> /dev/null', '>| /dev/null', '&>/dev/null', '&>> /dev/null',
    'FLAG=1 2>/dev/null OTHER="two words" > /dev/null',
  ].flatMap(prefix => [
    `${prefix} sudo id`,
    `echo safe && ${prefix} sudo id`,
    `echo safe; ${prefix} sudo id`,
    `echo safe || ${prefix} sudo id`,
    `echo safe | ${prefix} sudo id`,
    `echo safe\n${prefix} sudo id`,
    `echo "$(${prefix} sudo id)"`,
    'echo `' + prefix + ' sudo id`',
  ].map(line => ({line,privileged:true}))),
  ...['su','doas','pkexec','/usr/bin/sudo'].map(word => ({line:`FLAG=1 2>/dev/null ${word} id`,privileged:true})),
  ...[
    'echo "$(FLAG=1 >/dev/null echo "$(OTHER=2 sudo id)")"',
    'FLAG="$(OTHER=2 sudo id)" echo safe',
    '>"$(FLAG=1 sudo id)" echo safe',
    'FLAG=1>/dev/null sudo id',
  ].map(line => ({line,privileged:true})),
  ...[
    'FLAG="safe && sudo id" echo safe', "FLAG='$(sudo id)' echo safe",
    'FLAG=1 echo sudo', '>sudo echo safe', '>"sudo" echo safe', 'echo safe >sudo',
    '2>&1 echo sudo', '>"safe && sudo id" echo safe', '>FLAG=1 echo sudo', '<<<"sudo id" echo safe',
    'echo "FLAG=1 sudo id"', 'echo ">/dev/null sudo id"',
    'echo \'>/dev/null sudo id\'', 'echo \'$(FLAG=1 sudo id)\'',
    'echo "\\$(FLAG=1 sudo id)"', 'echo \\`FLAG=1 sudo id\\`',
    '"FLAG"=1 sudo id', "'FLAG=1' sudo id", 'FLAG\\=1 sudo id', '1FLAG=1 sudo id',
    '\\2>/dev/null sudo id', '\'2\'>/dev/null sudo id',
    'echo "$(>sudo echo safe)"', 'echo "$(FLAG=1 echo sudo)"',
    'echo "$(echo safe)" FLAG=1 sudo id',
    'echo \\>sudo', 'echo "safe && FLAG=1 sudo id"',
  ].map(line => ({line,privileged:true})),
];
const shellSyntaxCases = [
  ...[
    (command: string) => `! ${command}`,
    (command: string) => `{ ${command}; }`,
    (command: string) => `if true; then ${command}; fi`,
    (command: string) => `if ${command}; then :; fi`,
    (command: string) => `if false; then :; elif ${command}; then :; fi`,
    (command: string) => `if false; then :; else ${command}; fi`,
    (command: string) => `while ${command}; do :; done`,
    (command: string) => `until ${command}; do :; done`,
    (command: string) => `for item in safe; do ${command}; done`,
    (command: string) => `function check { ${command}; }`,
    (command: string) => `time -p ${command}`,
    (command: string) => `case safe in safe) ${command};; esac`,
    (command: string) => `! { if true; then ${command}; fi; }`,
    (command: string) => `if ! { ${command}; }; then :; fi`,
  ].flatMap(compound => ['', 'FLAG="two words" ', '2>/dev/null ', 'FLAG=1 2>&1 '].flatMap(prefix => {
    const body = compound(`${prefix}sudo id`);
    return [body, `echo safe && ${body}`, `echo "$(${body})"`, 'echo `' + body + '`'];
  })).map(line => ({line,privileged:true})),
  ...['su','doas','pkexec','/usr/bin/sudo'].map(word => ({line:`! { FLAG=1 > /dev/null ${word} id; }`,privileged:true})),
  ...[
    'if true; then FLAG="$(sudo id)" echo safe; fi',
    '! >"$(sudo id)" echo safe',
    '! echo "$(if true; then FLAG=1 sudo id; fi)"',
    '{ echo "$(echo safe && ! sudo id)"; }',
    '! "sudo" id', '{ \\sudo id; }',
    'echo "$(case safe in (other) :;; (safe|also) sudo id;; esac)"',
    'echo "$(case safe in safe) case inner in inner) sudo id;; esac;; esac)"',
    'echo "$(case safe in other) :;& safe) sudo id;;& *) :;; esac)"',
    'echo "$(case "$(sudo id)" in safe) :;; esac)"',
    'echo "$(case safe in "$(sudo id)") :;; esac)"',
    'echo "$(case safe in safe) :;; esac)$(sudo id)"',
    'time sudo id', 'time ! { sudo id; }', 'function check() { sudo id; }',
  ].map(line => ({line,privileged:true})),
  ...['!', '{', 'if', 'then', 'elif', 'else', 'while', 'until', 'do', 'time', 'function'].flatMap(word => [
    `"${word}" sudo id`, `'${word}' sudo id`, `\\${word} sudo id`,
    `echo ${word} sudo id`, `>${word} echo sudo`, `FLAG=${word} echo sudo`,
  ]).map(line => ({line,privileged:true})),
  ...[
    'echo "safe && ! sudo id"', "echo 'safe && { sudo id; }'", 'echo "if true; then sudo id; fi"',
    'echo \'$(if true; then sudo id; fi)\'', 'echo "\\$(echo safe && ! sudo id)"',
    'echo \\`! sudo id\\`',
    'echo !sudo id', '!sudo id', '{sudo id; }',
    'if true; then echo sudo; fi', 'while echo sudo; do :; done',
    'for item in sudo doas pkexec; do echo "$item"; done',
    'case sudo in sudo) echo safe;; esac',
    'echo "$(case sudo in (sudo|doas) echo safe;; esac)"',
    'echo "$(case safe in safe) echo safe;; sudo) :;; esac)" sudo',
    'echo "$(case safe in safe) case sudo in sudo) :;; esac;; esac)" sudo',
    'echo "$(case safe in safe) :;; esac)" sudo',
    'function sudo { echo safe; }', 'function sudo() { echo safe; }',
    'time -p echo sudo', 'time -p "!" sudo id',
    '! FLAG="safe && sudo id" echo safe', '{ >sudo echo safe; }',
  ].map(line => ({line,privileged:true})),
];
// Bypasses found against the old shell parser (review round 3 and likely next ones).
const bypassCases = [
  'echo safe # "\nsudo id', "echo safe # '\nsudo id", 'echo safe #\nsudo id',
  'env sudo id', 'env FOO=1 sudo id', "bash -c 'sudo id'", 'sh -c "sudo id"', 'eval "sudo id"',
  'bash <<EOF\nsudo id\nEOF', 'echo id | xargs sudo', 'nohup sudo id', 'exec sudo id', 'command sudo id',
  "s''udo id", 's\\udo id', '${X:-sudo} id', 'echo "unterminated\nsudo id',
  // Backslash-newline is a line continuation: the shell joins the halves.
  's\\\nudo id', 'su\\\r\ndo id', "bash -c 's\\\nudo id'", 'echo ok; d\\\no\\\nas id', 'pk\\\nexec id', 'env s\\\nu id',
].map(line => ({line,privileged:true}));
// Coordinator decision (2026-11-05): classification fails closed, so every table
// entry that names a privileged command is privileged, including ones that are
// only data to a real shell. Only lines that never spell a privileged name stay inert.
const commandCases = [...substitutionCases,...prefixCases,...shellSyntaxCases,...bypassCases,
  ...['echo safe','ls /tmp','echo sudoer pseudo','git log --oneline'].map(line => ({line,privileged:false}))];

test('classifyPermission: fails closed on any privileged name in line (substitutions, prefixes, shell syntax, comments, wrappers); lines without one stay inert', async t => {
  for (const {line,privileged} of commandCases) {
    await t.test(line, () => {
      const p = permission({kind:'exec',paths:[],command:{line,shell_operators:true},
        fidelity:{kind:'enforced',command:'line',paths:'none',host:'none'}});
      assert.deepEqual(classifyPermission(p,'/home/test'),privileged ? ['exec','privileged_exec'] : ['exec']);
    });
  }
});

test('routeApproval: the privileged preset sends executable substitutions, prefixes and shell compounds to the operator', async t => {
  const a = await setup();
  a.cfg.approvals.to_operator.presets = ['privileged'];
  for (const {line,privileged} of commandCases) {
    await t.test(line, () => {
      const p = permission({kind:'exec',paths:[],command:{line,shell_operators:true},
        fidelity:{kind:'enforced',command:'line',paths:'none',host:'none'}});
      const route = routeApproval(p,{requester:peer,policyKey:'default'},a.dir,a.cfg,'/home/test');
      assert.equal(route.to,privileged ? 'operator' : 'requester');
      assert.deepEqual(route.categories,privileged ? ['exec','privileged_exec'] : ['exec']);
    });
  }
});

test('routeApproval default (D10 presets none): everything goes to the requester with escalate option', async () => {
  const a = await setup();
  const route = routeApproval(permission({outside_workspace:true,paths:[{path:'/etc/passwd',access:'delete',in_workspace:false}]}),{requester:peer,policyKey:'default'},a.dir,a.cfg,'/home/test');
  assert.deepEqual(route,{to:'requester',escalateOption:true,escalateToOperator:true,parentExpiresMs:900000,onParentExpiry:'deny',categories:['edit','outside_workspace_writes','privileged_exec']});
  a.cfg.approvals.on_parent_expiry = 'escalate';
  assert.equal(routeApproval(permission(),{requester:peer,policyKey:'default'},a.dir,a.cfg,'/home/test').onParentExpiry,'escalate');
});

test('routeApproval presets outside-workspace + privileged; categories list; to_operator.requesters by principal', async () => {
  const a = await setup();
  const job = {requester:peer,policyKey:'default'};
  const outside = permission({outside_workspace:true,paths:[{path:'/outside',access:'write',in_workspace:false}]});
  const privileged = permission({command:{argv:['sudo','true'],shell_operators:false}});
  a.cfg.approvals.to_operator.presets = ['outside-workspace','privileged'];
  a.cfg.approvals.on_parent_expiry = 'escalate';
  for (const p of [outside,privileged]) {
    const route = routeApproval(p,job,a.dir,a.cfg,'/home/test');
    assert.equal(route.to,'operator'); assert.equal(route.escalateOption,false); assert.equal(route.escalateToOperator,false);
    assert.equal(route.parentExpiresMs,3600000); assert.equal(route.onParentExpiry,'deny');
  }
  assert.equal(routeApproval(permission(),job,a.dir,a.cfg,'/home/test').to,'requester');
  a.cfg.approvals.to_operator.presets = ['none'];
  a.cfg.approvals.to_operator.categories = ['network'];
  assert.equal(routeApproval(permission({kind:'network'}),job,a.dir,a.cfg,'/home/test').to,'operator');
  a.cfg.approvals.to_operator.categories = [];
  a.cfg.approvals.to_operator.requesters = [principal];
  // Membership must use hub provenance even without a principal ceiling entry.
  assert.equal(routeApproval(permission(),job,a.dir,a.cfg,'/home/test').to,'operator');
  a.cfg.approvals.to_operator.requesters = [peer];
  assert.equal(routeApproval(permission(),job,a.dir,a.cfg,'/home/test').to,'operator');
  a.cfg.approvals.to_operator.requesters = [sibling];
  assert.equal(routeApproval(permission(),job,a.dir,a.cfg,'/home/test').to,'requester');
});

test('routeApproval relay for a pagd principal: 35 min, no escalate option, escalate_to_operator false', async () => {
  const a = await setup([directoryEntry({peer_id:peer,created_by:principal}),directoryEntry({peer_id:sibling,name:principal,capabilities:[principal]})]);
  a.cfg.relays.pagd = {principals:[principal],parent_expires_ms:2100000,escalate_option:false,escalate_to_operator:false};
  a.cfg.approvals.to_operator.categories = ['edit'];
  a.cfg.approvals.to_operator.requesters = [principal];
  a.cfg.approvals.on_parent_expiry = 'escalate';
  assert.deepEqual(routeApproval(permission(),{requester:peer,policyKey:'default'},a.dir,a.cfg,'/home/test'),{
    to:'relay',escalateOption:false,escalateToOperator:false,parentExpiresMs:2100000,onParentExpiry:'deny',categories:['edit']});
  assert.equal(routeApproval(permission(),{requester:sibling,policyKey:principal},a.dir,a.cfg,'/home/test').to,'operator','category routing still applies to non-relays');
  a.cfg.approvals.to_operator.categories = [];
  assert.equal(routeApproval(permission(),{requester:sibling,policyKey:principal},a.dir,a.cfg,'/home/test').to,'requester','names, tags and caller-provided keys cannot forge relay or principal membership');
  a.cfg.relays.pagd.peers = [sibling];
  a.cfg.relays.pagd.escalate_option = true;
  a.cfg.relays.pagd.escalate_to_operator = true;
  const listed = routeApproval(permission(),{requester:sibling,policyKey:'default'},a.dir,a.cfg,'/home/test');
  assert.equal(listed.to,'relay'); assert.equal(listed.escalateOption,true); assert.equal(listed.escalateToOperator,true); assert.equal(listed.onParentExpiry,'escalate');
});
