package main

import (
	"net/url"
	"os"
	"strconv"
	"strings"
)

func (d *deployment) base() object {
	doc := obj(decode(d.docker.call("compose", "-f", "docker-stack.yml", "config", "--format", "json")))
	delete(doc, "name")
	doc["version"] = "3.9"
	for _, kind := range []string{"networks", "volumes"} {
		for _, v := range obj(doc[kind]) {
			m := obj(v)
			if m["external"] != true {
				delete(m, "name")
			}
		}
	}
	for _, v := range obj(doc["services"]) {
		m := obj(v)
		for _, k := range []string{"command", "entrypoint"} {
			if m[k] == nil {
				delete(m, k)
			}
		}
		delete(m, "depends_on")
		for _, v := range arr(m["ports"]) {
			p, ok := v.(map[string]any)
			if ok {
				if s, ok := p["published"].(string); ok {
					if n, e := strconv.Atoi(s); e == nil {
						p["published"] = n
					}
				}
			}
		}
	}
	return doc
}
func mountSecret(doc object, service, logical, actual, target string, uid int) {
	at(doc, "secrets")[logical] = object{"external": true, "name": actual}
	s := obj(obj(doc["services"])[service])
	s["secrets"] = append(arr(s["secrets"]), object{"source": logical, "target": target, "uid": strconv.Itoa(uid), "gid": strconv.Itoa(uid), "mode": 0400})
}
func mountConfig(doc object, service, logical, actual, target string) {
	at(doc, "configs")[logical] = object{"external": true, "name": actual}
	s := obj(obj(doc["services"])[service])
	s["configs"] = append(arr(s["configs"]), object{"source": logical, "target": target, "mode": 0444})
}
func fallback(m map[string]string, k, def string) string {
	if v := m[k]; v != "" {
		return v
	}
	return def
}
func (d *deployment) origin() string {
	return d.setting("BROKER_ORIGIN", "https://database-broker:8443")
}
func brokerURL(origin string) *url.URL { u, e := url.Parse(origin); must(e); return u }
func brokerPort(origin string) string {
	p := brokerURL(origin).Port()
	if p == "" {
		return "443"
	}
	return p
}
func (d *deployment) render(base object, enforcement string) object {
	doc := clone(base)
	services := obj(doc["services"])
	b := obj(services["database-broker"])
	if enforcement == "" {
		enforcement = d.mode
	}
	at(b, "environment")["AUTH_MODE"] = enforcement
	at(at(b, "deploy"), "update_config")["failure_action"] = "pause"
	if d.mode == "none" {
		for _, c := range []string{"razchess", "geoip_server"} {
			d.wrapper(doc, c, "http://database-broker:8080", false)
		}
		return doc
	}
	policy := d.policy
	if policy == nil {
		clients := object{}
		for c := range apps {
			clients[c] = object{"generation": 1}
		}
		policy = object{"key_id": "validation", "accepted_key_ids": []any{"validation"}, "clients": clients}
	}
	mountConfig(doc, "database-broker", "broker_policy", fallback(d.configs, "policy", "validation_policy"), "/run/configs/broker_policy.yml")
	e := at(b, "environment")
	e["AUTH_POLICY_FILE"] = "/run/configs/broker_policy.yml"
	e["AUTH_DEPLOYMENT_ID"] = d.identity
	var paths []string
	for _, v := range arr(policy["accepted_key_ids"]) {
		k := str(v)
		mountSecret(doc, "database-broker", "broker_master_"+k, fallback(d.keys, k, "validation_master_"+k), "broker_master_"+k, 0)
		paths = append(paths, k+"=/run/secrets/broker_master_"+k)
	}
	e["AUTH_KEY_FILES"] = strings.Join(paths, ",")
	mountSecret(doc, "database-broker", "broker_tls_key", d.setting("BROKER_TLS_KEY_SECRET", "validation_tls_key"), "broker_tls_key", 0)
	mountConfig(doc, "database-broker", "broker_tls_cert", fallback(d.configs, "cert", "validation_cert"), "/run/configs/broker_cert.pem")
	e["TLS_CERT_FILE"] = "/run/configs/broker_cert.pem"
	e["TLS_KEY_FILE"] = "/run/secrets/broker_tls_key"
	e["HEALTH_PORT"] = "8081"
	e["SERVICE_PORT"] = brokerPort(d.origin())
	at(b, "healthcheck")["test"] = []any{"CMD-SHELL", "wget -q -O /dev/null http://127.0.0.1:8081/readyz || exit 1"}
	l := at(at(b, "deploy"), "labels")
	l[label+"enforced"] = "token"
	l[label+"deployment"] = d.identity
	l[label+"policy-sha256"] = fallback(d.configs, "policy_digest", "validation")
	for _, client := range keys(apps) {
		service := str(apps[client])
		c := obj(obj(policy["clients"])[client])
		if len(c) == 0 || c["enabled"] == false {
			at(obj(services[service]), "deploy")["replicas"] = 0
			continue
		}
		uid := 0
		if client == "files" || client == "notes" {
			uid = 10001
		}
		mountSecret(doc, service, "broker_token_"+client, fallback(d.tokens, client, "validation_token_"+client), "broker_token", uid)
		mountConfig(doc, service, "broker_ca", fallback(d.configs, "ca", "validation_ca"), "/run/configs/broker_ca.pem")
		if client == "razchess" || client == "geoip_server" {
			d.wrapper(doc, client, d.origin(), true)
		} else {
			e := at(obj(services[service]), "environment")
			e["BROKER_TOKEN_FILE"] = "/run/secrets/broker_token"
			e["BROKER_CA_FILE"] = "/run/configs/broker_ca.pem"
			for _, field := range []string{"POSTGRES_URI_FROM", "REDIS_URI_FROM"} {
				if v, ok := e[field]; ok {
					_, path, found := strings.Cut(str(v), "/v1/")
					if !found {
						fail("Invalid broker discovery path")
					}
					path, _, _ = strings.Cut(path, "?")
					e[field] = d.origin() + "/v1/" + path + "?client=" + client
				}
			}
		}
	}
	return doc
}
func (d *deployment) wrapper(doc object, client, origin string, authenticated bool) {
	service := str(apps[client])
	app := obj(obj(doc["services"])[service])
	variable := "GEOIP_SERVER"
	if client == "razchess" {
		variable = "RAZCHESS"
	}
	app["image"] = fallback(d.env, variable+"_BROKER_IMAGE", "ghcr.io/razzie-cloud/"+service+"-broker:"+d.env["DATABASE_BROKER_IMAGE_TAG"])
	path := origin + "/v1/instances/valkey/" + client + "/uri"
	if authenticated {
		path += "?client=" + client
	}
	args := []any{"--program", "/" + service, "--url", path}
	if authenticated {
		args = append(args, "--token-file", "/run/secrets/broker_token", "--ca-file", "/run/configs/broker_ca.pem")
	}
	args = append(args, "--")
	old := arr(app["command"])
	for i := 0; i < len(old); i++ {
		v := str(old[i])
		if v == "--redis-from" || v == "-redis-from" {
			i++
			continue
		}
		if strings.HasPrefix(v, "--redis-from=") || strings.HasPrefix(v, "-redis-from=") {
			continue
		}
		args = append(args, v)
	}
	app["entrypoint"] = []any{"/broker-start"}
	app["command"] = args
}
func stackFile(doc object, fn func(string)) {
	f, e := os.CreateTemp("", "broker-stack-*.json")
	must(e)
	defer os.Remove(f.Name())
	defer f.Close()
	_, e = f.Write(encode(doc))
	must(e)
	must(f.Close())
	fn(f.Name())
}
func (d *deployment) validateStack(doc object) {
	stackFile(doc, func(p string) {
		d.docker.call("compose", "-f", p, "config", "--no-interpolate", "--quiet")
		d.docker.call("stack", "config", "--skip-interpolation", "-c", p)
	})
}
