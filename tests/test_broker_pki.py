import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class BrokerPKITest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        docker = self.root / 'docker'
        docker.write_bytes((ROOT / 'tests/fake_docker.py').read_bytes())
        docker.chmod(0o700)
        self.state = self.root / 'state.json'
        self.state.write_text('{}')
        self.env = dict(os.environ, PATH=str(self.root) + ':' + os.environ['PATH'],
                        FAKE_DOCKER_STATE=str(self.state), DOCKER_CONTEXT='isolated-pki-test',
                        STACK_NAME='test-pki', BROKER_AUTH_MODE='token',
                        BROKER_ORIGIN='https://database-broker:8443', BROKER_DEPLOYMENT_ID='test-pki',
                        BROKER_CA_FILE=str(self.root / 'pki/ca.pem'),
                        BROKER_TLS_CERT_FILE=str(self.root / 'pki/broker.pem'),
                        BROKER_CA_KEY_FILE=str(self.root / 'pki/ca-key.pem'),
                        BROKER_TLS_KEY_SECRET='test-tls-key')

    def run_setup(self, success=True):
        result = subprocess.run(['python3', str(ROOT / 'prepare-broker-pki.py')],
                                env=self.env, cwd=self.root, capture_output=True, timeout=30)
        self.assertEqual(result.returncode, 0 if success else 1, result.stderr.decode())
        self.assertNotIn(b'PRIVATE KEY', result.stdout + result.stderr)
        state = json.loads(self.state.read_text())
        self.assertFalse(state.get('config'))  # This run's deployment lock was removed.
        self.assertNotIn('PRIVATE KEY', self.state.read_text())
        self.assertNotIn('CERTIFICATE', self.state.read_text())
        return state

    def test_bootstrap_and_redeploy_preserve_keys(self):
        first = self.run_setup()
        self.assertEqual(set(first['contexts']), {'isolated-pki-test'})
        self.assertEqual(len(first['secret_input_lengths']), 1)
        before = {p.name: p.read_bytes() for p in (self.root / 'pki').iterdir()}
        self.assertEqual(set(before), {'ca.pem', 'ca-key.pem', 'broker.pem'})
        self.assertEqual((self.root / 'pki/ca-key.pem').stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.root / 'pki').stat().st_mode & 0o777, 0o700)
        second = self.run_setup()
        self.assertEqual(first['secret'], second['secret'])
        self.assertEqual(first['secret_input_lengths'], second['secret_input_lengths'])
        self.assertEqual(before, {p.name: p.read_bytes() for p in (self.root / 'pki').iterdir()})
        result = subprocess.run(['openssl', 'verify', '-CAfile', self.env['BROKER_CA_FILE'],
                                 '-verify_hostname', 'different-broker', self.env['BROKER_TLS_CERT_FILE']],
                                capture_output=True)
        self.assertNotEqual(result.returncode, 0)

    def test_missing_secret_is_not_recreated_for_an_existing_certificate(self):
        state = self.run_setup()
        state['secret'] = {}
        self.state.write_text(json.dumps(state))
        after = self.run_setup(False)
        self.assertEqual(after['secret'], {})
        self.assertEqual(after['secret_input_lengths'], state['secret_input_lengths'])

    def test_reuse_does_not_require_the_ca_private_key(self):
        self.run_setup()
        (self.root / 'pki/ca-key.pem').unlink()
        self.run_setup()

    def test_interrupted_public_certificate_publication_can_resume(self):
        first = self.run_setup()
        cert = self.root / 'pki/broker.pem'
        cert.rename(cert.with_name('broker.pem.pending'))
        after = self.run_setup()
        self.assertTrue(cert.is_file())
        self.assertEqual(first['secret_input_lengths'], after['secret_input_lengths'])

    def test_missing_public_inputs_never_replace_existing_secret(self):
        first = self.run_setup()
        (self.root / 'pki/broker.pem').unlink()
        after = self.run_setup(False)
        self.assertEqual(first['secret'], after['secret'])

    def test_plaintext_origin_and_legacy_mode_do_not_touch_swarm(self):
        self.env['BROKER_ORIGIN'] = 'http://database-broker:8080'
        self.assertEqual(self.run_setup(False), {})
        self.env['BROKER_AUTH_MODE'] = 'none'
        self.assertEqual(self.run_setup(False), {})


if __name__ == '__main__':
    unittest.main()
