package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

func exists(path string) bool {
	_, e := os.Stat(path)
	if os.IsNotExist(e) {
		return false
	}
	must(e)
	return true
}
func noSymlink(path string) {
	i, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return
	}
	must(e)
	if i.Mode()&os.ModeSymlink != 0 {
		fail("Broker PKI inputs must not be symlinks")
	}
}
func writeNew(path string, data []byte, mode os.FileMode) {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	must(e)
	defer f.Close()
	_, e = f.Write(data)
	must(e)
	must(f.Close())
}
func certificate(path string) *x509.Certificate {
	b := readPublic(path)
	p, _ := pem.Decode(b)
	if p == nil || p.Type != "CERTIFICATE" {
		fail("Invalid public certificate")
	}
	c, e := x509.ParseCertificate(p.Bytes)
	must(e)
	return c
}
func validateCertificates(ca, cert, hostname string) {
	roots := x509.NewCertPool()
	roots.AddCert(certificate(ca))
	_, e := certificate(cert).Verify(x509.VerifyOptions{Roots: roots, DNSName: hostname})
	must(e)
}
func privateKey() *ecdsa.PrivateKey {
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(e)
	return k
}
func keyPEM(k *ecdsa.PrivateKey) []byte {
	b, e := x509.MarshalPKCS8PrivateKey(k)
	must(e)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b})
}
func serial() *big.Int {
	n, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 159))
	must(e)
	return n.Add(n, big.NewInt(1))
}
func certPEM(template, parent *x509.Certificate, pub any, key crypto.Signer) []byte {
	b, e := x509.CreateCertificate(rand.Reader, template, parent, pub, key)
	must(e)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b})
}
func (d *deployment) preparePKI() {
	if d.mode != "token" {
		fail("Internal broker PKI setup requires token mode")
	}
	if !regexp.MustCompile(`^https://[A-Za-z0-9.-]+(?::[0-9]+)?$`).MatchString(d.origin()) {
		fail("Internal broker PKI requires an exact HTTPS broker origin")
	}
	hostname := brokerURL(d.origin()).Hostname()
	ca := d.setting("BROKER_CA_FILE", "./pki/broker-ca.pem")
	cert := d.setting("BROKER_TLS_CERT_FILE", "./pki/broker.pem")
	caKey := fallback(d.env, "BROKER_CA_KEY_FILE", filepath.Join(filepath.Dir(ca), "broker-ca-key.pem"))
	secret := d.setting("BROKER_TLS_KEY_SECRET", "broker_tls_key")
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`).MatchString(secret) {
		fail("Invalid broker TLS secret name")
	}
	paths := map[string]bool{}
	for _, p := range []string{ca, cert, caKey} {
		a, e := filepath.Abs(p)
		must(e)
		if paths[a] {
			fail("Broker PKI paths must be distinct")
		}
		paths[a] = true
	}
	info := obj(decode(d.docker.call("info", "--format", "{{json .Swarm}}")))
	if info["ControlAvailable"] != true || str(info["LocalNodeState"]) != "active" {
		fail("Internal broker PKI setup requires an active Swarm manager")
	}
	defer d.cleanup()
	d.acquire()
	existing := d.docker.inspect("secret", secret)
	pending := cert + ".pending"
	if existing != nil {
		if !exists(ca) {
			fail("Existing TLS secret needs its original CA and certificate; refusing replacement")
		}
		if !exists(cert) && exists(pending) {
			own(existing, extend(d.owner, object{"kind": "internal-tls-key", "hostname": hostname, "cert-sha256": digest(readPublic(pending))}))
			validateCertificates(ca, pending, hostname)
			must(os.Rename(pending, cert))
		}
		if !exists(cert) {
			fail("Existing TLS secret needs its original certificate; refusing replacement")
		}
		validateCertificates(ca, cert, hostname)
		fmt.Println("Reusing existing broker CA, certificate and TLS secret; no rotation.")
		return
	}
	if exists(cert) {
		fail("Broker certificate exists but TLS secret is missing; use explicit key renewal")
	}
	for _, p := range []string{ca, cert, caKey} {
		noSymlink(p)
		noSymlink(filepath.Dir(p))
		must(os.MkdirAll(filepath.Dir(p), 0700))
	}
	if exists(ca) != exists(caKey) {
		fail("Incomplete broker CA inputs; restore the matching certificate and key")
	}
	if !exists(ca) {
		k := privateKey()
		now := time.Now()
		template := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "Razzie Cloud Internal Broker CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, MaxPathLen: 0, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		public := certPEM(template, template, &k.PublicKey, k)
		writeNew(caKey, keyPEM(k), 0600)
		writeNew(ca, public, 0644)
	}
	infoKey, e := os.Stat(caKey)
	must(e)
	if infoKey.Mode().Perm()&0077 != 0 {
		fail("Broker CA private key must be readable only by its owner")
	}
	b, e := os.ReadFile(caKey)
	must(e)
	block, _ := pem.Decode(b)
	if block == nil {
		fail("Invalid broker CA private key")
	}
	var parsed any
	switch block.Type {
	case "PRIVATE KEY":
		parsed, e = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		parsed, e = x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		parsed, e = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		fail("Unsupported broker CA private key")
	}
	must(e)
	k, ok := parsed.(crypto.Signer)
	if !ok {
		fail("Unsupported broker CA private key")
	}
	parent := certificate(ca)
	public, e := x509.MarshalPKIXPublicKey(parent.PublicKey)
	must(e)
	keyPublic, e := x509.MarshalPKIXPublicKey(k.Public())
	must(e)
	if !bytes.Equal(public, keyPublic) {
		fail("Broker CA certificate and private key do not match")
	}
	leafKey := privateKey()
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: hostname}, DNSNames: []string{hostname}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(397 * 24 * time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leaf := certPEM(template, parent, &leafKey.PublicKey, k)
	noSymlink(pending)
	if exists(pending) {
		must(os.Remove(pending))
	}
	writeNew(pending, leaf, 0644)
	validateCertificates(ca, pending, hostname)
	expected := extend(d.owner, object{"kind": "internal-tls-key", "hostname": hostname, "cert-sha256": digest(leaf)})
	d.docker.input(keyPEM(leafKey), append(append([]string{"secret", "create"}, labelArgs(expected)...), secret, "-")...)
	must(os.Rename(pending, cert))
	fmt.Println("Prepared internal broker CA and certificate; TLS key streamed only to Swarm.")
	fmt.Println("Keep the ignored CA private key for renewal; back it up securely.")
}
