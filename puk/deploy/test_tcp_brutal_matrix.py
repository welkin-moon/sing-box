import importlib.util
from pathlib import Path
import unittest


HERE = Path(__file__).parent
spec = importlib.util.spec_from_file_location('tcp_brutal_matrix', HERE / 'tcp_brutal_matrix.py')
matrix = importlib.util.module_from_spec(spec)
spec.loader.exec_module(matrix)


def sample():
    return {
        'region': 'jpx',
        'port': 14443,
        'dual_hostname': 'jpx.example.test',
        'checks': {
            'linux_tcp_brutal_v2_active': True,
            'server_downlink_brutal_verified': True,
            'authenticated_tcp_proxy_verified': True,
            'measured_safe_down_mbps': 38,
            'dual_dns_a_aaaa_verified': True,
            'dual_external_https_verified': True
        },
        'families': {
            'v4': {'address': '192.0.2.15', 'external_tcp_connected': True,
                   'external_https_via_proxy_verified': True},
            'v6': {'address': '2001:db8::15', 'external_tcp_connected': True,
                   'external_https_via_proxy_verified': True}
        },
        'egress': {'direct': True, 'google': True, 'all': True}
    }


class VerifiedMatrixTests(unittest.TestCase):
    def test_all_proven_transports_make_nine(self):
        out = matrix.plan({'nodes': [sample()]})
        self.assertEqual(out['candidate_count'], 9)
        self.assertEqual(len({c['tag'] for c in out['nodes'][0]['candidates']}), 9)
        self.assertEqual(
            [x['tag'] for x in out['nodes'][0]['candidates']],
            [
                f'JPX-TCPBrutal-{fam}-{policy}'
                for policy in ('direct', 'google', 'all')
                for fam in ('v4', 'v6', 'dual')
            ]
        )
        self.assertEqual(out['nodes'][0]['candidates'][2]['server'], 'jpx.example.test')

    def test_no_ipv6_means_no_dual_or_v6(self):
        node = sample()
        node['families']['v6']['external_tcp_connected'] = False
        out = matrix.plan({'nodes': [node]})
        self.assertEqual(out['candidate_count'], 3)
        self.assertTrue(all(x['family'] == 'v4' for x in out['nodes'][0]['candidates']))

    def test_no_warp_never_labels_direct_as_warp(self):
        node = sample()
        node['egress']['google'] = False
        node['egress']['all'] = False
        out = matrix.plan({'nodes': [node]})
        self.assertEqual(out['candidate_count'], 3)
        self.assertTrue(all(x['egress'] == 'direct' for x in out['nodes'][0]['candidates']))

    def test_no_confirmed_kernel_returns_no_fake_node(self):
        node = sample()
        node['checks']['linux_tcp_brutal_v2_active'] = 'true'
        out = matrix.plan({'nodes': [node]})
        self.assertEqual(out['candidate_count'], 0)

    def test_no_measured_capacity_returns_no_fake_node(self):
        node = sample()
        node['checks']['measured_safe_down_mbps'] = '100'
        self.assertEqual(matrix.plan({'nodes': [node]})['candidate_count'], 0)

    def test_wrong_family_rejected(self):
        node = sample()
        node['families']['v6']['address'] = '192.0.2.88'
        with self.assertRaises(ValueError):
            matrix.plan({'nodes': [node]})

    def test_duplicate_regions_rejected(self):
        with self.assertRaises(ValueError):
            matrix.plan({'nodes': [sample(), sample()]})

    def test_dual_gated_on_external_e2e(self):
        node = sample()
        node['checks']['dual_external_https_verified'] = False
        out = matrix.plan({'nodes': [node]})
        self.assertEqual(out['candidate_count'], 6)
        self.assertFalse(any(x['family'] == 'dual' for x in out['nodes'][0]['candidates']))


if __name__ == '__main__':
    unittest.main()
