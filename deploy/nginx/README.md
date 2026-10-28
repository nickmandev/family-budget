# nginx front door

The API container publishes on loopback only (`127.0.0.1:8080`), so the host's
existing nginx is the sole public front door. TLS and certificate renewal stay
in the operator's existing Let's Encrypt flow — there is no ACME client in the
Compose stack.

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
