import importlib.util
import json
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
