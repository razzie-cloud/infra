package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (d *deployment) config(kind string, data []byte) string {
	sha := digest(data)
	name := resourceName(d.prefix + "-" + kind + "-" + sha[:24])
	expected := extend(d.owner, object{"kind": kind, "sha256": sha})
	o := d.docker.inspect("config", name)
	if o != nil {
		own(o, expected)
	} else {
		d.docker.input(data, append(append([]string{"config", "create"}, labelArgs(expected)...), name, "-")...)
	}
	return name
}
func (d *deployment) secret(name string, expected object, data []byte) string {
	if o := d.docker.inspect("secret", name); o != nil {
		own(o, expected)
		return str(o["ID"])
	}
	if data == nil {
		fail("Required existing secret is missing")
	}
	id := strings.TrimSpace(string(d.docker.input(data, append(append([]string{"secret", "create"}, labelArgs(expected)...), name, "-")...)))
	if id == "" {
		fail("Secret creation returned no identity")
	}
	return id
}
func (d *deployment) master(key string) {
	raw := d.prefix + "-master-" + key
	name := resourceName(raw)
	expected := extend(d.owner, object{"kind": "master", "key": key})
	anchorName := resourceName(raw + "-identity")
	anchor := d.docker.inspect("config", anchorName)
	o := d.docker.inspect("secret", name)
	if anchor != nil {
		own(anchor, extend(d.owner, object{"kind": "master-identity", "key": key}))
		if o == nil || str(o["ID"]) != str(labels(anchor)[label+"master-id"]) {
			fail("Master secret lost or replaced; recover with a new key_id")
		}
	}
	var id string
	if o != nil {
		own(o, expected)
		id = str(o["ID"])
	} else {
		existing := d.docker.call("secret", "ls", "--filter", "label="+label+"stack="+d.stack, "--filter", "label="+label+"deployment="+d.identity, "--filter", "label="+label+"key="+key, "--format", "{{.Name}}")
		if strings.TrimSpace(string(existing)) != "" {
			fail("Master missing for existing token state; use a new key_id")
		}
		id = d.secret(name, expected, random(32))
	}
	if anchor == nil {
		d.docker.input([]byte(id), append(append([]string{"config", "create"}, labelArgs(extend(d.owner, object{"kind": "master-identity", "key": key, "master-id": id}))...), anchorName, "-")...)
	}
	d.keys[key] = name
	d.masterIDs[key] = id
}
func taskIDs(d *deployment, service string, running bool) []string {
	a := []string{"service", "ps"}
	if running {
		a = append(a, "--filter", "desired-state=running")
	}
	a = append(a, "--no-trunc", "--format", "{{.ID}}", service)
	return strings.Fields(string(d.docker.call(a...)))
}
func (d *deployment) task(id string) object {
	a := arr(decode(d.docker.call("inspect", id)))
	if len(a) == 0 {
		fail("Invalid task response")
	}
	return obj(a[0])
}
func (d *deployment) helperStart(image, manager string) string {
	d.helper = resourceName(d.prefix + "-helper-" + d.runID)
	d.helperKind = "generator"
	key := str(d.policy["key_id"])
	a := []string{"service", "create", "--detach=true", "--name", d.helper, "--constraint", "node.id==" + manager, "--restart-condition", "none", "--no-healthcheck", "--log-driver", "none", "--read-only", "--cap-drop", "ALL", "--user", "0:0"}
	a = append(a, labelArgs(extend(d.owner, object{"kind": "generator", "run": d.runID}))...)
	a = append(a, "--secret", "source="+d.keys[key]+",target=master,mode=0400", "--config", "source="+d.configs["policy"]+",target=/policy.yml,mode=0444", "--entrypoint", "/broker-token", image, "hold", "--max-lifetime", "15m")
	d.docker.call(a...)
	end := time.Now().Add(d.timeout)
	for time.Now().Before(end) {
		for _, id := range taskIDs(d, d.helper, true) {
			t := d.task(id)
			s := obj(t["Status"])
			if str(t["NodeID"]) == manager && str(s["State"]) == "running" {
				if cid := str(obj(s["ContainerStatus"])["ContainerID"]); cid != "" {
					return cid
				}
			}
		}
		d.pause(time.Second)
	}
	fail("Token helper did not become runnable before deadline")
	return ""
}

var tokenPattern = regexp.MustCompile(`^v1\.([A-Za-z0-9][A-Za-z0-9_-]{0,63})\.([1-9][0-9]{0,15})\.[A-Za-z0-9_-]{43}$`)

func (d *deployment) prepareTokens(image, manager string) {
	type missingToken struct {
		client, name string
		expected     object
	}
	var missing []missingToken
	key := str(d.policy["key_id"])
	clients := obj(d.policy["clients"])
	for _, client := range keys(clients) {
		c := obj(clients[client])
		if c["enabled"] == false {
			continue
		}
		g := str(c["generation"])
		name := resourceName(d.prefix + "-" + key + "-" + client + "-g" + g)
		expected := extend(d.owner, object{"kind": "token", "key": key, "client": client, "generation": g, "master-id": d.masterIDs[key]})
		o := d.docker.inspect("secret", name)
		if o != nil {
			own(o, expected)
		} else {
			missing = append(missing, missingToken{client, name, expected})
		}
		d.tokens[client] = name
	}
	if len(missing) == 0 {
		return
	}
	cid := d.helperStart(image, manager)
	for _, m := range missing {
		token := d.docker.call("exec", cid, "/broker-token", "generate", "--policy", "/policy.yml", "--deployment-id", d.identity, "--key-file", "/run/secrets/master", "--client", m.client)
		match := tokenPattern.FindSubmatch(token)
		if match == nil || string(match[1]) != key || string(match[2]) != str(obj(clients[m.client])["generation"]) {
			fail("Generator returned an invalid or truncated token")
		}
		d.secret(m.name, m.expected, token)
	}
	d.removeHelper()
}
func (d *deployment) removeHelper() {
	if d.helper == "" {
		return
	}
	o := d.docker.inspect("service", d.helper)
	if o != nil {
		own(o, extend(d.owner, object{"kind": d.helperKind, "run": d.runID}))
		d.docker.call("service", "rm", d.helper)
		end := time.Now().Add(d.timeout)
		for {
			exists := d.docker.inspect("service", d.helper)
			containers := d.docker.call("ps", "--filter", "label=com.docker.swarm.service.name="+d.helper, "--format", "{{.ID}}")
			if exists == nil && strings.TrimSpace(string(containers)) == "" {
				break
			}
			if !time.Now().Before(end) {
				fail("Helper removal did not converge")
			}
			d.pause(time.Second)
		}
	}
	d.helper = ""
	d.helperKind = ""
}
func intValue(v any) int { n, e := strconv.Atoi(str(v)); must(e); return n }
