package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// Isolated control-plane fixture: consume secret stdin, retain only its length.
func fakeDocker() int {
	path := os.Getenv("FAKE_DOCKER_STATE")
	raw, e := os.ReadFile(path)
	must(e)
	s := obj(decode(raw))
	a := os.Args[1:]
	if len(a) < 3 || a[0] != "--context" {
		return 97
	}
	s["contexts"] = append(arr(s["contexts"]), a[1])
	a = a[2:]
	asAny := func(a []string) []any {
		b := make([]any, len(a))
		for i, v := range a {
			b[i] = v
		}
		return b
	}
	s["commands"] = append(arr(s["commands"]), asAny(a))
	s["stack_environment"] = append(arr(s["stack_environment"]), os.Getenv("STACK_NAME"))
	output := func(v any, code int) int {
		must(os.WriteFile(path, encode(s), 0600))
		if v != nil {
			if value, ok := v.(string); ok {
				fmt.Print(value)
			} else {
				fmt.Print(string(encode(v)))
			}
		}
		return code
	}
	opts := func(name string) []string {
		var b []string
		for i := 0; i < len(a)-1; i++ {
			if a[i] == name {
				b = append(b, a[i+1])
			}
		}
		return b
	}
	option := func(name string) string {
		v := opts(name)
		if len(v) == 0 {
			return ""
		}
		return v[0]
	}
	has := func(value string) bool {
		for _, v := range a {
			if v == value {
				return true
			}
		}
		return false
	}
	resourceLabels := func() object {
		m := object{}
		for _, v := range opts("--label") {
			k, v, _ := strings.Cut(v, "=")
			m[k] = v
		}
		return m
	}
	pair := func(x, y string) bool { return len(a) > 1 && a[0] == x && a[1] == y }
	input := func() []byte { b, e := io.ReadAll(os.Stdin); must(e); return b }
	switch {
	case a[0] == "compose":
		if option("-f") == "docker-stack.yml" {
			if s["invalid_base"] == true {
				return output(nil, 1)
			}
			return output(s["base"], 0)
		}
		b, e := os.ReadFile(option("-f"))
		must(e)
		if strings.Contains(string(b), "v1.k1.1.") {
			return output(nil, 1)
		}
		return output(nil, 0)
	case pair("stack", "config"):
		if s["invalid_swarm"] == true {
			return output(nil, 1)
		}
		return output(nil, 0)
	case a[0] == "info":
		return output(object{"ControlAvailable": true, "LocalNodeState": "active", "NodeID": "manager-target"}, 0)
	case pair("image", "pull"), pair("manifest", "inspect"):
		if s["missing_image"] == true {
			return output(nil, 1)
		}
		return output(nil, 0)
	case a[0] == "run":
		data := input()
		if has("validate-tls") {
			if s["invalid_pki"] == true {
				return output(nil, 1)
			}
			return output(nil, 0)
		}
		return output(decode(data), 0)
	case a[0] == "secret" || a[0] == "config":
		kind, op := a[0], a[1]
		bucket := at(s, kind)
		switch op {
		case "inspect":
			if o := bucket[a[2]]; o != nil {
				return output([]any{o}, 0)
			}
			return output(nil, 1)
		case "ls":
			var names []string
			for _, name := range keys(bucket) {
				match := true
				for _, f := range opts("--filter") {
					if strings.HasPrefix(f, "label=") {
						k, v, _ := strings.Cut(strings.TrimPrefix(f, "label="), "=")
						if str(labels(obj(bucket[name]))[k]) != v {
							match = false
						}
					}
				}
				if match {
					names = append(names, name)
				}
			}
			return output(strings.Join(names, "\n"), 0)
		case "create":
			name := a[len(a)-2]
			if !regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,62}[A-Za-z0-9])?$`).MatchString(name) || bucket[name] != nil {
				return output(nil, 1)
			}
			data := input()
			sequence := 1
			if s["sequence"] != nil {
				sequence += intValue(s["sequence"])
			}
			s["sequence"] = sequence
			o := object{"ID": kind + "-" + strconv.Itoa(sequence), "Spec": object{"Name": name, "Labels": resourceLabels()}}
			bucket[name] = o
			if kind == "config" && strings.HasSuffix(name, "-broker-deployment-lock") && s["promote_on_lock"] == true {
				service := obj(obj(s["service"])["custom-stack_database-broker"])
				at(obj(service["Spec"]), "Labels")[label+"enforced"] = "token"
				at(obj(obj(service["Spec"])["TaskTemplate"]), "ContainerSpec")["Env"] = []any{"AUTH_MODE=token"}
			}
			if kind == "secret" {
				s["secret_input_lengths"] = append(arr(s["secret_input_lengths"]), len(data))
			}
			return output(o["ID"], 0)
		case "rm":
			delete(bucket, a[2])
			return output(nil, 0)
		}
	case pair("service", "create"):
		if !has("--detach=true") {
			return output(nil, 1)
		}
		name := option("--name")
		at(s, "service")[name] = object{"ID": name, "helper": true, "probe": has("probe"), "Spec": object{"Name": name, "Labels": resourceLabels(), "TaskTemplate": object{"ContainerSpec": object{"Image": "helper"}}}}
		return output(name, 0)
	case pair("service", "inspect"):
		if o := obj(s["service"])[a[2]]; o != nil {
			return output([]any{o}, 0)
		}
		return output(nil, 1)
	case pair("service", "ls"):
		if f := option("--filter"); f != "" {
			_, name, _ := strings.Cut(f, "=")
			return output(name+" 1/1", 0)
		}
		return output(strings.Join(keys(obj(s["service"])), "\n"), 0)
	case pair("service", "rm"):
		delete(obj(s["service"]), a[2])
		return output(nil, 0)
	case pair("service", "ps"):
		name := a[len(a)-1]
		o := obj(obj(s["service"])[name])
		if len(o) == 0 {
			return output(nil, 0)
		}
		count := 1
		if v := obj(obj(obj(o["Spec"])["Mode"])["Replicated"])["Replicas"]; v != nil {
			count = intValue(v)
		}
		var ids []string
		for i := 0; i < count; i++ {
			ids = append(ids, "task:"+name+":"+strconv.Itoa(i))
		}
		return output(strings.Join(ids, "\n"), 0)
	case a[0] == "inspect":
		parts := strings.Split(a[1], ":")
		name := parts[1]
		number := intValue(parts[2])
		o := obj(obj(s["service"])[name])
		state := "running"
		if o["probe"] == true {
			state = "complete"
		}
		if s["pending_helper"] == true && o["helper"] == true {
			state = "pending"
		}
		spec := clone(obj(obj(o["Spec"])["TaskTemplate"]))
		if s["swarm_default_fields"] == true && o["helper"] != true {
			delete(spec, "Runtime")
			delete(obj(spec["RestartPolicy"]), "Delay")
			delete(obj(spec["ContainerSpec"]), "StopGracePeriod")
			delete(obj(spec["ContainerSpec"]), "DNSConfig")
		}
		if s["stale_task"] == true && o["helper"] != true {
			c := obj(spec["ContainerSpec"])
			c["Env"] = append(arr(c["Env"]), "STALE_SETTING=true")
		}
		stack := str(s["stack"])
		if stack == "" {
			stack = "razzie-cloud"
		}
		return output([]any{object{"NodeID": "manager-target", "Spec": spec, "Status": object{"State": state, "ContainerStatus": object{"ContainerID": "container:" + name, "ExitCode": 0}}, "NetworksAttachments": []any{object{"Network": object{"Spec": object{"Name": stack + "_internal"}}, "Addresses": []any{"10.0.0." + strconv.Itoa(10+number) + "/24"}}}}}, 0)
	case pair("container", "inspect"):
		return output([]any{object{"State": object{"Health": object{"Status": "healthy"}}}}, 0)
	case a[0] == "ps":
		return output(nil, 0)
	case a[0] == "exec":
		if s["interrupt_generator"] == true {
			must(os.WriteFile(path, encode(s), 0600))
			must(syscall.Kill(os.Getppid(), syscall.SIGTERM))
			return 1
		}
		client := option("--client")
		if s["fail_generator"] == true || str(s["fail_client"]) == client {
			return output("sensitive-partial-token", 1)
		}
		if s["truncate_generator"] == true {
			return output("v1.k1.1.partial", 0)
		}
		p := obj(s["policy"])
		return output("v1."+str(p["key_id"])+"."+str(obj(obj(p["clients"])[client])["generation"])+"."+strings.Repeat("A", 43), 0)
	case pair("stack", "deploy"):
		b, e := os.ReadFile(option("-c"))
		must(e)
		doc := obj(interpolateEscapes(decode(b)))
		stack := a[len(a)-1]
		s["stack"] = stack
		s["deploys"] = append(arr(s["deploys"]), doc)
		for _, name := range keys(obj(doc["services"])) {
			service := obj(obj(doc["services"])[name])
			var env []any
			for _, k := range keys(obj(service["environment"])) {
				env = append(env, k+"="+str(obj(service["environment"])[k]))
			}
			replicas := obj(service["deploy"])["replicas"]
			if replicas == nil {
				replicas = 1
			}
			args := service["command"]
			if args == nil {
				args = []any{}
			}
			spec := object{"Name": stack + "_" + name, "Labels": obj(service["deploy"])["labels"], "Mode": object{"Replicated": object{"Replicas": replicas}}, "TaskTemplate": object{"ContainerSpec": object{"Image": service["image"], "Env": env, "Args": args, "Secrets": service["secrets"]}}}
			if s["swarm_default_fields"] == true {
				t := obj(spec["TaskTemplate"])
				t["Runtime"] = "container"
				t["RestartPolicy"] = object{"Condition": "on-failure", "Delay": 5000000000}
				c := obj(t["ContainerSpec"])
				c["StopGracePeriod"] = 10000000000
				c["DNSConfig"] = object{}
			}
			state := "completed"
			if s["pause_rollout"] == true {
				state = "paused"
			}
			at(s, "service")[stack+"_"+name] = object{"ID": stack + "_" + name, "Spec": spec, "UpdateStatus": object{"State": state}}
		}
		return output(nil, 0)
	}
	return output(nil, 1)
}
func interpolateEscapes(v any) any {
	switch v := v.(type) {
	case string:
		return strings.ReplaceAll(v, "$$", "$")
	case []any:
		for i, a := range v {
			v[i] = interpolateEscapes(a)
		}
		return v
	case map[string]any:
		for k, a := range v {
			v[k] = interpolateEscapes(a)
		}
		return v
	}
	return v
}
