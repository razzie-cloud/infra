package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const postgresParent = "/var/lib/postgresql"

type postgresUpgrade struct {
	d                                       *deployment
	service, caddy, volume, node, user, db  string
	oldImage, oldContainerImage, oldMount   string
	image, upgradeImage, archive, directory string
	secret                                  object
	caddyReplicas                           int
	postgresStopped, caddyStopped, modified bool
}

func (d *deployment) upgradePostgres() {
	n, err := strconv.Atoi(fallback(d.env, "POSTGRES_UPGRADE_TIMEOUT", "1800"))
	if err != nil || n < 1 || n > 86400 {
		fail("POSTGRES_UPGRADE_TIMEOUT must be between 1 and 86400 seconds")
	}
	d.timeout = time.Duration(n) * time.Second
	u := &postgresUpgrade{d: d, service: d.stack + "_postgres", caddy: d.stack + "_caddy-docker-proxy"}
	defer func() {
		if d.helper != "" {
			fmt.Fprintln(os.Stderr, "Upgrade helper removal could not be verified. Deployment lock retained: "+d.lockName)
			return
		}
		d.cleanup()
	}()
	d.acquire()
	defer u.recover()
	u.preflight()

	d.announce("stop Caddy and PostgreSQL")
	u.caddyStopped = true
	u.scale(u.caddy, 0)
	u.postgresStopped = true
	u.scale(u.service, 0)

	d.announce("download cold PostgreSQL backup")
	control := string(d.docker.call("run", "--rm", "--network", "none", "--mount", "type=volume,source="+u.volume+",target=/backup,readonly", "--entrypoint", "pg_controldata", u.oldContainerImage, "/backup"))
	if !regexp.MustCompile(`(?m)^Database cluster state:\s+shut down\s*$`).MatchString(control) {
		fail("PostgreSQL did not shut down cleanly; upgrade has not started")
	}
	u.backup()

	d.announce("upgrade PostgreSQL volume")
	// From this point even an interrupted create may have started modifying data.
	u.modified = true
	d.helper = resourceName(d.stack + "-postgres-upgrade-" + d.runID)
	d.helperKind = "postgres-upgrade"
	a := []string{"service", "create", "--detach=true", "--name", d.helper, "--constraint", "node.id==" + u.node, "--restart-condition", "none", "--no-healthcheck", "--log-driver", "none",
		"--mount", "type=volume,source=" + u.volume + ",target=" + postgresParent,
		"--secret", "source=" + str(u.secret["SecretName"]) + ",target=postgres_password,mode=0400",
		"--env", "PGAUTO_ONESHOT=yes", "--env", "POSTGRES_USER=" + u.user, "--env", "POSTGRES_DB=" + u.db,
		"--env", "POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password", "--env", "PGDATA=" + postgresParent + "/18/docker"}
	a = append(a, labelArgs(extend(d.owner, object{"kind": d.helperKind, "run": d.runID}))...)
	d.docker.call(append(a, u.upgradeImage)...)
	u.waitUpgrade()
	d.removeHelper()
	version := d.docker.call("run", "--rm", "--network", "none", "--mount", "type=volume,source="+u.volume+",target=/backup,readonly", "--entrypoint", "cat", u.image, "/backup/18/docker/PG_VERSION")
	if strings.TrimSpace(string(version)) != "18" {
		fail("Upgrader did not produce a PostgreSQL 18 cluster")
	}

	d.announce("start upgraded PostgreSQL")
	d.docker.call("service", "update", "--detach=true", "--image", u.image, "--env-rm", "PGDATA", "--mount-rm", u.oldMount, "--mount-add", "type=volume,source="+u.volume+",target="+postgresParent, "--replicas", "0", u.service)
	u.scale(u.service, 1)
	u.waitSQL(18)
	d.announce("restore Caddy")
	u.scale(u.caddy, u.caddyReplicas)
	u.postgresStopped, u.caddyStopped = false, false
	fmt.Println("PostgreSQL upgrade complete. Cold backup and recovery metadata: " + u.directory)
	fmt.Println("Keep POSTGRES_IMAGE_TAG and POSTGRES_VOLUME_TARGET in the selected environment file for future deployments.")
}

func (u *postgresUpgrade) preflight() {
	d := u.d
	d.announce("PostgreSQL upgrade preflight")
	tag := d.env["POSTGRES_IMAGE_TAG"]
	if !regexp.MustCompile(`^18(?:\.[0-9]+)?-alpine(?:[0-9]+\.[0-9]+)?$`).MatchString(tag) {
		fail("This target supports upgrading the standard Alpine PostgreSQL 17 volume to an explicit PostgreSQL 18 Alpine tag")
	}
	if d.env["POSTGRES_VOLUME_TARGET"] != postgresParent {
		fail("Set POSTGRES_VOLUME_TARGET=/var/lib/postgresql alongside the destination POSTGRES_IMAGE_TAG before upgrading")
	}
	info := obj(decode(d.docker.call("info", "--format", "{{json .Swarm}}")))
	u.node = str(info["NodeID"])
	if info["ControlAvailable"] != true || str(info["LocalNodeState"]) != "active" || u.node == "" {
		fail("Selected Docker context must reach an active Swarm manager")
	}
	// shortcut: local-volume streaming requires the sole manager; use node helpers if multiple managers are added.
	managers := strings.Fields(string(d.docker.call("node", "ls", "--filter", "role=manager", "--format", "{{.ID}}")))
	if len(managers) != 1 || managers[0] != u.node {
		fail("PostgreSQL upgrade requires a single manager reached directly by DOCKER_CONTEXT")
	}
	pg := d.docker.inspect("service", u.service)
	caddy := d.docker.inspect("service", u.caddy)
	if pg == nil || caddy == nil {
		fail("Existing PostgreSQL and Caddy services are required")
	}
	for _, s := range []object{pg, caddy} {
		if str(labels(s)["com.docker.stack.namespace"]) != d.stack {
			fail("Service does not belong to the selected stack")
		}
		state := str(obj(s["UpdateStatus"])["State"])
		if state != "" && state != "completed" && state != "rollback_completed" {
			fail("Finish or resolve the existing service rollout before upgrading PostgreSQL")
		}
	}
	pgSpec, caddySpec := obj(pg["Spec"]), obj(caddy["Spec"])
	if obj(obj(pgSpec["Mode"])["Replicated"])["Replicas"] == nil || intValue(obj(obj(pgSpec["Mode"])["Replicated"])["Replicas"]) != 1 {
		fail("PostgreSQL must have exactly one configured replica")
	}
	cr := obj(obj(caddySpec["Mode"])["Replicated"])["Replicas"]
	if cr == nil || intValue(cr) < 1 {
		fail("Caddy must be running in replicated mode")
	}
	u.caddyReplicas = intValue(cr)
	c := obj(obj(pgSpec["TaskTemplate"])["ContainerSpec"])
	if len(arr(c["Command"])) != 0 || len(arr(c["Args"])) != 0 || str(c["User"]) != "" {
		fail("Custom PostgreSQL commands or users need a manual migration")
	}
	env := map[string]string{}
	for _, item := range arr(c["Env"]) {
		k, v, _ := strings.Cut(str(item), "=")
		env[k] = v
	}
	u.user, u.db = env["POSTGRES_USER"], env["POSTGRES_DB"]
	if u.user == "" || u.db == "" || env["POSTGRES_PASSWORD"] != "" || env["POSTGRES_PASSWORD_FILE"] != "/run/secrets/postgres_password" || (env["PGDATA"] != "" && env["PGDATA"] != postgresParent+"/data") || env["POSTGRES_INITDB_WALDIR"] != "" {
		fail("Expected standard PostgreSQL user/database, password secret and data layout")
	}
	for _, m := range arr(c["Mounts"]) {
		m := obj(m)
		if str(m["Target"]) == postgresParent+"/data" && str(m["Type"]) == "volume" && m["ReadOnly"] != true && obj(m["VolumeOptions"])["Subpath"] == nil {
			u.volume, u.oldMount = str(m["Source"]), str(m["Target"])
		}
	}
	if u.volume == "" || len(arr(c["Mounts"])) != 1 {
		fail("Expected one named PostgreSQL volume at /var/lib/postgresql/data")
	}
	for _, s := range arr(c["Secrets"]) {
		s := obj(s)
		if str(obj(s["File"])["Name"]) == "postgres_password" {
			u.secret = s
		}
	}
	if u.secret == nil || str(u.secret["SecretName"]) == "" {
		fail("PostgreSQL password secret reference is missing")
	}
	secret := d.docker.inspect("secret", str(u.secret["SecretName"]))
	if secret == nil || str(secret["ID"]) != str(u.secret["SecretID"]) {
		fail("PostgreSQL password secret identity changed")
	}
	cid := u.runningContainer(u.service)
	container := d.docker.inspect("container", cid)
	u.oldContainerImage = str(container["Image"])
	u.oldImage = str(c["Image"])
	if u.oldContainerImage == "" {
		fail("Cannot determine running PostgreSQL image")
	}
	live := strings.TrimSpace(string(d.docker.call("exec", cid, "psql", "-XAtw", "-U", u.user, "-d", u.db, "-c", "SELECT current_setting('server_version_num') || '|' || current_setting('data_directory')")))
	version, directory, ok := strings.Cut(live, "|")
	v, err := strconv.Atoi(version)
	if !ok || err != nil || v < 170000 || v >= 180000 || directory != u.oldMount {
		fail("Running cluster must be PostgreSQL 17 with the standard data directory")
	}
	d.docker.call("exec", cid, "cat", "/etc/alpine-release")
	vol := d.docker.inspect("volume", u.volume)
	if str(vol["Driver"]) != "local" || len(obj(vol["Options"])) != 0 {
		fail("Only a standard local PostgreSQL volume is supported")
	}
	destinationVersion := 0
	for _, name := range []string{"postgres:" + tag, "pgautoupgrade/pgautoupgrade:" + tag} {
		d.docker.run(nil, false, d.timeout, "image", "pull", name)
		i := d.docker.inspect("image", name)
		image := str(i["Id"])
		if image == "" {
			fail("Cannot determine pulled image identity")
		}
		actual := postgresBinaryVersion(d.docker.call("run", "--rm", "--network", "none", "--entrypoint", "postgres", image, "--version"))
		if actual/10000 != 18 {
			fail("Selected image does not contain PostgreSQL 18")
		}
		if strings.HasPrefix(name, "postgres:") {
			destinationVersion = actual
			// Service updates need a registry digest, not a node-local image ID.
			for _, ref := range arr(i["RepoDigests"]) {
				if strings.HasPrefix(str(ref), "postgres@sha256:") {
					u.image = str(ref)
				}
			}
		} else {
			if actual > destinationVersion {
				fail(fmt.Sprintf("Upgrader PostgreSQL %d.%d is newer than final destination %d.%d; select a destination at least as recent as the upgrader", actual/10000, actual%10000, destinationVersion/10000, destinationVersion%10000))
			}
			old := postgresBinaryVersion(d.docker.call("run", "--rm", "--network", "none", "--entrypoint", "/usr/local-pg17/bin/postgres", image, "--version"))
			if old/10000 != 17 || old < v {
				fail(fmt.Sprintf("Upgrader source binary %d.%d is older than running PostgreSQL %d.%d; use an updated upgrader image", old/10000, old%10000, v/10000, v%10000))
			}
			for _, ref := range arr(i["RepoDigests"]) {
				if strings.HasPrefix(str(ref), "pgautoupgrade/pgautoupgrade@sha256:") {
					u.upgradeImage = str(ref)
				}
			}
		}
	}
	if u.image == "" || u.upgradeImage == "" {
		fail("Pulled PostgreSQL images must have registry digests")
	}
	backupDir := fallback(d.env, "POSTGRES_BACKUP_DIR", "./backups")
	must(os.MkdirAll(backupDir, 0700))
	u.directory, err = os.MkdirTemp(backupDir, d.stack+"-postgres-"+time.Now().UTC().Format("20060102T150405Z")+"-")
	must(err)
	u.directory, err = filepath.Abs(u.directory)
	must(err)
	u.archive = filepath.Join(u.directory, "postgres.tar.gz")
	metadata := object{"docker_context": d.docker.context, "stack": d.stack, "node": u.node, "volume": u.volume, "original_image": u.oldImage, "original_container_image": u.oldContainerImage, "original_mount": u.oldMount, "postgres_replicas": 1, "caddy_replicas": u.caddyReplicas, "destination_tag": tag, "destination_image": u.image, "upgrade_image": u.upgradeImage}
	writeNew(filepath.Join(u.directory, "recovery.json"), append(encode(metadata), '\n'), 0600)
	fmt.Println("Cold backup destination: " + u.archive)
}

func postgresBinaryVersion(output []byte) int {
	m := regexp.MustCompile(`^postgres \(PostgreSQL\) ([0-9]+)\.([0-9]+)(?:\s.*)?$`).FindStringSubmatch(strings.TrimSpace(string(output)))
	if m == nil {
		fail("Cannot identify PostgreSQL binary version")
	}
	major, err := strconv.Atoi(m[1])
	must(err)
	minor, err := strconv.Atoi(m[2])
	must(err)
	return major*10000 + minor
}

func (u *postgresUpgrade) runningContainer(service string) string {
	ids := taskIDs(u.d, service, true)
	if len(ids) != 1 {
		fail("Expected one running task for " + service)
	}
	t := u.d.task(ids[0])
	s := obj(t["Status"])
	if str(t["NodeID"]) != u.node || str(s["State"]) != "running" {
		fail("PostgreSQL must be running on the manager reached by DOCKER_CONTEXT")
	}
	cid := str(obj(s["ContainerStatus"])["ContainerID"])
	if cid == "" {
		fail("Running task has no container")
	}
	return cid
}

func (u *postgresUpgrade) scale(service string, replicas int) {
	d := u.d
	d.docker.call("service", "scale", "--detach=true", service+"="+strconv.Itoa(replicas))
	end := time.Now().Add(d.timeout)
	for time.Now().Before(end) {
		running, pending := 0, false
		for _, id := range taskIDs(d, service, false) {
			t := d.task(id)
			state := str(obj(t["Status"])["State"])
			switch state {
			case "complete", "shutdown", "failed", "rejected", "remove":
			default:
				pending = true
			}
			if state == "running" && str(t["DesiredState"]) == "running" {
				running++
			}
		}
		if replicas == 0 && !pending || replicas > 0 && running == replicas {
			return
		}
		d.pause(time.Second)
	}
	fail("Service did not reach requested replica state: " + service)
}

func (u *postgresUpgrade) waitUpgrade() {
	d := u.d
	end := time.Now().Add(d.timeout)
	for time.Now().Before(end) {
		for _, id := range taskIDs(d, d.helper, false) {
			t := d.task(id)
			s := obj(t["Status"])
			switch str(s["State"]) {
			case "complete":
				if str(t["NodeID"]) == u.node && intValue(obj(s["ContainerStatus"])["ExitCode"]) == 0 {
					return
				}
				fail("PostgreSQL upgrade task did not complete successfully on the volume node")
			case "failed", "rejected", "shutdown", "orphaned":
				fail("PostgreSQL upgrade task failed; restore the cold backup before restarting")
			}
		}
		d.pause(time.Second)
	}
	fail("PostgreSQL upgrade task timed out; restore the cold backup before restarting")
}

func (u *postgresUpgrade) waitSQL(major int) {
	d := u.d
	end := time.Now().Add(d.timeout)
	for time.Now().Before(end) {
		cid := u.runningContainer(u.service)
		b := d.docker.run(nil, true, 10*time.Second, "exec", "--env", "PGCONNECT_TIMEOUT=2", cid, "psql", "-XAtw", "-U", u.user, "-d", u.db, "-c", "SELECT current_setting('server_version_num')")
		v, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err == nil && v/10000 == major {
			return
		}
		d.pause(time.Second)
	}
	fail("PostgreSQL did not pass its SQL readiness check")
}

func (u *postgresUpgrade) backup() {
	d := u.d
	f, err := os.OpenFile(u.archive+".partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	must(err)
	defer f.Close()
	name := resourceName(d.stack + "-postgres-backup-" + d.runID)
	defer func() {
		// Killing the CLI does not necessarily stop its remote container.
		ctx := d.docker.ctx
		d.docker.ctx = context.Background()
		defer func() { d.docker.ctx = ctx }()
		d.docker.run(nil, true, 30*time.Second, "container", "rm", "--force", name)
	}()
	ctx, cancel := context.WithTimeout(d.docker.ctx, d.timeout)
	defer cancel()
	cmd := d.docker.command(ctx, "run", "--rm", "--name", name, "--network", "none", "--log-driver", "none", "--mount", "type=volume,source="+u.volume+",target=/backup,readonly", "--entrypoint", "tar", u.oldContainerImage, "-czf", "-", "-C", "/backup", ".")
	cmd.Stdout = f
	if cmd.Run() != nil {
		fail("Cold backup download failed; incomplete archive retained with .partial suffix")
	}
	must(f.Sync())
	must(f.Close())
	if err := verifyPostgresArchive(ctx, u.archive+".partial"); err != nil {
		fail("Cold backup verification failed; upgrade has not started")
	}
	must(os.Rename(u.archive+".partial", u.archive))
	for _, name := range []string{u.directory, filepath.Dir(u.directory)} {
		dir, err := os.Open(name)
		must(err)
		err = dir.Sync()
		dir.Close()
		must(err)
	}
}

type postgresArchiveReader struct {
	context context.Context
	reader  io.Reader
}

func (r postgresArchiveReader) Read(b []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}

func verifyPostgresArchive(ctx context.Context, filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	z, err := gzip.NewReader(postgresArchiveReader{ctx, f})
	if err != nil {
		return err
	}
	defer z.Close()
	t := tar.NewReader(z)
	version, control := false, false
	for {
		h, err := t.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || h.Typeflag == tar.TypeSymlink {
			return fmt.Errorf("unsupported archive path or external storage link")
		}
		if name == "PG_VERSION" && h.Size < 32 {
			b, err := io.ReadAll(t)
			if err != nil {
				return err
			}
			version = strings.TrimSpace(string(b)) == "17"
		}
		if name == "global/pg_control" && h.Typeflag == tar.TypeReg && h.Size > 0 {
			control = true
		}
	}
	// Tar EOF alone does not verify the gzip trailer/checksum.
	if _, err := io.Copy(io.Discard, z); err != nil {
		return err
	}
	if !version || !control {
		return fmt.Errorf("archive is missing PostgreSQL 17 cluster files")
	}
	return ctx.Err()
}

func (u *postgresUpgrade) recover() {
	r := recover()
	if r == nil {
		return
	}
	d := u.d
	d.docker.ctx = context.Background()
	try := func(fn func()) bool {
		ok := false
		func() {
			defer func() { _ = recover() }()
			fn()
			ok = true
		}()
		return ok
	}
	helperGone := try(d.removeHelper)
	if !u.modified && helperGone {
		restored := !u.postgresStopped || try(func() {
			u.scale(u.service, 1)
			u.waitSQL(17)
		})
		if restored && u.caddyStopped {
			restored = try(func() { u.scale(u.caddy, u.caddyReplicas) })
		}
		if restored {
			fmt.Fprintln(os.Stderr, "Upgrade did not modify data; original service replica counts restored.")
		} else {
			fmt.Fprintln(os.Stderr, "Original replica counts could not be restored; inspect PostgreSQL and Caddy before restarting.")
		}
	} else {
		caddyOff := try(func() { u.scale(u.caddy, 0) })
		pgOff := try(func() { u.scale(u.service, 0) })
		fmt.Fprintf(os.Stderr, "Data may have been modified. Helper removed=%t; Caddy stopped=%t; PostgreSQL stopped=%t. Restore the cold backup before restarting.\n", helperGone, caddyOff, pgOff)
	}
	if u.directory != "" {
		fmt.Fprintln(os.Stderr, "Backup/recovery directory: "+u.directory+"; see docs/operations.md#postgresql-major-upgrade")
	}
	panic(r)
}
