import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
LABEL = 'io.razzie.broker.'
CLIENTS = {'access_gateway': 'access-gateway', 'files': 'files', 'notes': 'notes',
           'razchess': 'razchess', 'geoip_server': 'geoip-server'}


class DeploymentTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.dir = Path(self.temp.name)
        executable = self.dir / 'docker'
        executable.write_text((ROOT / 'tests/fake_docker.py').read_text())
        executable.chmod(0o755)
        self.policy = {'version': 1, 'deployment_id': 'test-deploy', 'key_id': 'k1', 'accepted_key_ids': ['k1'],
                       'clients': {c: {'generation': 1, 'accepted_generations': [1], 'allow': []} for c in CLIENTS}}
        self.policy_path = self.dir / 'policy.json'
        self.policy_path.write_text(json.dumps(self.policy))
        for name in ('ca.pem', 'cert.pem'):
            (self.dir / name).write_text('public-test-certificate')
        self.state_path = self.dir / 'state.json'
        services = {name: {'image': 'test/' + name + ':selected', 'environment': {}, 'deploy': {'replicas': 1}}
                    for name in ('database-broker', *CLIENTS.values(), 'postgres', 'valkey')}
        services['database-broker']['healthcheck'] = {'test': ['CMD', '/readyz']}
        for client in ('access_gateway', 'files', 'notes'):
            services[CLIENTS[client]]['environment']['POSTGRES_URI_FROM'] = 'http://database-broker:8080/v1/instances/postgres/' + client + '/uri'
        services['access-gateway']['environment']['REDIS_URI_FROM'] = 'http://database-broker:8080/v1/instances/valkey/access_gateway/uri'
        services['razchess']['command'] = ['--redis-from', 'http://database-broker/uri', '--session-timeout', '5m']
        self.state = {'base': {'services': services}, 'policy': self.policy,
                      'secret': {'pki_key': {'ID': 'operator-key', 'Spec': {'Labels': {}}}}}
        self.save()
        self.env = dict(os.environ, PATH=str(self.dir) + ':' + os.environ['PATH'],
                        FAKE_DOCKER_STATE=str(self.state_path), DOCKER_CONTEXT='ssh-custom-manager', STACK_NAME='custom-stack',
                        BROKER_DEPLOYMENT_ID='test-deploy', BROKER_CLIENTS_FILE=str(self.policy_path),
                        BROKER_CA_FILE=str(self.dir / 'ca.pem'), BROKER_TLS_CERT_FILE=str(self.dir / 'cert.pem'),
                        BROKER_TLS_KEY_SECRET='pki_key', BROKER_AUTH_MODE='token', DEPLOY_TIMEOUT='5', DATABASE_BROKER_IMAGE_TAG='selected')

    def save(self):
        self.state_path.write_text(json.dumps(self.state))
        self.policy_path.write_text(json.dumps(self.policy))

    def run_deploy(self, success=True):
        self.save()
        p = subprocess.run(['bash', str(ROOT / 'deploy.sh')], cwd=self.dir, env=self.env, capture_output=True, timeout=30)
        self.state = json.loads(self.state_path.read_text())
        self.assertEqual(p.returncode, 0 if success else 1, p.stderr.decode())
        self.assertNotIn('sensitive-partial-token', (p.stdout + p.stderr).decode())
        self.assertNotIn('v1.k1.1.', json.dumps(self.state))
        self.assertEqual(set(self.state['contexts']), {'ssh-custom-manager'})
        helpers = [o for o in self.state.get('service', {}).values() if o.get('helper')]
        self.assertEqual(helpers, [])
        self.assertFalse(any(o['Spec']['Labels'].get(LABEL + 'kind') == 'deployment-lock' for o in self.state.get('config', {}).values()))
        return p

    def test_first_redeploy_added_client_and_permission_edit(self):
        self.run_deploy()
        lengths = self.state['secret_input_lengths']
        self.assertEqual(lengths.count(32), 1)
        self.assertEqual(len(lengths), 8)  # master, two DB passwords, five client tokens
        first = copy.deepcopy(self.state)
        self.run_deploy()
        self.assertEqual(self.state['secret_input_lengths'], lengths)
        generated = lambda s: sum(c[:2] == ['service', 'create'] and 'hold' in c for c in s['commands'])
        self.assertEqual(generated(first), generated(self.state))
        self.policy['clients']['files']['allow'] = [{'adapter': 'postgres', 'instance': 'files'}]
        self.state['policy'] = self.policy
        self.run_deploy()
        self.assertEqual(generated(first), generated(self.state))
        self.policy['clients']['added'] = {'generation': 1, 'accepted_generations': [1], 'allow': []}
        self.state['policy'] = self.policy
        self.run_deploy()
        self.assertEqual(generated(self.state), generated(first) + 1)
        self.assertEqual(len(self.state['secret_input_lengths']), len(lengths) + 1)
        command = self.state['deploys'][-1]['services']['razchess']['command']
        self.assertEqual(command[-3:], ['--', '--session-timeout', '5m'])

    def test_failed_or_truncated_generation_and_retry_partial_state(self):
        for failure in ('fail_generator', 'truncate_generator', 'interrupt_generator'):
            self.state[failure] = True
            self.run_deploy(False)
            self.assertFalse(any(o['Spec']['Labels'].get(LABEL + 'kind') == 'token' for o in self.state['secret'].values()))
            del self.state[failure]
        self.run_deploy()
        # A failed run leaves reusable master/config state, not a second key.
        self.assertEqual(self.state['secret_input_lengths'].count(32), 1)

    def test_partial_client_state_reuses_successful_secrets(self):
        self.state['fail_client'] = 'files'
        self.run_deploy(False)
        tokens = [o for o in self.state['secret'].values() if o['Spec']['Labels'].get(LABEL + 'kind') == 'token']
        self.assertEqual(len(tokens), 1)
        del self.state['fail_client']
        self.run_deploy()
        self.assertEqual(len(self.state['secret_input_lengths']), 8)

    def test_helper_deadline_cleans_up(self):
        self.state['pending_helper'] = True
        self.run_deploy(False)
        self.assertNotIn('deploys', self.state)

    def test_existing_deployment_lock_is_preserved(self):
        lock_name = 'custom-stack-broker-deployment-lock'
        lock = {'ID': 'another-run', 'Spec': {'Labels': {LABEL + 'kind': 'deployment-lock', LABEL + 'run': 'other'}}}
        self.state['config'] = {lock_name: lock}
        self.save()
        result = subprocess.run(['bash', str(ROOT / 'deploy.sh')], cwd=self.dir, env=self.env, capture_output=True, timeout=30)
        self.assertEqual(result.returncode, 1)
        self.state = json.loads(self.state_path.read_text())
        self.assertEqual(self.state['config'][lock_name], lock)
        self.assertNotIn('secret_input_lengths', self.state)

    def test_lost_and_replaced_master_never_regenerated(self):
        self.run_deploy()
        master = next(n for n, o in self.state['secret'].items() if o['Spec']['Labels'].get(LABEL + 'kind') == 'master')
        original = self.state['secret'].pop(master)
        self.run_deploy(False)
        self.assertNotIn(master, self.state['secret'])
        self.state['secret'][master] = dict(original, ID='replacement')
        self.run_deploy(False)
        self.assertEqual(self.state['secret_input_lengths'].count(32), 1)

    def test_rotation_and_revocation(self):
        self.run_deploy()
        self.policy['key_id'] = 'k2'
        self.policy['accepted_key_ids'] = ['k1', 'k2']
        self.policy['clients']['files'].update(generation=2, accepted_generations=[1, 2])
        self.state['policy'] = self.policy
        self.run_deploy()
        final = self.state['deploys'][-1]
        self.assertIn('k2=', final['services']['database-broker']['environment']['AUTH_KEY_FILES'])
        self.assertIn('k1=', final['services']['database-broker']['environment']['AUTH_KEY_FILES'])
        self.assertEqual(self.state['secret_input_lengths'].count(32), 2)
        self.policy['clients']['files']['enabled'] = False
        self.state['policy'] = self.policy
        self.run_deploy()
        self.assertEqual(self.state['deploys'][-1]['services']['files']['deploy']['replicas'], 0)

    def test_existing_tokenless_upgrade_order_and_no_downgrade(self):
        self.state['service'] = {'custom-stack_database-broker': {'ID': 'old', 'Spec': {'Labels': {},
            'TaskTemplate': {'ContainerSpec': {'Image': 'old-broker', 'Env': []}}}}}
        self.run_deploy()
        phases = self.state['deploys']
        self.assertEqual(len(phases), 3)
        self.assertEqual(list(phases[0]['services']), ['database-broker'])
        self.assertEqual(phases[0]['services']['database-broker']['environment']['LEGACY_HTTP_PORT'], '8080')
        self.assertEqual(phases[1]['services']['database-broker']['environment']['AUTH_MODE'], 'none')
        self.assertEqual(phases[2]['services']['database-broker']['environment']['AUTH_MODE'], 'token')
        self.env['BROKER_AUTH_MODE'] = 'none'
        before = len(phases)
        self.run_deploy(False)
        self.assertEqual(len(self.state['deploys']), before)

    def test_none_has_no_authentication_resources(self):
        self.env.update(BROKER_AUTH_MODE='none', BROKER_CLIENTS_FILE='/missing', BROKER_TLS_KEY_SECRET='', BROKER_CA_FILE='/missing')
        self.run_deploy()
        self.assertEqual(len(self.state['secret_input_lengths']), 2)
        self.assertFalse(any(o['Spec']['Labels'].get(LABEL + 'kind') in ('master', 'token') for o in self.state['secret'].values()))
        doc = self.state['deploys'][-1]
        self.assertEqual(doc['services']['database-broker']['environment']['AUTH_MODE'], 'none')
        self.assertNotIn('--token-file', doc['services']['razchess']['command'])

    def test_direct_script_defaults_reach_compose(self):
        self.env.pop('STACK_NAME')
        self.env.pop('DOCKER_CONTEXT')
        self.env['BROKER_AUTH_MODE'] = 'none'
        self.save()
        result = subprocess.run(['bash', str(ROOT / 'deploy.sh')], cwd=self.dir, env=self.env, capture_output=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.state = json.loads(self.state_path.read_text())
        self.assertEqual(set(self.state['contexts']), {'razcloud'})
        self.assertEqual(set(self.state['stack_environment']), {'razzie-cloud'})
        self.assertIn('razzie-cloud_database-broker', self.state['service'])

    def test_invalid_preflight_does_not_mutate_swarm(self):
        self.state['missing_image'] = True
        self.run_deploy(False)
        self.assertNotIn('config', self.state)
        self.assertNotIn('secret_input_lengths', self.state)

    def test_update_failure_stops_with_cleanup(self):
        self.state['pause_rollout'] = True
        self.run_deploy(False)
        self.assertEqual(len(self.state['deploys']), 1)

    def test_conflicting_existing_token_is_rejected(self):
        self.run_deploy()
        obj = next(o for o in self.state['secret'].values() if o['Spec']['Labels'].get(LABEL + 'kind') == 'token')
        obj['Spec']['Labels'][LABEL + 'master-id'] = 'wrong-master'
        self.run_deploy(False)


if __name__ == '__main__':
    unittest.main()
