import base64
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('render_jpx', Path(__file__).with_name('render_jpx.py'))
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


class RendererTests(unittest.TestCase):
    def test_idempotent_and_no_insecure_tls(self):
        with tempfile.TemporaryDirectory() as root:
            r = Path(root)
            state, tls, subs = r / 'state', r / 'tls', r / 'subs'
            state.mkdir()
            tls.mkdir()
            for x in ('fullchain.pem', 'privkey.pem'):
                (tls / x).write_text('fixture-only')
            (state / 'jpx-reality-keypair.txt').write_text('PrivateKey: ' + 'P' * 43 + '\nPublicKey: ' + 'K' * 43 + '\n')
            (state / 'hy2-user1').write_text('existing_hy2_credential_value_01234567\n')
            params = (state, tls, r / 'config.json', subs, 'jpx.example.org', 'www.example.org', 'sing-box', 8)
            first = mod.render(*params)
            before = {p.name: p.read_bytes() for p in subs.iterdir()}
            first_config = json.loads((r / 'config.json.next').read_text())
            first_ids = (state / 'vless-identities.json').read_text()
            first_token = (state / 'source-token').read_text()
            second = mod.render(*params)
            self.assertEqual(first['users'], 8)
            self.assertEqual(second['subscription_files'], 8)
            self.assertEqual(first_ids, (state / 'vless-identities.json').read_text())
            self.assertEqual(first_token, (state / 'source-token').read_text())
            self.assertEqual(before, {p.name: p.read_bytes() for p in subs.iterdir()})
            self.assertEqual(len(first_config['inbounds']), 2)
            self.assertEqual(len(first_config['inbounds'][0]['users']), 8)
            self.assertEqual(len(first_config['inbounds'][1]['users']), 8)
            for name, data in before.items():
                lines = base64.b64decode(data).decode().strip().splitlines()
                self.assertEqual(len(lines), 2)
                self.assertIn('sni=jpx.example.org', lines[0])
                self.assertIn('security=reality', lines[1])
                self.assertNotIn('allowInsecure', '\n'.join(lines))
                self.assertNotIn('insecure=1', '\n'.join(lines))
                self.assertNotIn('skip-cert-verify', '\n'.join(lines))


if __name__ == '__main__':
    unittest.main()
