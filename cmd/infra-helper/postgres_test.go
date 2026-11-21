package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func newPostgresFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.state["postgres_fixture"] = true
	f.state["data_version"] = "17"
	f.state["secret"] = object{"postgres_password": object{"ID": "pg-secret-id", "Spec": object{}}}
	f.state["service"] = object{}
	for _, name := range []string{"postgres", "caddy-docker-proxy"} {
		at(f.state, "service")["custom-stack_"+name] = object{"ID": name, "Spec": object{"Labels": object{"com.docker.stack.namespace": "custom-stack"}, "Mode": object{"Replicated": object{"Replicas": 1}}, "TaskTemplate": object{"ContainerSpec": object{"Image": "postgres:17-alpine@sha256:old", "Env": []any{"POSTGRES_USER=razcloud", "POSTGRES_DB=razcloud", "POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password"}, "Mounts": []any{object{"Type": "volume", "Source": "actual-postgres-volume", "Target": postgresParent + "/data"}}, "Secrets": []any{object{"SecretName": "postgres_password", "SecretID": "pg-secret-id", "File": object{"Name": "postgres_password"}}}}}}}
	}
	f.env["POSTGRES_IMAGE_TAG"] = "18-alpine"
	f.env["POSTGRES_VOLUME_TARGET"] = postgresParent
	f.env["POSTGRES_BACKUP_DIR"] = filepath.Join(f.dir, "backups")
	f.env["POSTGRES_UPGRADE_TIMEOUT"] = "2"
	return f
}

func postgresReplicas(s object, service string) int {
	spec := obj(obj(obj(s["service"])["custom-stack_"+service])["Spec"])
	return intValue(obj(obj(spec["Mode"])["Replicated"])["Replicas"])
}

func TestPostgresUpgrade(t *testing.T) {
	f := newPostgresFixture(t)
	oldContainer := obj(obj(obj(obj(f.state["service"])["custom-stack_postgres"])["Spec"])["TaskTemplate"])
	oldSpec := obj(oldContainer["ContainerSpec"])
	oldSpec["Env"] = append(arr(oldSpec["Env"]), "PGDATA="+postgresParent+"/data")
	f.execute(true, "--postgres-upgrade")
	equal(t, postgresReplicas(f.state, "postgres"), 1)
	equal(t, postgresReplicas(f.state, "caddy-docker-proxy"), 1)
	c := obj(obj(obj(obj(f.state["service"])["custom-stack_postgres"])["Spec"])["TaskTemplate"])
	equal(t, obj(c["ContainerSpec"])["Image"], "postgres@sha256:"+strings.Repeat("a", 64))
	equal(t, obj(arr(obj(c["ContainerSpec"])["Mounts"])[0])["Target"], postgresParent)
	var stages []string
	for _, command := range arr(f.state["commands"]) {
		a := arr(command)
		joined := strings.Join(anyStrings(a), " ")
		switch {
		case strings.Contains(joined, "image pull"):
			stages = append(stages, "pull")
		case strings.Contains(joined, "service scale"):
			stages = append(stages, str(a[len(a)-1]))
		case strings.Contains(joined, "--entrypoint tar"):
			stages = append(stages, "backup")
		case strings.Contains(joined, "service create"):
			stages = append(stages, "upgrade")
			for _, required := range []string{"--detach=true", "node.id==manager-target", "PGAUTO_ONESHOT=yes", "POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password", "source=postgres_password,target=postgres_password", "PGDATA=" + postgresParent + "/18/docker"} {
				if !strings.Contains(joined, required) {
					t.Fatal("missing upgrade argument", required)
				}
			}
		case strings.Contains(joined, "service update"):
			stages = append(stages, "update")
			if !strings.Contains(joined, "--env-rm PGDATA") {
				t.Fatal("legacy PGDATA override would point at the wrong directory")
			}
		case strings.Contains(joined, "exec --env"):
			stages = append(stages, "SQL")
		case strings.Contains(joined, "stack deploy"):
			t.Fatal("upgrader deployed the whole stack")
		}
	}
	equal(t, stages, []string{"pull", "pull", "custom-stack_caddy-docker-proxy=0", "custom-stack_postgres=0", "backup", "upgrade", "update", "custom-stack_postgres=1", "SQL", "custom-stack_caddy-docker-proxy=1"})
	for _, v := range arr(f.state["contexts"]) {
		equal(t, v, "ssh-custom-manager")
	}
	files, err := filepath.Glob(filepath.Join(f.env["POSTGRES_BACKUP_DIR"], "*", "*"))
	if err != nil || len(files) != 2 {
		t.Fatal("missing backup or metadata", files, err)
	}
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("backup permissions", file, err)
		}
		if strings.HasSuffix(file, "postgres.tar.gz") {
			if err := verifyPostgresArchive(context.Background(), file); err != nil {
				t.Fatal(err)
			}
		} else {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			metadata := obj(decode(b))
			equal(t, metadata["volume"], "actual-postgres-volume")
			equal(t, metadata["original_image"], "postgres:17-alpine@sha256:old")
			equal(t, metadata["original_mount"], postgresParent+"/data")
		}
	}
}

func anyStrings(a []any) []string {
	b := make([]string, len(a))
	for i, v := range a {
		b[i] = str(v)
	}
	return b
}

func TestPostgresUpgradeFailures(t *testing.T) {
	for _, scenario := range []string{"missing_image", "multiple_managers", "wrong_node", "wrong_image_version", "newer_upgrader", "older_source_binary", "unclean_shutdown", "bad_archive", "external_storage", "interrupted_backup", "failed_upgrade", "interrupted_upgrade", "no_upgrade", "sql_unready", "upgrade_timeout", "update_failed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPostgresFixture(t)
			f.state[scenario] = true
			f.execute(false, "--postgres-upgrade")
			afterUpgrade := scenario == "failed_upgrade" || scenario == "interrupted_upgrade" || scenario == "no_upgrade" || scenario == "sql_unready" || scenario == "upgrade_timeout" || scenario == "update_failed"
			wantReplicas := 1
			if afterUpgrade {
				wantReplicas = 0
			}
			equal(t, postgresReplicas(f.state, "postgres"), wantReplicas)
			equal(t, postgresReplicas(f.state, "caddy-docker-proxy"), wantReplicas)
			created := false
			for _, command := range arr(f.state["commands"]) {
				a := arr(command)
				if len(a) > 1 && a[0] == "service" && a[1] == "create" {
					created = true
				}
				if afterUpgrade && len(a) > 2 && a[0] == "service" && a[1] == "scale" && a[len(a)-1] == "custom-stack_caddy-docker-proxy=1" {
					t.Fatal("traffic reopened after a potentially destructive failure")
				}
			}
			equal(t, created, afterUpgrade)
		})
	}
}

func TestPostgresUpgradeAllowsNewerDestinationMinor(t *testing.T) {
	f := newPostgresFixture(t)
	f.state["older_destination_binary"] = true
	f.execute(true, "--postgres-upgrade")
	equal(t, postgresReplicas(f.state, "postgres"), 1)
	equal(t, postgresReplicas(f.state, "caddy-docker-proxy"), 1)
}

func TestPostgresUpgradeRetainsLockWhenHelperRemovalFails(t *testing.T) {
	f := newPostgresFixture(t)
	f.state["keep_upgrade_helper"] = true
	f.state["failed_upgrade"] = true
	output := f.execute(false, "--postgres-upgrade")
	if !strings.Contains(output, "Deployment lock retained") || obj(f.state["config"])["custom-stack-broker-deployment-lock"] == nil {
		t.Fatal("deployment lock was not retained", output)
	}
	equal(t, postgresReplicas(f.state, "postgres"), 0)
	equal(t, postgresReplicas(f.state, "caddy-docker-proxy"), 0)
}

func TestPostgresUpgradeWaitsForShutdown(t *testing.T) {
	f := newPostgresFixture(t)
	f.state["slow_shutdown"] = true
	f.execute(true, "--postgres-upgrade")
	equal(t, obj(f.state["draining"])["custom-stack_postgres"], 0)
	equal(t, obj(f.state["draining"])["custom-stack_caddy-docker-proxy"], 0)
}

func TestPostgresUpgradeRejectsConfigurationBeforeDowntime(t *testing.T) {
	for _, scenario := range []string{"bad_tag", "bad_mount", "lock_held", "wrong_volume", "wrong_secret", "extra_replica", "unfinished_rollout"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPostgresFixture(t)
			c := obj(obj(obj(obj(f.state["service"])["custom-stack_postgres"])["Spec"])["TaskTemplate"])
			switch scenario {
			case "bad_tag":
				f.env["POSTGRES_IMAGE_TAG"] = "latest"
			case "bad_mount":
				f.env["POSTGRES_VOLUME_TARGET"] = postgresParent + "/data"
			case "lock_held":
				f.state["config"] = object{"custom-stack-broker-deployment-lock": object{"ID": "held", "Spec": object{"Labels": object{label + "kind": "deployment-lock", label + "run": "other"}}}}
			case "wrong_volume":
				obj(arr(obj(c["ContainerSpec"])["Mounts"])[0])["Type"] = "bind"
			case "wrong_secret":
				obj(f.state["secret"])["postgres_password"] = object{"ID": "replacement", "Spec": object{}}
			case "extra_replica":
				at(obj(obj(obj(f.state["service"])["custom-stack_postgres"])["Spec"]), "Mode")["Replicated"] = object{"Replicas": 2}
			case "unfinished_rollout":
				obj(obj(f.state["service"])["custom-stack_postgres"])["UpdateStatus"] = object{"State": "paused"}
			}
			f.execute(false, "--postgres-upgrade")
			for _, command := range arr(f.state["commands"]) {
				a := arr(command)
				if len(a) > 1 && a[0] == "service" && (a[1] == "scale" || a[1] == "update" || a[1] == "create") {
					t.Fatal("service changed before preflight passed", a)
				}
			}
		})
	}
}

func TestPostgresUpgradeMakeDelegation(t *testing.T) {
	f := newPostgresFixture(t)
	for _, file := range []string{"Makefile", "infra/Makefile"} {
		source := "../../../" + file
		b, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		must(os.MkdirAll(filepath.Dir(filepath.Join(f.dir, file)), 0700))
		must(os.WriteFile(filepath.Join(f.dir, file), b, 0600))
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f.env["TEST_HELPER_BINARY"] = exe
	must(os.WriteFile(filepath.Join(f.dir, "infra", "deploy.sh"), []byte("#!/bin/sh\nexec \"$TEST_HELPER_BINARY\" \"$@\"\n"), 0700))
	env := filepath.Join(f.dir, "chosen.env")
	must(os.WriteFile(env, []byte("POSTGRES_IMAGE_TAG=invalid\nPOSTGRES_VOLUME_TARGET=/var/lib/postgresql\nDOCKER_CONTEXT=from-env-file\n"), 0600))
	f.command = []string{"make"}
	f.execute(true, "postgres-upgrade", "INFRA_ENV_FILE="+env, "POSTGRES_IMAGE_TAG=18-alpine")
	for _, v := range arr(f.state["contexts"]) {
		equal(t, v, "from-env-file")
	}
}

func postgresArchive(external bool) []byte {
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	for _, name := range []string{"./PG_VERSION", "./global/pg_control", "./base/1/123"} {
		data := []byte("test-data")
		if name == "./PG_VERSION" {
			data = []byte("17\n")
		}
		must(w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}))
		_, err := w.Write(data)
		must(err)
	}
	if external {
		must(w.WriteHeader(&tar.Header{Name: "./pg_wal", Typeflag: tar.TypeSymlink, Linkname: "/external-wal"}))
	}
	must(w.Close())
	must(z.Close())
	return b.Bytes()
}

func TestPostgresArchiveVerification(t *testing.T) {
	for _, scenario := range []string{"valid", "truncated", "bad_checksum", "external", "empty", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			data := postgresArchive(scenario == "external")
			switch scenario {
			case "truncated":
				data = data[:len(data)/2]
			case "bad_checksum":
				data[len(data)-8] ^= 0xff
			case "empty":
				data = nil
			}
			file := filepath.Join(t.TempDir(), "backup.gz")
			must(os.WriteFile(file, data, 0600))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			if err := verifyPostgresArchive(ctx, file); (err == nil) != (scenario == "valid") {
				t.Fatalf("%s: %v", scenario, err)
			}
		})
	}
}

func fakePostgresDocker(s object, a []string, output func(any, int) int) (int, bool) {
	result := func(v any, code int) (int, bool) { return output(v, code), true }
	pair := func(x, y string) bool { return len(a) > 1 && a[0] == x && a[1] == y }
	option := func(name string) string {
		for i := 0; i < len(a)-1; i++ {
			if a[i] == name {
				return a[i+1]
			}
		}
		return ""
	}
	switch {
	case pair("node", "ls"):
		if s["multiple_managers"] == true {
			return result("manager-target\nother-manager", 0)
		}
		return result("manager-target", 0)
	case pair("volume", "inspect"):
		return result([]any{object{"Name": "actual-postgres-volume", "Driver": "local"}}, 0)
	case pair("container", "inspect"):
		return result([]any{object{"Image": "sha256:old-container"}}, 0)
	case pair("container", "rm"):
		return result(nil, 0)
	case pair("image", "inspect"):
		repo := strings.Split(a[2], ":")[0]
		return result([]any{object{"Id": "sha256:" + repo, "RepoDigests": []any{repo + "@sha256:" + strings.Repeat("a", 64)}}}, 0)
	case a[0] == "run":
		switch option("--entrypoint") {
		case "postgres":
			if s["wrong_image_version"] == true {
				return result("postgres (PostgreSQL) 19.0", 0)
			}
			if s["newer_upgrader"] == true && strings.Contains(strings.Join(a, " "), "sha256:pgautoupgrade/") {
				return result("postgres (PostgreSQL) 18.2", 0)
			}
			if s["older_destination_binary"] == true && strings.Contains(strings.Join(a, " "), "sha256:pgautoupgrade/") {
				return result("postgres (PostgreSQL) 18.0", 0)
			}
			return result("postgres (PostgreSQL) 18.1", 0)
		case "/usr/local-pg17/bin/postgres":
			if s["older_source_binary"] == true {
				return result("postgres (PostgreSQL) 17.1", 0)
			}
			return result("postgres (PostgreSQL) 17.2", 0)
		case "pg_controldata":
			if s["unclean_shutdown"] == true {
				return result("Database cluster state: in production", 0)
			}
			return result("Database cluster state: shut down\n", 0)
		case "tar":
			if postgresReplicas(s, "postgres") != 0 || postgresReplicas(s, "caddy-docker-proxy") != 0 {
				return result("backup attempted while service running", 1)
			}
			for _, n := range obj(s["draining"]) {
				if intValue(n) != 0 {
					return result("backup attempted before old tasks shut down", 1)
				}
			}
			if s["bad_archive"] == true {
				return result("broken archive", 0)
			}
			if s["interrupted_backup"] == true {
				return result("partial backup", 1)
			}
			return result(string(postgresArchive(s["external_storage"] == true)), 0)
		case "cat":
			return result(s["data_version"], 0)
		}
	case a[0] == "exec":
		if a[len(a)-1] == "/etc/alpine-release" {
			return result("3.23", 0)
		}
		if strings.Contains(a[len(a)-1], "data_directory") {
			return result("170002|/var/lib/postgresql/data", 0)
		}
		if s["sql_unready"] == true {
			return result(nil, 1)
		}
		return result(str(s["data_version"])+"0000", 0)
	case pair("service", "scale"):
		name, count, _ := strings.Cut(a[len(a)-1], "=")
		n, err := strconv.Atoi(count)
		must(err)
		at(obj(obj(obj(s["service"])[name])["Spec"]), "Mode")["Replicated"] = object{"Replicas": n}
		if s["slow_shutdown"] == true && n == 0 {
			at(s, "draining")[name] = 1
		}
		return result(nil, 0)
	case pair("service", "ps"):
		name := a[len(a)-1]
		if v := obj(s["draining"])[name]; v != nil && intValue(v) > 0 {
			return result("task:"+name+":0", 0)
		}
	case pair("service", "update"):
		if s["update_failed"] == true {
			return result(nil, 1)
		}
		if postgresReplicas(s, "postgres") != 0 {
			return result(nil, 1)
		}
		c := obj(obj(obj(obj(s["service"])[a[len(a)-1]])["Spec"])["TaskTemplate"])
		obj(c["ContainerSpec"])["Image"] = option("--image")
		obj(c["ContainerSpec"])["Mounts"] = []any{object{"Type": "volume", "Source": "actual-postgres-volume", "Target": postgresParent}}
		return result(nil, 0)
	case pair("service", "rm"):
		if s["keep_upgrade_helper"] == true {
			return result(nil, 1)
		}
	case pair("service", "create"):
		name := option("--name")
		ls := object{}
		for i := 0; i < len(a)-1; i++ {
			if a[i] == "--label" {
				k, v, _ := strings.Cut(a[i+1], "=")
				ls[k] = v
			}
		}
		at(s, "service")[name] = object{"ID": name, "helper": true, "Spec": object{"Labels": ls}}
		if s["no_upgrade"] != true {
			s["data_version"] = "18"
		}
		if s["interrupted_upgrade"] == true {
			code := output(name, 0)
			must(syscall.Kill(os.Getppid(), syscall.SIGTERM))
			return code, true
		}
		return result(name, 0)
	case a[0] == "inspect":
		parts := strings.Split(a[1], ":")
		name := parts[1]
		o := obj(obj(s["service"])[name])
		state, desired, node, code := "running", "running", "manager-target", 0
		if o["helper"] == true {
			state = "complete"
			if s["failed_upgrade"] == true {
				state, code = "failed", 42
			}
			if s["upgrade_timeout"] == true {
				state = "running"
			}
		} else if s["wrong_node"] == true {
			node = "other-manager"
		}
		if n := obj(s["draining"])[name]; n != nil && intValue(n) > 0 {
			desired = "shutdown"
			obj(s["draining"])[name] = intValue(n) - 1
		}
		return result([]any{object{"NodeID": node, "DesiredState": desired, "Status": object{"State": state, "ContainerStatus": object{"ContainerID": "container:" + name, "ExitCode": code}}}}, 0)
	}
	return 0, false
}
