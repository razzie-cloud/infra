#!/usr/bin/env python3
"""Isolated Docker control-plane fixture. Secret stdin is consumed, never saved."""
import json
import os
import re
import signal
import sys
from pathlib import Path

path = Path(os.environ['FAKE_DOCKER_STATE'])
s = json.loads(path.read_text())
a = sys.argv[1:]
assert a[:1] == ['--context']
context = a[1]
a = a[2:]
s.setdefault('contexts', []).append(context)
s.setdefault('commands', []).append(a)
s.setdefault('stack_environment', []).append(os.environ.get('STACK_NAME'))


def done(value='', code=0):
    path.write_text(json.dumps(s))
    if not isinstance(value, str):
        value = json.dumps(value)
    sys.stdout.write(value)
    sys.exit(code)


def opts(args, name):
    return [args[i + 1] for i, v in enumerate(args[:-1]) if v == name]


def resource_labels():
    return dict(v.split('=', 1) for v in opts(a, '--label'))


def interpolate_escapes(value):
    if isinstance(value, str):
        return value.replace('$$', '$')
    if isinstance(value, list):
        return [interpolate_escapes(v) for v in value]
    if isinstance(value, dict):
        return {k: interpolate_escapes(v) for k, v in value.items()}
    return value


if a[0] == 'compose':
    file = a[a.index('-f') + 1]
    if file == 'docker-stack.yml':
        if s.get('invalid_base'):
            done(code=1)
        done(s['base'])
    doc = json.loads(Path(file).read_text())
    assert 'v1.k1.1.' not in json.dumps(doc)
    done()
if a[:2] == ['stack', 'config']:
    doc = json.loads(Path(opts(a, '-c')[0]).read_text())
    invalid = any(any(k in service and service[k] is None for k in ('command', 'entrypoint'))
                  or isinstance(service.get('depends_on'), dict)
                  or any(isinstance(port, dict) and isinstance(port.get('published'), str)
                         for port in service.get('ports', [])) for service in doc['services'].values())
    done(code=1 if invalid or s.get('invalid_swarm') else 0)
if a[:1] == ['info']:
    done({'LocalNodeState': 'active', 'ControlAvailable': True, 'NodeID': 'manager-target'})
if a[:2] in (['image', 'pull'], ['manifest', 'inspect']):
    done(code=1 if s.get('missing_image') else 0)
if a[0] == 'run':
    data = sys.stdin.buffer.read()
    if 'validate-tls' in a:
        done(code=1 if s.get('invalid_pki') else 0)
    done(json.loads(data))
if a[0] in ('secret', 'config'):
    kind, op = a[:2]
    bucket = s.setdefault(kind, {})
    if op == 'inspect':
        done([bucket[a[2]]]) if a[2] in bucket else done(code=1)
    if op == 'ls':
        filters = opts(a, '--filter')
        names = [name for name, obj in bucket.items() if all(
            obj['Spec']['Labels'].get(f[6:].split('=', 1)[0]) == f[6:].split('=', 1)[1]
            for f in filters if f.startswith('label='))]
        done('\n'.join(names))
    if op == 'create':
        name = a[-2]
        if not re.fullmatch(r'[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,62}[A-Za-z0-9])?', name):
            done(code=1)
        if name in bucket:
            done(code=1)
        # Only lengths/ownership are retained, never bytes or their digests.
        data = sys.stdin.buffer.read()
        s['sequence'] = s.get('sequence', 0) + 1
        obj = {'ID': kind + '-' + str(s['sequence']), 'Spec': {'Name': name, 'Labels': resource_labels()}}
        bucket[name] = obj
        if kind == 'config' and name.endswith('-broker-deployment-lock') and s.get('promote_on_lock'):
            # Another deploy finishes between this run's preflight and lock acquisition.
            service = s['service']['custom-stack_database-broker']
            service['Spec']['Labels']["io.razzie.broker.enforced"] = 'token'
            service['Spec']['TaskTemplate']['ContainerSpec']['Env'] = ['AUTH_MODE=token']
        if kind == 'secret':
            s.setdefault('secret_input_lengths', []).append(len(data))
        done(obj['ID'])
    if op == 'rm':
        bucket.pop(a[2], None)
        done()
if a[:2] == ['service', 'create']:
    # A short-lived probe can finish before the CLI observes a running replica.
    # Synchronous service create would keep waiting instead of returning its ID.
    if '--detach=true' not in a:
        done(code=1)
    name = opts(a, '--name')[0]
    s.setdefault('service', {})[name] = {'ID': name, 'helper': True, 'probe': 'probe' in a,
        'Spec': {'Name': name, 'Labels': resource_labels(), 'TaskTemplate': {'ContainerSpec': {'Image': 'helper'}}}}
    done(name)
if a[:2] == ['service', 'inspect']:
    obj = s.get('service', {}).get(a[2])
    done([obj]) if obj else done(code=1)
if a[:2] == ['service', 'ls']:
    if '--filter' in a:
        name = opts(a, '--filter')[0].split('=', 1)[1]
        done(name + ' 1/1')
    done('\n'.join(s.get('service', {})))
if a[:2] == ['service', 'rm']:
    s.get('service', {}).pop(a[2], None)
    done()
if a[:2] == ['service', 'ps']:
    name = a[-1]
    obj = s.get('service', {}).get(name)
    if not obj:
        done()
    count = obj['Spec'].get('Mode', {}).get('Replicated', {}).get('Replicas', 1)
    done('\n'.join('task:' + name + ':' + str(i) for i in range(count)))
if a[0] == 'inspect':
    _, name, number = a[1].split(':')
    obj = s['service'][name]
    state = 'complete' if obj.get('probe') else 'running'
    if s.get('pending_helper') and obj.get('helper'):
        state = 'pending'
    task_spec = json.loads(json.dumps(obj['Spec']['TaskTemplate']))
    if s.get('swarm_default_fields') and not obj.get('helper'):
        task_spec.pop('Runtime', None)
        task_spec['RestartPolicy'].pop('Delay', None)
        task_spec['ContainerSpec'].pop('StopGracePeriod', None)
        task_spec['ContainerSpec'].pop('DNSConfig', None)
    if s.get('stale_task') and not obj.get('helper'):
        task_spec['ContainerSpec']['Env'].append('STALE_SETTING=true')
    done([{'NodeID': 'manager-target', 'Spec': task_spec,
           'Status': {'State': state, 'ContainerStatus': {'ContainerID': 'container:' + name, 'ExitCode': 0}},
           'NetworksAttachments': [{'Network': {'Spec': {'Name': s.get('stack', 'razzie-cloud') + '_internal'}},
                                    'Addresses': ['10.0.0.' + str(10 + int(number)) + '/24']}]}])
if a[:2] == ['container', 'inspect']:
    done([{'State': {'Health': {'Status': 'healthy'}}}])
if a[0] == 'ps':
    done()
if a[0] == 'exec':
    if s.get('interrupt_generator'):
        path.write_text(json.dumps(s))
        os.kill(os.getppid(), signal.SIGTERM)
        done(code=1)
    if s.get('fail_generator') or s.get('fail_client') == opts(a, '--client')[0]:
        done('sensitive-partial-token', code=1)
    if s.get('truncate_generator'):
        done('v1.k1.1.partial')
    client = opts(a, '--client')[0]
    policy = s['policy']
    done('v1.' + policy['key_id'] + '.' + str(policy['clients'][client]['generation']) + '.' + 'A' * 43)
if a[:2] == ['stack', 'deploy']:
    doc = interpolate_escapes(json.loads(Path(opts(a, '-c')[0]).read_text()))
    s['stack'] = a[-1]
    s.setdefault('deploys', []).append(doc)
    for name, service in doc['services'].items():
        env = [k + '=' + str(v) for k, v in service.get('environment', {}).items()]
        spec = {'Name': a[-1] + '_' + name, 'Labels': service.get('deploy', {}).get('labels', {}),
                'Mode': {'Replicated': {'Replicas': service.get('deploy', {}).get('replicas', 1)}},
                'TaskTemplate': {'ContainerSpec': {'Image': service['image'], 'Env': env,
                    'Args': service.get('command', []), 'Secrets': service.get('secrets', [])}}}
        if s.get('swarm_default_fields'):
            spec['TaskTemplate']['Runtime'] = 'container'
            spec['TaskTemplate']['RestartPolicy'] = {'Condition': 'on-failure', 'Delay': 5000000000}
            spec['TaskTemplate']['ContainerSpec'].update(StopGracePeriod=10000000000, DNSConfig={})
        s.setdefault('service', {})[spec['Name']] = {'ID': spec['Name'], 'Spec': spec,
            'UpdateStatus': {'State': 'paused' if s.get('pause_rollout') else 'completed'}}
    done()
done(code=1)
