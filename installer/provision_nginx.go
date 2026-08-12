package main

import (
	"fmt"
	"os"
	"strings"
)

func writeNginxCatchall() {
	// Generate self-signed fallback cert for the 443 catchall
	os.MkdirAll("/etc/ssl/wphpanel", 0755)
	run("bash", "-c", `openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
		-keyout /etc/ssl/wphpanel/default.key \
		-out /etc/ssl/wphpanel/default.crt \
		-subj '/CN=wphpanel-default/O=WPHPanel' 2>/dev/null`)

	conf := `# WPHPanel — Default Catchall (HTTP + HTTPS)
# Handles ACME challenges + error pages for unconfigured domains

server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;
    location /.well-known/acme-challenge/ { root /var/www/acme-challenge; try_files $uri =404; }
    location / { root /opt/wphpanel/error-pages; try_files /default.html =404; }
}

server {
    listen 443 ssl default_server;
    listen [::]:443 ssl default_server;
    http2 on;
    server_name _;

    ssl_certificate /etc/ssl/wphpanel/default.crt;
    ssl_certificate_key /etc/ssl/wphpanel/default.key;
    ssl_protocols TLSv1.2 TLSv1.3;

    location /.well-known/acme-challenge/ { root /var/www/acme-challenge; try_files $uri =404; }
    location / { root /opt/wphpanel/error-pages; try_files /default.html =404; }
}
`
	os.WriteFile("/etc/nginx/sites-available/000-default-catchall", []byte(conf), 0644)
	os.Symlink("/etc/nginx/sites-available/000-default-catchall", "/etc/nginx/sites-enabled/000-default-catchall")
}

func writePanelNginx(cfg InstallConfig) {
	serverIP := strings.TrimSpace(shellOutput("curl -s4 ifconfig.me --connect-timeout 5 2>/dev/null"))
	if serverIP == "" {
		serverIP = detectServerIP()
	}
	phpVer := detectHostPHPVersion()

	// Step 1: Write initial config with self-signed cert (works immediately)
	initialConf := fmt.Sprintf(`# pgAdmin SSO maps
# pgAdmin SSO — HMAC-signed cookie validation via auth_request
# Login gate: "0" = block (no cookie at all), "1" = has cookie (validated by auth_request)
map $cookie___pgadmin_sso $pgadmin_login_allowed {
    default  "0";
    "~.+"    "1";
}

# pgAdmin SSO: Only inject X-Forwarded-User when __pgadmin_sso cookie exists.
map $cookie___pgadmin_sso $pgadmin_sso_user {
    default  "";
    "~.+"    $cookie___pgadmin_sso;
}

server {
    listen 80;
    listen 443 ssl;
    http2 on;
    server_name %s %s;

    ssl_certificate /etc/ssl/wphpanel/default.crt;
    ssl_certificate_key /etc/ssl/wphpanel/default.key;
    ssl_protocols TLSv1.2 TLSv1.3;

    location /.well-known/acme-challenge/ {
        root /var/www/acme-challenge;
        try_files $uri =404;
    }

    # phpMyAdmin — SSO login + database management
    location ^~ /phpmyadmin {
        root /usr/share/;
        index index.php;
        try_files $uri $uri/ =404;

        location ~ \.php$ {
            include fastcgi_params;
            fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
            fastcgi_pass unix:/run/php/php__PHP_VER__-fpm.sock;
            fastcgi_read_timeout 300;
        }
    }

    # ── pgAdmin 4 — Multi-Tenant Hardened ────────────────────────────────────

    # Internal auth_request endpoint — validates HMAC-signed __pgadmin_sso cookie.
    location = /_pgadmin_auth {
        internal;
        proxy_pass http://127.0.0.1:8080/api/v1/databases/pgadmin-auth-check;
        proxy_pass_request_body off;
        proxy_set_header Content-Length "";
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # Login page — SSO only. Uses auth_request to validate HMAC-signed cookie.
    location = /pgadmin4/login {
        if ($pgadmin_login_allowed = "0") {
            return 302 /;
        }
        auth_request /_pgadmin_auth;
        auth_request_set $pgadmin_user $upstream_http_x_pgadmin_user;
        error_page 401 = @pgadmin_denied;

        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_user;
        proxy_redirect off;
        add_header Cache-Control "no-store, no-cache, must-revalidate" always;
    }

    location @pgadmin_denied {
        return 302 /;
    }

    # ── BLOCKED ROUTES — Security-sensitive operations ──────────────────────

    # 1. Password management — managed by WPHPanel only
    location ~ ^/pgadmin4/(reset_password|forgot_password|change_password) {
        return 403;
    }

    # 2. User management — block write operations but allow current_user.js
    #    current_user.js is a read-only JS file that pgAdmin's React app
    #    REQUIRES to initialize. Without it, pgAdmin hangs on "Loading...".
    location = /pgadmin4/user_management/current_user.js {
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }
    location ~ ^/pgadmin4/user_management/ {
        return 403;
    }

    # 3. Server management — prevent users from adding/editing/deleting servers
    location ~ ^/pgadmin4/browser/server/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 4. Server change_password endpoint — block ALTER USER password changes
    location ~ ^/pgadmin4/browser/server/change_password/ {
        return 403;
    }

    # 5. Role/Login management — block creating/editing/deleting PostgreSQL roles
    location ~ ^/pgadmin4/browser/role/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 6. Server group management — prevent adding/editing server groups
    location ~ ^/pgadmin4/browser/server_group/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 7. Cloud deployment — not applicable for hosting
    location ~ ^/pgadmin4/misc/cloud/ {
        return 403;
    }

    # 8. Shared server management — prevent sharing servers between users
    location ~ ^/pgadmin4/browser/shared_server/ {
        return 403;
    }

    # 9. pgAdmin file manager — block filesystem access
    location ~ ^/pgadmin4/file_manager/ {
        return 403;
    }

    # pgAdmin general — session-based access after SSO login.
    location /pgadmin4 {
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
        add_header Cache-Control "no-store, no-cache, must-revalidate" always;
    }

    # Single-Binary Proxy — Backend API + Embedded Frontend SPA + WebSockets
    location / {
        client_max_body_size 512M;
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
        proxy_buffering off;
    }
}
`, cfg.Hostname, serverIP)
	initialConf = strings.ReplaceAll(initialConf, "__PHP_VER__", phpVer)
	os.WriteFile("/etc/nginx/sites-available/wphpanel.conf", []byte(initialConf), 0644)
	os.Symlink("/etc/nginx/sites-available/wphpanel.conf", "/etc/nginx/sites-enabled/wphpanel.conf")
	run("bash", "-c", "nginx -t 2>/dev/null && systemctl reload nginx || true")

	// Step 2: Try to issue real SSL cert via Lego (best-effort)
	issueErr := issueSSLViaLego(cfg.Hostname, cfg.AdminEmail)
	if issueErr != nil {
		fmt.Printf("SSL generation failed: %v\n", issueErr)
		return // SSL failed — panel stays on self-signed cert
	}
	certDir := "/etc/letsencrypt/live/" + cfg.Hostname

	// Step 4: Upgrade to real cert with HTTP→HTTPS redirect
	sslConf := fmt.Sprintf(`# pgAdmin SSO maps
# pgAdmin SSO — HMAC-signed cookie validation via auth_request
# Login gate: "0" = block (no cookie at all), "1" = has cookie (validated by auth_request)
map $cookie___pgadmin_sso $pgadmin_login_allowed {
    default  "0";
    "~.+"    "1";
}

# pgAdmin SSO: Only inject X-Forwarded-User when __pgadmin_sso cookie exists.
map $cookie___pgadmin_sso $pgadmin_sso_user {
    default  "";
    "~.+"    $cookie___pgadmin_sso;
}

server {
    listen 80;
    server_name %s %s;
    location /.well-known/acme-challenge/ {
        root /var/www/acme-challenge;
        try_files $uri =404;
    }
    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    http2 on;
    server_name %s %s;

    ssl_certificate %s/fullchain.pem;
    ssl_certificate_key %s/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 1d;
    add_header Strict-Transport-Security "max-age=63072000; includeSubDomains; preload" always;

    location /.well-known/acme-challenge/ {
        root /var/www/acme-challenge;
        try_files $uri =404;
    }

    # phpMyAdmin — SSO login + database management
    location ^~ /phpmyadmin {
        root /usr/share/;
        index index.php;
        try_files $uri $uri/ =404;

        location ~ \.php$ {
            include fastcgi_params;
            fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
            fastcgi_pass unix:/run/php/php__PHP_VER__-fpm.sock;
            fastcgi_read_timeout 300;
        }
    }

    # ── pgAdmin 4 — Multi-Tenant Hardened ────────────────────────────────────

    # Internal auth_request endpoint — validates HMAC-signed __pgadmin_sso cookie.
    location = /_pgadmin_auth {
        internal;
        proxy_pass http://127.0.0.1:8080/api/v1/databases/pgadmin-auth-check;
        proxy_pass_request_body off;
        proxy_set_header Content-Length "";
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # Login page — SSO only. Uses auth_request to validate HMAC-signed cookie.
    location = /pgadmin4/login {
        if ($pgadmin_login_allowed = "0") {
            return 302 /;
        }
        auth_request /_pgadmin_auth;
        auth_request_set $pgadmin_user $upstream_http_x_pgadmin_user;
        error_page 401 = @pgadmin_denied;

        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_user;
        proxy_redirect off;
        add_header Cache-Control "no-store, no-cache, must-revalidate" always;
    }

    location @pgadmin_denied {
        return 302 /;
    }

    # ── BLOCKED ROUTES — Security-sensitive operations ──────────────────────

    # 1. Password management — managed by WPHPanel only
    location ~ ^/pgadmin4/(reset_password|forgot_password|change_password) {
        return 403;
    }

    # 2. User management — block write operations but allow current_user.js
    #    current_user.js is a read-only JS file that pgAdmin's React app
    #    REQUIRES to initialize. Without it, pgAdmin hangs on "Loading...".
    location = /pgadmin4/user_management/current_user.js {
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }
    location ~ ^/pgadmin4/user_management/ {
        return 403;
    }

    # 3. Server management — prevent users from adding/editing/deleting servers
    location ~ ^/pgadmin4/browser/server/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 4. Server change_password endpoint — block ALTER USER password changes
    location ~ ^/pgadmin4/browser/server/change_password/ {
        return 403;
    }

    # 5. Role/Login management — block creating/editing/deleting PostgreSQL roles
    location ~ ^/pgadmin4/browser/role/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 6. Server group management — prevent adding/editing server groups
    location ~ ^/pgadmin4/browser/server_group/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 7. Cloud deployment — not applicable for hosting
    location ~ ^/pgadmin4/misc/cloud/ {
        return 403;
    }

    # 8. Shared server management — prevent sharing servers between users
    location ~ ^/pgadmin4/browser/shared_server/ {
        return 403;
    }

    # 9. pgAdmin file manager — block filesystem access
    location ~ ^/pgadmin4/file_manager/ {
        return 403;
    }

    # pgAdmin general — session-based access after SSO login.
    location /pgadmin4 {
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
        add_header Cache-Control "no-store, no-cache, must-revalidate" always;
    }

    # Single-Binary Proxy — Backend API + Embedded Frontend SPA + WebSockets
    location / {
        client_max_body_size 512M;
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
        proxy_buffering off;
    }
}
`, cfg.Hostname, serverIP, cfg.Hostname, serverIP, certDir, certDir)
	sslConf = strings.ReplaceAll(sslConf, "__PHP_VER__", phpVer)
	os.WriteFile("/etc/nginx/sites-available/wphpanel.conf", []byte(sslConf), 0644)
	run("bash", "-c", "nginx -t 2>/dev/null && systemctl reload nginx || true")
}
