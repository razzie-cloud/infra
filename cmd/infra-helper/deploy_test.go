package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("RAZCLOUD_HELPER_TEST_PROCESS") == "1" {
		if len(os.Args) > 1 && os.Args[1] == "--context" {
			os.Exit(fakeDocker())
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP, os.Interrupt)
		defer stop()
		os.Exit(run(ctx, os.Args[1:]))
	}
	os.Exit(m.Run())
}

type fixture struct {
	t                          *testing.T
	dir, statePath, policyPath string
	state, policy              object
	env                        map[string]string
	command                    []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	f := &fixture{t: t, dir: dir, statePath: filepath.Join(dir, "state.json"), policyPath: filepath.Join(dir, "policy.json"), env: map[string]string{}}
	clients := object{}
	for c := range apps {
		clients[c] = object{"generation": 1, "accepted_generations": []any{1}, "allow": []any{}}
	}
	f.policy = object{"version": 1, "deployment_id": "test-deploy", "key_id": "k1", "accepted_key_ids": []any{"k1"}, "clients": clients}
	services := object{}
	for _, name := range []string{"database-broker", "access-gateway", "files", "notes", "razchess", "geoip-server", "postgres", "valkey"} {
		services[name] = object{"image": "test/" + name + ":selected", "environment": object{}, "deploy": object{"replicas": 1}}
	}
	at(obj(services["database-broker"]), "healthcheck")["test"] = []any{"CMD", "/readyz"}
	for _, c := range []string{"access_gateway", "files", "notes"} {
		at(obj(services[str(apps[c])]), "environment")["POSTGRES_URI_FROM"] = "http://database-broker:8080/v1/instances/postgres/" + c + "/uri"
	}
	at(obj(services["access-gateway"]), "environment")["REDIS_URI_FROM"] = "http://database-broker:8080/v1/instances/valkey/access_gateway/uri"
	obj(services["razchess"])["command"] = []any{"--redis-from", "http://database-broker/uri", "--session-timeout", "5m"}
	f.state = object{"base": object{"services": services}, "policy": f.policy, "secret": object{"pki_key": object{"ID": "operator-key", "Spec": object{"Labels": object{}}}}}
	for _, v := range os.Environ() {
		k, v, _ := strings.Cut(v, "=")
		f.env[k] = v
	}
	for k, v := range map[string]string{"DOCKER_CLI": exe, "GORACE": "atexit_sleep_ms=0", "RAZCLOUD_HELPER_TEST_PROCESS": "1", "FAKE_DOCKER_STATE": f.statePath, "DOCKER_CONTEXT": "ssh-custom-manager", "STACK_NAME": "custom-stack", "BROKER_DEPLOYMENT_ID": "test-deploy", "BROKER_CLIENTS_FILE": f.policyPath, "BROKER_CA_FILE": filepath.Join(dir, "ca.pem"), "BROKER_TLS_CERT_FILE": filepath.Join(dir, "cert.pem"), "BROKER_TLS_KEY_SECRET": "pki_key", "BROKER_AUTH_MODE": "token", "DEPLOY_TIMEOUT": "5", "DATABASE_BROKER_IMAGE_TAG": "selected"} {
		f.env[k] = v
	}
	for _, p := range []string{f.env["BROKER_CA_FILE"], f.env["BROKER_TLS_CERT_FILE"]} {
		must(os.WriteFile(p, []byte("public-test-certificate"), 0600))
	}
	return f
}
func (f *fixture) save() {
	f.t.Helper()
	f.state["policy"] = f.policy
	must(os.WriteFile(f.statePath, encode(f.state), 0600))
	must(os.WriteFile(f.policyPath, encode(f.policy), 0600))
}
func (f *fixture) execute(success bool, args ...string) string {
	f.t.Helper()
	f.save()
	exe, e := os.Executable()
	if e != nil {
		f.t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	if len(f.command) > 0 {
		cmd = exec.CommandContext(ctx, f.command[0], append(f.command[1:], args...)...)
	}
	cmd.Dir = f.dir
	for k, v := range f.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	b, e := cmd.CombinedOutput()
	if (e == nil) != success {
		f.t.Fatalf("success=%v error=%v output=%s", success, e, b)
	}
	raw, e := os.ReadFile(f.statePath)
	if e != nil {
		f.t.Fatal(e)
	}
	f.state = obj(decode(raw))
	for _, s := range []string{"sensitive-partial-token", "v1.k1.1.", "PRIVATE KEY"} {
		if strings.Contains(string(b), s) || strings.Contains(string(raw), s) {
			f.t.Fatalf("sensitive data retained: %s", s)
		}
	}
	for _, o := range obj(f.state["service"]) {
		if obj(o)["helper"] == true {
			f.t.Fatal("helper leaked")
		}
	}
	for _, o := range obj(f.state["config"]) {
		if str(labels(obj(o))[label+"kind"]) == "deployment-lock" && str(labels(obj(o))[label+"run"]) != "other" {
			f.t.Fatal("lock leaked")
		}
	}
	return string(b)
}
func (f *fixture) lengths() []any { return arr(f.state["secret_input_lengths"]) }
func (f *fixture) final() object  { a := arr(f.state["deploys"]); return obj(a[len(a)-1]) }
func equal(t *testing.T, a, b any) {
	t.Helper()
	if !reflect.DeepEqual(decode(encode(a)), decode(encode(b))) {
		t.Fatalf("got %s; want %s", encode(a), encode(b))
	}
}
func generationCount(s object) int {
	n := 0
	for _, v := range arr(s["commands"]) {
		a := arr(v)
		if len(a) > 1 && str(a[0]) == "service" && str(a[1]) == "create" {
			for _, v := range a {
				if v == "hold" {
					n++
				}
			}
		}
	}
	return n
}
func kindCount(s object, kind string) int {
	n := 0
	for _, o := range obj(s["secret"]) {
		if str(labels(obj(o))[label+"kind"]) == kind {
			n++
		}
	}
	return n
}
func TestRedeployAndPolicyEdits(t *testing.T) {
	f := newFixture(t)
	f.execute(true)
	lengths := f.lengths()
	if len(lengths) != 8 || kindCount(f.state, "master") != 1 {
		t.Fatal("unexpected initial secrets")
	}
	count := generationCount(f.state)
	f.execute(true)
	equal(t, f.lengths(), lengths)
	equal(t, generationCount(f.state), count)
	obj(obj(f.policy["clients"])["files"])["allow"] = []any{object{"adapter": "postgres", "instance": "files"}}
	f.execute(true)
	equal(t, generationCount(f.state), count)
	obj(f.policy["clients"])["added"] = object{"generation": 1, "accepted_generations": []any{1}, "allow": []any{}}
	f.execute(true)
	equal(t, generationCount(f.state), count+1)
	equal(t, len(f.lengths()), len(lengths)+1)
	command := arr(obj(obj(f.final()["services"])["razchess"])["command"])
	equal(t, command[len(command)-3:], []any{"--", "--session-timeout", "5m"})
	for _, v := range arr(f.state["contexts"]) {
		equal(t, v, "ssh-custom-manager")
	}
}
func TestGenerationFailuresAndRetry(t *testing.T) {
	f := newFixture(t)
	for _, failure := range []string{"fail_generator", "truncate_generator", "interrupt_generator"} {
		f.state[failure] = true
		f.execute(false)
		equal(t, kindCount(f.state, "token"), 0)
		delete(f.state, failure)
	}
	f.execute(true)
	equal(t, kindCount(f.state, "master"), 1)
	equal(t, len(f.lengths()), 8)
}
func TestLongNamesPreserveIdentities(t *testing.T) {
	f := newFixture(t)
	f.env["STACK_NAME"] = "stack" + strings.Repeat("x", 43)
	key := strings.Repeat("k", 64)
	f.policy["key_id"] = key
	f.policy["accepted_key_ids"] = []any{key}
	obj(f.policy["clients"])[strings.Repeat("c", 64)] = object{"generation": 9007199254740991, "accepted_generations": []any{9007199254740991}, "allow": []any{}}
	f.execute(true)
	before := clone(f.state)
	for _, kind := range []string{"secret", "config"} {
		for name := range obj(f.state[kind]) {
			if len(name) > 64 {
				t.Fatal("resource name too long")
			}
		}
	}
	f.execute(true)
	for _, kind := range []string{"secret", "config", "secret_input_lengths"} {
		equal(t, f.state[kind], before[kind])
	}
}
func TestComposeNormalization(t *testing.T) {
	f := newFixture(t)
	s := obj(obj(f.state["base"])["services"])
	for _, v := range s {
		m := obj(v)
		m["entrypoint"] = nil
		if _, ok := m["command"]; !ok {
			m["command"] = nil
		}
	}
	obj(s["database-broker"])["depends_on"] = object{"postgres": object{"condition": "service_started", "required": true}}
	obj(s["postgres"])["command"] = []any{}
	obj(s["postgres"])["ports"] = []any{object{"target": 5432, "published": "15432", "mode": "host"}}
	f.execute(true)
	final := obj(f.final()["services"])
	for _, k := range []string{"entrypoint", "command", "depends_on"} {
		if _, ok := obj(final["database-broker"])[k]; ok {
			t.Fatal("field retained:", k)
		}
	}
	equal(t, obj(final["postgres"])["command"], []any{})
	equal(t, obj(final["postgres"])["ports"], []any{object{"target": 5432, "published": 15432, "mode": "host"}})
}
func TestShellEscapesOnce(t *testing.T) {
	f := newFixture(t)
	v := obj(obj(obj(f.state["base"])["services"])["valkey"])
	v["command"] = []any{"sh", "-ec", `hash=$$(cat /secret); echo "$$hash" "$$$$"`}
	v["healthcheck"] = object{"test": []any{"CMD-SHELL", "AUTH=$$(cat /secret) check"}}
	v["environment"] = object{"LITERAL": "value-$${NOT_SET}"}
	f.execute(true)
	got := obj(obj(f.final()["services"])["valkey"])
	equal(t, got["command"], []any{"sh", "-ec", `hash=$(cat /secret); echo "$hash" "$$"`})
	equal(t, got["environment"], object{"LITERAL": "value-${NOT_SET}"})
	equal(t, got["healthcheck"], object{"test": []any{"CMD-SHELL", "AUTH=$(cat /secret) check"}})
}
func TestSwarmDefaultsAndStaleTasks(t *testing.T) {
	t.Run("defaults", func(t *testing.T) { f := newFixture(t); f.state["swarm_default_fields"] = true; f.execute(true) })
	t.Run("stale", func(t *testing.T) {
		f := newFixture(t)
		f.state["swarm_default_fields"] = true
		f.state["stale_task"] = true
		f.execute(false)
	})
}
func TestImagePulls(t *testing.T) {
	f := newFixture(t)
	f.execute(true)
	pulled := map[string]bool{}
	for _, v := range arr(f.state["commands"]) {
		a := arr(v)
		if len(a) > 2 && a[0] == "image" && a[1] == "pull" {
			pulled[str(a[2])] = true
		}
		if len(a) > 1 && a[0] == "manifest" {
			t.Fatal("local manifest check")
		}
	}
	for _, s := range obj(f.final()["services"]) {
		if !pulled[str(obj(s)["image"])] {
			t.Fatal("image not pulled")
		}
	}
}
func TestExplicitClientPath(t *testing.T) {
	f := newFixture(t)
	selected := filepath.Join(f.dir, "selected docker client")
	must(os.Symlink(f.env["DOCKER_CLI"], selected))
	f.env["DOCKER_CLI"] = selected
	f.execute(true)
}
func TestPartialClientRetry(t *testing.T) {
	f := newFixture(t)
	f.state["fail_client"] = "files"
	f.execute(false)
	equal(t, kindCount(f.state, "token"), 1)
	delete(f.state, "fail_client")
	f.execute(true)
	equal(t, len(f.lengths()), 8)
}
func TestHelperDeadline(t *testing.T) {
	f := newFixture(t)
	f.state["pending_helper"] = true
	f.execute(false)
	if f.state["deploys"] != nil {
		t.Fatal("deployed before helper ready")
	}
}
func TestExistingLockPreserved(t *testing.T) {
	f := newFixture(t)
	lock := object{"ID": "another-run", "Spec": object{"Labels": object{label + "kind": "deployment-lock", label + "run": "other"}}}
	f.state["config"] = object{"custom-stack-broker-deployment-lock": lock}
	f.execute(false)
	equal(t, obj(f.state["config"])["custom-stack-broker-deployment-lock"], lock)
	equal(t, len(f.lengths()), 0)
}
func TestLostReplacedMaster(t *testing.T) {
	f := newFixture(t)
	f.execute(true)
	var master string
	for n, o := range obj(f.state["secret"]) {
		if str(labels(obj(o))[label+"kind"]) == "master" {
			master = n
		}
	}
	original := obj(obj(f.state["secret"])[master])
	delete(obj(f.state["secret"]), master)
	f.execute(false)
	if obj(f.state["secret"])[master] != nil {
		t.Fatal("lost master regenerated")
	}
	original["ID"] = "replacement"
	obj(f.state["secret"])[master] = original
	f.execute(false)
	equal(t, len(f.lengths()), 8)
}
func TestRotationRevocation(t *testing.T) {
	f := newFixture(t)
	f.execute(true)
	f.policy["key_id"] = "k2"
	f.policy["accepted_key_ids"] = []any{"k1", "k2"}
	c := obj(obj(f.policy["clients"])["files"])
	c["generation"] = 2
	c["accepted_generations"] = []any{1, 2}
	f.execute(true)
	e := obj(obj(obj(f.final()["services"])["database-broker"])["environment"])
	if !strings.Contains(str(e["AUTH_KEY_FILES"]), "k2=") || !strings.Contains(str(e["AUTH_KEY_FILES"]), "k1=") {
		t.Fatal("overlap missing")
	}
	equal(t, kindCount(f.state, "master"), 2)
	c["enabled"] = false
	f.execute(true)
	equal(t, obj(obj(obj(f.final()["services"])["files"])["deploy"])["replicas"], 0)
}
func legacyService() object {
	return object{"ID": "old", "Spec": object{"Labels": object{}, "TaskTemplate": object{"ContainerSpec": object{"Image": "old-broker", "Env": []any{}}}}}
}
func TestLegacyMigrationAndDowngrade(t *testing.T) {
	f := newFixture(t)
	f.state["service"] = object{"custom-stack_database-broker": legacyService()}
	f.execute(true)
	equal(t, len(arr(f.state["deploys"])), 3)
	deploys := arr(f.state["deploys"])
	equal(t, keys(obj(obj(deploys[0])["services"])), []string{"database-broker"})
	equal(t, obj(obj(obj(obj(deploys[0])["services"])["database-broker"])["environment"])["AUTH_MODE"], "none")
	equal(t, obj(obj(obj(obj(deploys[0])["services"])["database-broker"])["environment"])["LEGACY_HTTP_PORT"], "8080")
	equal(t, obj(obj(obj(obj(deploys[1])["services"])["database-broker"])["environment"])["AUTH_MODE"], "none")
	equal(t, obj(obj(obj(obj(deploys[2])["services"])["database-broker"])["environment"])["AUTH_MODE"], "token")
	before := len(arr(f.state["deploys"]))
	f.env["BROKER_AUTH_MODE"] = "none"
	f.execute(false)
	equal(t, len(arr(f.state["deploys"])), before)
}
func TestModeRecheckedUnderLock(t *testing.T) {
	for _, mode := range []string{"token", "none"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.env["BROKER_AUTH_MODE"] = mode
			f.state["service"] = object{"custom-stack_database-broker": legacyService()}
			f.state["promote_on_lock"] = true
			f.execute(mode == "token")
			want := 2
			if mode == "none" {
				want = 0
			}
			equal(t, len(arr(f.state["deploys"])), want)
			for _, v := range arr(f.state["deploys"]) {
				equal(t, obj(obj(obj(obj(v)["services"])["database-broker"])["environment"])["AUTH_MODE"], "token")
			}
		})
	}
}
func TestNoneMode(t *testing.T) {
	f := newFixture(t)
	f.env["BROKER_AUTH_MODE"] = "none"
	f.env["BROKER_CLIENTS_FILE"] = "/missing"
	f.env["BROKER_CA_FILE"] = "/missing"
	f.env["BROKER_TLS_KEY_SECRET"] = ""
	f.execute(true)
	equal(t, len(f.lengths()), 2)
	equal(t, kindCount(f.state, "master"), 0)
	if strings.Contains(string(encode(f.final())), "--token-file") {
		t.Fatal("none mode token mount")
	}
}
func TestDirectDefaults(t *testing.T) {
	f := newFixture(t)
	delete(f.env, "STACK_NAME")
	delete(f.env, "DOCKER_CONTEXT")
	f.env["BROKER_AUTH_MODE"] = "none"
	f.execute(true)
	for _, v := range arr(f.state["contexts"]) {
		equal(t, v, "razcloud")
	}
	for _, v := range arr(f.state["stack_environment"]) {
		equal(t, v, "razzie-cloud")
	}
}
func TestInvalidPreflight(t *testing.T) {
	for _, failure := range []string{"missing_image", "invalid_pki", "invalid_swarm", "invalid_base"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.state[failure] = true
			f.execute(false)
			equal(t, len(f.lengths()), 0)
			equal(t, len(obj(f.state["config"])), 0)
			equal(t, len(arr(f.state["deploys"])), 0)
		})
	}
}
func TestPausedRollout(t *testing.T) {
	f := newFixture(t)
	f.state["pause_rollout"] = true
	f.execute(false)
	equal(t, len(arr(f.state["deploys"])), 1)
}
func TestConflictingToken(t *testing.T) {
	f := newFixture(t)
	f.execute(true)
	for _, o := range obj(f.state["secret"]) {
		if str(labels(obj(o))[label+"kind"]) == "token" {
			labels(obj(o))[label+"master-id"] = "wrong-master"
			break
		}
	}
	f.execute(false)
}
func TestOfflineCheck(t *testing.T) {
	f := newFixture(t)
	f.execute(true, "--check")
	equal(t, len(arr(f.state["deploys"])), 0)
	equal(t, len(f.lengths()), 0)
	for _, v := range arr(f.state["commands"]) {
		a := arr(v)
		if a[0] != "compose" && !(a[0] == "stack" && a[1] == "config") {
			t.Fatal("offline check contacted daemon")
		}
	}
}

func TestShellEntrypoint(t *testing.T) {
	path, err := filepath.Abs("../../deploy.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "check-from-another-directory", true: "interrupt-cleanup"}[interrupted], func(t *testing.T) {
			f := newFixture(t)
			f.command = []string{"bash", path}
			if interrupted {
				f.state["interrupt_generator"] = true
				f.execute(false)
			} else {
				f.execute(true, "--check")
			}
		})
	}
}
