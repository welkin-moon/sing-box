import importlib.util
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location(
    'enable_jpx_nginx_ipv6', Path(__file__).with_name('enable_jpx_nginx_ipv6.py')
)
impl = importlib.util.module_from_spec(spec)
spec.loader.exec_module(impl)

GOOD = """server {
    listen 443 ssl;
    server_name jpx.20100823.xyz;
    ssl_certificate /etc/letsencrypt/live/jpx.20100823.xyz/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/jpx.20100823.xyz/privkey.pem;
    location = /healthz { return 200 'ready'; }
    location /sub/ { return 403; }
}
"""


class IPv6NginxTests(unittest.TestCase):
    def test_adds_one_ipv6_listener_preserving_security(self):
        actual = impl.patched(GOOD)
        self.assertIn('listen 443 ssl;', actual)
        self.assertIn('listen [::]:443 ssl;', actual)
        self.assertIn('location /sub/ { return 403; }', actual)
        self.assertEqual(actual.count('listen [::]:443 ssl;'), 1)

    def test_idempotent(self):
        once = impl.patched(GOOD)
        self.assertEqual(impl.patched(once), once)

    def test_refuses_wrong_host(self):
        with self.assertRaises(ValueError):
            impl.patched(GOOD.replace('jpx.20100823.xyz', 'unrelated.example'))

    def test_refuses_no_tls_key(self):
        with self.assertRaises(ValueError):
            impl.patched(GOOD.replace('    ssl_certificate_key /etc/letsencrypt/live/jpx.20100823.xyz/privkey.pem;\n', ''))

    def test_check_only_never_modifies_file(self):
        with tempfile.TemporaryDirectory() as folder:
            file = Path(folder) / 'vhost.conf'
            file.write_text(GOOD, encoding='utf-8')
            self.assertTrue(impl.run(file, test_only=True))
            self.assertEqual(file.read_text(), GOOD)


if __name__ == '__main__':
    unittest.main()
