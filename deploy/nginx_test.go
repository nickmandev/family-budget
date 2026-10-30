package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nginxSnippetPath is the drop-in server block the operator copies into the
// host's existing nginx. TLS is handled by their existing Let's Encrypt flow.
const nginxSnippetPath = "deploy/nginx/family-budget.conf"

func TestNginxSnippetProxiesToLoopbackAPI(t *testing.T) {
	snippet := readDeployFile(t, nginxSnippetPath)

	required := []string{
		"server_name budget.example.com;",
		"CHANGE-ME",
		"return 301 https://",
		"1.25.1",
		"listen 443 ssl http2;",
		"listen 443 ssl",
		"ssl_certificate",
		"ssl_certificate_key",
		"proxy_pass http://127.0.0.1:8080",
		"proxy_set_header Host $host",
		"proxy_set_header X-Real-IP $remote_addr",
		"proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for",
		"proxy_set_header X-Forwarded-Proto $scheme",
		"client_max_body_size 1m",
		"Strict-Transport-Security",
		"X-Content-Type-Options",
		"X-Frame-Options",
		"Referrer-Policy",
		// Cloudflare proxies the edge, so the snippet must restore the real
		// client IP from CF-Connecting-IP within the published ranges.
		"set_real_ip_from",
		"real_ip_header CF-Connecting-IP",
		"https://www.cloudflare.com/ips-v4",
	}
	for _, want := range required {
		if !strings.Contains(snippet, want) {
			t.Errorf("%s does not contain %q", nginxSnippetPath, want)
		}
	}

	for _, forbidden := range []string{"0.0.0.0", "proxy_pass http://api:"} {
		if strings.Contains(snippet, forbidden) {
			t.Errorf("%s contains %q; it must proxy to the loopback API", nginxSnippetPath, forbidden)
		}
	}
}

func TestNginxSnippetHasNoSecretMaterial(t *testing.T) {
	snippet := readDeployFile(t, nginxSnippetPath)
	lower := strings.ToLower(snippet)

	for _, secret := range []string{
		"private key-----",
		"begin openssh",
		"age-secret-key",
		"postgres_password",
		"database" + "_url",
		"password=",
		"age1",
	} {
		if strings.Contains(lower, secret) {
			t.Errorf("%s appears to contain secret material (%q)", nginxSnippetPath, secret)
		}
	}
}

func TestNginxSnippetIsDocumented(t *testing.T) {
	readme := readDeployFile(t, "deploy/nginx/README.md")

	for _, want := range []string{
		"/etc/nginx/sites-available/",
		"/etc/nginx/sites-enabled/",
		"nginx -t",
		"systemctl reload nginx",
		"certbot",
		"/readyz",
		"1.25.1",
		"listen 443 ssl http2;",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("deploy/nginx/README.md does not mention %q", want)
		}
	}
}

func TestNginxFrontDoorTopology(t *testing.T) {
	// The old proxy config must stay deleted.
	if _, err := os.Stat(filepath.Join("..", caddyfilePath)); !os.IsNotExist(err) {
		t.Errorf("Caddyfile must not exist (os.Stat err = %v)", err)
	}

	// The API's only host exposure is the loopback publish.
	api := requireService(t, loadCompose(t), "api")
	if len(api.Ports) != 1 {
		t.Fatalf("api ports = %v, want exactly one loopback publish", api.Ports)
	}
	if !strings.HasPrefix(api.Ports[0], "127.0.0.1:") {
		t.Errorf("api host binding = %q, want a 127.0.0.1: loopback prefix", api.Ports[0])
	}
}
