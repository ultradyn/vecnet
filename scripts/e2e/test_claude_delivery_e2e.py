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
        scope, generation, recipient = 'test-scope', 'test-gen', 'peer_test'
        ids = {name: (f'dly_{name}', f'msg_{name}') for name in ('positive', 'hold', 'crash', 'cap')}
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
                       teardown=dict(private_server_stopped=True, default_server_strays=0, raw_disposition='disposed'))
        self.put('98-summary.json', dict(results=results, nonce='test', frames=2, run_dir=str(self.run)))
        self.put('00-run-meta.json', dict(started='2026-11-19T00:00:00+00:00', nonce='test'))
        self.put('32-runtime-snapshot.json', dict(bindings=[dict(binding_key=scope, generation=generation,
                   state='registered', admission=dict(live=True, policy=True, version_qualified=True),
                   scope=dict(caller=recipient))]))
        for name, filename in zip(ids, ('40-send-wake.json', '70-hold-send.json', '80-crash-send.json', '90-cap-send.json')):
            dly, msg = ids[name]
            self.put(filename, dict(message_id=msg, deliveries=[dict(delivery_id=dly, recipient=recipient, state='queued')]))
        self.put('41-hub-delivery.json', states[:1])
        self.put('62-claude-deliveries.json', dict(deliveries=[positive], pending_count=0))
        self.put('73-hub-acks.json', [])
        self.put('74-hub-delivery-states.json', states)
        admission = dict(msg=harness.ADMISSION, scope=scope, generation=generation)
        life = [admission] + [dict(msg=event, scope=scope, deliveryId=positive['delivery_id'])
                              for event in harness.CLAIM_EVENTS]
        life[-1].update(exit=0, state='released', disposition='attempted')
        crash_attempt = dict(msg='claude_delivery_claim_attempting', scope=scope, deliveryId=crash['delivery_id'])
        control_events = [dict(msg='claude_delivery_claim_held', scope=scope, deliveryId=hold['delivery_id']),
                          crash_attempt, dict(msg='claude_delivery_uncertain_restart', scope=scope),
                          dict(msg='claude_delivery_blocked_pending_limit', scope=scope, limit=128)]
        for filename, events in [('50-daemon.log', life + control_events), ('51-daemon-chain.txt', life)]:
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
        for name in ('10-hub-init.txt', '11-hub-serve.log', '20-enrol-sender.txt', '30-daemon-env.txt', '40b-send-wake-cli.txt'):
            (self.evidence / name).touch()
        (self.evidence / '22-config.toml.redacted').write_text(''.join(
            f'[profiles.{name}]\nhub="loopback"\nhub_pubkey="key"\npeer_id="peer_test"\nallow_insecure_http=true\n'
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
        rows[1], rows[2] = rows[2], rows[1]
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
