import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('harness', Path(__file__).with_name('claude-delivery-e2e.py'))
harness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(harness)


class ProofTests(unittest.TestCase):
    def test_lifecycle_requires_order_and_exact_delivery(self):
        admission = dict(msg='claude_delivery_scope_admitted', scope='scope', generation='gen')
        reserved, attempting, disposition = [
            dict(msg='claude_delivery_' + event, scope='scope', deliveryId='dly_1')
            for event in ('claim_reserved', 'claim_attempting', 'disposition')
        ]
        self.assertEqual(harness.ordered_lifecycle([admission, reserved, attempting, disposition],
                                                   'scope', 'gen', 'dly_1'),
                         [admission, reserved, attempting, disposition])
        for events in ([admission, attempting, reserved, disposition],
                       [admission, reserved, dict(attempting, deliveryId='dly_10'), disposition],
                       [dict(admission, generation='old'), reserved, attempting, disposition],
                       [admission, reserved, attempting, disposition, attempting]):
            with self.assertRaises(harness.Fail):
                harness.ordered_lifecycle(events, 'scope', 'gen', 'dly_1')

    def test_transcript_requires_one_enqueue_and_one_successful_pair(self):
        msg, cmd, marker = 'msg_1', '/lab/vec.sh inbox --json --limit 8', 'WAKE-E2E-test'
        body = f'Vecnet message available.\nFind {msg} in thread thr_1'
        enqueue = dict(type='queue-operation', operation='enqueue', content=body)
        peer = dict(type='user', origin=dict(kind='peer', body=body))
        call = dict(type='assistant', message=dict(content=[dict(type='tool_use', name='Bash',
                    id='toolu_1', input=dict(command=cmd))]))
        data = {'events': [{'data': {'message': {'message_id': msg, 'content': {'text': marker}}}}]}
        result = dict(type='user', message=dict(content=[dict(type='tool_result',
                      tool_use_id='toolu_1', content=json.dumps(data), is_error=False)]))
        rows = [enqueue, peer, call, result]
        self.assertEqual(harness.transcript_proof(rows, msg, cmd, marker)[1], 'toolu_1')
        mismatched = dict(type='user', message=dict(content=[dict(result['message']['content'][0],
                                                                  tool_use_id='toolu_other')]))
        for bad in ([enqueue, enqueue, peer, call, result], [*rows, call], [*rows, result],
                    [enqueue, peer, call, mismatched], [enqueue, peer, result, call]):
            with self.assertRaises(harness.Fail):
                harness.transcript_proof(bad, msg, cmd, marker)

        second_call = dict(type='assistant', message=dict(content=[dict(type='tool_use', name='Bash',
                           id='toolu_2', input=dict(command=cmd + ' '))]))
        second_result = result['message']['content'][0] | {'tool_use_id': 'toolu_2'}
        for content in (second_result['content'], second_result['content'].replace(marker, 'other text'),
                        [{'type': 'text', 'text': second_result['content']}],
                        'tool warning\n' + second_result['content']):
            with self.subTest(content=content), self.assertRaisesRegex(harness.Fail, 'successful fetch'):
                harness.transcript_proof([*rows, second_call, dict(type='user', message=dict(content=[
                    second_result | {'content': content}]))], msg, cmd, marker)
        for block in (second_result | {'is_error': True},
                      second_result | {'content': json.dumps(data).replace('msg_1', 'msg_10')},
                      second_result | {'content': 'not inbox JSON'}):
            self.assertEqual(harness.transcript_proof([*rows, second_call,
                dict(type='user', message=dict(content=[block]))], msg, cmd, marker)[0], rows)

    def test_control_pointer_in_early_frame_fails_even_without_body(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'frame-0000.txt').write_text('Vecnet message available.\nFind msg_1 in thread thr_1')
            (root / 'frame-0001.txt').write_text('No pointer on screen now')
            with self.assertRaisesRegex(harness.Fail, 'frame-0000'):
                harness.verify_control_frames(sorted(root.glob('*.txt')), 'msg_1', 'HOLD-E2E-test')
            harness.verify_control_frames(sorted(root.glob('*.txt')), 'msg_10', 'HOLD-E2E-test')

    def test_default_disposes_raw_material_but_keeps_evidence(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in ('evidence', 'frames', 'hub', 'home', 'logs'):
                (root / name).mkdir()
            (root / 'ca.key').write_text('private')
            self.assertEqual(harness.dispose_run(root, keep_evidence=False), 'disposed')
            self.assertEqual({p.name for p in root.iterdir()}, {'evidence', 'frames'})
            (root / 'logs').mkdir()
            self.assertEqual(harness.dispose_run(root, keep_evidence=True), 'retained privately')
            self.assertTrue((root / 'logs').is_dir())


class ProvisionFreeTests(unittest.TestCase):
    def test_meta_lists_no_provisions(self):
        meta = harness.run_meta('n', 'vecnet-test-n', 'c', '2.1.322', 'now')
        self.assertEqual(meta['provisions'], [])
        self.assertEqual(meta['canary_opt_in'], 'vecnet --net claude-rx claude canary enable')
        self.assertNotIn('claude_code_version_provision', meta)
        launch = harness.claude_launch_env('/h', '/c', '/s', '/r', '/cfg')
        self.assertFalse(any(e.startswith('CLAUDE_CODE_VERSION=') for e in launch))
        for kwargs in ({}, {'seam': True}):
            env = harness.daemon_env('/h', '/cfg', '/bin/vecnet', **kwargs)
            self.assertFalse(any(e.startswith('VECNET_CLAUDE_DELIVERY_CANARY=') for e in env), env)
        self.assertIn('VECNET_CLAUDE_DELIVERY_CRASH_BEFORE_WRITE=1', harness.daemon_env('/h', '/c', '/b', seam=True))
        self.assertIn('VECNET_CLAUDE_DELIVERY_CANARY=0', harness.daemon_env('/h', '/c', '/b', kill_switch=True))
        self.assertNotIn('VECNET_CLAUDE_DELIVERY_CANARY=1', harness.daemon_env('/h', '/c', '/b', kill_switch=True))

    def test_build_check_disabled_and_no_provision(self):
        environ = b'HOME=/h\0CLAUDE_CONFIG_DIR=/c\0'
        status = dict(mode='canary', canary_opt_in=True, build_check='disabled')
        self.assertEqual(harness.check_no_provision(environ, status)['build_check'], 'disabled')
        for env in (environ + b'CLAUDE_CODE_VERSION=2.1.322\0', b'CLAUDE_CODE_VERSION=\0' + environ):
            with self.assertRaisesRegex(harness.Fail, 'CLAUDE_CODE_VERSION'):
                harness.check_no_provision(env, status)
        # A value merely mentioning the name is not the variable.
        harness.check_no_provision(environ + b'NOTE=CLAUDE_CODE_VERSION\0', status)
        for bad in (status | {'build_check': 'enabled'}, status | {'mode': 'legacy'},
                    status | {'canary_opt_in': False}, {k: v for k, v in status.items() if k != 'build_check'}):
            with self.assertRaises(harness.Fail):
                harness.check_no_provision(environ, bad)

    def test_canary_off_control_requires_no_claim_and_legacy_mode(self):
        fenced = dict(delivery_id='fence-0000', state='released', reason='fenced_scope_not_admitted')
        held = dict(delivery_id='dly_hold', state='held', reason='inbox_only')
        status = dict(mode='legacy', canary_opt_in=False, build_check='disabled')
        doc = dict(deliveries=[fenced, held])
        self.assertEqual(harness.check_canary_off(doc, status, 'dly_off', 'fence-0000')['mode'], 'legacy')
        bad_docs = (dict(deliveries=[fenced, dict(delivery_id='dly_off', state='reserved')]),
                    dict(deliveries=[held]),
                    dict(deliveries=[fenced | dict(state='reserved', reason=None)]),
                    dict(deliveries=[fenced | dict(reason='fenced_generation_changed')]),
                    dict(deliveries=[fenced, dict(delivery_id='other', state='attempting')]))
        for bad in bad_docs:
            with self.subTest(doc=bad), self.assertRaises(harness.Fail):
                harness.check_canary_off(bad, status, 'dly_off', 'fence-0000')
        for bad in (status | {'mode': 'canary'}, status | {'canary_opt_in': True}):
            with self.subTest(status=bad), self.assertRaises(harness.Fail):
                harness.check_canary_off(doc, bad, 'dly_off', 'fence-0000')

    def test_pre_admission_backlog_must_never_be_pointered(self):
        with tempfile.TemporaryDirectory() as tmp:
            frame = Path(tmp) / 'frame-0000.txt'
            frame.write_text('BACKLOG-E2E-x body seen in a fetch, but no pointer')
            harness.check_backlog([frame], [], [], 'msg_b', 'dly_b')
            frame.write_text('Vecnet message available.\nFind msg_b in thread thr_1')
            with self.assertRaisesRegex(harness.Fail, 'pointered'):
                harness.check_backlog([frame], [], [], 'msg_b', 'dly_b')
            frame.write_text('')
            with self.assertRaisesRegex(harness.Fail, 'claim lifecycle'):
                harness.check_backlog([frame], [], [dict(msg='claude_delivery_claim_reserved', deliveryId='dly_b')],
                                      'msg_b', 'dly_b')

    def test_rejected_cursor_rebaselines_once(self):
        rej = dict(msg='claude_delivery_cursor_rejected', scope='s')
        base = dict(msg='claude_delivery_scope_baselined', scope='s', generation='g')
        self.assertEqual(harness.check_rebaseline([rej, base], 's', 'g')[1], base['msg'])
        for bad in ([base], [rej], [rej, base, rej, base], [rej, base | {'generation': 'old'}],
                    [rej, base, dict(msg='claude_delivery_reconcile_failed')]):
            with self.subTest(events=bad), self.assertRaises(harness.Fail):
                harness.check_rebaseline(bad, 's', 'g')


class PageTests(unittest.TestCase):
    builder = Path(__file__).with_name('build-e2e-page.py')

    def setUp(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        self.run = root / 'run'
        self.page = root / 'page'
        self.evidence = self.run / 'evidence'
        self.frames = self.run / 'frames'
        self.evidence.mkdir(parents=True)
        self.frames.mkdir()
        scope, generation, recipient = 'test-scope', 'test-gen', 'peer_01M7S8M4PGR1RFW0DJSPDFMXEP'
        ids = {name: (f'dly_{name}', f'msg_{name}') for name in
               ('positive', 'hold', 'crash', 'cap', 'backlog', 'canary_off', 'kill_switch')}
        states = [dict(dly_id=dly, recipient=recipient, state='queued') for dly, _ in ids.values()]
        claims = {name: dict(delivery_id=dly, message_id=msg, scope=scope, generation=generation)
                  for name, (dly, msg) in ids.items()}
        positive = claims['positive'] | dict(state='released', disposition='attempted', enqueues=1,
                    fetch_pairs=1, tool_use_id='toolu_1', lifecycle=[harness.ADMISSION, *harness.CLAIM_EVENTS])
        hold = claims['hold'] | dict(state='held', disposition='held')
        crash = claims['crash'] | dict(state='uncertain', disposition='uncertain', reason='restart_may_have_written')
        results = dict(build=dict(commit='test-commit', claude='2.1.322'), enrollment=dict(phase='registered'),
                       admission=dict(binding_key=scope, generation=generation), positive=positive,
                       hold=hold, crash=crash | dict(final_state='uncertain'),
                       cap=claims['cap'] | dict(pending=129, blocked=True),
                       no_ack=dict(acks=0, deliveries=states),
                       no_provision=dict(claude_code_version_present=False, mode='canary',
                                         canary_opt_in=True, build_check='disabled'),
                       baseline=dict(stored_cursor='e1.2', head_cursor='e1.2', backlog_cursor='e1.1'),
                       backlog=claims['backlog'] | dict(pointer_frames=0),
                       rebaseline=dict(events=['claude_delivery_cursor_rejected', 'claude_delivery_scope_baselined'],
                                       stored_after='e1.9'),
                       canary_off=claims['canary_off'] | dict(mode='legacy', canary_opt_in=False,
                                                              fenced='fence-0000', claims_for_wake=0),
                       kill_switch=claims['kill_switch'] | dict(adapter_events=0, mode='legacy', canary_opt_in=True),
                       teardown=dict(private_server_stopped=True, default_server_strays=0, raw_disposition='disposed'))
        self.put('98-summary.json', dict(results=results, nonce='test', frames=2, run_dir=str(self.run)))
        self.put('00-run-meta.json', harness.run_meta('test', 'vecnet-test-test', 'c', '2.1.322',
                                                      '2026-11-19T00:00:00+00:00'))
        self.put('32-runtime-snapshot.json', dict(bindings=[dict(binding_key=scope, generation=generation,
                   state='registered', admission=dict(live=True, policy=True, version_qualified=True,
                                                      build_check='disabled', canary=True, witness=True),
                   scope=dict(caller=recipient))]))
        for name, filename in zip(ids, ('40-send-wake.json', '70-hold-send.json', '80-crash-send.json', '90-cap-send.json',
                                        '26-backlog-send.json', '94-off-send.json', '100-kill-send.json')):
            dly, msg = ids[name]
            self.put(filename, dict(message_id=msg, deliveries=[dict(delivery_id=dly, recipient=recipient, state='queued')]))
        self.put('41-hub-delivery.json', states[:1])
        self.put('62-claude-deliveries.json', dict(deliveries=[positive], pending_count=0))
        self.put('73-hub-acks.json', [])
        self.put('74-hub-delivery-states.json', states)
        admission = dict(msg=harness.ADMISSION, scope=scope, generation=generation)
        baselined = dict(msg='claude_delivery_scope_baselined', scope=scope, generation=generation)
        life = [admission] + [dict(msg=event, scope=scope, deliveryId=positive['delivery_id'])
                              for event in harness.CLAIM_EVENTS]
        life[-1].update(exit=0, state='released', disposition='attempted')
        crash_attempt = dict(msg='claude_delivery_claim_attempting', scope=scope, deliveryId=crash['delivery_id'])
        rejected = dict(msg='claude_delivery_cursor_rejected', scope=scope)
        rebased = dict(baselined, at='later')
        fence = dict(msg='claude_delivery_claim_fenced', deliveryId='fence-0000', scope=scope, state='released')
        control_events = [dict(msg='claude_delivery_claim_held', scope=scope, deliveryId=hold['delivery_id']),
                          rejected, rebased,
                          crash_attempt, dict(msg='claude_delivery_uncertain_restart', scope=scope),
                          dict(msg='claude_delivery_blocked_pending_limit', scope=scope, limit=128), fence]
        self.log_rows = [admission, baselined, *life[1:], *control_events]
        for filename, events in [('50-daemon.log', self.log_rows), ('51-daemon-chain.txt', life)]:
            (self.evidence / filename).write_text(''.join(json.dumps(e) + '\n' for e in events))
        (self.evidence / '81-crash-attempting.txt').write_text(json.dumps(crash_attempt) + '\ndaemon exit rc=137\n')
        self.put('82-crash-claim.json', dict(at_crash=crash | dict(state='attempting'), after_restart=crash, transcript_rows=0))
        self.put('91-cap-seed.json', dict(seeded=128, scope=scope, generation=generation))
        self.put('92-cap-deliveries.json', dict(deliveries=[positive, hold, crash], pending_count=129, blocked_pending_limit=True))
        self.put('93-cap-status.json', dict(generation=generation, pending_count=129, blocked_pending_limit=True))
        message = dict(message_id=positive['message_id'], content=dict(text='WAKE-E2E-test: wake'))
        fetched = dict(events=[dict(data=dict(message=message, delivery_id=positive['delivery_id']))])
        held_message = dict(message_id=hold['message_id'], priority='hold', content=dict(text='HOLD-E2E-test: hold'))
        self.put('71-hold-inbox.json', dict(events=[dict(data=dict(message=held_message, delivery_id=hold['delivery_id']))]))
        body = f'Vecnet message available.\nFind {message["message_id"]} in thread thr_test'
        call = dict(type='tool_use', name='Bash', id='toolu_1', input=dict(command=f'{self.run}/vec.sh inbox --json --limit 8'))
        result = dict(type='tool_result', tool_use_id='toolu_1', content=json.dumps(fetched))
        observations = [dict(role='assistant', content=[call]), dict(role='user', content=[result])]
        self.put('60-transcript-rows.json', [dict(type='queue-operation', operation='enqueue', content=body),
                 dict(type='user', origin=dict(kind='peer', body=body)),
                 *(dict(type=m['role'], message=m) for m in observations)])
        self.observations = observations
        self.stub_requests(observations, observations)  # Messages API repeats history.
        self.acknowledgement = states[0]
        (self.frames / 'index.json').write_text(json.dumps([
            dict(n=n, t=n, file=f'frame-{n:04d}.txt') for n in range(2)]))
        (self.frames / 'frame-0000.txt').write_text(harness.PREVIEW)
        (self.frames / 'frame-0001.txt').write_text(body + '\nWAKE-E2E-test: wake')
        (self.evidence / '21-enrol-claude-rx.txt').write_text('credentials saved; claude setup registered: fixture\n')
        (self.evidence / '23-runtime-stage.txt').write_text('Automatic enrollment staged the embedded delivery runtime\n')
        (self.evidence / '99-teardown.txt').write_text('private server: no server running\ndefault server vecnet-test-* sessions: 0\n')
        for name in ('10-hub-init.txt', '11-hub-serve.log', '20-enrol-sender.txt', '40b-send-wake-cli.txt',
                     '33b-head-read-cli.txt'):
            (self.evidence / name).touch()
        (self.evidence / '24-claude-environ-names.txt').write_text(
            '/proc/<disposable claude pid>/environ variable names (values withheld)\n'
            'CLAUDE_CONFIG_DIR is the disposable config root: true\nCLAUDE_CODE_VERSION present: false\n'
            'CLAUDE_CONFIG_DIR\nHOME\n')
        (self.evidence / '25-canary-enable.txt').write_text(
            '$ vecnet --net claude-rx claude canary enable\nclaude native canary enabled\n')
        (self.evidence / '30-daemon-env.txt').write_text('HOME=/h\nVECNET_LOCAL_MAINTENANCE=0\n')
        self.put('33-baseline.json', dict(baselined=baselined, stored_cursor='e1.2', cli_head_next_cursor='e1.2',
                                          cli_head_events=[], backlog_cursor='e1.1',
                                          backlog_delivery_id=ids['backlog'][0]))
        self.put('34-status-canary.json', dict(version=1, owner='registered', mode='canary', generation=generation,
                                               pending_count=0, blocked_pending_limit=False,
                                               canary_opt_in=True, build_check='disabled'))
        self.put('75-rebaseline.json', dict(stored_before='e1.5', corrupted_to=harness.FOREIGN_CURSOR,
                                            real_cli_refusal='cursor_purged: cursor is from another epoch or below retention',
                                            stored_after='e1.9', events=[rejected, rebased]))
        self.put('95-off-deliveries.json', dict(deliveries=[dict(delivery_id='fence-0000', state='released',
                                                                 reason='fenced_scope_not_admitted'), hold]))
        self.put('96-off-status.json', dict(mode='legacy', canary_opt_in=False, build_check='disabled'))
        (self.evidence / '97-off-fence.txt').write_text(json.dumps(fence) + '\n')
        self.put('101-kill-status.json', dict(mode='legacy', canary_opt_in=True, build_check='disabled',
                  reason='canary opted in; the delivery host has not admitted this binding'))
        (self.evidence / '102-kill-env.txt').write_text('HOME=/h\nVECNET_CLAUDE_DELIVERY_CANARY=0\n')
        (self.evidence / '22-config.toml.redacted').write_text(''.join(
            f'[profiles.{name}]\nhub="loopback"\nhub_pubkey="key"\npeer_id="{recipient}"\nallow_insecure_http=true\n'
            for name in ('claude-rx', 'sender')))

    def put(self, name, value):
        (self.evidence / name).write_text(json.dumps(value))

    def stub_requests(self, *requests):
        (self.evidence / '31-stub-observed.jsonl').write_text(''.join(json.dumps(dict(
            path='/v1/messages', body=json.dumps(dict(messages=messages)))) + '\n' for messages in requests))

    def build(self):
        return subprocess.run([sys.executable, '-I', str(self.builder), str(self.run), str(self.page)],
                              text=True, capture_output=True)

    def assert_failed_build(self, gate):
        built = self.build()
        self.assertNotEqual(built.returncode, 0, built.stdout)
        self.assertIn(gate, built.stderr)
        self.assertFalse((self.page / 'assets').exists(), 'failed build exposed evidence')
        self.assertFalse((self.page / 'player.html').exists(), 'failed build left the player live')
        index = self.page / 'index.html'
        if index.exists():
            self.assertIn('Proof failed', index.read_text())
            self.assertIn(gate, index.read_text())

    def test_passing_build_publishes_proof(self):
        built = self.build()
        self.assertEqual(built.returncode, 0, built.stderr)
        self.assertIn('proven end to end', (self.page / 'index.html').read_text())
        self.assertIn('WAKE-E2E-test', (self.page / 'assets/frames/frame-0001.txt').read_text())

    def test_late_control_pointer_withholds_all_excerpts_even_on_rebuild(self):
        built = self.build()
        self.assertEqual(built.returncode, 0, built.stderr)
        path = self.frames / 'frame-0001.txt'
        path.write_text(path.read_text() + '\nVecnet message available.\nFind msg_hold in thread thr_control')
        self.assert_failed_build('control frames')

    def test_contains_no_success_page_for_failed_lifecycle(self):
        path = self.evidence / '50-daemon.log'
        rows = path.read_text().splitlines()
        rows[2], rows[3] = rows[3], rows[2]  # reserved <-> attempting
        path.write_text('\n'.join(rows))
        self.assert_failed_build('ordered lifecycle')

    def test_duplicate_fetch_in_stub_history_is_not_hidden_by_selected_transcript(self):
        first, second = self.observations
        call = first['content'][0]
        duplicate = [dict(role='assistant', content=[call | dict(id='toolu_2', input=dict(
                         command=call['input']['command'] + ' '))]),
                     dict(role='user', content=[second['content'][0] | dict(tool_use_id='toolu_2')])]
        self.stub_requests(self.observations, [*self.observations, *duplicate])
        self.assert_failed_build('exactly-one fetch')

    def test_nonempty_ack_query_withholds_proof(self):
        self.put('73-hub-acks.json', [self.acknowledgement])
        self.assert_failed_build('hub acknowledgements')

    def test_wrong_control_final_state_withholds_proof(self):
        path = self.evidence / '82-crash-claim.json'
        claim = json.loads(path.read_text())
        claim['after_restart']['state'] = 'released'
        self.put(path.name, claim)
        self.assert_failed_build('control final states')

    def test_provisioned_meta_withholds_proof(self):
        meta = json.loads((self.evidence / '00-run-meta.json').read_text())
        self.put('00-run-meta.json', meta | {'provisions': ['CLAUDE_CODE_VERSION=2.1.322']})
        self.assert_failed_build('no provision')

    def test_canary_env_on_daemon_withholds_proof(self):
        (self.evidence / '30-daemon-env.txt').write_text('HOME=/h\nVECNET_CLAUDE_DELIVERY_CANARY=1\n')
        self.assert_failed_build('no provision')

    def test_claim_before_baseline_withholds_proof(self):
        rows = list(self.log_rows)
        rows.insert(1, dict(msg='claude_delivery_claim_reserved', scope='test-scope', deliveryId='dly_early'))
        (self.evidence / '50-daemon.log').write_text(''.join(json.dumps(e) + '\n' for e in rows))
        self.assert_failed_build('baseline before any send')

    def test_pointered_backlog_withholds_proof(self):
        (self.frames / 'frame-0000.txt').write_text(harness.PREVIEW + '\nVecnet message available.\nFind msg_backlog in thread thr_x')
        self.assert_failed_build('control frames')

    def test_canary_off_claim_withholds_proof(self):
        doc = json.loads((self.evidence / '95-off-deliveries.json').read_text())
        doc['deliveries'].append(dict(delivery_id='dly_canary_off', state='reserved'))
        self.put('95-off-deliveries.json', doc)
        self.assert_failed_build('canary off')

    def test_kill_switch_admission_withholds_proof(self):
        self.put('101-kill-status.json', dict(mode='canary', canary_opt_in=True, build_check='disabled', reason=''))
        self.assert_failed_build('kill switch')

    def test_rebaseline_loop_withholds_proof(self):
        doc = json.loads((self.evidence / '75-rebaseline.json').read_text())
        doc['events'] = doc['events'] * 2
        self.put('75-rebaseline.json', doc)
        self.assert_failed_build('rejected-cursor rebaseline')

    def test_api_key_dialog_value_is_masked_in_published_frames(self):
        index = json.loads((self.frames / 'index.json').read_text())
        (self.frames / 'frame-0002.txt').write_text('Detected a custom API key\n  ANTHROPIC_API_KEY: sk-ant-...dummy-e2e-key\n')
        (self.frames / 'index.json').write_text(json.dumps(index + [dict(n=2, t=2, file='frame-0002.txt')]))
        summary = json.loads((self.evidence / '98-summary.json').read_text())
        self.put('98-summary.json', summary | dict(frames=3))
        built = self.build()
        self.assertEqual(built.returncode, 0, built.stderr)
        text = (self.page / 'assets/frames/frame-0002.txt').read_text()
        self.assertIn('ANTHROPIC_API_KEY: <redacted>', text)
        self.assertNotIn('dummy-e2e-key', text)
