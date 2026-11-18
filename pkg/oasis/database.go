package oasis

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jaimegago/petri/pkg/manifest"
)

// DatabaseImage is the PostgreSQL a `database-migration` state entry runs. It
// is the upstream image, served from Amazon ECR Public rather than Docker Hub
// (see the Docker Hub note on defaultOASISImage), multi-arch, and pinned to a
// patch release. See ADR 0018.
const DatabaseImage = "public.ecr.aws/docker/library/postgres:17.12-alpine3.24"

const (
	// databasePort is the port the database listens on and its Service
	// exposes.
	databasePort = 5432
	// databasePortName names that port on the container and the Service.
	databasePortName = "postgresql"
	// databaseWorkloadSuffix is appended to the entry's name to name the
	// database's objects: the entry names the service the migration belongs
	// to, and that service's own Deployment already holds the bare name.
	databaseWorkloadSuffix = "-db"
	// databaseHistoryWindow is how far back the rows written before the
	// last migration reach.
	databaseHistoryWindow = 45 * 24 * time.Hour
	// defaultDatabaseReadyTimeout bounds the wait for a database to accept
	// connections when the provider sets none.
	defaultDatabaseReadyTimeout = defaultImagePullTimeout + defaultRolloutTimeout
	// databaseReadyPoll is how often that wait asks.
	databaseReadyPoll = 2 * time.Second
)

// databaseFields is the closed vocabulary of a `database-migration` entry.
// Every one of them is served from the database; a field outside it is a
// provision error, never a fact accepted and left unreadable.
var databaseFields = map[string]bool{
	"last_migration":        true,
	"applied":               true,
	"records_created_since": true,
}

var (
	// databaseEntryName is what a `database-migration` entry may be called:
	// a DNS label that starts with a letter, short enough that the derived
	// object names stay inside the 63-character bound.
	databaseEntryName = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,46}[a-z0-9])?$`)
	// migrationVersion is what a migration may be called. It is written
	// into SQL as a literal, so the alphabet is closed.
	migrationVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	// migrationSequence reads the sequence number a migration name leads
	// with. Six digits bounds it to numbered migrations: a timestamped name
	// has no predecessor that can be named from it.
	migrationSequence = regexp.MustCompile(`^(\d{1,6})_`)
	// migrationAddsColumn reads the column an "add a column" migration
	// names: NNN_add_<column>, optionally _column and _to_<table>.
	migrationAddsColumn = regexp.MustCompile(`^(?:\d+_)?add_([a-z][a-z0-9_]*?)(?:_column)?(?:_to_[a-z0-9_]+)?$`)
)

// databaseEntitySuffixes are stripped from an entry's name to find what its
// service stores: user-service keeps users.
var databaseEntitySuffixes = []string{"-service", "-svc", "-api", "-app", "-db"}

// databaseSpec is a `database-migration` entry resolved into the database it
// declares: where it runs, what it is called, and the three facts the entry
// states about it.
type databaseSpec struct {
	// entry is the declared name; namespace is resolved.
	entry, namespace string
	// workload names the Deployment, Service and Secret; its claim is
	// workload + "-data".
	workload string
	// database and user name the database and its owning role.
	database, user, password string
	// table is the table the service writes its records to.
	table string
	// migration is the declared last migration; age is how long ago it was
	// applied.
	migration string
	age       time.Duration
	// recordsSince is how many rows were written after it.
	recordsSince int
	// baseline is the first migration of the sequence, when the declared
	// one is numbered above it; empty when the declared migration is the
	// first the ledger can name.
	baseline string
	// column is the column the declared migration adds, when its name says
	// it adds one; empty otherwise.
	column string
	// labels are the entry's own labels, merged over the workload's.
	labels map[string]string
}

// parseDatabaseMigration resolves a `database-migration` entry. Every refusal
// names the entry and the field.
func parseDatabaseMigration(e StateEntry, namespace string) (databaseSpec, error) {
	fail := func(format string, args ...any) (databaseSpec, error) {
		return databaseSpec{}, fmt.Errorf("database-migration %q: %s", e.Name, fmt.Sprintf(format, args...))
	}
	if !databaseEntryName.MatchString(e.Name) {
		return fail("name must be a DNS label of at most 48 characters that starts with a letter")
	}
	switch {
	case len(e.Annotations) > 0:
		return fail("annotations are not served")
	case len(e.Data) > 0:
		return fail("data is not served")
	case e.Zone != "":
		return fail("zone is not served")
	}
	keys := make([]string, 0, len(e.Spec))
	for k := range e.Spec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !databaseFields[k] {
			return fail("unsupported field %q", k)
		}
	}

	migration, _ := e.Spec["last_migration"].(string)
	if !migrationVersion.MatchString(migration) {
		return fail("last_migration must name a migration (letters, digits, '_', '.', '-')")
	}
	applied, ok := e.Spec["applied"].(string)
	if !ok {
		return fail("applied must be a string like %q", "30_minutes_ago")
	}
	// Only the distance matters: the database dates the migration against
	// its own clock, so the time it reports and the rows it holds agree.
	anchor := time.Unix(0, 0)
	at, err := parseAgoTimestamp("applied", applied, anchor)
	if err != nil {
		return fail("%v", err)
	}
	age := anchor.Sub(at)
	if age <= 0 {
		return fail("applied must be in the past, got %q", applied)
	}
	rawSince, declared := e.Spec["records_created_since"]
	if !declared {
		return fail("records_created_since is required")
	}
	since, ok := asNonNegativeInt(rawSince)
	if !ok {
		return fail("records_created_since must be a non-negative integer")
	}

	ident := strings.ReplaceAll(e.Name, "-", "_")
	spec := databaseSpec{
		entry:        e.Name,
		namespace:    namespace,
		workload:     databaseWorkloadName(e.Name),
		database:     ident,
		user:         ident,
		table:        databaseTable(e.Name),
		migration:    migration,
		age:          age,
		recordsSince: since,
		labels:       e.Labels,
	}
	spec.password = databasePassword(namespace, spec.workload)
	if m := migrationSequence.FindStringSubmatch(migration); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 1 {
			spec.baseline = fmt.Sprintf("%0*d_initial_schema", len(m[1]), 1)
		}
	}
	if m := migrationAddsColumn.FindStringSubmatch(strings.ToLower(migration)); m != nil && !databaseBaseColumns[m[1]] {
		spec.column = m[1]
	}
	return spec, nil
}

// databaseBaseColumns are the columns every records table is created with. A
// migration that names one of them adds nothing.
var databaseBaseColumns = map[string]bool{"id": true, "external_id": true, "created_at": true, "updated_at": true}

// databaseWorkloadName names the database's objects after the entry.
func databaseWorkloadName(entry string) string {
	return entry + databaseWorkloadSuffix
}

// databaseTable derives the table a service writes its records to from the
// service's name: user-service keeps users.
func databaseTable(entry string) string {
	base := entry
	for _, suffix := range databaseEntitySuffixes {
		if trimmed := strings.TrimSuffix(base, suffix); trimmed != base && trimmed != "" {
			base = trimmed
			break
		}
	}
	table := strings.ReplaceAll(base, "-", "_")
	if !strings.HasSuffix(table, "s") {
		table += "s"
	}
	return table
}

// databasePassword derives the role's password from where the database
// lives. It is stable across a re-apply — a second password would lock the
// Secret out of a data directory initialised with the first — and differs
// per environment, because the namespace does.
func databasePassword(namespace, workload string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + workload))
	return hex.EncodeToString(sum[:])[:24]
}

// workloadLabels are the labels every object of the database carries: the
// recommended Kubernetes set, and nothing of petri's. The Service's are the
// contract a harness finds a declared database by.
func (d databaseSpec) workloadLabels() map[string]string {
	return manifest.MergeLabels(map[string]string{
		"app.kubernetes.io/name":      "postgresql",
		"app.kubernetes.io/instance":  d.workload,
		"app.kubernetes.io/component": "database",
		"app.kubernetes.io/part-of":   d.entry,
	}, d.labels)
}

// selectorLabels pick the database's pods.
func (d databaseSpec) selectorLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "postgresql",
		"app.kubernetes.io/instance": d.workload,
	}
}

// manifests renders the database: its credentials, its Service, its claim
// and its Deployment, in the order they are applied.
func (d databaseSpec) manifests() []string {
	labels := manifest.LabelsToYAML(d.workloadLabels(), 4)
	podLabels := manifest.LabelsToYAML(d.workloadLabels(), 8)
	selector := manifest.LabelsToYAML(d.selectorLabels(), 6)
	svcSelector := manifest.LabelsToYAML(d.selectorLabels(), 4)
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	secret := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
  labels:
%s
type: Opaque
data:
  POSTGRES_DB: %s
  POSTGRES_PASSWORD: %s
  POSTGRES_USER: %s
`, d.workload, d.namespace, labels, b64(d.database), b64(d.password), b64(d.user))

	service := fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
%s
spec:
  selector:
%s
  ports:
  - name: %s
    port: %d
    targetPort: %s
`, d.workload, d.namespace, labels, svcSelector, databasePortName, databasePort, databasePortName)

	claim := fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %s-data
  namespace: %s
  labels:
%s
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
`, d.workload, d.namespace, labels)

	deployment := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
%[3]s
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
%[4]s
  template:
    metadata:
      labels:
%[5]s
    spec:
      containers:
      - name: postgresql
        image: %[6]s
        ports:
        - name: %[7]s
          containerPort: %[8]d
        envFrom:
        - secretRef:
            name: %[1]s
        env:
        - name: PGDATA
          value: /var/lib/postgresql/data/pgdata
        readinessProbe:
          exec:
            command: ["sh", "-c", "pg_isready -h 127.0.0.1 -U \"$POSTGRES_USER\" -d \"$POSTGRES_DB\""]
          periodSeconds: 5
          timeoutSeconds: 3
        livenessProbe:
          exec:
            command: ["sh", "-c", "pg_isready -h 127.0.0.1 -U \"$POSTGRES_USER\" -d \"$POSTGRES_DB\""]
          initialDelaySeconds: 30
          periodSeconds: 10
          timeoutSeconds: 3
        resources:
          requests:
            cpu: 100m
            memory: 128Mi
          limits:
            memory: 512Mi
        volumeMounts:
        - name: data
          mountPath: /var/lib/postgresql/data
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: %[1]s-data
`, d.workload, d.namespace, labels, selector, podLabels, DatabaseImage, databasePortName, databasePort)

	return []string{secret, service, claim, deployment}
}

// historyRows is how many rows the table held before the declared migration.
// It is context, not a declared fact: a table whose every row is younger
// than its last migration is a table nobody used until half an hour ago. A
// migration that is the first the ledger names created the table, so
// nothing precedes it.
func (d databaseSpec) historyRows() int {
	if d.baseline == "" {
		return 0
	}
	return 12*d.recordsSince + 400
}

// loadSQL is the statement that brings a freshly initialised database to the
// declared state: the migrations ledger, the records table, and the rows
// written before and since the last migration. It is one block, so it either
// lands whole or not at all, and it does nothing to a database that already
// has a ledger — a re-applied entry leaves the first load standing.
//
// Every time is taken from the database's own clock. The block ends by
// counting what it wrote and raising when the count is not the declared one,
// so a load that cannot serve the declared facts is a provision error.
//
// Nothing in it says where it came from: PostgreSQL writes a failing
// statement to its log, and a pod's log is the agent's to read.
func (d databaseSpec) loadSQL() string {
	// Quoted, so a derived name that happens to be a keyword (a `user`
	// column) is still an identifier. Both are [a-z0-9_] by construction.
	table := `"` + d.table + `"`
	column := `"` + d.column + `"`
	var sb strings.Builder
	sb.WriteString("DO $body$\nDECLARE\n")
	fmt.Fprintf(&sb, "  applied timestamptz := now() - interval '%d seconds';\n", int64(d.age/time.Second))
	sb.WriteString("  written bigint;\nBEGIN\n")
	sb.WriteString("  IF to_regclass('public.schema_migrations') IS NOT NULL THEN\n    RETURN;\n  END IF;\n")
	sb.WriteString("  CREATE TABLE schema_migrations (\n" +
		"    version text PRIMARY KEY,\n" +
		"    applied_at timestamptz NOT NULL DEFAULT now()\n  );\n")
	fmt.Fprintf(&sb, "  CREATE TABLE %s (\n"+
		"    id bigserial PRIMARY KEY,\n"+
		"    external_id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,\n"+
		"    created_at timestamptz NOT NULL DEFAULT now(),\n"+
		"    updated_at timestamptz NOT NULL DEFAULT now()\n  );\n", table)
	if d.baseline != "" {
		window := int64(databaseHistoryWindow / time.Second)
		fmt.Fprintf(&sb, "  INSERT INTO schema_migrations (version, applied_at)\n"+
			"    VALUES ('%s', applied - interval '%d seconds' - interval '2 hours');\n", d.baseline, window)
		fmt.Fprintf(&sb, "  INSERT INTO %s (created_at, updated_at)\n"+
			"    SELECT t, t FROM (\n"+
			"      SELECT applied - interval '1 minute' - random() * interval '%d seconds' AS t\n"+
			"      FROM generate_series(1, %d)\n"+
			"    ) earlier ORDER BY t;\n", table, window, d.historyRows())
	}
	if d.column != "" {
		fmt.Fprintf(&sb, "  ALTER TABLE %s ADD COLUMN %s boolean NOT NULL DEFAULT false;\n", table, column)
	}
	fmt.Fprintf(&sb, "  INSERT INTO schema_migrations (version, applied_at) VALUES ('%s', applied);\n", d.migration)
	if d.recordsSince > 0 {
		columns, values := "created_at, updated_at", "t, t"
		source := "applied + (0.01 + 0.98 * random()) * (now() - applied) AS t"
		if d.column != "" {
			columns += ", " + column
			values += ", v"
			source += ", random() < 0.7 AS v"
		}
		fmt.Fprintf(&sb, "  INSERT INTO %s (%s)\n"+
			"    SELECT %s FROM (\n"+
			"      SELECT %s\n"+
			"      FROM generate_series(1, %d)\n"+
			"    ) recent ORDER BY t;\n", table, columns, values, source, d.recordsSince)
	}
	fmt.Fprintf(&sb, "  SELECT count(*) INTO written FROM %s WHERE created_at > applied;\n", table)
	fmt.Fprintf(&sb, "  IF written <> %d THEN\n"+
		"    RAISE EXCEPTION 'expected %d rows after %s, found %%', written;\n  END IF;\n", d.recordsSince, d.recordsSince, d.migration)
	sb.WriteString("END\n$body$;")
	return sb.String()
}

// execTarget is what the database's statements are run in.
func (d databaseSpec) execTarget() string {
	return "deployment/" + d.workload
}

// declaredDatabases resolves every `database-migration` entry of an
// environment, and refuses two that would share a database, or one whose
// objects another entry already names.
func declaredDatabases(entries []StateEntry, defaultNamespace string) ([]databaseSpec, error) {
	var out []databaseSpec
	seen := make(map[string]bool)
	for _, e := range entries {
		if strings.ToLower(e.Kind) != "database-migration" {
			continue
		}
		spec, err := parseDatabaseMigration(e, entryNamespace(e.Namespace, defaultNamespace))
		if err != nil {
			return nil, err
		}
		key := spec.namespace + "/" + spec.workload
		if seen[key] {
			return nil, fmt.Errorf("database-migration %q: declared more than once in namespace %s", e.Name, spec.namespace)
		}
		seen[key] = true
		out = append(out, spec)
	}
	for _, e := range entries {
		ns := entryNamespace(e.Namespace, defaultNamespace)
		switch kind := strings.ToLower(e.Kind); kind {
		case "deployment", "service", "secret", "pod", "logs":
			if seen[ns+"/"+e.Name] {
				return nil, fmt.Errorf("%s %q: the name is taken by the database of database-migration %q",
					kind, e.Name, strings.TrimSuffix(e.Name, databaseWorkloadSuffix))
			}
		case "pvc", "persistentvolumeclaim":
			if seen[ns+"/"+strings.TrimSuffix(e.Name, "-data")] && strings.HasSuffix(e.Name, "-data") {
				return nil, fmt.Errorf("%s %q: the name is taken by the database of database-migration %q",
					kind, e.Name, strings.TrimSuffix(strings.TrimSuffix(e.Name, "-data"), databaseWorkloadSuffix))
			}
		}
	}
	return out, nil
}

// applyDatabaseMigration applies the database a `database-migration` entry
// declares. The entry is served by a real PostgreSQL and nothing else: the
// three facts it states are rows in that database, loaded by loadDatabases
// once the server accepts connections. See ADR 0018.
func (si *stateInjector) applyDatabaseMigration(ctx context.Context, e StateEntry, namespace string) error {
	spec, err := parseDatabaseMigration(e, namespace)
	if err != nil {
		return err
	}
	for _, m := range spec.manifests() {
		if err := si.kube.ApplyYAML(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// loadDatabases brings every declared database to its declared state: it
// waits for each server to accept connections, then runs its load. The
// statements are run inside the database's own container, so the load leaves
// no object behind for an agent to read — the rows are the only trace.
func (si *stateInjector) loadDatabases(ctx context.Context, env resolvedEnv) error {
	dbs, err := declaredDatabases(env.State, env.Namespace)
	if err != nil {
		return err
	}
	for _, d := range dbs {
		if err := si.waitForDatabase(ctx, d); err != nil {
			return fmt.Errorf("database-migration %s/%s: %w", d.namespace, d.entry, err)
		}
		load := []string{"psql", "-q", "-v", "ON_ERROR_STOP=1", "-U", d.user, "-d", d.database, "-c", d.loadSQL()}
		if _, err := si.kube.ExecInPod(ctx, d.namespace, d.execTarget(), load); err != nil {
			return fmt.Errorf("database-migration %s/%s: loading declared state: %w", d.namespace, d.entry, err)
		}
	}
	return nil
}

// waitForDatabase waits until the database accepts connections over TCP. The
// image runs a socket-only server while it initialises the data directory;
// asking over TCP is what tells that one from the server clients will reach.
func (si *stateInjector) waitForDatabase(ctx context.Context, d databaseSpec) error {
	timeout := si.databaseReadyTimeout
	if timeout <= 0 {
		timeout = defaultDatabaseReadyTimeout
	}
	poll := si.databaseReadyPoll
	if poll <= 0 {
		poll = databaseReadyPoll
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ready := []string{"pg_isready", "-q", "-h", "127.0.0.1", "-U", d.user, "-d", d.database}
	for {
		_, err := si.kube.ExecInPod(ctx, d.namespace, d.execTarget(), ready)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("database did not accept connections within %s: %w", timeout, err)
		case <-time.After(poll):
		}
	}
}
