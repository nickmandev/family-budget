package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"family-budget/internal/config"
	"gopkg.in/yaml.v3"
)

// caddyfilePath is the old reverse-proxy config. It must not exist: the
// operator's host nginx with Let's Encrypt is the public front door.
const caddyfilePath = "Caddyfile"

// composeDocument is the subset of the Compose schema these static guards
// inspect. It is intentionally not a full schema model.
type composeDocument struct {
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]yaml.Node      `yaml:"volumes"`
	Networks map[string]composeNetwork `yaml:"networks"`
}

type composeService struct {
	Image       string         `yaml:"image"`
	Build       *build         `yaml:"build"`
	Command     []string       `yaml:"command"`
	Ports       []string       `yaml:"ports"`
	Expose      []string       `yaml:"expose"`
	Restart     string         `yaml:"restart"`
	ShmSize     string         `yaml:"shm_size"`
	Volumes     []string       `yaml:"volumes"`
	Networks    []string       `yaml:"networks"`
	Environment envMap         `yaml:"environment"`
	Healthcheck *health        `yaml:"healthcheck"`
	DependsOn   dependsOn      `yaml:"depends_on"`
	Logging     *logging       `yaml:"logging"`
	Deploy      *composeDeploy `yaml:"deploy"`
}

type composeDeploy struct {
	Resources composeResources `yaml:"resources"`
}

type composeResources struct {
	Limits composeLimits `yaml:"limits"`
}

type composeLimits struct {
	CPUs   string `yaml:"cpus"`
	Memory string `yaml:"memory"`
}

type build struct {
	Context string `yaml:"context"`
	Target  string `yaml:"target"`
}

type envMap map[string]*string

type health struct {
	Test []string `yaml:"test"`
}

type dependsOn map[string]struct {
	Condition string `yaml:"condition"`
}

type logging struct {
	Driver  string            `yaml:"driver"`
	Options map[string]string `yaml:"options"`
}

type composeNetwork struct {
	Internal bool `yaml:"internal"`
}

func loadCompose(t *testing.T) composeDocument {
	t.Helper()

	var doc composeDocument
	if err := yaml.Unmarshal([]byte(readDeployFile(t, "compose.yaml")), &doc); err != nil {
		t.Fatalf("parse compose.yaml: %v", err)
	}
	if len(doc.Services) == 0 {
		t.Fatal("compose.yaml declares no services")
	}
	return doc
}

func requireService(t *testing.T, doc composeDocument, name string) composeService {
	t.Helper()

	service, ok := doc.Services[name]
	if !ok {
		t.Fatalf("compose.yaml has no %q service", name)
	}
	return service
}

func TestComposeTopology(t *testing.T) {
	doc := loadCompose(t)
	db := requireService(t, doc, "db")
	api := requireService(t, doc, "api")
	migrate := requireService(t, doc, "migrate")

	// (a) Caddy is gone; the host nginx is the public front door.
	if _, ok := doc.Services["caddy"]; ok {
		t.Error("compose.yaml still declares a caddy service; the host nginx terminates TLS")
	}

	// (b) PostgreSQL must not be reachable from the host.
	if len(db.Ports) != 0 {
		t.Errorf("db publishes host ports %v; PostgreSQL must stay on the private network", db.Ports)
	}
	if !contains(db.Expose, "5432") {
		t.Errorf("db exposes %v, want the internal 5432 port", db.Expose)
	}

	// (c) Only the API publishes a host port, and only on loopback.
	for name, service := range doc.Services {
		if name == "api" {
			continue
		}
		if len(service.Ports) != 0 {
			t.Errorf("%s publishes host ports %v; only api may publish a loopback port", name, service.Ports)
		}
	}
	if len(api.Ports) != 1 {
		t.Fatalf("api ports = %v, want exactly one loopback publish", api.Ports)
	}
	binding := api.Ports[0]
	if !strings.HasPrefix(binding, "127.0.0.1:") {
		t.Errorf("api host binding = %q, want a 127.0.0.1: loopback prefix", binding)
	}
	if strings.Contains(binding, "0.0.0.0") || strings.HasPrefix(binding, ":") {
		t.Errorf("api host binding = %q must not bind all interfaces", binding)
	}

	// (d) Internal database network and a separate non-internal edge network.
	backend, ok := doc.Networks["backend"]
	if !ok {
		t.Fatal("compose.yaml has no backend network")
	}
	if !backend.Internal {
		t.Error("backend network must be internal so db has no outbound route")
	}
	frontend, ok := doc.Networks["frontend"]
	if !ok {
		t.Error("compose.yaml has no frontend network")
	} else if frontend.Internal {
		t.Error("frontend network must be non-internal for the loopback publish to work")
	}
	if !contains(db.Networks, "backend") || contains(db.Networks, "frontend") {
		t.Errorf("db networks = %v, want backend only", db.Networks)
	}
	if !contains(api.Networks, "backend") || !contains(api.Networks, "frontend") {
		t.Errorf("api networks = %v, want backend and frontend", api.Networks)
	}
	for name, service := range doc.Services {
		if name != "api" && contains(service.Networks, "frontend") {
			t.Errorf("%s is on the frontend network; only api may use it", name)
		}
	}

	// (e) Durable PostgreSQL data on a named volume (PG18 path); no proxy state.
	if _, ok := doc.Volumes["pgdata"]; !ok {
		t.Error("compose.yaml declares no pgdata volume")
	}
	for _, gone := range []string{"caddy_data", "caddy_config"} {
		if _, ok := doc.Volumes[gone]; ok {
			t.Errorf("compose.yaml still declares the %s volume", gone)
		}
	}
	if !contains(db.Volumes, "pgdata:/var/lib/postgresql") {
		t.Errorf("db volumes = %v, want pgdata:/var/lib/postgresql for PostgreSQL 18", db.Volumes)
	}

	// Build wiring must use the Dockerfile stages instead of a floating image.
	if api.Build == nil || api.Build.Target != "runtime" {
		t.Errorf("api build target = %+v, want the Dockerfile runtime stage", api.Build)
	}
	if migrate.Build == nil || migrate.Build.Target != "migrate" {
		t.Errorf("migrate build target = %+v, want the Dockerfile migrate stage", migrate.Build)
	}

	// Secrets stay with the services that need them. The api and migrate
	// binaries construct the connection URL internally, so both receive the
	// POSTGRES_* components; the db container needs only the initdb trio.
	for _, name := range []string{"api", "migrate"} {
		service := doc.Services[name]
		for _, component := range []string{
			"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_USER",
			"POSTGRES_PASSWORD", "POSTGRES_DB", "POSTGRES_SSLMODE",
		} {
			if _, ok := service.Environment[component]; !ok {
				t.Errorf("%s does not receive %s; it must build the DSN from components", name, component)
			}
		}
	}
	for _, component := range []string{"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB"} {
		if _, ok := db.Environment[component]; !ok {
			t.Errorf("db does not receive %s", component)
		}
	}
	for _, component := range []string{"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_SSLMODE"} {
		if _, ok := db.Environment[component]; ok {
			t.Errorf("db receives %s; only api and migrate need connection components", component)
		}
	}
	for name, service := range doc.Services {
		for _, gone := range []string{"DOMAIN", "ACME_EMAIL"} {
			if _, ok := service.Environment[gone]; ok {
				t.Errorf("%s still receives %s; TLS is the host nginx's job", name, gone)
			}
		}
	}
}

func TestComposeImagesPinnedAndLogged(t *testing.T) {
	doc := loadCompose(t)

	for name, service := range doc.Services {
		// (b) No untagged or latest image references.
		if service.Image == "" {
			t.Errorf("%s has no image reference", name)
		} else if !isPinned(service.Image) {
			t.Errorf("%s image %q is not pinned to an explicit tag or digest", name, service.Image)
		}
		if strings.Contains(strings.ToLower(service.Image), ":latest") {
			t.Errorf("%s uses the floating latest tag: %q", name, service.Image)
		}

		// (c) Log rotation on every service.
		if service.Logging == nil {
			t.Errorf("%s has no logging configuration", name)
			continue
		}
		if service.Logging.Driver != "json-file" {
			t.Errorf("%s log driver = %q, want json-file", name, service.Logging.Driver)
		}
		if service.Logging.Options["max-size"] == "" || service.Logging.Options["max-file"] == "" {
			t.Errorf("%s logging options = %v, want max-size and max-file", name, service.Logging.Options)
		}
	}

	// Long-running services restart; the one-shot migration does not.
	for _, name := range []string{"db", "api"} {
		if restart := doc.Services[name].Restart; restart != "unless-stopped" {
			t.Errorf("%s restart = %q, want unless-stopped", name, restart)
		}
	}
	if restart := doc.Services["migrate"].Restart; restart == "unless-stopped" {
		t.Error("migrate is one-shot and must not use restart: unless-stopped")
	}
}

func TestComposeHealthAndDependencies(t *testing.T) {
	doc := loadCompose(t)
	db := requireService(t, doc, "db")
	api := requireService(t, doc, "api")
	migrate := requireService(t, doc, "migrate")

	// (d) Health checks for db and api.
	if db.Healthcheck == nil || len(db.Healthcheck.Test) == 0 {
		t.Fatal("db has no healthcheck")
	}
	if joined := strings.Join(db.Healthcheck.Test, " "); !strings.Contains(joined, "pg_isready") {
		t.Errorf("db healthcheck = %q, want pg_isready", joined)
	}
	if api.Healthcheck == nil || len(api.Healthcheck.Test) == 0 {
		t.Fatal("api has no healthcheck")
	}
	if joined := strings.Join(api.Healthcheck.Test, " "); !strings.Contains(joined, "/readyz") {
		t.Errorf("api healthcheck = %q, want the /readyz readiness probe", joined)
	}

	// (e) Ordered startup: db healthy -> migrate completes -> api starts.
	if got := migrate.DependsOn["db"].Condition; got != "service_healthy" {
		t.Errorf("migrate depends_on db = %q, want service_healthy", got)
	}
	if got := api.DependsOn["migrate"].Condition; got != "service_completed_successfully" {
		t.Errorf("api depends_on migrate = %q, want service_completed_successfully", got)
	}
	if got := api.DependsOn["db"].Condition; got != "service_healthy" {
		t.Errorf("api depends_on db = %q, want service_healthy", got)
	}
	if _, ok := api.DependsOn["caddy"]; ok {
		t.Error("api still depends_on caddy")
	}
}

func TestComposeResourceLimits(t *testing.T) {
	doc := loadCompose(t)

	// Explicit ceilings keep the stack from competing with nginx, Calibre and
	// the operator's other projects on the shared box. Ranges document the
	// intent rather than freezing one number forever.
	cases := []struct {
		service string
		minMem  int64
		maxMem  int64
	}{
		{service: "db", minMem: 512 << 20, maxMem: 1 << 30},
		{service: "api", minMem: 128 << 20, maxMem: 256 << 20},
		{service: "migrate", minMem: 64 << 20, maxMem: 256 << 20},
	}
	for _, tc := range cases {
		service := requireService(t, doc, tc.service)
		if service.Deploy == nil {
			t.Errorf("%s has no deploy.resources limits", tc.service)
			continue
		}
		limits := service.Deploy.Resources.Limits
		if limits.CPUs == "" {
			t.Errorf("%s has no CPU limit", tc.service)
		}
		if limits.Memory == "" {
			t.Errorf("%s has no memory limit", tc.service)
			continue
		}
		if _, err := strconv.ParseFloat(limits.CPUs, 64); err != nil {
			t.Errorf("%s cpus = %q, want a number", tc.service, limits.CPUs)
		}
		memory, err := parseMemoryBytes(limits.Memory)
		if err != nil {
			t.Errorf("%s memory limit = %q: %v", tc.service, limits.Memory, err)
			continue
		}
		if memory < tc.minMem || memory > tc.maxMem {
			t.Errorf("%s memory limit = %s, want between %d MiB and %d MiB",
				tc.service, limits.Memory, tc.minMem>>20, tc.maxMem>>20)
		}
	}

	// The database needs shared memory for parallel query; the image default
	// is only 64 MB.
	if db := requireService(t, doc, "db"); db.ShmSize == "" {
		t.Error("db has no shm_size; parallel query needs more than the 64 MB default")
	}
}

func TestComposePostgresTuningWired(t *testing.T) {
	doc := loadCompose(t)
	db := requireService(t, doc, "db")

	const (
		hostPath   = "deploy/postgres/99-small-vps.conf"
		configPath = "/etc/postgresql/99-small-vps.conf"
	)
	tuning := readDeployFile(t, filepath.Join("deploy", "postgres", "99-small-vps.conf"))

	mounted := false
	for _, volume := range db.Volumes {
		if strings.Contains(volume, hostPath) && strings.Contains(volume, configPath+":ro") {
			mounted = true
		}
	}
	if !mounted {
		t.Errorf("db volumes = %v, want a read-only mount of %s at %s", db.Volumes, hostPath, configPath)
	}
	if command := strings.Join(db.Command, " "); !strings.Contains(command, "config_file="+configPath) {
		t.Errorf("db command = %q, want config_file=%s", command, configPath)
	}

	// The wrapper must load the initdb-generated config so image defaults such
	// as listen_addresses = '*' survive.
	if !strings.Contains(tuning, "include = '/var/lib/postgresql/18/docker/postgresql.conf'") {
		t.Error("tuning file does not include the PostgreSQL 18 initdb-generated postgresql.conf")
	}

	for _, setting := range []string{
		"shared_buffers",
		"effective_cache_size",
		"work_mem",
		"maintenance_work_mem",
		"max_connections",
		"wal_buffers",
		"min_wal_size",
		"max_wal_size",
		"checkpoint_timeout",
		"checkpoint_completion_target",
	} {
		if !settingAssignment(setting).MatchString(tuning) {
			t.Errorf("tuning file does not set %s", setting)
		}
	}

	maxConnections, ok := settingInt(tuning, "max_connections")
	if !ok {
		t.Fatal("cannot parse max_connections from the tuning file")
	}
	pool := config.Default().Database
	if maxConnections < pool.MaxOpenConns {
		t.Errorf("max_connections = %d, want at least the API pool max %d", maxConnections, pool.MaxOpenConns)
	}
	if maxConnections > 30 {
		t.Errorf("max_connections = %d, want <= 30 on a small shared host", maxConnections)
	}
}

func TestComposeAPIRuntimeBounds(t *testing.T) {
	doc := loadCompose(t)
	api := requireService(t, doc, "api")

	for _, name := range []string{"GOMEMLIMIT", "GOMAXPROCS"} {
		value, ok := api.Environment[name]
		if !ok || value == nil || strings.TrimSpace(*value) == "" {
			t.Errorf("api environment has no %s; the Go runtime must stay inside the container limit", name)
		}
	}

	// The pool env defaults must match internal/config's defaults and fit
	// inside the documented PostgreSQL max_connections.
	defaults := config.Default().Database
	for name, want := range map[string]int{
		"DATABASE_MAX_OPEN_CONNS": defaults.MaxOpenConns,
		"DATABASE_MAX_IDLE_CONNS": defaults.MaxIdleConns,
	} {
		raw, ok := api.Environment[name]
		if !ok || raw == nil {
			t.Errorf("api environment has no %s", name)
			continue
		}
		value := *raw
		if literal := defaultLiteral(value); literal != "" {
			got, err := strconv.Atoi(literal)
			if err != nil || got != want {
				t.Errorf("api %s default = %q, want %d", name, value, want)
			}
		}
	}
}

func TestNoCaddyfileOrCaddyReferences(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", caddyfilePath)); !os.IsNotExist(err) {
		t.Errorf("Caddyfile must not exist (os.Stat err = %v)", err)
	}

	for name, content := range map[string]string{
		"compose.yaml": readDeployFile(t, "compose.yaml"),
		".env.example": readDeployFile(t, ".env.example"),
	} {
		if strings.Contains(strings.ToLower(content), "caddy") {
			t.Errorf("%s still references caddy; the host nginx is the front door", name)
		}
	}
}

func TestEnvExampleDocumentsRequiredVariables(t *testing.T) {
	example := readDeployFile(t, ".env.example")

	required := []string{
		"POSTGRES_HOST",
		"POSTGRES_PORT",
		"POSTGRES_USER",
		"POSTGRES_PASSWORD",
		"POSTGRES_DB",
		"POSTGRES_SSLMODE",
		"API_HOST_PORT",
		"PORT",
		"HTTP_MAX_BODY_BYTES",
		"AUTH_ARGON2_TIME",
		"AUTH_SESSION_TTL",
		"AUTH_RATE_LIMIT_MAX",
	}
	for _, name := range required {
		if !strings.Contains(example, name+"=") {
			t.Errorf(".env.example does not document %s", name)
		}
	}

	for _, gone := range []string{"DOMAIN=", "ACME_EMAIL="} {
		if strings.Contains(example, gone) {
			t.Errorf(".env.example still defines %s; the hostname belongs to the nginx snippet", gone)
		}
	}

	for _, insecure := range []string{"POSTGRES_PASSWORD=postgres", "POSTGRES_PASSWORD=password", "POSTGRES_PASSWORD=changeme"} {
		if strings.Contains(strings.ToLower(example), insecure) {
			t.Errorf(".env.example contains the insecure default %q", insecure)
		}
	}

	gitignore := readDeployFile(t, ".gitignore")
	if !strings.Contains(gitignore, ".env") || !strings.Contains(gitignore, "!.env.example") {
		t.Error(".gitignore must ignore .env/.env.* while keeping .env.example")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// settingAssignment matches a PostgreSQL assignment such as `work_mem = 4MB`.
func settingAssignment(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*=\s*\S+`)
}

// settingInt parses an integer PostgreSQL assignment from a tuning file.
func settingInt(content, name string) (int, bool) {
	pattern := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*=\s*(\d+)\s*(?:#.*)?$`)
	match := pattern.FindStringSubmatch(content)
	if match == nil {
		return 0, false
	}
	value, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return value, true
}

// defaultLiteral extracts the fallback from a `${VAR:-literal}` compose value.
func defaultLiteral(value string) string {
	index := strings.Index(value, ":-")
	if index < 0 {
		return ""
	}
	return strings.TrimSuffix(value[index+2:], "}")
}

// parseMemoryBytes converts a Compose memory value such as 768m or 1g to bytes.
func parseMemoryBytes(value string) (int64, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix     string
		multiplier int64
	}{
		{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30},
		{"kb", 1000}, {"mb", 1000 * 1000}, {"gb", 1000 * 1000 * 1000},
		{"k", 1 << 10}, {"m", 1 << 20}, {"g", 1 << 30},
	} {
		if strings.HasSuffix(normalized, unit.suffix) {
			normalized = strings.TrimSuffix(normalized, unit.suffix)
			multiplier = unit.multiplier
			break
		}
	}
	number, err := strconv.ParseFloat(normalized, 64)
	if err != nil {
		return 0, err
	}
	return int64(number * float64(multiplier)), nil
}
