#!/usr/bin/env python3
"""Isolated Docker control-plane fixture. Secret stdin is consumed, never saved."""
import json
import os
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


if a[0] == 'compose':
    file = a[a.index('-f') + 1]
    if file == 'docker-stack.yml':
        if s.get('invalid_base'):
            done(code=1)
        done(s['base'])
    doc = json.loads(Path(file).read_text())
    assert 'v1.k1.1.' not in json.dumps(doc)
    done()
if a[:1] == ['info']:
    done({'LocalNodeState': 'active', 'ControlAvailable': True, 'NodeID': 'manager-target'})
if a[:2] in (['image', 'pull'], ['manifest', 'inspect']):
    done(code=1 if s.get('missing_image') else 0)
if a[0] == 'run':
    data = sys.stdin.buffer.read()
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
        if name in bucket:
            done(code=1)
        # Only lengths/ownership are retained, never bytes or their digests.
        data = sys.stdin.buffer.read()
        s['sequence'] = s.get('sequence', 0) + 1
        obj = {'ID': kind + '-' + str(s['sequence']), 'Spec': {'Name': name, 'Labels': resource_labels()}}
        bucket[name] = obj
        if kind == 'secret':
            s.setdefault('secret_input_lengths', []).append(len(data))
        done(obj['ID'])
    if op == 'rm':
        bucket.pop(a[2], None)
        done()
if a[:2] == ['service', 'create']:
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
    done([{'NodeID': 'manager-target', 'Spec': obj['Spec']['TaskTemplate'],
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
    doc = json.loads(Path(opts(a, '-c')[0]).read_text())
    s['stack'] = a[-1]
    s.setdefault('deploys', []).append(doc)
    for name, service in doc['services'].items():
        env = [k + '=' + str(v) for k, v in service.get('environment', {}).items()]
        spec = {'Name': a[-1] + '_' + name, 'Labels': service.get('deploy', {}).get('labels', {}),
                'Mode': {'Replicated': {'Replicas': service.get('deploy', {}).get('replicas', 1)}},
                'TaskTemplate': {'ContainerSpec': {'Image': service['image'], 'Env': env,
                    'Args': service.get('command', []), 'Secrets': service.get('secrets', [])}}}
        s.setdefault('service', {})[spec['Name']] = {'ID': spec['Name'], 'Spec': spec,
            'UpdateStatus': {'State': 'paused' if s.get('pause_rollout') else 'completed'}}
    done()
done(code=1)
