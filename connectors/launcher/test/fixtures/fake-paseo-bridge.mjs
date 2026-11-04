#!/usr/bin/env node
import { readFileSync, writeFileSync, appendFileSync, statSync } from 'node:fs';
const args = process.argv.slice(2);
if (args.shift() !== '--json') throw new Error('expected global --json');
const state = JSON.parse(readFileSync(process.env.FAKE_PASEO_STATE, 'utf8'));
const command = args[0];
const value = flag => args[args.indexOf(flag) + 1];
const record = { args: ['--json', ...args], cwd: process.cwd() };
let out = { ok: true }, code = 0;
if (command === 'preset') out = state.presets;
else if (command === 'launch') {
  record.messageFile = value('--message-file');
  record.mode = statSync(record.messageFile).mode & 0o777;
  record.prompt = readFileSync(record.messageFile, 'utf8');
  out = state.launch ?? { agent_id: 'a1', preset: value('--preset'), mode: 'full-access' };
  if (out.agent_id) state.agents.push({ id: out.agent_id, name: value('--title'), cwd: value('--cwd'), status: 'running', archived: false });
} else if (command === 'wait') {
  const inner = state.waits.shift() ?? { agentId: args[1], status: state.agents.find(a => a.id === args[1])?.status === 'idle' ? 'idle' : 'timeout' };
  out = { ok: inner.status !== 'error', stdout: JSON.stringify(inner) };
  code = inner.status === 'error' ? 1 : 0;
  const agent = state.agents.find(a => a.id === inner.agentId);
  if (agent) agent.status = inner.status === 'idle' ? 'idle' : agent.status;
} else if (command === 'read') {
  const lines = state.reads.shift() ?? ['No activity to display.'];
  out = { id: args[1], after: Number(value('--after')), lines };
} else if (command === 'ls') out = state.agents.filter(a => args.includes('--all') || !a.archived);
else if (command === 'doctor') { out = state.doctor; code = out.ok ? 0 : 1; }
else if (command === 'send') { state.sends = (state.sends ?? 0) + 1; code = state.sendCode ?? 0; }
else if (command === 'stop') { const agent = state.agents.find(a => a.id === args[1]); if (agent) agent.status = 'idle'; }
else if (command === 'settle') {
  const agent = state.agents.find(a => a.id === args[1]);
  if (agent?.status === 'running') { code = 1; out = { ok: false, error: 'refusing to settle running agent without --force' }; }
  else if (agent) agent.archived = true;
} else throw new Error('unexpected command');
appendFileSync(process.env.FAKE_PASEO_LOG, JSON.stringify(record) + '\n');
writeFileSync(process.env.FAKE_PASEO_STATE, JSON.stringify(state));
console.log(JSON.stringify(out));
process.exitCode = code;
