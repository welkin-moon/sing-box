import importlib.util
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location(
    'render_tcp_brutal_canary', Path(__file__).with_name('render_tcp_brutal_canary.py')
)
target = importlib.util.module_from_spec(spec)
spec.loader.exec_module(target)

UUID = '123e4567-e89b-12d3-a456-426614174000'


class CanaryConfigTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.cert = Path(self.temp.name)
        (self.cert / 'fullchain.pem').write_text('only-a-fixture')
        (self.cert / 'privkey.pem').write_text('only-a-fixture')
        self.identities = {'users': {'ss1': UUID}}
        self.names = {'v4': 'jpx4.example.org', 'v6': 'jpx6.example.org',
                      'dual': 'jpxdual.example.org'}

    def render(self):
        return target.render(self.identities, 'JPX', 'jpx.example.org',
                             self.names, self.cert, 14443, 20, 40)

    def test_separate_tls_multiplex_config(self):
        r = self.render()
        inbound = r['server_candidate']['inbounds'][0]
        self.assertEqual(inbound['type'], 'vless')
        self.assertEqual(inbound['listen_port'], 14443)
        self.assertNotIn('flow', inbound['users'][0])
        self.assertTrue(inbound['multiplex']['brutal']['enabled'])
        self.assertEqual(inbound['multiplex']['brutal']['down_mbps'], 40)
        self.assertEqual(set(r['client_canary_outbounds']), {'v4', 'v6', 'dual'})
        for family, client in r['client_canary_outbounds'].items():
            self.assertTrue(client['tag'].startswith('CANARY-'))
            self.assertNotIn('flow', client)
            self.assertEqual(client['tls']['server_name'], 'jpx.example.org')
            self.assertEqual(client['server'], self.names[family])
            self.assertNotIn('insecure', client['tls'])
            self.assertEqual(client['multiplex']['protocol'], 'smux')

    def test_eight_subscribers_have_independent_outbounds(self):
        self.identities['users'] = {
            f'ss{i}': f'123e4567-e89b-12d3-a456-{426614174000 + i:012d}'
            for i in range(1, 9)
        }
        result = self.render()
        sources = result['client_canary_outbounds_by_user']
        self.assertEqual(len(sources), 8)
        self.assertEqual(set(sources), {f'ss{i}' for i in range(1, 9)})
        ids = set()
        for name, families in sources.items():
            self.assertEqual(set(families), {'v4', 'v6', 'dual'})
            own = {v['uuid'] for v in families.values()}
            self.assertEqual(len(own), 1)
            ids.update(own)
            self.assertTrue(all(v['tls']['server_name'] == 'jpx.example.org'
                                for v in families.values()))
        self.assertEqual(len(ids), 8)
        self.assertEqual(result['client_canary_outbounds']['v4'],
                         sources['ss1']['v4'])

    def test_ipv4_only_region_does_not_require_ipv6_socket(self):
        self.names = {'v4': 'us4.example.org'}
        config = self.render()
        server = config['server_candidate']['inbounds'][0]
        self.assertEqual(server['listen'], '0.0.0.0')
        self.assertEqual(set(config['client_canary_outbounds']), {'v4'})

    def test_dual_stack_region_binds_ipv6_mapped_socket(self):
        server = self.render()['server_candidate']['inbounds'][0]
        self.assertEqual(server['listen'], '::')

    def test_rate_must_be_measured_valid_integer(self):
        for invalid in (0, -1, True, '40', 100001):
            with self.subTest(invalid=invalid):
                with self.assertRaises(ValueError):
                    target.render(self.identities, 'JPX', 'jpx.example.org',
                                  self.names, self.cert, 14443, 10, invalid)

    def test_not_allowed_without_private_identity(self):
        with self.assertRaises(ValueError):
            target.render({}, 'JPX', 'jpx.example.org',
                          self.names, self.cert, 14443, 20, 30)

    def test_not_allowed_without_cert(self):
        (self.cert / 'privkey.pem').unlink()
        with self.assertRaises(FileNotFoundError):
            self.render()

    def test_bad_family_label_rejected(self):
        self.names['warp'] = 'something.example.org'
        with self.assertRaises(ValueError):
            self.render()


if __name__ == '__main__':
    unittest.main()
