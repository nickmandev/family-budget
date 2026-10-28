package config

import (
	"strings"
	"testing"
	"time"
)

func lookupFrom(values map[string]string) LookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		EnvDatabaseURL: "postgres://user:pass@localhost:5432/family_budget?sslmode=disable",
	}
}

func TestLoadFromDefaults(t *testing.T) {
	cfg, err := LoadFrom(lookupFrom(validEnvironment()))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	defaults := Default()
	if cfg.HTTP.Port != defaults.HTTP.Port {
		t.Errorf("port = %d, want %d", cfg.HTTP.Port, defaults.HTTP.Port)
	}
	if cfg.HTTP.MaxBodyBytes != defaults.HTTP.MaxBodyBytes {
		t.Errorf("max body = %d, want %d", cfg.HTTP.MaxBodyBytes, defaults.HTTP.MaxBodyBytes)
	}
	if cfg.Auth.Argon2 != defaults.Auth.Argon2 {
		t.Errorf("argon2 = %+v, want %+v", cfg.Auth.Argon2, defaults.Auth.Argon2)
	}
	if cfg.Auth.SessionTTL != defaults.Auth.SessionTTL {
		t.Errorf("session ttl = %v, want %v", cfg.Auth.SessionTTL, defaults.Auth.SessionTTL)
	}
	if cfg.Auth.RateLimitMax != defaults.Auth.RateLimitMax {
		t.Errorf("rate limit max = %d, want %d", cfg.Auth.RateLimitMax, defaults.Auth.RateLimitMax)
	}
	if cfg.Database != defaults.Database {
		t.Errorf("database pool = %+v, want %+v", cfg.Database, defaults.Database)
	}
}

func TestLoadFromOverrides(t *testing.T) {
	environment := validEnvironment()
	environment[EnvPort] = "9090"
	environment[EnvHTTPRequestTimeout] = "3s"
	environment[EnvHTTPMaxBodyBytes] = "2048"
	environment[EnvArgon2Time] = "5"
	environment[EnvArgon2MemoryKiB] = "131072"
	environment[EnvArgon2Threads] = "2"
	environment[EnvArgon2KeyLength] = "64"
	environment[EnvArgon2SaltLength] = "24"
	environment[EnvSessionTTL] = "24h"
	environment[EnvRateLimitMax] = "5"
	environment[EnvRateLimitWindow] = "2m"
	environment[EnvDatabaseMaxOpenConns] = "7"
	environment[EnvDatabaseMaxIdleConns] = "3"
	environment[EnvDatabaseConnMaxLifetime] = "30m"
	environment[EnvDatabaseConnMaxIdleTime] = "5m"

	cfg, err := LoadFrom(lookupFrom(environment))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.HTTP.Port != 9090 {
		t.Errorf("port = %d, want 9090", cfg.HTTP.Port)
	}
	if cfg.HTTP.RequestTimeout != 3*time.Second {
		t.Errorf("request timeout = %v, want 3s", cfg.HTTP.RequestTimeout)
	}
	if cfg.HTTP.MaxBodyBytes != 2048 {
		t.Errorf("max body = %d, want 2048", cfg.HTTP.MaxBodyBytes)
	}
	if cfg.Auth.Argon2.Time != 5 || cfg.Auth.Argon2.MemoryKiB != 131072 ||
		cfg.Auth.Argon2.Threads != 2 || cfg.Auth.Argon2.KeyLength != 64 ||
		cfg.Auth.Argon2.SaltLength != 24 {
		t.Errorf("argon2 = %+v", cfg.Auth.Argon2)
	}
	if cfg.Auth.SessionTTL != 24*time.Hour {
		t.Errorf("session ttl = %v, want 24h", cfg.Auth.SessionTTL)
	}
	if cfg.Auth.RateLimitMax != 5 || cfg.Auth.RateLimitWindow != 2*time.Minute {
		t.Errorf("rate limit = %d/%v", cfg.Auth.RateLimitMax, cfg.Auth.RateLimitWindow)
	}
	wantPool := DatabaseConfig{MaxOpenConns: 7, MaxIdleConns: 3, ConnMaxLifetime: 30 * time.Minute, ConnMaxIdleTime: 5 * time.Minute}
	if cfg.Database != wantPool {
		t.Errorf("database pool = %+v, want %+v", cfg.Database, wantPool)
	}
}

func TestLoadFromRejectsInvalidEnvironment(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
		remove   bool
	}{
		{name: "missing database url", remove: true},
		{name: "database url scheme", variable: EnvDatabaseURL, value: "mysql://user:pass@localhost/db"},
		{name: "database url host", variable: EnvDatabaseURL, value: "postgres:///db"},
		{name: "database url database", variable: EnvDatabaseURL, value: "postgres://localhost"},
		{name: "port zero", variable: EnvPort, value: "0"},
		{name: "port too high", variable: EnvPort, value: "70000"},
		{name: "port not a number", variable: EnvPort, value: "http"},
		{name: "body too small", variable: EnvHTTPMaxBodyBytes, value: "10"},
		{name: "body not a number", variable: EnvHTTPMaxBodyBytes, value: "big"},
		{name: "request timeout not a duration", variable: EnvHTTPRequestTimeout, value: "soon"},
		{name: "request timeout negative", variable: EnvHTTPRequestTimeout, value: "-1s"},
		{name: "argon2 time too low", variable: EnvArgon2Time, value: "0"},
		{name: "argon2 time too high", variable: EnvArgon2Time, value: "99"},
		{name: "argon2 memory too low", variable: EnvArgon2MemoryKiB, value: "1024"},
		{name: "argon2 threads zero", variable: EnvArgon2Threads, value: "0"},
		{name: "argon2 key too short", variable: EnvArgon2KeyLength, value: "8"},
		{name: "argon2 salt too short", variable: EnvArgon2SaltLength, value: "1"},
		{name: "session ttl zero", variable: EnvSessionTTL, value: "0s"},
		{name: "session ttl negative", variable: EnvSessionTTL, value: "-1h"},
		{name: "rate limit zero", variable: EnvRateLimitMax, value: "0"},
		{name: "rate window zero", variable: EnvRateLimitWindow, value: "0s"},
		{name: "pool open zero", variable: EnvDatabaseMaxOpenConns, value: "0"},
		{name: "pool open too high", variable: EnvDatabaseMaxOpenConns, value: "101"},
		{name: "pool idle negative", variable: EnvDatabaseMaxIdleConns, value: "-1"},
		{name: "pool idle above open", variable: EnvDatabaseMaxIdleConns, value: "11"},
		{name: "pool lifetime too short", variable: EnvDatabaseConnMaxLifetime, value: "30s"},
		{name: "pool lifetime not a duration", variable: EnvDatabaseConnMaxLifetime, value: "forever"},
		{name: "pool idle time too short", variable: EnvDatabaseConnMaxIdleTime, value: "30s"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := validEnvironment()
			if test.remove {
				delete(environment, EnvDatabaseURL)
			} else {
				environment[test.variable] = test.value
			}
			_, err := LoadFrom(lookupFrom(environment))
			if err == nil {
				t.Fatalf("LoadFrom accepted invalid config %s=%q", test.variable, test.value)
			}
		})
	}
}

func TestLoadFromHTTPTimeouts(t *testing.T) {
	environment := validEnvironment()
	environment[EnvHTTPReadHeaderTimeout] = "1s"
	environment[EnvHTTPReadTimeout] = "2s"
	environment[EnvHTTPWriteTimeout] = "3s"
	environment[EnvHTTPIdleTimeout] = "4s"
	environment[EnvHTTPShutdownTimeout] = "5s"

	cfg, err := LoadFrom(lookupFrom(environment))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.HTTP.ReadHeaderTimeout != time.Second ||
		cfg.HTTP.ReadTimeout != 2*time.Second ||
		cfg.HTTP.WriteTimeout != 3*time.Second ||
		cfg.HTTP.IdleTimeout != 4*time.Second ||
		cfg.HTTP.ShutdownTimeout != 5*time.Second {
		t.Fatalf("http timeouts = %+v", cfg.HTTP)
	}
}

func TestLoadFromDoesNotEchoSecrets(t *testing.T) {
	environment := validEnvironment()
	environment[EnvDatabaseURL] = "mysql://user:super-secret@localhost/db"

	_, err := LoadFrom(lookupFrom(environment))
	if err == nil {
		t.Fatal("LoadFrom accepted a non-postgres DSN")
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("validation error leaked the DSN secret: %v", err)
	}
}

func TestLoadFromReportsMultipleProblems(t *testing.T) {
	environment := validEnvironment()
	environment[EnvPort] = "0"
	environment[EnvSessionTTL] = "0s"

	_, err := LoadFrom(lookupFrom(environment))
	if err == nil {
		t.Fatal("LoadFrom accepted invalid config")
	}
	for _, variable := range []string{EnvPort, EnvSessionTTL} {
		if !strings.Contains(err.Error(), variable) {
			t.Errorf("error %q does not mention %s", err, variable)
		}
	}
}
