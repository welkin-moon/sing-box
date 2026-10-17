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
