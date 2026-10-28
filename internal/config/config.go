// Package config loads and validates the process configuration from the
// environment (STACK_DECISIONS §7). Configuration is validated before the
// server starts so a misconfigured deployment fails fast instead of running
// with unsafe defaults.
//
// Secrets are never formatted into errors or logs. In particular, a malformed
// DATABASE_URL is reported by variable name, not by value, because the value
// may contain a password.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variable names. They are exported so deployment tooling and
// documentation can refer to one canonical list.
const (
	EnvDatabaseURL = "DATABASE_URL"

	// Connection-pool bounds for the api process. They are deliberately below
	// PostgreSQL's max_connections (deploy/postgres/99-small-vps.conf) so the
	// application, migrations and admin sessions all fit on a small host.
	EnvDatabaseMaxOpenConns    = "DATABASE_MAX_OPEN_CONNS"
	EnvDatabaseMaxIdleConns    = "DATABASE_MAX_IDLE_CONNS"
	EnvDatabaseConnMaxLifetime = "DATABASE_CONN_MAX_LIFETIME"
	EnvDatabaseConnMaxIdleTime = "DATABASE_CONN_MAX_IDLE_TIME"

	EnvPort = "PORT"

	EnvHTTPReadHeaderTimeout = "HTTP_READ_HEADER_TIMEOUT"
	EnvHTTPReadTimeout       = "HTTP_READ_TIMEOUT"
	EnvHTTPWriteTimeout      = "HTTP_WRITE_TIMEOUT"
	EnvHTTPIdleTimeout       = "HTTP_IDLE_TIMEOUT"
	EnvHTTPRequestTimeout    = "HTTP_REQUEST_TIMEOUT"
	EnvHTTPShutdownTimeout   = "HTTP_SHUTDOWN_TIMEOUT"
	EnvHTTPMaxBodyBytes      = "HTTP_MAX_BODY_BYTES"

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
	// DatabaseURL is the PostgreSQL DSN. It is required for the server binary
	// and must be a postgres:// or postgresql:// URL.
	DatabaseURL string
	Database    DatabaseConfig
	HTTP        HTTPConfig
	Auth        AuthConfig
}

// DatabaseConfig holds the database/sql connection-pool bounds. They keep the
// api process from opening more connections than PostgreSQL is tuned to serve
// on a small shared VPS.
type DatabaseConfig struct {
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

// Default returns the built-in defaults. DATABASE_URL has no safe default and
// is therefore empty until LoadFrom validates it.
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

	// DatabaseURL is mandatory and never echoed on failure.
	dsn, ok := nonEmpty(lookup, EnvDatabaseURL)
	if !ok {
		add("%s is required", EnvDatabaseURL)
	} else if err := validateDatabaseURL(dsn); err != nil {
		add("%s is invalid: %v", EnvDatabaseURL, err)
	} else {
		cfg.DatabaseURL = dsn
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

// validateDatabaseURL checks the DSN shape without ever including the value in
// the returned error.
func validateDatabaseURL(dsn string) error {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return errors.New("not a valid URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "postgres", "postgresql":
	default:
		return errors.New("scheme must be postgres or postgresql")
	}
	if parsed.Host == "" {
		return errors.New("host is required")
	}
	if strings.TrimPrefix(parsed.Path, "/") == "" {
		return errors.New("database name is required")
	}
	return nil
}
