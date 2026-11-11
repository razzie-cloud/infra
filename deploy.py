#!/usr/bin/env python3
"""Swarm-only key lifecycle. Never log subprocess output or secret-bearing argv."""
import argparse
import hashlib
import json
import os
import re
import signal
import subprocess
import sys
import tempfile
import time
import uuid
from urllib.parse import urlsplit
from pathlib import Path

LABEL = 'io.razzie.broker.'
APPS = {'access_gateway': 'access-gateway', 'files': 'files', 'notes': 'notes',
        'razchess': 'razchess', 'geoip_server': 'geoip-server'}
TOKEN = re.compile(rb'v1\.([A-Za-z0-9][A-Za-z0-9_-]{0,63})\.([1-9][0-9]{0,15})\.[A-Za-z0-9_-]{43}\Z')


class Failure(Exception):
    pass


class Docker:
    def __init__(self, context, env=None):
        self.context = context
        self.env = env if env is not None else os.environ

    def run(self, *args, data=None, optional=False, timeout=90):
        try:
            p = subprocess.run(['docker', '--context', self.context, *args], input=data,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, env=self.env)
        except (OSError, subprocess.TimeoutExpired):
            raise Failure('Docker operation unavailable or timed out') from None
        if p.returncode:
            if optional:
                return None
            # Raw Docker diagnostics can contain container arguments or response bodies.
            raise Failure('Docker operation failed: ' + ' '.join(args[:2]))
        return p.stdout

    def inspect(self, kind, name):
        raw = self.run(kind, 'inspect', name, optional=True)
        if raw is None:
            # Distinguish an absent object from an unreachable daemon/permission error.
            names = self.run(kind, 'ls', '--format', '{{.Name}}').decode().splitlines()
            if name in names:
                raise Failure('Cannot inspect existing ' + kind)
            return None
        try:
            return json.loads(raw)[0]
        except (ValueError, IndexError, TypeError):
            raise Failure('Invalid Docker inspection response') from None


def escape_dollars(value):
    # stack deploy interpolates again; preserve literal shell variables from Compose.
    if isinstance(value, str):
        return value.replace('$', '$$')
    if isinstance(value, list):
        return [escape_dollars(v) for v in value]
    if isinstance(value, dict):
        return {k: escape_dollars(v) for k, v in value.items()}
    return value


def digest(data):
    return hashlib.sha256(data).hexdigest()


def labels(obj):
    return obj.get('Spec', {}).get('Labels', {}) or {}


def own(obj, expected):
    if any(labels(obj).get(LABEL + k) != str(v) for k, v in expected.items()):
        raise Failure('Conflicting preexisting Swarm resource ownership')


def label_args(values):
    return [a for k, v in sorted(values.items()) for a in ('--label', LABEL + k + '=' + str(v))]


def read_public(path):
    try:
        data = Path(path).read_bytes()
    except OSError:
        raise Failure('Cannot read public policy/certificate input') from None
    if not data or len(data) > 1048576:
        raise Failure('Invalid public input size')
    return data


class Deployment:
    def __init__(self, env=None, docker=None):
        self.env = dict(os.environ if env is None else env)
        self.stack = self.env.get('STACK_NAME', 'razzie-cloud')
        if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]{0,47}', self.stack):
            raise Failure('Invalid stack name')
        self.mode = self.env.get('BROKER_AUTH_MODE', 'token')
        if self.mode not in ('token', 'none'):
            raise Failure('BROKER_AUTH_MODE must be token or none')
        self.deployment = self.env.get('BROKER_DEPLOYMENT_ID', 'razzie-cloud-production')
        if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]{0,63}', self.deployment):
            raise Failure('Invalid broker deployment identity')
        self.env.setdefault('STACK_NAME', self.stack)
        self.docker = docker or Docker(self.env.get('DOCKER_CONTEXT', 'razcloud'), self.env)
        self.prefix = self.stack + '-broker-' + digest(self.deployment.encode())[:12] + '-v1'
        self.owner = {'stack': self.stack, 'deployment': self.deployment, 'protocol': 'v1'}
        self.run_id = uuid.uuid4().hex[:16]
        self.helper = None
        self.helper_kind = None
        self.lock = None
        self.stage = 'preflight'
        self.timeout = int(self.env.get('DEPLOY_TIMEOUT', '300'))
        if not 5 <= self.timeout <= 900:
            raise Failure('DEPLOY_TIMEOUT must be between 5 and 900 seconds')
        self.policy = None
        self.keys = {}
        self.tokens = {}
        self.configs = {}
        self.master_ids = {}

    def announce(self, stage):
        self.stage = stage
        print('Deployment stage: ' + stage, flush=True)

    def base(self):
        # Compose validates image interpolation and resolves relative public file paths.
        raw = self.docker.run('compose', '-f', 'docker-stack.yml', 'config', '--format', 'json')
        try:
            base = json.loads(raw)
        except (ValueError, TypeError):
            raise Failure('Invalid rendered Compose configuration') from None
        base.pop('name', None)
        base['version'] = '3.9'
        for value in base.get('networks', {}).values():
            if not value.get('external'):
                value.pop('name', None)
        for value in base.get('volumes', {}).values():
            if not value.get('external'):
                value.pop('name', None)
        return base

    def mount_secret(self, doc, service, logical, actual, target, uid=0):
        doc.setdefault('secrets', {})[logical] = {'external': True, 'name': actual}
        doc['services'][service].setdefault('secrets', []).append(
            {'source': logical, 'target': target, 'uid': str(uid), 'gid': str(uid), 'mode': 0o400})

    def mount_config(self, doc, service, logical, actual, target):
        doc.setdefault('configs', {})[logical] = {'external': True, 'name': actual}
        doc['services'][service].setdefault('configs', []).append(
            {'source': logical, 'target': target, 'mode': 0o444})

    def render(self, base, *, enforcement=None, old_broker=None, placeholders=False):
        doc = json.loads(json.dumps(base))
        services = doc['services']
        b = services['database-broker']
        b.setdefault('environment', {})['AUTH_MODE'] = self.mode if enforcement is None else enforcement
        if old_broker:
            # Consumer preparation must not change any existing broker configuration.
            doc['services']['database-broker'] = old_broker
            b = old_broker
        if self.mode == 'none':
            # Use the same bounded adapter, with no token or trust resources required.
            for client in ('razchess', 'geoip_server'):
                self.wrapper(doc, client, 'http://database-broker:8080', authenticated=False)
            return doc
        policy = self.policy or {'key_id': 'validation', 'accepted_key_ids': ['validation'],
                                 'clients': {c: {'generation': 1} for c in APPS}}
        if not old_broker:
            self.mount_config(doc, 'database-broker', 'broker_policy', self.configs.get('policy', 'validation_policy'), '/run/configs/broker_policy.yml')
            b['environment'].update(AUTH_POLICY_FILE='/run/configs/broker_policy.yml', AUTH_DEPLOYMENT_ID=self.deployment)
            paths = []
            for key in policy['accepted_key_ids']:
                self.mount_secret(doc, 'database-broker', 'broker_master_' + key, self.keys.get(key, 'validation_master_' + key), 'broker_master_' + key)
                paths.append(key + '=/run/secrets/broker_master_' + key)
            b['environment']['AUTH_KEY_FILES'] = ','.join(paths)
            self.mount_secret(doc, 'database-broker', 'broker_tls_key', self.env.get('BROKER_TLS_KEY_SECRET', 'validation_tls_key'), 'broker_tls_key')
            self.mount_config(doc, 'database-broker', 'broker_tls_cert', self.configs.get('cert', 'validation_cert'), '/run/configs/broker_cert.pem')
            b['environment'].update(TLS_CERT_FILE='/run/configs/broker_cert.pem', TLS_KEY_FILE='/run/secrets/broker_tls_key', HEALTH_PORT='8081', SERVICE_PORT=str(urlsplit(self.env.get('BROKER_ORIGIN', 'https://database-broker:8443')).port or 443))
            b['healthcheck']['test'] = ['CMD-SHELL', 'wget -q -O /dev/null http://127.0.0.1:8081/readyz || exit 1']
            b.setdefault('deploy', {}).setdefault('labels', {}).update({LABEL + 'enforced': 'token', LABEL + 'deployment': self.deployment,
                LABEL + 'policy-sha256': self.configs.get('policy_digest', 'validation')})
        origin = self.env.get('BROKER_ORIGIN', 'https://database-broker:8443')
        for client, service in APPS.items():
            c = policy['clients'].get(client)
            if not c or c.get('enabled') is False:
                # Revoked consumers are stopped; never give them an old token or tokenless URL.
                services[service].setdefault('deploy', {})['replicas'] = 0
                continue
            actual = self.tokens.get(client, 'validation_token_' + client)
            self.mount_secret(doc, service, 'broker_token_' + client, actual, 'broker_token', 10001 if client in ('files', 'notes') else 0)
            self.mount_config(doc, service, 'broker_ca', self.configs.get('ca', 'validation_ca'), '/run/configs/broker_ca.pem')
            if client in ('razchess', 'geoip_server'):
                self.wrapper(doc, client, origin, authenticated=True)
            else:
                e = services[service].setdefault('environment', {})
                e['BROKER_TOKEN_FILE'] = '/run/secrets/broker_token'
                e['BROKER_CA_FILE'] = '/run/configs/broker_ca.pem'
                for field in ('POSTGRES_URI_FROM', 'REDIS_URI_FROM'):
                    if field in e:
                        path = e[field].split('/v1/', 1)[1].split('?', 1)[0]
                        e[field] = origin + '/v1/' + path + '?client=' + client
        return doc

    def wrapper(self, doc, client, origin, authenticated):
        service = APPS[client]
        app = doc['services'][service]
        # Derived images preserve upstream app layers and add the selected broker's adapter.
        tag = self.env.get('DATABASE_BROKER_IMAGE_TAG', '')
        app['image'] = self.env.get(('RAZCHESS' if client == 'razchess' else 'GEOIP_SERVER') + '_BROKER_IMAGE') or 'ghcr.io/razzie-cloud/' + service + '-broker:' + tag
        args = ['--program', '/' + service, '--url', origin + '/v1/instances/valkey/' + client + '/uri' + ('?client=' + client if authenticated else '')]
        if authenticated:
            args += ['--token-file', '/run/secrets/broker_token', '--ca-file', '/run/configs/broker_ca.pem']
        old_args = app.get('command', [])
        extra = []
        i = 0
        while i < len(old_args):
            if old_args[i] in ('--redis-from', '-redis-from'):
                i += 2
                continue
            if old_args[i].startswith(('--redis-from=', '-redis-from=')):
                i += 1
                continue
            extra.append(old_args[i])
            i += 1
        app['entrypoint'] = ['/broker-start']
        # Additional upstream arguments belong here, after --; no Redis overrides.
        app['command'] = args + ['--'] + extra

    def validate_stack(self, doc):
        with self.stack_file(doc) as path:
            self.docker.run('compose', '-f', path, 'config', '--no-interpolate', '--quiet')

    def stack_file(self, doc):
        class StackFile:
            def __enter__(inner):
                inner.f = tempfile.NamedTemporaryFile(mode='w', suffix='.json', prefix='broker-stack-', delete=False)
                os.chmod(inner.f.name, 0o600)
                json.dump(escape_dollars(doc), inner.f)
                inner.f.close()
                return inner.f.name
            def __exit__(inner, *args):
                os.unlink(inner.f.name)
        return StackFile()

    def config(self, kind, data):
        sha = digest(data)
        name = self.prefix + '-' + kind + '-' + sha[:24]
        expected = dict(self.owner, kind=kind, sha256=sha)
        obj = self.docker.inspect('config', name)
        if obj:
            own(obj, expected)
        else:
            self.docker.run('config', 'create', *label_args(expected), name, '-', data=data)
        return name

    def secret(self, name, expected, data=None):
        obj = self.docker.inspect('secret', name)
        if obj:
            own(obj, expected)
            return obj['ID']
        if data is None:
            raise Failure('Required existing secret is missing')
        secret_id = self.docker.run('secret', 'create', *label_args(expected), name, '-', data=data).decode().strip()
        if not secret_id:
            raise Failure('Secret creation returned no identity')
        return secret_id

    def master(self, key):
        name = self.prefix + '-master-' + key
        expected = dict(self.owner, kind='master', key=key)
        anchor_name = name + '-identity'
        anchor = self.docker.inspect('config', anchor_name)
        obj = self.docker.inspect('secret', name)
        if anchor:
            own(anchor, dict(self.owner, kind='master-identity', key=key))
            if not obj or obj['ID'] != labels(anchor).get(LABEL + 'master-id'):
                raise Failure('Master secret lost or replaced; recover with a new key_id')
        if obj:
            own(obj, expected)
            master_id = obj['ID']
        else:
            existing = self.docker.run('secret', 'ls', '--filter', 'label=' + LABEL + 'stack=' + self.stack,
                                       '--filter', 'label=' + LABEL + 'deployment=' + self.deployment,
                                       '--filter', 'label=' + LABEL + 'key=' + key, '--format', '{{.Name}}')
            if existing.strip():
                raise Failure('Master missing for existing token state; use a new key_id')
            # Only fresh versions may create a new key; no local persistent copy exists.
            master_id = self.secret(name, expected, os.urandom(32))
        if not anchor:
            self.docker.run('config', 'create', *label_args(dict(self.owner, kind='master-identity', key=key, **{'master-id': master_id})),
                            anchor_name, '-', data=master_id.encode())
        self.keys[key] = name
        self.master_ids[key] = master_id

    def acquire(self):
        name = self.stack + '-broker-deployment-lock'
        # Config create is atomic across deploy hosts, unlike a local flock.
        lock_id = self.docker.run('config', 'create', *label_args(dict(self.owner, kind='deployment-lock', run=self.run_id)),
                                  name, '-', data=self.run_id.encode()).decode().strip()
        self.lock = (name, lock_id)

    def helper_start(self, image, manager):
        self.helper = self.prefix + '-helper-' + self.run_id
        self.helper_kind = 'generator'
        key = self.policy['key_id']
        self.docker.run('service', 'create', '--name', self.helper, '--constraint', 'node.id==' + manager,
                        '--restart-condition', 'none', '--no-healthcheck', '--log-driver', 'none', '--read-only',
                        '--cap-drop', 'ALL', '--user', '0:0',
                        *label_args(dict(self.owner, kind='generator', run=self.run_id)),
                        '--secret', 'source=' + self.keys[key] + ',target=master,mode=0400',
                        '--config', 'source=' + self.configs['policy'] + ',target=/policy.yml,mode=0444',
                        '--entrypoint', '/broker-token', image, 'hold', '--max-lifetime', '15m')
        end = time.monotonic() + self.timeout
        while time.monotonic() < end:
            ids = self.docker.run('service', 'ps', '--filter', 'desired-state=running', '--format', '{{.ID}}', '--no-trunc', self.helper).decode().splitlines()
            for task_id in ids:
                task = json.loads(self.docker.run('inspect', task_id))[0]
                if task.get('NodeID') == manager and task.get('Status', {}).get('State') == 'running':
                    cid = task['Status'].get('ContainerStatus', {}).get('ContainerID')
                    if cid:
                        return cid
            time.sleep(1)
        raise Failure('Token helper did not become runnable before deadline')

    def prepare_tokens(self, image, manager):
        missing = []
        key = self.policy['key_id']
        for client, c in sorted(self.policy['clients'].items()):
            if c.get('enabled') is False:
                continue
            generation = str(c['generation'])
            name = self.prefix + '-' + key + '-' + client + '-g' + generation
            expected = dict(self.owner, kind='token', key=key, client=client, generation=generation, **{'master-id': self.master_ids[key]})
            obj = self.docker.inspect('secret', name)
            if obj:
                own(obj, expected)
            else:
                missing.append((client, name, expected))
            self.tokens[client] = name
        if not missing:
            return
        cid = self.helper_start(image, manager)
        for client, name, expected in missing:
            token = self.docker.run('exec', cid, '/broker-token', 'generate', '--policy', '/policy.yml',
                                    '--deployment-id', self.deployment, '--key-file', '/run/secrets/master', '--client', client)
            match = TOKEN.fullmatch(token)
            if not match or match[1].decode() != key or int(match[2]) != self.policy['clients'][client]['generation']:
                raise Failure('Generator returned an invalid or truncated token')
            self.secret(name, expected, token)
            del token
        self.remove_helper()

    def remove_helper(self):
        if self.helper:
            obj = self.docker.inspect('service', self.helper)
            if obj:
                own(obj, dict(self.owner, kind=self.helper_kind, run=self.run_id))
                self.docker.run('service', 'rm', self.helper)
                end = time.monotonic() + self.timeout
                while True:
                    exists = self.docker.inspect('service', self.helper)
                    containers = self.docker.run('ps', '--filter', 'label=com.docker.swarm.service.name=' + self.helper, '--format', '{{.ID}}')
                    if not exists and not containers.strip():
                        break
                    if time.monotonic() >= end:
                        raise Failure('Helper removal did not converge')
                    time.sleep(1)
            self.helper = None
            self.helper_kind = None

    def cleanup(self):
        # Only ephemeral resources owned by this invocation may be removed.
        try:
            self.remove_helper()
        finally:
            if self.lock:
                name, lock_id = self.lock
                obj = self.docker.inspect('config', name)
                if obj and obj['ID'] == lock_id:
                    own(obj, dict(self.owner, kind='deployment-lock', run=self.run_id))
                    self.docker.run('config', 'rm', name)
                self.lock = None

    def rollout(self, doc, stage, only=None):
        self.announce(stage)
        self.validate_stack(doc)
        if only:
            # Deploy a one-service document without pruning existing services.
            doc = dict(doc, services={s: doc['services'][s] for s in only})
        with self.stack_file(doc) as path:
            self.docker.run('stack', 'deploy', '--with-registry-auth', '-c', path, self.stack)
        self.converge(doc['services'])
        if self.mode == 'token':
            self.acceptance(doc, readiness_only=True)

    def converge(self, services):
        end = time.monotonic() + self.timeout
        while time.monotonic() < end:
            ready = True
            for name, desired in services.items():
                actual = self.docker.inspect('service', self.stack + '_' + name)
                if not actual:
                    ready = False
                    continue
                update = actual.get('UpdateStatus', {}).get('State', '')
                if update in ('paused', 'rollback_paused', 'rollback_started', 'rollback_completed'):
                    raise Failure('Service update failed or rolled back: ' + name)
                spec = actual['Spec']
                if spec['TaskTemplate']['ContainerSpec']['Image'].split('@')[0] != desired['image'].split('@')[0]:
                    ready = False
                mode = spec.get('Mode', {})
                # The manager's service summary supplies authoritative desired global tasks.
                count = mode.get('Replicated', {}).get('Replicas')
                if count is None:
                    summary = self.docker.run('service', 'ls', '--filter', 'name=' + self.stack + '_' + name, '--format', '{{.Name}} {{.Replicas}}').decode().splitlines()
                    exact = [line.split()[1] for line in summary if line.split()[0] == self.stack + '_' + name]
                    if not exact or '/' not in exact[0]:
                        ready = False
                        continue
                    count = int(exact[0].split('/')[1].split()[0])
                tasks = self.docker.run('service', 'ps', '--filter', 'desired-state=running', '--format', '{{.ID}}', '--no-trunc', self.stack + '_' + name).decode().splitlines()
                running = 0
                target_version = spec['TaskTemplate']
                for task_id in tasks:
                    task = json.loads(self.docker.run('inspect', task_id))[0]
                    if task.get('Status', {}).get('State') == 'running' and task.get('Spec') == target_version:
                        # Swarm considers tasks RUNNING before Docker health settles.
                        node_id = task.get('NodeID')
                        cid = task.get('Status', {}).get('ContainerStatus', {}).get('ContainerID')
                        if node_id == self.manager and cid:
                            container = json.loads(self.docker.run('container', 'inspect', cid))[0]
                            if container.get('State', {}).get('Health', {}).get('Status', 'healthy') != 'healthy':
                                continue
                        running += 1
                if running != count or update in ('updating', 'rollback_started'):
                    ready = False
            if ready:
                return
            time.sleep(2)
        raise Failure('Readiness/convergence deadline exceeded')

    def previous_broker(self):
        obj = self.docker.inspect('service', self.stack + '_database-broker')
        if not obj:
            return None, False
        env = dict(v.split('=', 1) for v in obj['Spec']['TaskTemplate']['ContainerSpec'].get('Env', []) if '=' in v)
        args = obj['Spec']['TaskTemplate']['ContainerSpec'].get('Args', [])
        explicit_mode = env.get('AUTH_MODE')
        for i, arg in enumerate(args):
            if arg.startswith('--auth-mode='):
                explicit_mode = arg.split('=', 1)[1]
            elif arg == '--auth-mode' and i + 1 < len(args):
                explicit_mode = args[i + 1]
        has_auth_config = bool(env.get('AUTH_POLICY_FILE') or env.get('AUTH_KEY_FILES') or any(a.startswith(('--auth-policy-file', '--auth-key-files')) for a in args))
        enforced = (labels(obj).get(LABEL + 'enforced') == 'token' or explicit_mode == 'token'
                    or (explicit_mode != 'none' and has_auth_config))
        # An unknown nonempty mode is unsafe to classify as legacy.
        if explicit_mode not in (None, '', 'none', 'token'):
            raise Failure('Unknown deployed authentication mode')
        return obj, enforced

    def preflight(self, base):
        info = json.loads(self.docker.run('info', '--format', '{{json .Swarm}}'))
        if not info.get('ControlAvailable') or info.get('LocalNodeState') != 'active' or not info.get('NodeID'):
            raise Failure('Selected Docker context must reach an active Swarm manager')
        self.manager = info['NodeID']
        previous, enforced = self.previous_broker()
        if self.mode == 'none' and enforced:
            raise Failure('Refusing to downgrade an enforcing installation to none')
        if self.mode == 'token':
            origin = self.env.get('BROKER_ORIGIN', 'https://database-broker:8443')
            if not re.fullmatch(r'https://[A-Za-z0-9.-]+(?::[0-9]+)?', origin):
                raise Failure('Production token discovery requires an exact HTTPS broker origin')
            port = urlsplit(origin).port or 443
            if port < 1 or port > 65535 or port in (8080, 8081):
                raise Failure('Broker HTTPS port must be valid and separate from legacy/health ports')
            self.public_policy = read_public(self.env.get('BROKER_CLIENTS_FILE', './broker-clients.yml'))
            image = base['services']['database-broker']['image']
            # Send policy bytes over stdin, including with SSH contexts. No host bind mount.
            self.docker.run('image', 'pull', image, timeout=300)
            raw = self.docker.run('run', '--rm', '-i', '--network', 'none', '--read-only', '--log-driver', 'none',
                                  '--entrypoint', '/broker-token', image, 'validate', '--policy', '-',
                                  '--deployment-id', self.deployment, data=self.public_policy)
            try:
                self.policy = json.loads(raw)
            except (ValueError, TypeError):
                raise Failure('Invalid policy validator response') from None
            # Production inputs must be provisioned by the operator PKI before rollout.
            self.ca = read_public(self.env.get('BROKER_CA_FILE', './pki/broker-ca.pem'))
            self.cert = read_public(self.env.get('BROKER_TLS_CERT_FILE', './pki/broker.pem'))
            secret_name = self.env.get('BROKER_TLS_KEY_SECRET', '')
            if not secret_name or not self.docker.inspect('secret', secret_name):
                raise Failure('Operator-managed broker TLS key secret is required')
        doc = self.render(base)
        self.validate_stack(doc)
        for image in sorted({s['image'] for s in doc['services'].values()}):
            self.docker.run('manifest', 'inspect', image, timeout=120)
        return previous, enforced

    def execute(self, check=False):
        base = self.base()
        if check:
            # Offline interpolation/reference validation; the selected image validates
            # policy semantics and PKI inputs during deployment before Swarm mutation.
            self.validate_stack(self.render(base, placeholders=True))
            return
        self.announce('preflight')
        previous, enforced = self.preflight(base)
        try:
            self.acquire()
            self.announce('prepare reusable resources')
            for name in ('postgres_password', 'valkey_password'):
                if not self.docker.inspect('secret', name):
                    # Preserve historical hex passwords, streamed without a local file.
                    self.docker.run('secret', 'create', name, '-', data=os.urandom(32).hex().encode())
            if self.mode == 'none':
                self.rollout(self.render(base), 'explicit legacy mode')
                return
            for key in self.policy['accepted_key_ids']:
                self.master(key)
            self.configs['policy'] = self.config('policy', self.public_policy)
            self.configs['policy_digest'] = digest(self.public_policy)
            self.configs['ca'] = self.config('ca', self.ca)
            self.configs['cert'] = self.config('cert', self.cert)
            image = base['services']['database-broker']['image']
            self.prepare_tokens(image, self.manager)
            final = self.render(base)
            if previous and not enforced:
                print('Migrating existing tokenless deployment; exposure remains until enforcement cutover.', flush=True)
                # First make new broker TLS available in explicit none mode. Only an
                # already-tokenless stack may enter this phase; never downgrade token mode.
                interim = self.render(base, enforcement='none')
                interim['services']['database-broker']['environment']['LEGACY_HTTP_PORT'] = '8080'
                interim['services']['database-broker']['deploy']['labels'].pop(LABEL + 'enforced', None)
                self.rollout(interim, 'legacy broker TLS preparation', only=['database-broker'])
                self.rollout(interim, 'authenticated consumer migration')
                self.rollout(final, 'enable broker enforcement')
            else:
                # Policy/key acceptance reaches every replica before new consumers.
                self.rollout(final, 'broker policy and key rollout', only=['database-broker'])
                self.rollout(final, 'consumer rollout')
            self.announce('enforcement active; verify scoped access')
            self.acceptance(final)
        finally:
            self.cleanup()

    def acceptance(self, final, readiness_only=False):
        # This separate helper gets a single app token, no master or backend credentials.
        # Probe every broker task directly while retaining the TLS hostname for SNI.
        ids = self.docker.run('service', 'ps', '--filter', 'desired-state=running', '--no-trunc', '--format', '{{.ID}}', self.stack + '_database-broker').decode().splitlines()
        addresses = []
        for task_id in ids:
            task = json.loads(self.docker.run('inspect', task_id))[0]
            if task.get('Status', {}).get('State') != 'running':
                raise Failure('Broker task is not running at acceptance')
            for attachment in task.get('NetworksAttachments', []):
                if attachment.get('Network', {}).get('Spec', {}).get('Name') == self.stack + '_internal':
                    for address in attachment.get('Addresses', []):
                        ip = address.split('/')[0]
                        addresses.append(('[' + ip + ']' if ':' in ip else ip) + ':' + str(urlsplit(self.env.get('BROKER_ORIGIN', 'https://database-broker:8443')).port or 443))
        if not addresses or len(addresses) < len(ids):
            raise Failure('Cannot resolve every broker replica for acceptance')
        client = next((c for c in sorted(self.tokens) if c in APPS), None)
        mounts = []
        probe_args = ['probe', '--origin', self.env.get('BROKER_ORIGIN', 'https://database-broker:8443'),
                      '--ca-file', '/ca.pem', '--addresses', ','.join(sorted(set(addresses)))]
        if client and not readiness_only:
            mounts = ['--secret', 'source=' + self.tokens[client] + ',target=token,mode=0400']
            probe_args += ['--token-file', '/run/secrets/token', '--client', client]
        if readiness_only:
            probe_args.append('--readiness-only')
        ready_addresses = []
        for service in ('access-gateway', 'files', 'notes'):
            if service not in final['services'] or final['services'][service].get('deploy', {}).get('replicas', 1) == 0:
                continue
            tasks = self.docker.run('service', 'ps', '--filter', 'desired-state=running', '--no-trunc', '--format', '{{.ID}}', self.stack + '_' + service).decode().splitlines()
            for task_id in tasks:
                task = json.loads(self.docker.run('inspect', task_id))[0]
                for attachment in task.get('NetworksAttachments', []):
                    if attachment.get('Network', {}).get('Spec', {}).get('Name') == self.stack + '_internal':
                        for address in attachment.get('Addresses', []):
                            ip = address.split('/')[0]
                            ready_addresses.append(('[' + ip + ']' if ':' in ip else ip) + ':8080')
        if ready_addresses:
            probe_args += ['--ready-addresses', ','.join(sorted(set(ready_addresses)))]
        self.helper = self.prefix + '-probe-' + self.run_id
        self.helper_kind = 'probe'
        self.docker.run('service', 'create', '--name', self.helper, '--constraint', 'node.id==' + self.manager,
                        '--restart-condition', 'none', '--no-healthcheck', '--log-driver', 'none', '--read-only',
                        '--cap-drop', 'ALL', *label_args(dict(self.owner, kind='probe', run=self.run_id)),
                        '--network', self.stack + '_internal', '--entrypoint', '/broker-token',
                        '--config', 'source=' + self.configs['ca'] + ',target=/ca.pem', *mounts,
                        final['services']['database-broker']['image'], *probe_args)
        end = time.monotonic() + self.timeout
        while time.monotonic() < end:
            ids = self.docker.run('service', 'ps', '--no-trunc', '--format', '{{.ID}}', self.helper).decode().splitlines()
            for task_id in ids:
                task = json.loads(self.docker.run('inspect', task_id))[0]
                status = task.get('Status', {})
                if status.get('State') == 'complete' and status.get('ContainerStatus', {}).get('ExitCode') == 0:
                    self.remove_helper()
                    return
                if status.get('State') in ('failed', 'rejected'):
                    raise Failure('Broker enforcement acceptance failed')
            time.sleep(1)
        raise Failure('Broker enforcement acceptance deadline exceeded')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='offline stack interpolation/reference validation')
    args = parser.parse_args()
    # Disable environment-driven shell tracing by never invoking a shell.
    for sig in (signal.SIGTERM, signal.SIGHUP, signal.SIGINT):
        signal.signal(sig, lambda *_: (_ for _ in ()).throw(Failure('Interrupted')))
    deployment = None
    try:
        deployment = Deployment()
        deployment.execute(check=args.check)
    except Failure as error:
        stage = deployment.stage if deployment else 'configuration'
        print('Deployment stopped during ' + stage + ': ' + str(error) + '. No automatic authentication downgrade.', file=sys.stderr)
        return 1
    except (ValueError, KeyError, TypeError, OSError):
        # Only controlled messages are allowed; JSON/key errors could include data.
        stage = deployment.stage if deployment else 'configuration'
        print('Deployment stopped during ' + stage + '; no automatic authentication downgrade. Check public inputs and Swarm resource ownership.', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
