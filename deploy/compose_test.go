package deploy

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// composeDocument is the subset of the Compose schema these static guards
// inspect. It is intentionally not a full schema model.
type composeDocument struct {
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]yaml.Node      `yaml:"volumes"`
	Networks map[string]composeNetwork `yaml:"networks"`
}

type composeService struct {
	Image       string    `yaml:"image"`
	Build       *build    `yaml:"build"`
	Ports       []string  `yaml:"ports"`
	Expose      []string  `yaml:"expose"`
	Restart     string    `yaml:"restart"`
	Volumes     []string  `yaml:"volumes"`
	Networks    []string  `yaml:"networks"`
	Environment envMap    `yaml:"environment"`
	Healthcheck *health   `yaml:"healthcheck"`
	DependsOn   dependsOn `yaml:"depends_on"`
	Logging     *logging  `yaml:"logging"`
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
	caddy := requireService(t, doc, "caddy")

	// (a) PostgreSQL must not be reachable from the host.
	if len(db.Ports) != 0 {
		t.Errorf("db publishes host ports %v; PostgreSQL must stay on the private network", db.Ports)
	}
	if !contains(db.Expose, "5432") {
		t.Errorf("db exposes %v, want the internal 5432 port", db.Expose)
	}
	for _, service := range []string{"db", "api", "migrate"} {
		if ports := doc.Services[service].Ports; service != "db" && len(ports) != 0 {
			t.Errorf("%s publishes host ports %v; only caddy may publish ports", service, ports)
		}
	}

	// Only Caddy publishes ports, and only 80/443.
	if len(caddy.Ports) == 0 {
		t.Fatal("caddy publishes no ports; HTTPS would be unreachable")
	}
	if !contains(caddy.Ports, "443:443") || !contains(caddy.Ports, "80:80") {
		t.Errorf("caddy ports = %v, want 80:80 and 443:443", caddy.Ports)
	}

	// Internal database network and a separate public-edge network.
	backend, ok := doc.Networks["backend"]
	if !ok {
		t.Fatal("compose.yaml has no backend network")
	}
	if !backend.Internal {
		t.Error("backend network must be internal so db has no outbound route")
	}
	if _, ok := doc.Networks["frontend"]; !ok {
		t.Error("compose.yaml has no frontend network")
	}
	if !contains(db.Networks, "backend") || contains(db.Networks, "frontend") {
		t.Errorf("db networks = %v, want backend only", db.Networks)
	}
	if !contains(api.Networks, "backend") || !contains(api.Networks, "frontend") {
		t.Errorf("api networks = %v, want backend and frontend", api.Networks)
	}
	if !contains(caddy.Networks, "frontend") || contains(caddy.Networks, "backend") {
		t.Errorf("caddy networks = %v, want frontend only", caddy.Networks)
	}

	// (f) Durable PostgreSQL data on a named volume (PG18 path).
	if _, ok := doc.Volumes["pgdata"]; !ok {
		t.Error("compose.yaml declares no pgdata volume")
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

	// Secrets stay with the service that needs them.
	if _, ok := caddy.Environment["DATABASE_URL"]; ok {
		t.Error("caddy must not receive DATABASE_URL")
	}
	if _, ok := caddy.Environment["POSTGRES_PASSWORD"]; ok {
		t.Error("caddy must not receive POSTGRES_PASSWORD")
	}
	if _, ok := api.Environment["POSTGRES_PASSWORD"]; ok {
		t.Error("api must not receive POSTGRES_PASSWORD; it needs only DATABASE_URL")
	}
	if _, ok := caddy.Environment["DOMAIN"]; !ok {
		t.Error("caddy does not receive DOMAIN")
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
	for _, name := range []string{"db", "api", "caddy"} {
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
	if got := doc.Services["caddy"].DependsOn["api"].Condition; got != "service_healthy" {
		t.Errorf("caddy depends_on api = %q, want service_healthy", got)
	}
}

func TestCaddyfileProxiesToAPIService(t *testing.T) {
	caddyfile := readDeployFile(t, "Caddyfile")

	if !strings.Contains(caddyfile, "reverse_proxy api:") {
		t.Error("Caddyfile does not reverse_proxy to the compose service `api`")
	}
	if !strings.Contains(caddyfile, "{$DOMAIN") {
		t.Error("Caddyfile does not take the site address from {$DOMAIN}")
	}
	if !strings.Contains(caddyfile, "encode ") {
		t.Error("Caddyfile does not enable response compression")
	}
	if !strings.Contains(caddyfile, "Strict-Transport-Security") {
		t.Error("Caddyfile is missing HSTS and the rest of the security headers")
	}
}

func TestEnvExampleDocumentsRequiredVariables(t *testing.T) {
	example := readDeployFile(t, ".env.example")

	required := []string{
		"POSTGRES_USER",
		"POSTGRES_PASSWORD",
		"POSTGRES_DB",
		"DATABASE_URL",
		"DOMAIN",
		"ACME_EMAIL",
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
