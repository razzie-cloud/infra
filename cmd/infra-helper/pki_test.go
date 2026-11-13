package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func pkiFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.state = object{}
	f.env["DOCKER_CONTEXT"] = "isolated-pki-test"
	f.env["STACK_NAME"] = "test-pki"
	f.env["BROKER_DEPLOYMENT_ID"] = "test-pki"
	f.env["BROKER_ORIGIN"] = "https://database-broker:8443"
	for k, v := range map[string]string{"BROKER_CA_FILE": "ca.pem", "BROKER_TLS_CERT_FILE": "broker.pem", "BROKER_CA_KEY_FILE": "ca-key.pem"} {
		f.env[k] = filepath.Join(f.dir, "pki", v)
	}
	f.env["BROKER_TLS_KEY_SECRET"] = "test-tls-key"
	return f
}
func TestPKIBootstrapReuse(t *testing.T) {
	f := pkiFixture(t)
	f.execute(true, "--pki")
	before := clone(f.state)
	saved := map[string]string{}
	for _, k := range []string{"BROKER_CA_FILE", "BROKER_TLS_CERT_FILE", "BROKER_CA_KEY_FILE"} {
		b, e := os.ReadFile(f.env[k])
		must(e)
		saved[k] = string(b)
	}
	for path, want := range map[string]os.FileMode{f.env["BROKER_CA_KEY_FILE"]: 0600, filepath.Dir(f.env["BROKER_CA_FILE"]): 0700} {
		i, e := os.Stat(path)
		must(e)
		equal(t, i.Mode().Perm(), want)
	}
	f.execute(true, "--pki")
	equal(t, f.state["secret"], before["secret"])
	equal(t, f.lengths(), before["secret_input_lengths"])
	for k, b := range saved {
		got, e := os.ReadFile(f.env[k])
		must(e)
		equal(t, string(got), b)
	}
	if _, e := exec.LookPath("openssl"); e != nil {
		t.Skip("OpenSSL unavailable for independent certificate verification")
	}
	cmd := exec.Command("openssl", "verify", "-CAfile", f.env["BROKER_CA_FILE"], "-verify_hostname", "database-broker", f.env["BROKER_TLS_CERT_FILE"])
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("OpenSSL: %s %v", b, e)
	}
	if exec.Command("openssl", "verify", "-CAfile", f.env["BROKER_CA_FILE"], "-verify_hostname", "different-broker", f.env["BROKER_TLS_CERT_FILE"]).Run() == nil {
		t.Fatal("wrong hostname accepted")
	}
}
func TestPKIMissingSecret(t *testing.T) {
	f := pkiFixture(t)
	f.execute(true, "--pki")
	before := f.lengths()
	f.state["secret"] = object{}
	f.execute(false, "--pki")
	equal(t, f.lengths(), before)
	equal(t, len(obj(f.state["secret"])), 0)
}
func TestPKIReuseWithoutCAKey(t *testing.T) {
	f := pkiFixture(t)
	f.execute(true, "--pki")
	must(os.Remove(f.env["BROKER_CA_KEY_FILE"]))
	f.execute(true, "--pki")
}
func TestPKIResumePublication(t *testing.T) {
	f := pkiFixture(t)
	f.execute(true, "--pki")
	before := f.lengths()
	cert := f.env["BROKER_TLS_CERT_FILE"]
	must(os.Rename(cert, cert+".pending"))
	f.execute(true, "--pki")
	if !exists(cert) {
		t.Fatal("certificate not published")
	}
	equal(t, f.lengths(), before)
}
func TestPKIMissingCertificate(t *testing.T) {
	f := pkiFixture(t)
	f.execute(true, "--pki")
	before := clone(obj(f.state["secret"]))
	must(os.Remove(f.env["BROKER_TLS_CERT_FILE"]))
	f.execute(false, "--pki")
	equal(t, f.state["secret"], before)
}
func TestPKIInvalidModeOrigin(t *testing.T) {
	f := pkiFixture(t)
	f.env["BROKER_ORIGIN"] = "http://database-broker:8080"
	f.execute(false, "--pki")
	equal(t, f.state, object{"policy": f.policy})
	f.env["BROKER_AUTH_MODE"] = "none"
	f.execute(false, "--pki")
	equal(t, f.state, object{"policy": f.policy})
}
