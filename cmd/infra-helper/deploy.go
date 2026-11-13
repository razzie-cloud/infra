package main

import (
	"fmt"
	"net"
	"reflect"
	"regexp"
	"strings"
	"time"
)

func taskSpec(spec object) object {
	s := clone(spec)
	if s["Runtime"] == nil {
		s["Runtime"] = "container"
	}
	if s["RestartPolicy"] != nil {
		r := obj(s["RestartPolicy"])
		if r["Delay"] == nil {
			r["Delay"] = 5000000000
		}
	}
	if s["ContainerSpec"] != nil {
		c := obj(s["ContainerSpec"])
		if c["StopGracePeriod"] == nil {
			c["StopGracePeriod"] = 10000000000
		}
		if c["DNSConfig"] == nil {
			c["DNSConfig"] = object{}
		}
	}
	return clone(s)
}
func (d *deployment) previousBroker() (object, bool) {
	o := d.docker.inspect("service", d.stack+"_database-broker")
	if o == nil {
		return nil, false
	}
	c := obj(obj(obj(o["Spec"])["TaskTemplate"])["ContainerSpec"])
	e := map[string]string{}
	for _, v := range arr(c["Env"]) {
		k, v, ok := strings.Cut(str(v), "=")
		if ok {
			e[k] = v
		}
	}
	args := arr(c["Args"])
	mode := e["AUTH_MODE"]
	hasConfig := e["AUTH_POLICY_FILE"] != "" || e["AUTH_KEY_FILES"] != ""
	for i, v := range args {
		a := str(v)
		if strings.HasPrefix(a, "--auth-mode=") {
			mode = strings.TrimPrefix(a, "--auth-mode=")
		} else if a == "--auth-mode" && i+1 < len(args) {
			mode = str(args[i+1])
		}
		if strings.HasPrefix(a, "--auth-policy-file") || strings.HasPrefix(a, "--auth-key-files") {
			hasConfig = true
		}
	}
	if mode != "" && mode != "token" && mode != "none" {
		fail("Unknown deployed authentication mode")
	}
	return o, str(labels(o)[label+"enforced"]) == "token" || mode == "token" || (mode != "none" && hasConfig)
}
func (d *deployment) preflight(base object) {
	info := obj(decode(d.docker.call("info", "--format", "{{json .Swarm}}")))
	if info["ControlAvailable"] != true || str(info["LocalNodeState"]) != "active" || str(info["NodeID"]) == "" {
		fail("Selected Docker context must reach an active Swarm manager")
	}
	d.manager = str(info["NodeID"])
	_, enforced := d.previousBroker()
	if d.mode == "none" && enforced {
		fail("Refusing to downgrade an enforcing installation to none")
	}
	pulled := map[string]bool{}
	if d.mode == "token" {
		if !regexp.MustCompile(`^https://[A-Za-z0-9.-]+(?::[0-9]+)?$`).MatchString(d.origin()) {
			fail("Production token discovery requires an exact HTTPS broker origin")
		}
		port := intValue(brokerPort(d.origin()))
		if port < 1 || port > 65535 || port == 8080 || port == 8081 {
			fail("Broker HTTPS port must be valid and separate from legacy/health ports")
		}
		d.publicPolicy = readPublic(d.setting("BROKER_CLIENTS_FILE", "./broker-clients.yml"))
		image := str(obj(obj(base["services"])["database-broker"])["image"])
		d.docker.run(nil, false, 300*time.Second, "image", "pull", image)
		pulled[image] = true
		a := []string{"run", "--rm", "-i", "--network", "none", "--read-only", "--log-driver", "none", "--entrypoint", "/broker-token", image}
		d.policy = obj(decode(d.docker.input(d.publicPolicy, append(a, "validate", "--policy", "-", "--deployment-id", d.identity)...)))
		d.ca = readPublic(d.setting("BROKER_CA_FILE", "./pki/broker-ca.pem"))
		d.cert = readPublic(d.setting("BROKER_TLS_CERT_FILE", "./pki/broker.pem"))
		d.docker.input(encode(object{"ca": string(d.ca), "cert": string(d.cert)}), append(a, "validate-tls", "--hostname", brokerURL(d.origin()).Hostname())...)
		name := d.setting("BROKER_TLS_KEY_SECRET", "")
		if name == "" || d.docker.inspect("secret", name) == nil {
			fail("Operator-managed broker TLS key secret is required")
		}
	}
	doc := d.render(base, "")
	d.validateStack(doc)
	images := object{}
	for _, s := range obj(doc["services"]) {
		images[str(obj(s)["image"])] = true
	}
	for _, image := range keys(images) {
		if !pulled[image] {
			d.docker.run(nil, false, 300*time.Second, "image", "pull", image)
		}
	}
}
func (d *deployment) execute(check bool) {
	base := d.base()
	if check {
		d.validateStack(d.render(base, ""))
		return
	}
	d.announce("preflight")
	d.preflight(base)
	defer d.cleanup()
	d.acquire()
	previous, enforced := d.previousBroker()
	if d.mode == "none" && enforced {
		fail("Refusing to downgrade an enforcing installation to none")
	}
	d.announce("prepare reusable resources")
	for _, name := range []string{"postgres_password", "valkey_password"} {
		if d.docker.inspect("secret", name) == nil {
			d.docker.input([]byte(fmt.Sprintf("%x", random(32))), "secret", "create", name, "-")
		}
	}
	if d.mode == "none" {
		d.rollout(d.render(base, ""), "explicit legacy mode", nil)
		return
	}
	for _, k := range arr(d.policy["accepted_key_ids"]) {
		d.master(str(k))
	}
	d.configs["policy"] = d.config("policy", d.publicPolicy)
	d.configs["policy_digest"] = digest(d.publicPolicy)
	d.configs["ca"] = d.config("ca", d.ca)
	d.configs["cert"] = d.config("cert", d.cert)
	d.prepareTokens(str(obj(obj(base["services"])["database-broker"])["image"]), d.manager)
	final := d.render(base, "")
	if previous != nil && !enforced {
		fmt.Println("Migrating existing tokenless deployment; exposure remains until enforcement cutover.")
		interim := d.render(base, "none")
		b := obj(obj(interim["services"])["database-broker"])
		at(b, "environment")["LEGACY_HTTP_PORT"] = "8080"
		delete(at(at(b, "deploy"), "labels"), label+"enforced")
		d.rollout(interim, "legacy broker TLS preparation", []string{"database-broker"})
		d.rollout(interim, "authenticated consumer migration", nil)
		d.rollout(final, "enable broker enforcement", nil)
	} else {
		d.rollout(final, "broker policy and key rollout", []string{"database-broker"})
		d.rollout(final, "consumer rollout", nil)
	}
	d.announce("enforcement active; verify scoped access")
	d.acceptance(final, false)
}
func (d *deployment) rollout(doc object, stage string, only []string) {
	d.announce(stage)
	d.validateStack(doc)
	if only != nil {
		doc = clone(doc)
		s := object{}
		for _, name := range only {
			s[name] = obj(doc["services"])[name]
		}
		doc["services"] = s
	}
	stackFile(doc, func(p string) { d.docker.call("stack", "deploy", "--with-registry-auth", "-c", p, d.stack) })
	d.converge(obj(doc["services"]))
	if d.mode == "token" {
		d.acceptance(doc, true)
	}
}
func (d *deployment) converge(services object) {
	end := time.Now().Add(d.timeout)
	for time.Now().Before(end) {
		ready := true
		for _, name := range keys(services) {
			desired := obj(services[name])
			actual := d.docker.inspect("service", d.stack+"_"+name)
			if actual == nil {
				ready = false
				continue
			}
			update := str(obj(actual["UpdateStatus"])["State"])
			switch update {
			case "paused", "rollback_paused", "rollback_started", "rollback_completed":
				fail("Service update failed or rolled back: " + name)
			}
			spec := obj(actual["Spec"])
			template := obj(spec["TaskTemplate"])
			if strings.Split(str(obj(template["ContainerSpec"])["Image"]), "@")[0] != strings.Split(str(desired["image"]), "@")[0] {
				ready = false
			}
			countValue := obj(obj(spec["Mode"])["Replicated"])["Replicas"]
			count := 0
			if countValue == nil {
				summary := strings.Split(string(d.docker.call("service", "ls", "--filter", "name="+d.stack+"_"+name, "--format", "{{.Name}} {{.Replicas}}")), "\n")
				found := false
				for _, line := range summary {
					fields := strings.Fields(line)
					if len(fields) > 1 && fields[0] == d.stack+"_"+name {
						_, n, ok := strings.Cut(fields[1], "/")
						if ok {
							count = intValue(n)
							found = true
						}
					}
				}
				if !found {
					ready = false
					continue
				}
			} else {
				count = intValue(countValue)
			}
			running := 0
			target := taskSpec(template)
			for _, id := range taskIDs(d, d.stack+"_"+name, true) {
				t := d.task(id)
				status := obj(t["Status"])
				if str(status["State"]) != "running" || !reflect.DeepEqual(taskSpec(obj(t["Spec"])), target) {
					continue
				}
				cid := str(obj(status["ContainerStatus"])["ContainerID"])
				if str(t["NodeID"]) == d.manager && cid != "" {
					a := arr(decode(d.docker.call("container", "inspect", cid)))
					if len(a) == 0 {
						fail("Invalid container response")
					}
					health := str(obj(obj(obj(a[0])["State"])["Health"])["Status"])
					if health != "" && health != "healthy" {
						continue
					}
				}
				running++
			}
			if running != count || update == "updating" {
				ready = false
			}
		}
		if ready {
			return
		}
		d.pause(2 * time.Second)
	}
	fail("Readiness/convergence deadline exceeded")
}
func (d *deployment) addresses(service, port string, requireRunning bool) []string {
	var addresses []string
	ids := taskIDs(d, d.stack+"_"+service, true)
	for _, id := range ids {
		t := d.task(id)
		if requireRunning && str(obj(t["Status"])["State"]) != "running" {
			fail("Broker task is not running at acceptance")
		}
		for _, a := range arr(t["NetworksAttachments"]) {
			attachment := obj(a)
			if str(obj(obj(attachment["Network"])["Spec"])["Name"]) == d.stack+"_internal" {
				for _, v := range arr(attachment["Addresses"]) {
					ip, _, _ := strings.Cut(str(v), "/")
					addresses = append(addresses, net.JoinHostPort(ip, port))
				}
			}
		}
	}
	if requireRunning && (len(addresses) == 0 || len(addresses) < len(ids)) {
		fail("Cannot resolve every broker replica for acceptance")
	}
	return addresses
}
func unique(a []string) string {
	m := object{}
	for _, s := range a {
		m[s] = true
	}
	return strings.Join(keys(m), ",")
}
func (d *deployment) acceptance(final object, readinessOnly bool) {
	addresses := d.addresses("database-broker", brokerPort(d.origin()), true)
	probe := []string{"probe", "--origin", d.origin(), "--ca-file", "/ca.pem", "--addresses", unique(addresses), "--max-wait", d.timeout.String()}
	var mounts []string
	if !readinessOnly {
		names := object{}
		for c := range d.tokens {
			names[c] = true
		}
		for _, c := range keys(names) {
			if apps[c] != nil {
				mounts = []string{"--secret", "source=" + d.tokens[c] + ",target=token,mode=0400"}
				probe = append(probe, "--token-file", "/run/secrets/token", "--client", c)
				break
			}
		}
	} else {
		probe = append(probe, "--readiness-only")
	}
	var ready []string
	for _, name := range []string{"access-gateway", "files", "notes"} {
		s := obj(obj(final["services"])[name])
		if len(s) == 0 || str(obj(s["deploy"])["replicas"]) == "0" {
			continue
		}
		ready = append(ready, d.addresses(name, "8080", false)...)
	}
	if len(ready) > 0 {
		probe = append(probe, "--ready-addresses", unique(ready))
	}
	d.helper = resourceName(d.prefix + "-probe-" + d.runID)
	d.helperKind = "probe"
	a := []string{"service", "create", "--detach=true", "--name", d.helper, "--constraint", "node.id==" + d.manager, "--restart-condition", "none", "--no-healthcheck", "--log-driver", "none", "--read-only", "--cap-drop", "ALL"}
	a = append(a, labelArgs(extend(d.owner, object{"kind": "probe", "run": d.runID}))...)
	a = append(a, "--network", d.stack+"_internal", "--entrypoint", "/broker-token", "--config", "source="+d.configs["ca"]+",target=/ca.pem")
	a = append(a, mounts...)
	a = append(a, str(obj(obj(final["services"])["database-broker"])["image"]))
	a = append(a, probe...)
	d.docker.call(a...)
	end := time.Now().Add(d.timeout)
	for time.Now().Before(end) {
		for _, id := range taskIDs(d, d.helper, false) {
			s := obj(d.task(id)["Status"])
			if str(s["State"]) == "complete" && str(obj(s["ContainerStatus"])["ExitCode"]) == "0" {
				d.removeHelper()
				return
			}
			if str(s["State"]) == "failed" || str(s["State"]) == "rejected" {
				fail("Broker enforcement acceptance failed")
			}
		}
		d.pause(time.Second)
	}
	fail("Broker enforcement acceptance deadline exceeded")
}
