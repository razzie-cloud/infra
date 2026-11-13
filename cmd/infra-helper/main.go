// Run with go run ./cmd/infra-helper [--check|--pki], from the stack directory.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type object = map[string]any
type failure string

func fail(s string) { panic(failure(s)) }
func must(err error) {
	if err != nil {
		fail("Operation failed; check public inputs and resource ownership")
	}
}
func obj(v any) object {
	if v == nil {
		return object{}
	}
	m, ok := v.(map[string]any)
	if !ok {
		fail("Invalid object in control-plane response")
	}
	return m
}
func at(m object, k string) object {
	if m[k] == nil {
		m[k] = object{}
	}
	return obj(m[k])
}
func arr(v any) []any {
	if v == nil {
		return nil
	}
	a, ok := v.([]any)
	if !ok {
		fail("Invalid array in control-plane response")
	}
	return a
}
func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func keys(m object) []string {
	a := make([]string, 0, len(m))
	for k := range m {
		a = append(a, k)
	}
	sort.Strings(a)
	return a
}
func decode(b []byte) any {
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	must(d.Decode(&v))
	return v
}
func encode(v any) []byte    { b, e := json.Marshal(v); must(e); return b }
func clone(m object) object  { return obj(decode(encode(m))) }
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func random(n int) []byte    { b := make([]byte, n); _, e := rand.Read(b); must(e); return b }
func resourceName(s string) string {
	if len(s) <= 64 {
		return s
	}
	return strings.TrimRight(s[:31], "-_.") + "-" + digest([]byte(s))[:32]
}
func labels(o object) object { return obj(obj(o["Spec"])["Labels"]) }

const label = "io.razzie.broker."

var apps = object{"access_gateway": "access-gateway", "files": "files", "notes": "notes", "razchess": "razchess", "geoip_server": "geoip-server"}

func extend(m object, more object) object {
	n := clone(m)
	for k, v := range more {
		n[k] = v
	}
	return n
}
func own(o, expected object) {
	for k, v := range expected {
		if str(labels(o)[label+k]) != str(v) {
			fail("Conflicting preexisting Swarm resource ownership")
		}
	}
}
func labelArgs(m object) []string {
	var a []string
	for _, k := range keys(m) {
		a = append(a, "--label", label+k+"="+str(m[k]))
	}
	return a
}
func readPublic(path string) []byte {
	b, e := os.ReadFile(path)
	must(e)
	if len(b) == 0 || len(b) > 1048576 {
		fail("Invalid public input size")
	}
	return b
}

type docker struct {
	ctx     context.Context
	env     map[string]string
	context string
}

func (d *docker) run(data []byte, optional bool, timeout time.Duration, args ...string) []byte {
	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()
	command := d.env["DOCKER_CLI"]
	if command == "" {
		command = "docker"
	}
	cmd := exec.CommandContext(ctx, command, append([]string{"--context", d.context}, args...)...)
	for k, v := range d.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = bytes.NewReader(data)
	b, e := cmd.Output()
	if ctx.Err() != nil {
		fail("Docker operation unavailable or timed out")
	}
	if e != nil {
		if optional {
			if _, ok := e.(*exec.ExitError); ok {
				return nil
			}
		}
		fail("Docker operation failed: " + strings.Join(args[:min(2, len(args))], " "))
	}
	return b
}
func (d *docker) call(args ...string) []byte { return d.run(nil, false, 90*time.Second, args...) }
func (d *docker) input(data []byte, args ...string) []byte {
	return d.run(data, false, 90*time.Second, args...)
}
func (d *docker) inspect(kind, name string) object {
	b := d.run(nil, true, 90*time.Second, kind, "inspect", name)
	if b == nil {
		for _, s := range strings.Fields(string(d.call(kind, "ls", "--format", "{{.Name}}"))) {
			if s == name {
				fail("Cannot inspect existing " + kind)
			}
		}
		return nil
	}
	a := arr(decode(b))
	if len(a) == 0 {
		fail("Invalid Docker inspection response")
	}
	return obj(a[0])
}

type deployment struct {
	env                                                                                        map[string]string
	docker                                                                                     *docker
	stack, mode, identity, prefix, runID, stage, manager, helper, helperKind, lockName, lockID string
	timeout                                                                                    time.Duration
	owner, policy                                                                              object
	keys, tokens, configs, masterIDs                                                           map[string]string
	publicPolicy, ca, cert                                                                     []byte
}

func (d *deployment) setting(k, def string) string {
	if v, ok := d.env[k]; ok {
		return v
	}
	return def
}
func newDeployment(ctx context.Context) *deployment {
	d := &deployment{env: map[string]string{}, keys: map[string]string{}, tokens: map[string]string{}, configs: map[string]string{}, masterIDs: map[string]string{}, stage: "preflight"}
	for _, v := range os.Environ() {
		k, v, _ := strings.Cut(v, "=")
		d.env[k] = v
	}
	d.stack = d.setting("STACK_NAME", "razzie-cloud")
	d.mode = d.setting("BROKER_AUTH_MODE", "token")
	d.identity = d.setting("BROKER_DEPLOYMENT_ID", "razzie-cloud-production")
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$`).MatchString(d.stack) {
		fail("Invalid stack name")
	}
	if d.mode != "token" && d.mode != "none" {
		fail("BROKER_AUTH_MODE must be token or none")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`).MatchString(d.identity) {
		fail("Invalid broker deployment identity")
	}
	n, e := strconv.Atoi(d.setting("DEPLOY_TIMEOUT", "300"))
	if e != nil || n < 5 || n > 900 {
		fail("DEPLOY_TIMEOUT must be between 5 and 900 seconds")
	}
	d.timeout = time.Duration(n) * time.Second
	d.env["STACK_NAME"] = d.stack
	d.docker = &docker{ctx: ctx, env: d.env, context: d.setting("DOCKER_CONTEXT", "razcloud")}
	d.prefix = d.stack + "-broker-" + digest([]byte(d.identity))[:12] + "-v1"
	d.runID = hex.EncodeToString(random(8))
	d.owner = object{"stack": d.stack, "deployment": d.identity, "protocol": "v1"}
	return d
}
func (d *deployment) announce(s string) { d.stage = s; fmt.Println("Deployment stage: " + s) }
func (d *deployment) pause(t time.Duration) {
	select {
	case <-d.docker.ctx.Done():
		fail("Interrupted")
	case <-time.After(t):
	}
}
func (d *deployment) acquire() {
	d.lockName = resourceName(d.stack + "-broker-deployment-lock")
	id := d.docker.input([]byte(d.runID), append([]string{"config", "create"}, append(labelArgs(extend(d.owner, object{"kind": "deployment-lock", "run": d.runID})), d.lockName, "-")...)...)
	d.lockID = strings.TrimSpace(string(id))
}
func (d *deployment) cleanup() {
	// Cancellation stops work, but cleanup retains a bounded independent context.
	d.docker.ctx = context.Background()
	defer func() {
		if d.lockID != "" {
			o := d.docker.inspect("config", d.lockName)
			if o != nil && str(o["ID"]) == d.lockID {
				own(o, extend(d.owner, object{"kind": "deployment-lock", "run": d.runID}))
				d.docker.call("config", "rm", d.lockName)
			}
			d.lockID = ""
		}
	}()
	d.removeHelper()
}
func run(ctx context.Context, args []string) (code int) {
	var d *deployment
	pki := len(args) == 1 && args[0] == "--pki"
	defer func() {
		if r := recover(); r != nil {
			code = 1
			if pki {
				fmt.Fprintln(os.Stderr, "Broker PKI setup stopped. Check public inputs, existing secret state and file permissions; no automatic rotation.")
			} else {
				stage := "configuration"
				if d != nil {
					stage = d.stage
				}
				message := "Check public inputs and Swarm resource ownership"
				if f, ok := r.(failure); ok {
					message = string(f)
				}
				fmt.Fprintln(os.Stderr, "Deployment stopped during "+stage+": "+message+". No automatic authentication downgrade.")
			}
		}
	}()
	if len(args) > 1 || (len(args) == 1 && args[0] != "--check" && !pki) {
		fail("Usage: go run ./cmd/infra-helper [--check|--pki]")
	}
	d = newDeployment(ctx)
	if pki {
		d.preparePKI()
	} else {
		d.execute(len(args) == 1)
	}
	return 0
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP, os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:]))
}
