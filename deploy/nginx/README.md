# nginx front door

The API container publishes on loopback only (`127.0.0.1:8080`), so the host's
existing nginx is the sole public front door. TLS and certificate renewal stay
in the operator's existing Let's Encrypt flow — there is no ACME client in the
Compose stack.

## Requirements

The snippet uses `http2 on;`, which requires nginx >= 1.25.1. With older nginx,
replace the HTTPS listen lines with `listen 443 ssl http2;` and
`listen [::]:443 ssl http2;`, then drop the `http2 on;` directive.

Remediation: upgrade via the official nginx.org stable repository, or use the older listen syntax above.

## Install

```sh
# 1. Edit the snippet: replace budget.example.com (CHANGE-ME) everywhere.
sudo cp deploy/nginx/family-budget.conf /etc/nginx/sites-available/
sudo ln -s /etc/nginx/sites-available/family-budget.conf \
    /etc/nginx/sites-enabled/family-budget.conf

# 2. Validate and reload.
sudo nginx -t && sudo systemctl reload nginx

# 3. Issue/renew the certificate exactly as for the other projects on this
#    host (certbot --nginx or the existing webroot flow):
sudo certbot --nginx -d budget.example.com

# 4. Verify through the public hostname.
curl -fsS https://budget.example.com/readyz
curl -sSI https://budget.example.com/ | grep -i strict-transport-security
```

If `API_HOST_PORT` in `.env` is not `8080`, update the `proxy_pass` line in the
snippet to match. The DNS A/AAAA record must point at this VPS before step 3.

## Cloudflare prerequisites

The public hostname is `budget-api.<domain>` and Cloudflare fronts the origin:

- **DNS:** an `A` (and optionally `AAAA`) record for `budget-api.<domain>`
  pointing at the VPS, with the proxy status **proxied** (orange cloud). A
  grey-cloud record bypasses Cloudflare and the real-IP restoration never runs.
- **SSL/TLS mode:** **Full (strict)**, never **Flexible**. The origin already
  serves a valid Let's Encrypt certificate; Flexible would send plaintext to the
  origin and break the origin TLS guarantee.
- **Always Use HTTPS:** enabled, so Cloudflare redirects HTTP before it reaches
  the origin.
- **Origin firewall:** allow inbound `:80` and `:443` only from the Cloudflare
  ranges in `family-budget.conf` (`https://www.cloudflare.com/ips-v4` and
  `/ips-v6`). This is what makes `CF-Connecting-IP` trustworthy: a client that
  can reach the origin directly could spoof that header. Keep the snippet's
  `set_real_ip_from` list in sync with those published ranges.
- **Bot protection** is Cloudflare's job at the edge. The application still
  enforces authentication and per-IP rate limits, and relies on the restored
  real client IP (see the `set_real_ip_from` block) so those limits are per
  user rather than per Cloudflare edge.
