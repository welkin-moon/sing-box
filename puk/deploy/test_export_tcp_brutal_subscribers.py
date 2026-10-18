import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location(
    'export_tcp_brutal_subscribers',
    Path(__file__).with_name('export_tcp_brutal_subscribers.py')
)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


def outbound(name: str, family: str):
    return {
        'type': 'vless', 'tag': 'CANARY-XYZ-TCPBrutal-' + family,
        'uuid': name, 'server': family + '.example.test', 'server_port': 14443,
        'tls': {'enabled': True, 'server_name': 'node.example.test'},
        'multiplex': {
            'enabled': True, 'protocol': 'smux',
            'brutal': {'enabled': True, 'up_mbps': 20, 'down_mbps': 40}
        }
    }


class SubscriberExportTests(unittest.TestCase):
    def test_two_users_separate_private_files(self):
        with tempfile.TemporaryDirectory() as d:
            target = Path(d) / 'private-subs'
            uuids = {
                'ss1': '123e4567-e89b-12d3-a456-426614174001',
                'ss2': '123e4567-e89b-12d3-a456-426614174002'
            }
            result = mod.export({
                'client_canary_outbounds_by_user': {
                    user: {
                        'v4': outbound(uid, 'v4'),
                        'v6': outbound(uid, 'v6'),
                        'dual': outbound(uid, 'dual'),
                    }
                    for user, uid in uuids.items()
                }
            }, target)
            self.assertEqual(result['count'], 2)
            self.assertEqual(sorted(result['written_users']), ['ss1', 'ss2'])
            self.assertEqual(target.stat().st_mode & 0o777, 0o700)
            for user, uid in uuids.items():
                path = target / (user + '.json')
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                data = path.read_text(encoding='utf-8')
                self.assertIn(uid, data)
                for other, other_uid in uuids.items():
                    if other != user:
                        self.assertNotIn(other_uid, data)
                conf = json.loads(data)
                outbound_list = conf['outbounds']
                self.assertEqual(outbound_list[0]['type'], 'selector')
                self.assertEqual(len(outbound_list), 5)
                self.assertEqual(outbound_list[0]['outbounds'][0],
                                 'CANARY-XYZ-TCPBrutal-dual')
                self.assertEqual(conf['route']['final'], 'proxy')

    def test_v4_only_default(self):
        cfg = mod.compose({'v4': outbound('test-uuid', 'v4')})
        self.assertEqual(cfg['outbounds'][0]['outbounds'],
                         ['CANARY-XYZ-TCPBrutal-v4'])
        self.assertEqual(cfg['outbounds'][0]['default'],
                         'CANARY-XYZ-TCPBrutal-v4')

    def test_untrusted_tls_rejected(self):
        v = outbound('test', 'v4')
        v['tls']['insecure'] = True
        with self.assertRaises(ValueError):
            mod.compose({'v4': v})

    def test_plain_tcp_cannot_masquerade_as_brutal(self):
        v = outbound('test', 'v4')
        del v['multiplex']['brutal']
        with self.assertRaises(ValueError):
            mod.compose({'v4': v})

    def test_other_network_family_rejected(self):
        with self.assertRaises(ValueError):
            mod.compose({'auto': outbound('test', 'auto')})


if __name__ == '__main__':
    unittest.main()
