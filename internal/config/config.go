// Package config loads and validates the process configuration from the
// environment (STACK_DECISIONS §7). Configuration is validated before the
// server starts so a misconfigured deployment fails fast instead of running
// with unsafe defaults.
//
// Secrets are never formatted into errors or logs. In particular, a missing or
// invalid database component is reported by variable name, not by value,
// because one of the values is a password.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variable names. They are exported so deployment tooling and
// documentation can refer to one canonical list.
const (
	// Database connection components. They are separate so a human never has
	// to hand-build a connection URL; DatabaseConfig.DSN constructs it.
	EnvDatabaseHost     = "POSTGRES_HOST"
	EnvDatabasePort     = "POSTGRES_PORT"
	EnvDatabaseUser     = "POSTGRES_USER"
	EnvDatabasePassword = "POSTGRES_PASSWORD"
	EnvDatabaseName     = "POSTGRES_DB"
	EnvDatabaseSSLMode  = "POSTGRES_SSLMODE"

	// Connection-pool bounds for the api process. They are deliberately below
	// PostgreSQL's max_connections (deploy/postgres/99-small-vps.conf) so the
	// application, migrations and admin sessions all fit on a small host.
	EnvDatabaseMaxOpenConns    = "DATABASE_MAX_OPEN_CONNS"
	EnvDatabaseMaxIdleConns    = "DATABASE_MAX_IDLE_CONNS"
	EnvDatabaseConnMaxLifetime = "DATABASE_CONN_MAX_LIFETIME"
	EnvDatabaseConnMaxIdleTime = "DATABASE_CONN_MAX_IDLE_TIME"

	EnvPort = "PORT"

	EnvHTTPReadHeaderTimeout  = "HTTP_READ_HEADER_TIMEOUT"
	EnvHTTPReadTimeout        = "HTTP_READ_TIMEOUT"
	EnvHTTPWriteTimeout       = "HTTP_WRITE_TIMEOUT"
	EnvHTTPIdleTimeout        = "HTTP_IDLE_TIMEOUT"
	EnvHTTPRequestTimeout     = "HTTP_REQUEST_TIMEOUT"
	EnvHTTPShutdownTimeout    = "HTTP_SHUTDOWN_TIMEOUT"
	EnvHTTPMaxBodyBytes       = "HTTP_MAX_BODY_BYTES"
	EnvHTTPCORSAllowedOrigins = "HTTP_CORS_ALLOWED_ORIGINS"

	// EnvHTTPTrustedProxies lists the peer CIDRs whose forwarding headers
	// (X-Forwarded-For, X-Real-IP) may be trusted. The default is loopback
	// only, because the host nginx is the sole proxy in front of the API.
	EnvHTTPTrustedProxies = "HTTP_TRUSTED_PROXIES"

	EnvArgon2Time       = "AUTH_ARGON2_TIME"
	EnvArgon2MemoryKiB  = "AUTH_ARGON2_MEMORY_KIB"
	EnvArgon2Threads    = "AUTH_ARGON2_THREADS"
	EnvArgon2KeyLength  = "AUTH_ARGON2_KEY_LENGTH"
	EnvArgon2SaltLength = "AUTH_ARGON2_SALT_LENGTH"

	EnvSessionTTL      = "AUTH_SESSION_TTL"
	EnvRateLimitMax    = "AUTH_RATE_LIMIT_MAX"
	EnvRateLimitWindow = "AUTH_RATE_LIMIT_WINDOW"
)

// Config is the validated process configuration.
type Config struct {
	Database               DatabaseConfig
	HTTP                   HTTPConfig
	Auth                   AuthConfig
	HTTPCORSAllowedOrigins []string
}

// DatabaseConfig holds the database connection components and the
// database/sql connection-pool bounds. The components are supplied separately
// by the operator; DSN is the only place that turns them into a URL. The pool
// bounds keep the api process from opening more connections than PostgreSQL is
// tuned to serve on a small shared VPS.
type DatabaseConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string

	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// HTTPConfig holds transport limits and timeouts.
type HTTPConfig struct {
	Port              int
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	RequestTimeout    time.Duration
	ShutdownTimeout   time.Duration
	MaxBodyBytes      int64

	// TrustedProxies are the peer networks allowed to supply the real client
	// IP through X-Forwarded-For or X-Real-IP. Requests from any other peer
	// use their transport address, so a direct caller cannot spoof the header.
	TrustedProxies []string
}

// AuthConfig holds password-hashing, session and auth rate-limit settings.
type AuthConfig struct {
	Argon2          Argon2Config
	SessionTTL      time.Duration
	RateLimitMax    int
	RateLimitWindow time.Duration
}

// Argon2Config holds the Argon2id cost parameters. They are configurable so a
// deployment can re-benchmark them on its own hardware
// (PLATFORM_DECISIONS §B). Defaults are documented in Default.
type Argon2Config struct {
	Time       uint32 // passes
	MemoryKiB  uint32 // memory cost in KiB
	Threads    uint8  // parallelism
	KeyLength  uint32 // derived key length in bytes
	SaltLength uint32 // random salt length in bytes
}

// Default returns the built-in defaults. The database host, user, password and
// name have no safe default and are therefore empty until LoadFrom validates
// them; the port and sslmode defaults are suitable for the private compose
// network.
//
// Argon2id defaults (time=3, memory=64 MiB, threads=4, key=32 B, salt=16 B)
// target interactive login latency on a small VPS. They are deliberately
// configurable; see the auth package for the hash encoding and the benchmark
// used to justify them.
func Default() Config {
	return Config{
		// Ten open connections is well below the 25 max_connections in the
		// shipped PostgreSQL tuning file, leaving room for migrations, the
		// healthcheck and an operator psql session.
		Database: DatabaseConfig{
			Port:            5432,
			SSLMode:         "disable",
			MaxOpenConns:    10,
			MaxIdleConns:    5,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 15 * time.Minute,
		},
		HTTP: HTTPConfig{
			Port:              8080,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
			RequestTimeout:    10 * time.Second,
			ShutdownTimeout:   10 * time.Second,
			MaxBodyBytes:      1 << 20, // 1 MiB
			// The host nginx proxies over loopback; nothing else is trusted
			// by default.
			TrustedProxies: []string{"127.0.0.0/8", "::1/128"},
		},
		Auth: AuthConfig{
			Argon2: Argon2Config{
				Time:       3,
				MemoryKiB:  64 * 1024, // 64 MiB
				Threads:    4,
				KeyLength:  32,
				SaltLength: 16,
			},
			SessionTTL:      30 * 24 * time.Hour,
			RateLimitMax:    10,
			RateLimitWindow: time.Minute,
		},
		HTTPCORSAllowedOrigins: []string{},
	}
}

// LookupFunc is os.LookupEnv's shape, injected so tests do not mutate the
// process environment.
type LookupFunc func(key string) (string, bool)

// Load reads the process environment and validates it.
func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

// LoadFrom builds a Config from lookup, applying defaults and collecting all
// validation errors so an operator can fix several problems at once.
func LoadFrom(lookup LookupFunc) (Config, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}

	cfg := Default()
	var problems []error
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	// Database connection components are mandatory and never echoed on
	// failure. A password is assigned verbatim: trimming it would silently
	// change a legitimate secret.
	cfg.Database.Host, _ = nonEmpty(lookup, EnvDatabaseHost)
	cfg.Database.User, _ = nonEmpty(lookup, EnvDatabaseUser)
	if raw, ok := lookup(EnvDatabasePassword); ok {
		cfg.Database.Password = raw
	}
	cfg.Database.Name, _ = nonEmpty(lookup, EnvDatabaseName)
	if raw, ok := nonEmpty(lookup, EnvDatabaseSSLMode); ok {
		cfg.Database.SSLMode = strings.ToLower(raw)
	}
	cfg.Database.Port = lookupInt(lookup, EnvDatabasePort, cfg.Database.Port, add)
	if err := cfg.Database.Validate(); err != nil {
		problems = append(problems, err)
	}

	cfg.Database.MaxOpenConns = lookupInt(lookup, EnvDatabaseMaxOpenConns, cfg.Database.MaxOpenConns, add)
	cfg.Database.MaxIdleConns = lookupInt(lookup, EnvDatabaseMaxIdleConns, cfg.Database.MaxIdleConns, add)
	cfg.Database.ConnMaxLifetime = lookupDuration(lookup, EnvDatabaseConnMaxLifetime, cfg.Database.ConnMaxLifetime, add)
	cfg.Database.ConnMaxIdleTime = lookupDuration(lookup, EnvDatabaseConnMaxIdleTime, cfg.Database.ConnMaxIdleTime, add)

	if cfg.Database.MaxOpenConns < 1 || cfg.Database.MaxOpenConns > 100 {
		add("%s must be between 1 and 100", EnvDatabaseMaxOpenConns)
	}
	if cfg.Database.MaxIdleConns < 0 || cfg.Database.MaxIdleConns > cfg.Database.MaxOpenConns {
		add("%s must be between 0 and %s", EnvDatabaseMaxIdleConns, EnvDatabaseMaxOpenConns)
	}
	if cfg.Database.ConnMaxLifetime < time.Minute || cfg.Database.ConnMaxLifetime > 24*time.Hour {
		add("%s must be between 1m and 24h", EnvDatabaseConnMaxLifetime)
	}
	if cfg.Database.ConnMaxIdleTime < time.Minute || cfg.Database.ConnMaxIdleTime > 24*time.Hour {
		add("%s must be between 1m and 24h", EnvDatabaseConnMaxIdleTime)
	}

	cfg.HTTP.Port = lookupInt(lookup, EnvPort, cfg.HTTP.Port, add)
	if cfg.HTTP.Port < 1 || cfg.HTTP.Port > 65535 {
		add("%s must be between 1 and 65535", EnvPort)
	}

	cfg.HTTP.ReadHeaderTimeout = lookupDuration(lookup, EnvHTTPReadHeaderTimeout, cfg.HTTP.ReadHeaderTimeout, add)
	cfg.HTTP.ReadTimeout = lookupDuration(lookup, EnvHTTPReadTimeout, cfg.HTTP.ReadTimeout, add)
	cfg.HTTP.WriteTimeout = lookupDuration(lookup, EnvHTTPWriteTimeout, cfg.HTTP.WriteTimeout, add)
	cfg.HTTP.IdleTimeout = lookupDuration(lookup, EnvHTTPIdleTimeout, cfg.HTTP.IdleTimeout, add)
	cfg.HTTP.RequestTimeout = lookupDuration(lookup, EnvHTTPRequestTimeout, cfg.HTTP.RequestTimeout, add)
	cfg.HTTP.ShutdownTimeout = lookupDuration(lookup, EnvHTTPShutdownTimeout, cfg.HTTP.ShutdownTimeout, add)

	for _, entry := range []struct {
		name  string
		value time.Duration
	}{
		{EnvHTTPReadHeaderTimeout, cfg.HTTP.ReadHeaderTimeout},
		{EnvHTTPReadTimeout, cfg.HTTP.ReadTimeout},
		{EnvHTTPWriteTimeout, cfg.HTTP.WriteTimeout},
		{EnvHTTPIdleTimeout, cfg.HTTP.IdleTimeout},
		{EnvHTTPRequestTimeout, cfg.HTTP.RequestTimeout},
		{EnvHTTPShutdownTimeout, cfg.HTTP.ShutdownTimeout},
	} {
		if entry.value <= 0 {
			add("%s must be positive", entry.name)
		}
	}

	cfg.HTTP.MaxBodyBytes = int64(lookupInt64(lookup, EnvHTTPMaxBodyBytes, cfg.HTTP.MaxBodyBytes, add))
	if cfg.HTTP.MaxBodyBytes < 1024 || cfg.HTTP.MaxBodyBytes > 16<<20 {
		add("%s must be between 1024 and %d", EnvHTTPMaxBodyBytes, 16<<20)
	}

	cfg.HTTP.TrustedProxies = lookupCSV(lookup, EnvHTTPTrustedProxies, cfg.HTTP.TrustedProxies)
	for _, cidr := range cfg.HTTP.TrustedProxies {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			add("%s contains invalid CIDR %q", EnvHTTPTrustedProxies, cidr)
		}
	}

	corsOrigins, err := lookupCORSOrigins(lookup, EnvHTTPCORSAllowedOrigins)
	if err != nil {
		problems = append(problems, err)
	}
	cfg.HTTPCORSAllowedOrigins = corsOrigins

	cfg.Auth.Argon2.Time = uint32(lookupInt(lookup, EnvArgon2Time, int(cfg.Auth.Argon2.Time), add))
	cfg.Auth.Argon2.MemoryKiB = uint32(lookupInt64(lookup, EnvArgon2MemoryKiB, int64(cfg.Auth.Argon2.MemoryKiB), add))
	cfg.Auth.Argon2.Threads = uint8(lookupInt(lookup, EnvArgon2Threads, int(cfg.Auth.Argon2.Threads), add))
	cfg.Auth.Argon2.KeyLength = uint32(lookupInt(lookup, EnvArgon2KeyLength, int(cfg.Auth.Argon2.KeyLength), add))
	cfg.Auth.Argon2.SaltLength = uint32(lookupInt(lookup, EnvArgon2SaltLength, int(cfg.Auth.Argon2.SaltLength), add))

	if cfg.Auth.Argon2.Time < 1 || cfg.Auth.Argon2.Time > 10 {
		add("%s must be between 1 and 10", EnvArgon2Time)
	}
	if cfg.Auth.Argon2.MemoryKiB < 8*1024 || cfg.Auth.Argon2.MemoryKiB > 1024*1024 {
		add("%s must be between 8192 and 1048576", EnvArgon2MemoryKiB)
	}
	if cfg.Auth.Argon2.Threads < 1 || cfg.Auth.Argon2.Threads > 64 {
		add("%s must be between 1 and 64", EnvArgon2Threads)
	}
	if cfg.Auth.Argon2.KeyLength < 16 || cfg.Auth.Argon2.KeyLength > 128 {
		add("%s must be between 16 and 128", EnvArgon2KeyLength)
	}
	if cfg.Auth.Argon2.SaltLength < 8 || cfg.Auth.Argon2.SaltLength > 64 {
		add("%s must be between 8 and 64", EnvArgon2SaltLength)
	}

	cfg.Auth.SessionTTL = lookupDuration(lookup, EnvSessionTTL, cfg.Auth.SessionTTL, add)
	if cfg.Auth.SessionTTL < time.Minute || cfg.Auth.SessionTTL > 8760*time.Hour {
		add("%s must be between 1m and 8760h", EnvSessionTTL)
	}

	cfg.Auth.RateLimitMax = lookupInt(lookup, EnvRateLimitMax, cfg.Auth.RateLimitMax, add)
	if cfg.Auth.RateLimitMax < 1 || cfg.Auth.RateLimitMax > 100000 {
		add("%s must be between 1 and 100000", EnvRateLimitMax)
	}

	cfg.Auth.RateLimitWindow = lookupDuration(lookup, EnvRateLimitWindow, cfg.Auth.RateLimitWindow, add)
	if cfg.Auth.RateLimitWindow < time.Second || cfg.Auth.RateLimitWindow > 24*time.Hour {
		add("%s must be between 1s and 24h", EnvRateLimitWindow)
	}

	if len(problems) > 0 {
		return Config{}, errors.Join(problems...)
	}
	return cfg, nil
}

func nonEmpty(lookup LookupFunc, key string) (string, bool) {
	raw, ok := lookup(key)
	if !ok {
		return "", false
	}
	trimmed := strings.TrimSpace(raw)
	return trimmed, trimmed != ""
}

func lookupInt(lookup LookupFunc, key string, fallback int, add func(string, ...any)) int {
	raw, ok := nonEmpty(lookup, key)
	if !ok {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		add("%s must be an integer", key)
		return fallback
	}
	return value
}

func lookupInt64(lookup LookupFunc, key string, fallback int64, add func(string, ...any)) int64 {
	raw, ok := nonEmpty(lookup, key)
	if !ok {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		add("%s must be an integer", key)
		return fallback
	}
	return value
}

func lookupDuration(lookup LookupFunc, key string, fallback time.Duration, add func(string, ...any)) time.Duration {
	raw, ok := nonEmpty(lookup, key)
	if !ok {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		add("%s must be a duration such as 10s or 1h", key)
		return fallback
	}
	return value
}

// lookupCSV parses a comma-separated list, trimming each entry. An unset or
// empty value keeps the fallback; an explicitly empty list trusts no proxies.
func lookupCSV(lookup LookupFunc, key string, fallback []string) []string {
	raw, ok := nonEmpty(lookup, key)
	if !ok {
		return fallback
	}
	values := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

func lookupCORSOrigins(lookup LookupFunc, key string) ([]string, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}

	origins := make([]string, 0)
	seen := make(map[string]struct{})
	var problems []error
	for _, part := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		origin, err := normalizeOrigin(entry)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s contains invalid origin %q: %w", key, entry, err))
			continue
		}
		if _, exists := seen[origin]; exists {
			problems = append(problems, fmt.Errorf("%s contains duplicate origin %q after normalization", key, origin))
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins, errors.Join(problems...)
}

func normalizeOrigin(value string) (string, error) {
	if strings.Contains(value, "*") {
		return "", errors.New("wildcards are not allowed")
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", errors.New("must be a valid http or https origin")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("scheme must be http or https")
	}
	if u.Opaque != "" || u.User != nil || u.Host == "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") {
		return "", errors.New("must contain only a scheme and host, with no path, query or fragment")
	}

	hostname := u.Hostname()
	if hostname == "" {
		return "", errors.New("host must not be empty")
	}
	if net.ParseIP(hostname) == nil && !validOriginHostname(hostname) {
		return "", errors.New("host is malformed")
	}

	port, hasPort, err := originPort(u.Host)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(hostname)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if hasPort {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}

func originPort(authority string) (string, bool, error) {
	var port string
	if strings.HasPrefix(authority, "[") {
		close := strings.IndexByte(authority, ']')
		if close < 0 {
			return "", false, errors.New("host is malformed")
		}
		suffix := authority[close+1:]
		if suffix == "" {
			return "", false, nil
		}
		if !strings.HasPrefix(suffix, ":") {
			return "", false, errors.New("host is malformed")
		}
		port = suffix[1:]
	} else {
		if strings.Count(authority, ":") > 1 {
			return "", false, errors.New("IPv6 hosts must be enclosed in brackets")
		}
		if index := strings.LastIndexByte(authority, ':'); index >= 0 {
			port = authority[index+1:]
		}
	}
	if port == "" {
		if strings.HasSuffix(authority, ":") {
			return "", false, errors.New("port must not be empty")
		}
		return "", false, nil
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 0 || value > 65535 {
		return "", false, errors.New("port must be a number between 0 and 65535")
	}
	return port, true, nil
}

func validOriginHostname(hostname string) bool {
	if strings.HasSuffix(hostname, ".") {
		hostname = strings.TrimSuffix(hostname, ".")
	}
	if hostname == "" {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-') {
				return false
			}
		}
	}
	return true
}

// Validate reports missing or malformed connection components. Every error
// names the environment variable to fix and never includes its value.
func (c DatabaseConfig) Validate() error {
	var problems []error
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}
	if strings.TrimSpace(c.Host) == "" {
		add("%s is required", EnvDatabaseHost)
	}
	if c.Port < 1 || c.Port > 65535 {
		add("%s must be between 1 and 65535", EnvDatabasePort)
	}
	if strings.TrimSpace(c.User) == "" {
		add("%s is required", EnvDatabaseUser)
	}
	if strings.TrimSpace(c.Password) == "" {
		add("%s is required", EnvDatabasePassword)
	}
	if strings.TrimSpace(c.Name) == "" {
		add("%s is required", EnvDatabaseName)
	}
	switch strings.ToLower(strings.TrimSpace(c.SSLMode)) {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		add("%s must be one of disable, allow, prefer, require, verify-ca or verify-full", EnvDatabaseSSLMode)
	}
	return errors.Join(problems...)
}

// DSN is the single place that turns the connection components into a
// PostgreSQL URL. net/url percent-encodes the user and password, so reserved
// characters such as @ : / ? # % & and space are safe. Callers must not accept
// a user-supplied URL.
func (c DatabaseConfig) DSN() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(strings.TrimSpace(c.Host), strconv.Itoa(c.Port)),
		Path:   "/" + strings.TrimSpace(c.Name),
	}
	query := url.Values{}
	query.Set("sslmode", strings.ToLower(strings.TrimSpace(c.SSLMode)))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// Target returns a log-safe host:port/database description without credentials.
func (c DatabaseConfig) Target() string {
	host := strings.TrimSpace(c.Host)
	if host == "" {
		return "postgres"
	}
	target := net.JoinHostPort(host, strconv.Itoa(c.Port))
	if name := strings.TrimSpace(c.Name); name != "" {
		target += "/" + name
	}
	return target
}

// RedactError removes the constructed DSN, its userinfo and the password from
// an error message so driver errors can be reported without leaking a secret.
func (c DatabaseConfig) RedactError(err error, dsn string) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if dsn != "" {
		message = strings.ReplaceAll(message, dsn, c.Target())
		if parsed, parseErr := url.Parse(dsn); parseErr == nil && parsed.User != nil {
			message = strings.ReplaceAll(message, parsed.User.String(), "[redacted]")
		}
	}
	if c.Password != "" {
		message = strings.ReplaceAll(message, c.Password, "[redacted]")
	}
	return message
}
