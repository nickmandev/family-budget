package config

import (
	"errors"
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
		EnvDatabaseHost:     "localhost",
		EnvDatabasePort:     "5432",
		EnvDatabaseUser:     "user",
		EnvDatabasePassword: "pass",
		EnvDatabaseName:     "family_budget",
		EnvDatabaseSSLMode:  "disable",
	}
}

func validDatabaseConfig() DatabaseConfig {
	return DatabaseConfig{
		Host:     "localhost",
		Port:     5432,
		User:     "user",
		Password: "pass",
		Name:     "family_budget",
		SSLMode:  "disable",
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

	want := defaults.Database
	want.Host = "localhost"
	want.User = "user"
	want.Password = "pass"
	want.Name = "family_budget"
	want.Port = 5432
	want.SSLMode = "disable"
	if cfg.Database != want {
		t.Errorf("database = %+v, want %+v", cfg.Database, want)
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
	environment[EnvDatabaseHost] = "db.example"
	environment[EnvDatabasePort] = "6543"
	environment[EnvDatabaseUser] = "budget"
	environment[EnvDatabasePassword] = "s3cret"
	environment[EnvDatabaseName] = "budget_db"
	environment[EnvDatabaseSSLMode] = "require"
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
	wantPool := DatabaseConfig{
		Host: "db.example", Port: 6543, User: "budget", Password: "s3cret",
		Name: "budget_db", SSLMode: "require",
		MaxOpenConns: 7, MaxIdleConns: 3,
		ConnMaxLifetime: 30 * time.Minute, ConnMaxIdleTime: 5 * time.Minute,
	}
	if cfg.Database != wantPool {
		t.Errorf("database = %+v, want %+v", cfg.Database, wantPool)
	}
}

func TestLoadFromRejectsInvalidEnvironment(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
		remove   []string
	}{
		{name: "missing host", remove: []string{EnvDatabaseHost}},
		{name: "missing user", remove: []string{EnvDatabaseUser}},
		{name: "missing password", remove: []string{EnvDatabasePassword}},
		{name: "missing database", remove: []string{EnvDatabaseName}},
		{name: "port zero", variable: EnvDatabasePort, value: "0"},
		{name: "port too high", variable: EnvDatabasePort, value: "70000"},
		{name: "port not a number", variable: EnvDatabasePort, value: "http"},
		{name: "sslmode invalid", variable: EnvDatabaseSSLMode, value: "sometimes"},
		{name: "port zero for api", variable: EnvPort, value: "0"},
		{name: "port too high for api", variable: EnvPort, value: "70000"},
		{name: "port not a number for api", variable: EnvPort, value: "http"},
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
			for _, name := range test.remove {
				delete(environment, name)
			}
			if test.variable != "" {
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
	environment[EnvDatabasePassword] = "super-secret"
	environment[EnvDatabaseSSLMode] = "not-a-mode"

	_, err := LoadFrom(lookupFrom(environment))
	if err == nil {
		t.Fatal("LoadFrom accepted an invalid sslmode")
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("validation error leaked the password: %v", err)
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

func TestDatabaseDSNEncodesReservedCharacters(t *testing.T) {
	tests := []struct {
		name string
		cfg  DatabaseConfig
		want string
	}{
		{
			name: "simple",
			cfg: DatabaseConfig{
				Host: "localhost", Port: 5432, User: "user", Password: "pass",
				Name: "family_budget", SSLMode: "disable",
			},
			want: "postgres://user:pass@localhost:5432/family_budget?sslmode=disable",
		},
		{
			name: "reserved characters in user and password",
			cfg: DatabaseConfig{
				Host: "db.example", Port: 5432, User: "us@er:name",
				Password: "p@ss:w/rd?%&# ", Name: "family_budget", SSLMode: "require",
			},
			want: "postgres://us%40er%3Aname:p%40ss%3Aw%2Frd%3F%25&%23%20@db.example:5432/family_budget?sslmode=require",
		},
		{
			name: "ipv6 host",
			cfg: DatabaseConfig{
				Host: "::1", Port: 5432, User: "user",
				Password: "pa ss#?&%/@:", Name: "db", SSLMode: "require",
			},
			want: "postgres://user:pa%20ss%23%3F&%25%2F%40%3A@[::1]:5432/db?sslmode=require",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.cfg.DSN()
			if err != nil {
				t.Fatalf("DSN: %v", err)
			}
			if got != test.want {
				t.Fatalf("DSN = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDatabaseDSNValidatesRequiredComponents(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DatabaseConfig)
		want   string
	}{
		{"missing host", func(c *DatabaseConfig) { c.Host = "" }, EnvDatabaseHost},
		{"missing port", func(c *DatabaseConfig) { c.Port = 0 }, EnvDatabasePort},
		{"missing user", func(c *DatabaseConfig) { c.User = "" }, EnvDatabaseUser},
		{"missing password", func(c *DatabaseConfig) { c.Password = "" }, EnvDatabasePassword},
		{"missing database", func(c *DatabaseConfig) { c.Name = "" }, EnvDatabaseName},
		{"invalid sslmode", func(c *DatabaseConfig) { c.SSLMode = "sometimes" }, EnvDatabaseSSLMode},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validDatabaseConfig()
			test.mutate(&cfg)
			_, err := cfg.DSN()
			if err == nil {
				t.Fatal("DSN accepted an incomplete configuration")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not name %s", err, test.want)
			}
		})
	}
}

func TestDatabaseTargetAndRedaction(t *testing.T) {
	cfg := validDatabaseConfig()
	cfg.Host = "db.example"
	cfg.User = "budget"
	cfg.Password = "super-secret"
	cfg.Name = "family"

	if got := cfg.Target(); got != "db.example:5432/family" {
		t.Fatalf("Target = %q, want db.example:5432/family", got)
	}
	if strings.Contains(cfg.Target(), cfg.Password) || strings.Contains(cfg.Target(), cfg.User) {
		t.Fatalf("Target leaked credentials: %q", cfg.Target())
	}

	dsn, err := cfg.DSN()
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}
	driverErr := errors.New("dial failed: " + dsn + " password=super-secret")
	redacted := cfg.RedactError(driverErr, dsn)
	for _, secret := range []string{"super-secret", "budget:", dsn} {
		if strings.Contains(redacted, secret) {
			t.Errorf("redacted error leaked %q: %v", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "db.example:5432/family") {
		t.Errorf("redacted error %q does not name the database target", redacted)
	}
}

func TestLoadFromTrustedProxies(t *testing.T) {
	cfg, err := LoadFrom(lookupFrom(validEnvironment()))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := strings.Join(cfg.HTTP.TrustedProxies, ","); got != "127.0.0.0/8,::1/128" {
		t.Errorf("default trusted proxies = %q, want loopback only", got)
	}

	environment := validEnvironment()
	environment[EnvHTTPTrustedProxies] = "10.0.0.0/8, 2001:db8::/32"
	cfg, err = LoadFrom(lookupFrom(environment))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := strings.Join(cfg.HTTP.TrustedProxies, ","); got != "10.0.0.0/8,2001:db8::/32" {
		t.Errorf("trusted proxies = %q, want the configured CIDRs", got)
	}

	environment[EnvHTTPTrustedProxies] = "not-a-cidr"
	if _, err := LoadFrom(lookupFrom(environment)); err == nil || !strings.Contains(err.Error(), EnvHTTPTrustedProxies) {
		t.Errorf("invalid CIDR error = %v, want it to name %s", err, EnvHTTPTrustedProxies)
	}
}
