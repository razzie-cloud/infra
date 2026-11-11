#!/usr/bin/env python3
"""Prepare an internal broker CA and TLS secret without rotating existing inputs."""
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
from urllib.parse import urlsplit

from deploy import Deployment, Failure, digest, label_args, own


def openssl(*args, data=None):
    try:
        result = subprocess.run(['openssl', *args], input=data, capture_output=True, timeout=30)
    except (OSError, subprocess.TimeoutExpired):
        raise Failure('OpenSSL is required and must finish within 30 seconds') from None
    if result.returncode:
        raise Failure('Broker PKI generation or certificate validation failed')
    return result.stdout


def write_new(path, data, mode=0o600):
    # Never truncate an existing key or follow a symlink at the destination.
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(data)


def validate(ca, cert, hostname):
    openssl('verify', '-CAfile', str(ca), '-verify_hostname', hostname, str(cert))


def prepare(deployment):
    if deployment.mode != 'token':
        raise Failure('Internal broker PKI setup requires token mode')
    origin = deployment.env.get('BROKER_ORIGIN', 'https://database-broker:8443')
    if not re.fullmatch(r'https://[A-Za-z0-9.-]+(?::[0-9]+)?', origin):
        raise Failure('Internal broker PKI requires an exact HTTPS broker origin')
    hostname = urlsplit(origin).hostname
    ca = Path(deployment.env.get('BROKER_CA_FILE', './pki/broker-ca.pem'))
    cert = Path(deployment.env.get('BROKER_TLS_CERT_FILE', './pki/broker.pem'))
    ca_key = Path(deployment.env.get('BROKER_CA_KEY_FILE') or ca.with_name('broker-ca-key.pem'))
    secret = deployment.env.get('BROKER_TLS_KEY_SECRET', 'broker_tls_key')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,63}', secret):
        raise Failure('Invalid broker TLS secret name')
    if len({p.resolve() for p in (ca, cert, ca_key)}) != 3:
        raise Failure('Broker PKI paths must be distinct')
    info = deployment.docker.run('info', '--format', '{{json .Swarm}}')
    manager = json.loads(info)
    if not manager.get('ControlAvailable') or manager.get('LocalNodeState') != 'active':
        raise Failure('Internal broker PKI setup requires an active Swarm manager')
    # Share the deployment lock: setup cannot race a rollout or another setup.
    deployment.acquire()
    try:
        existing = deployment.docker.inspect('secret', secret)
        pending = cert.with_name(cert.name + '.pending')
        if existing:
            if not ca.is_file():
                raise Failure('Existing TLS secret needs its original CA and certificate; refusing replacement')
            if not cert.exists() and pending.is_file():
                # Recover interruption after Swarm accepted the private key.
                own(existing, dict(deployment.owner, kind='internal-tls-key',
                                   hostname=hostname, **{'cert-sha256': digest(pending.read_bytes())}))
                validate(ca, pending, hostname)
                os.replace(pending, cert)
            if not cert.is_file():
                raise Failure('Existing TLS secret needs its original certificate; refusing replacement')
            validate(ca, cert, hostname)
            print('Reusing existing broker CA, certificate and TLS secret; no rotation.')
            return
        if cert.exists():
            raise Failure('Broker certificate exists but TLS secret is missing; use explicit key renewal')
        for path in (ca, cert, ca_key):
            if path.is_symlink() or path.parent.is_symlink():
                raise Failure('Broker PKI inputs must not be symlinks')
            if not path.parent.exists():
                path.parent.mkdir(parents=True, mode=0o700)
        if ca.exists() != ca_key.exists():
            raise Failure('Incomplete broker CA inputs; restore the matching certificate and key')
        if not ca.exists():
            key = openssl('genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:P-256')
            public = openssl('req', '-new', '-x509', '-key', '/dev/stdin', '-days', '3650',
                             '-subj', '/CN=Razzie Cloud Internal Broker CA',
                             '-addext', 'basicConstraints=critical,CA:TRUE,pathlen:0',
                             '-addext', 'keyUsage=critical,keyCertSign,cRLSign', data=key)
            write_new(ca_key, key)
            del key
            write_new(ca, public, 0o644)
        if ca_key.stat().st_mode & 0o077:
            raise Failure('Broker CA private key must be readable only by its owner')
        # Check that the persisted CA key matches before creating a leaf secret.
        public_key = openssl('pkey', '-in', str(ca_key), '-pubout')
        if public_key != openssl('x509', '-in', str(ca), '-pubkey', '-noout'):
            raise Failure('Broker CA certificate and private key do not match')
        key = openssl('genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:P-256')
        csr = openssl('req', '-new', '-key', '/dev/stdin', '-subj', '/CN=' + hostname,
                      '-addext', 'subjectAltName=DNS:' + hostname,
                      '-addext', 'basicConstraints=critical,CA:FALSE',
                      '-addext', 'extendedKeyUsage=serverAuth', data=key)
        public = openssl('x509', '-req', '-CA', str(ca), '-CAkey', str(ca_key),
                         '-set_serial', '0x' + os.urandom(20).hex(), '-days', '397',
                         '-copy_extensions', 'copy', data=csr)
        # A stale public pending certificate has no private key when no secret exists.
        if pending.exists():
            if pending.is_symlink():
                raise Failure('Pending broker certificate must not be a symlink')
            pending.unlink()
        write_new(pending, public, 0o644)
        validate(ca, pending, hostname)
        expected = dict(deployment.owner, kind='internal-tls-key', hostname=hostname,
                        **{'cert-sha256': digest(public)})
        deployment.docker.run('secret', 'create', *label_args(expected), secret, '-', data=key)
        del key
        os.replace(pending, cert)
        print('Prepared internal broker CA and certificate; TLS key streamed only to Swarm.')
        print('Keep the ignored CA private key for renewal; back it up securely.')
    finally:
        deployment.cleanup()


def main():
    for sig in (signal.SIGTERM, signal.SIGHUP, signal.SIGINT):
        signal.signal(sig, lambda *_: (_ for _ in ()).throw(Failure('Interrupted')))
    try:
        prepare(Deployment())
    except (Failure, OSError, ValueError, KeyError, TypeError):
        # OpenSSL errors can contain key input; emit no subprocess diagnostics.
        print('Broker PKI setup stopped. Check public inputs, existing secret state and file permissions; no automatic rotation.', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
