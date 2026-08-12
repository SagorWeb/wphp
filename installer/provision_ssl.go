package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-acme/lego/v5/acme"
	"github.com/go-acme/lego/v5/certcrypto"
	"github.com/go-acme/lego/v5/certificate"
	"github.com/go-acme/lego/v5/lego"
	"github.com/go-acme/lego/v5/providers/http/webroot"
	"github.com/go-acme/lego/v5/registration"
)

func setupSSLDirs(cfg InstallConfig) {
	os.MkdirAll("/etc/letsencrypt/live", 0755)
	os.MkdirAll("/opt/wphpanel/lego", 0700)
}

func writeSSLRenewalTimer() {
	renewalScript := `#!/bin/bash
# WPHPanel — SSL edge reload (backup)
# Primary renew: wphpanel-api StartAutoRenew (in-process Lego, every 24h).
# This timer only reloads nginx so renewed certs on disk are picked up, and logs
# certificates that are within 30 days of expiry for operators.
set -uo pipefail

for CERT_DIR in /etc/letsencrypt/live/*/; do
  DOMAIN=$(basename "$CERT_DIR")
  CERT="${CERT_DIR}fullchain.pem"
  [ -f "$CERT" ] || continue
  if ! openssl x509 -checkend 2592000 -noout -in "$CERT" 2>/dev/null; then
    logger -t ssl-renew "WARNING: ${DOMAIN} expires within 30 days — rely on wphpanel-api auto-renew"
  fi
done

nginx -t 2>/dev/null && systemctl reload nginx 2>/dev/null || true
`
	os.WriteFile("/opt/wphpanel/bin/ssl-renew.sh", []byte(renewalScript), 0755)

	sslService := `[Unit]
Description=WPHPanel SSL Certificate Renewal
[Service]
Type=oneshot
ExecStart=/opt/wphpanel/bin/ssl-renew.sh
`
	os.WriteFile("/etc/systemd/system/wphpanel-ssl-renew.service", []byte(sslService), 0644)

	sslTimer := `[Unit]
Description=WPHPanel SSL Certificate Renewal Timer
[Timer]
OnCalendar=*-*-* 02:30:00
RandomizedDelaySec=3600
Persistent=true
[Install]
WantedBy=timers.target
`
	os.WriteFile("/etc/systemd/system/wphpanel-ssl-renew.timer", []byte(sslTimer), 0644)
	run("systemctl", "daemon-reload")
	run("systemctl", "enable", "wphpanel-ssl-renew.timer")
	run("systemctl", "start", "wphpanel-ssl-renew.timer")
}

type legoUser struct {
	email        string
	registration *acme.ExtendedAccount
	key          *ecdsa.PrivateKey
}

func (u *legoUser) GetEmail() string                       { return u.email }
func (u *legoUser) GetRegistration() *acme.ExtendedAccount { return u.registration }
func (u *legoUser) GetPrivateKey() crypto.Signer           { return u.key }

func issueSSLViaLego(hostname, email string) error {
	stateDir := "/opt/wphpanel/lego"
	os.MkdirAll(stateDir, 0700)

	keyPath := filepath.Join(stateDir, "account.key")
	uriPath := filepath.Join(stateDir, "account.uri")

	var privateKey *ecdsa.PrivateKey
	var err error

	// Try loading existing key
	if keyBytes, err := os.ReadFile(keyPath); err == nil {
		block, _ := pem.Decode(keyBytes)
		if block != nil {
			privateKey, _ = x509.ParseECPrivateKey(block.Bytes)
		}
	}

	if privateKey == nil {
		privateKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return fmt.Errorf("generate key: %w", err)
		}
		keyDER, _ := x509.MarshalECPrivateKey(privateKey)
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		os.WriteFile(keyPath, keyPEM, 0600)
	}

	user := &legoUser{
		email: email,
		key:   privateKey,
	}

	if uriBytes, err := os.ReadFile(uriPath); err == nil {
		uriStr := strings.TrimSpace(string(uriBytes))
		if strings.HasPrefix(uriStr, "https://") {
			user.registration = &acme.ExtendedAccount{
				Location: uriStr,
			}
		}
	}

	config := lego.NewConfig(user)
	config.CADirURL = lego.DirectoryURLLetsEncrypt

	client, err := lego.NewClient(config)
	if err != nil {
		return fmt.Errorf("create ACME client: %w", err)
	}

	// Webroot provider (local filesystem since installer runs on host)
	webrootProvider, err := webroot.NewHTTPProvider("/var/www/acme-challenge")
	if err != nil {
		return fmt.Errorf("init webroot provider: %w", err)
	}
	if err := client.Challenge.SetHTTP01Provider(webrootProvider); err != nil {
		return fmt.Errorf("set HTTP-01 provider: %w", err)
	}

	// Register account if needed
	if user.registration == nil {
		reg, err := client.Registration.Register(context.Background(), registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return fmt.Errorf("ACME registration: %w", err)
		}
		user.registration = reg
		os.WriteFile(uriPath, []byte(reg.Location), 0644)
	}

	request := certificate.ObtainRequest{
		Domains: []string{hostname},
		Bundle:  true,
		KeyType: certcrypto.EC256,
	}

	certificates, err := client.Certificate.Obtain(context.Background(), request)
	if err != nil {
		return fmt.Errorf("obtain cert: %w", err)
	}

	certDir := "/etc/letsencrypt/live/" + hostname
	os.MkdirAll(certDir, 0755)

	if len(certificates.Certificate) > 0 {
		os.WriteFile(filepath.Join(certDir, "fullchain.pem"), certificates.Certificate, 0644)
		certPEM := string(certificates.Certificate)
		block, rest := pem.Decode([]byte(certPEM))
		if block != nil {
			leafPEM := pem.EncodeToMemory(block)
			os.WriteFile(filepath.Join(certDir, "cert.pem"), leafPEM, 0644)
			if len(rest) > 0 {
				os.WriteFile(filepath.Join(certDir, "chain.pem"), []byte(rest), 0644)
			}
		}
	}

	if len(certificates.PrivateKey) > 0 {
		os.WriteFile(filepath.Join(certDir, "privkey.pem"), certificates.PrivateKey, 0600)
	}

	return nil
}
