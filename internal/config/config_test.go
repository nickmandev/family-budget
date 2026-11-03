package config

import (
	"bytes"
	"encoding/base64"
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
	if cfg.SyncWorkerPort != 8081 {
		t.Errorf("sync worker port = %d, want 8081", cfg.SyncWorkerPort)
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
	if cfg.EnableBanking.Enabled != defaults.EnableBanking.Enabled ||
		!bytes.Equal(cfg.EnableBanking.EncryptionKey, defaults.EnableBanking.EncryptionKey) ||
		cfg.EnableBanking.ApplicationID != defaults.EnableBanking.ApplicationID ||
		cfg.EnableBanking.PrivateKeyFile != defaults.EnableBanking.PrivateKeyFile ||
		cfg.EnableBanking.RedirectURL != defaults.EnableBanking.RedirectURL ||
		cfg.EnableBanking.ResultURL != defaults.EnableBanking.ResultURL ||
		cfg.EnableBanking.BaseURL != defaults.EnableBanking.BaseURL ||
		cfg.EnableBanking.TokenTTL != defaults.EnableBanking.TokenTTL ||
		cfg.EnableBanking.Country != defaults.EnableBanking.Country ||
		cfg.EnableBanking.SyncInterval != defaults.EnableBanking.SyncInterval {
		t.Errorf("Enable Banking = %+v, want defaults %+v", cfg.EnableBanking, defaults.EnableBanking)
	}
	if len(cfg.HTTPCORSAllowedOrigins) != 0 {
		t.Errorf("CORS allowed origins = %v, want disabled by default", cfg.HTTPCORSAllowedOrigins)
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

func TestLoadFromEnableBankingDisabledWithoutVariables(t *testing.T) {
	config, err := LoadFrom(lookupFrom(validEnvironment()))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if config.EnableBanking.Enabled {
		t.Fatal("Enable Banking is enabled without an explicit setting")
	}
}

func TestLoadFromEnableBankingEnabled(t *testing.T) {
	environment := validEnvironment()
	setEnableBankingEnvironment(environment)

	config, err := LoadFrom(lookupFrom(environment))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	want := EnableBankingConfig{
		Enabled:        true,
		EncryptionKey:  make([]byte, 32),
		ApplicationID:  "123e4567-e89b-12d3-a456-426614174000",
		PrivateKeyFile: "/run/secrets/enablebanking-key.pem",
		RedirectURL:    "https://budget-api.nickmandev.com/callbacks/enablebanking",
		ResultURL:      "",
		BaseURL:        "https://api.enablebanking.com",
		TokenTTL:       5 * time.Minute,
		Country:        "BG",
		SyncInterval:   6 * time.Hour,
	}
	if config.EnableBanking.Enabled != want.Enabled || !bytes.Equal(config.EnableBanking.EncryptionKey, want.EncryptionKey) ||
		config.EnableBanking.ApplicationID != want.ApplicationID || config.EnableBanking.PrivateKeyFile != want.PrivateKeyFile ||
		config.EnableBanking.RedirectURL != want.RedirectURL || config.EnableBanking.BaseURL != want.BaseURL ||
		config.EnableBanking.ResultURL != want.ResultURL ||
		config.EnableBanking.TokenTTL != want.TokenTTL || config.EnableBanking.Country != want.Country ||
		config.EnableBanking.SyncInterval != want.SyncInterval {
		t.Errorf("Enable Banking = %+v, want %+v", config.EnableBanking, want)
	}
}

func TestLoadFromEnableBankingResultURL(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		valid   bool
		wantURL string
	}{
		{name: "valid", value: "https://budget.nickmandev.com/bank/callback", valid: true, wantURL: "https://budget.nickmandev.com/bank/callback"},
		{name: "empty", valid: true},
		{name: "relative", value: "/bank/callback"},
		{name: "remote http", value: "http://budget.example/bank/callback"},
		{name: "query", value: "https://budget.example/bank/callback?status=done"},
		{name: "loopback http", value: "http://localhost:3000/bank/callback", valid: true, wantURL: "http://localhost:3000/bank/callback"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := validEnvironment()
			if test.value != "" {
				environment[EnvEnableBankingResultURL] = test.value
			}
			cfg, err := LoadFrom(lookupFrom(environment))
			if test.valid {
				if err != nil {
					t.Fatalf("LoadFrom: %v", err)
				}
				if cfg.EnableBanking.ResultURL != test.wantURL {
					t.Errorf("ResultURL = %q, want %q", cfg.EnableBanking.ResultURL, test.wantURL)
				}
				return
			}
			assertEnableBankingErrorNames(t, err, EnvEnableBankingResultURL)
		})
	}
}

func TestLoadFromEnableBankingRedirectURL(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		valid   bool
		wantURL string
	}{
		{name: "https", value: "https://budget-api.nickmandev.com/callbacks/enablebanking", valid: true, wantURL: "https://budget-api.nickmandev.com/callbacks/enablebanking"},
		{name: "localhost http", value: "http://localhost:8080/callbacks/enablebanking", valid: true, wantURL: "http://localhost:8080/callbacks/enablebanking"},
		{name: "ipv4 loopback http", value: "http://127.0.0.1:8080/callbacks/enablebanking", valid: true, wantURL: "http://127.0.0.1:8080/callbacks/enablebanking"},
		{name: "remote http", value: "http://budget.example/callbacks"},
		{name: "query", value: "https://budget.example/callbacks?x=1"},
		{name: "fragment", value: "https://budget.example/callbacks#frag"},
		{name: "relative path", value: "/callbacks/enablebanking"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := validEnvironment()
			setEnableBankingEnvironment(environment)
			environment[EnvEnableBankingRedirectURL] = test.value
			cfg, err := LoadFrom(lookupFrom(environment))
			if test.valid {
				if err != nil {
					t.Fatalf("LoadFrom: %v", err)
				}
				if cfg.EnableBanking.RedirectURL != test.wantURL {
					t.Errorf("RedirectURL = %q, want %q", cfg.EnableBanking.RedirectURL, test.wantURL)
				}
				return
			}
			assertEnableBankingErrorNames(t, err, EnvEnableBankingRedirectURL)
		})
	}
}

func TestLoadFromEnableBankingDefaults(t *testing.T) {
	environment := validEnvironment()
	environment[EnvEnableBankingEnabled] = "true"
	environment[EnvEnableBankingEncryptionKey] = base64.StdEncoding.EncodeToString(make([]byte, 32))
	environment[EnvEnableBankingApplicationID] = "123e4567-e89b-12d3-a456-426614174000"
	environment[EnvEnableBankingPrivateKeyFile] = "/run/secrets/enablebanking-key.pem"
	environment[EnvEnableBankingRedirectURL] = "https://budget-api.nickmandev.com/callbacks/enablebanking"

	config, err := LoadFrom(lookupFrom(environment))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if config.EnableBanking.Country != "BG" || config.EnableBanking.SyncInterval != 6*time.Hour ||
		config.EnableBanking.TokenTTL != 5*time.Minute || config.EnableBanking.BaseURL != "https://api.enablebanking.com" {
		t.Errorf("Enable Banking defaults = %+v", config.EnableBanking)
	}
}

func TestLoadFromEnableBankingRequiresValidConfigurationWhenEnabled(t *testing.T) {
	missingTests := []struct {
		name     string
		variable string
	}{
		{name: "application ID", variable: EnvEnableBankingApplicationID},
		{name: "private key file", variable: EnvEnableBankingPrivateKeyFile},
		{name: "redirect URL", variable: EnvEnableBankingRedirectURL},
		{name: "encryption key", variable: EnvEnableBankingEncryptionKey},
	}
	for _, test := range missingTests {
		t.Run("missing "+test.name, func(t *testing.T) {
			environment := validEnvironment()
			setEnableBankingEnvironment(environment)
			delete(environment, test.variable)
			_, err := LoadFrom(lookupFrom(environment))
			assertEnableBankingErrorNames(t, err, test.variable)
		})
	}

	invalidTests := []struct {
		name     string
		variable string
		value    string
	}{
		{name: "application ID", variable: EnvEnableBankingApplicationID, value: "not-a-uuid"},
		{name: "private key path", variable: EnvEnableBankingPrivateKeyFile, value: "relative/key.pem"},
		{name: "redirect URL scheme", variable: EnvEnableBankingRedirectURL, value: "http://budget.example/callback"},
		{name: "redirect URL query", variable: EnvEnableBankingRedirectURL, value: "https://budget.example/callback?state=secret"},
		{name: "redirect URL fragment", variable: EnvEnableBankingRedirectURL, value: "https://budget.example/callback#fragment"},
		{name: "base URL path", variable: EnvEnableBankingBaseURL, value: "https://api.enablebanking.com/v1"},
		{name: "remote HTTP base URL", variable: EnvEnableBankingBaseURL, value: "http://api.enablebanking.com"},
		{name: "country lowercase", variable: EnvEnableBankingCountry, value: "bg"},
		{name: "country wrong length", variable: EnvEnableBankingCountry, value: "BGR"},
		{name: "token TTL below range", variable: EnvEnableBankingTokenTTL, value: "500ms"},
		{name: "token TTL above range", variable: EnvEnableBankingTokenTTL, value: "25h"},
		{name: "token TTL invalid", variable: EnvEnableBankingTokenTTL, value: "soon"},
		{name: "sync interval below range", variable: EnvEnableBankingSyncInterval, value: "30s"},
		{name: "sync interval above range", variable: EnvEnableBankingSyncInterval, value: "25h"},
		{name: "sync interval invalid", variable: EnvEnableBankingSyncInterval, value: "later"},
		{name: "encryption key malformed", variable: EnvEnableBankingEncryptionKey, value: "not-base64"},
		{name: "encryption key wrong length", variable: EnvEnableBankingEncryptionKey, value: base64.StdEncoding.EncodeToString([]byte("too-short"))},
	}
	for _, test := range invalidTests {
		t.Run("invalid "+test.name, func(t *testing.T) {
			environment := validEnvironment()
			setEnableBankingEnvironment(environment)
			environment[test.variable] = test.value
			_, err := LoadFrom(lookupFrom(environment))
			assertEnableBankingErrorNames(t, err, test.variable)
			if strings.Contains(err.Error(), test.value) {
				t.Errorf("validation error included the configured value: %v", err)
			}
		})
	}
}

func TestLoadFromEnableBankingAllowsLoopbackHTTPBaseURL(t *testing.T) {
	environment := validEnvironment()
	setEnableBankingEnvironment(environment)
	environment[EnvEnableBankingBaseURL] = "http://127.0.0.1:8080"

	if _, err := LoadFrom(lookupFrom(environment)); err != nil {
		t.Fatalf("LoadFrom loopback base URL: %v", err)
	}
}

func TestLoadFromEnableBankingSkipsFieldsWhenDisabled(t *testing.T) {
	environment := validEnvironment()
	environment[EnvEnableBankingApplicationID] = "invalid"
	environment[EnvEnableBankingPrivateKeyFile] = "relative"
	environment[EnvEnableBankingCountry] = "bad"

	if _, err := LoadFrom(lookupFrom(environment)); err != nil {
		t.Fatalf("LoadFrom with disabled, invalid Enable Banking fields: %v", err)
	}
}

func setEnableBankingEnvironment(environment map[string]string) {
	environment[EnvEnableBankingEnabled] = "true"
	environment[EnvEnableBankingEncryptionKey] = base64.StdEncoding.EncodeToString(make([]byte, 32))
	environment[EnvEnableBankingApplicationID] = "123e4567-e89b-12d3-a456-426614174000"
	environment[EnvEnableBankingPrivateKeyFile] = "/run/secrets/enablebanking-key.pem"
	environment[EnvEnableBankingRedirectURL] = "https://budget-api.nickmandev.com/callbacks/enablebanking"
}

func assertEnableBankingErrorNames(t *testing.T, err error, variable string) {
	t.Helper()
	if err == nil {
		t.Fatalf("LoadFrom accepted invalid Enable Banking setting %s", variable)
	}
	if !strings.Contains(err.Error(), variable) {
		t.Errorf("error %q does not name %s", err, variable)
	}
}

func TestLoadFromHTTPCORSAllowedOrigins(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{name: "unset"},
		{name: "single", value: "https://budget.example", want: []string{"https://budget.example"}},
		{name: "multiple", value: "https://budget.example,http://localhost:3000", want: []string{"https://budget.example", "http://localhost:3000"}},
		{name: "whitespace", value: " https://BUDGET.example , , http://localhost:3000 ", want: []string{"https://budget.example", "http://localhost:3000"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := validEnvironment()
			if test.name != "unset" {
				environment[EnvHTTPCORSAllowedOrigins] = test.value
			}
			cfg, err := LoadFrom(lookupFrom(environment))
			if err != nil {
				t.Fatalf("LoadFrom: %v", err)
			}
			if strings.Join(cfg.HTTPCORSAllowedOrigins, ",") != strings.Join(test.want, ",") {
				t.Errorf("CORS allowed origins = %v, want %v", cfg.HTTPCORSAllowedOrigins, test.want)
			}
		})
	}
}

func TestLoadFromRejectsInvalidHTTPCORSAllowedOrigins(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "wildcard", value: "*"},
		{name: "path", value: "https://budget.example/app"},
		{name: "trailing slash", value: "https://budget.example/"},
		{name: "malformed", value: "https://[not-an-ip"},
		{name: "duplicate after normalization", value: "https://BUDGET.example, https://budget.example"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := validEnvironment()
			environment[EnvHTTPCORSAllowedOrigins] = test.value
			_, err := LoadFrom(lookupFrom(environment))
			if err == nil {
				t.Fatal("LoadFrom accepted invalid CORS allowed origins")
			}
			if !strings.Contains(err.Error(), EnvHTTPCORSAllowedOrigins) {
				t.Errorf("error %q does not name %s", err, EnvHTTPCORSAllowedOrigins)
			}
		})
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
		{name: "sync worker port zero", variable: EnvSyncWorkerPort, value: "0"},
		{name: "sync worker port too high", variable: EnvSyncWorkerPort, value: "70000"},
		{name: "sync worker port not a number", variable: EnvSyncWorkerPort, value: "http"},
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
