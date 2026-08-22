// SentinelDesk
// A collaborative operating system for people and AI agents.
//
// Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
//
// Licensed under the Apache License, Version 2.0.
//
// This product's name and logo are trademarks of Federico Pereira and are not
// covered by the license above. See the README for the trademark policy.
//
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"github.com/sentineldesk/desktop/pkg/config"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnsureTLS resolves the server's TLS mode:
//   - TLS_CERT + TLS_KEY definidos → usa esos archivos (certificado propio,
//     a purchased or wildcard certificate; a Let's Encrypt fullchain works too).
//   - TLS_SELFSIGNED=1 → generate a self-signed certificate once and keep it in
//     TLS_DIR, which survives restarts through the home volume. Regenerating it
//     on every boot would retrain people to click through the warning.
//   - nothing set → plain HTTP, behind a TLS proxy or in development.
//
// It returns the certificate and key paths, or empty strings for HTTP.
func EnsureTLS(cfg config.Config) (string, string, error) {
	if cfg.TLSCert != "" || cfg.TLSKey != "" {
		if cfg.TLSCert == "" || cfg.TLSKey == "" {
			return "", "", fmt.Errorf("TLS_CERT y TLS_KEY deben definirse juntos")
		}
		for _, f := range []string{cfg.TLSCert, cfg.TLSKey} {
			if _, err := os.Stat(f); err != nil {
				return "", "", fmt.Errorf("certificado TLS: %w", err)
			}
		}
		return cfg.TLSCert, cfg.TLSKey, nil
	}
	if !cfg.TLSSelfSigned {
		return "", "", nil
	}

	certPath := filepath.Join(cfg.TLSDir, "cert.pem")
	keyPath := filepath.Join(cfg.TLSDir, "key.pem")
	if fileExists(certPath) && fileExists(keyPath) {
		return certPath, keyPath, nil
	}
	if err := generateSelfSigned(certPath, keyPath, cfg.TLSHosts); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// generateSelfSigned creates a self-signed ECDSA P-256 certificate covering the
// requested SANs (TLS_HOSTS, comma separated) plus localhost and the machine's
// own hostname.
func generateSelfSigned(certPath, keyPath, hosts string) error {
	dnsNames := []string{"localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if hn, err := os.Hostname(); err == nil && hn != "" {
		dnsNames = append(dnsNames, hn)
	}
	for _, h := range strings.Split(hosts, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			ips = append(ips, ip)
		} else {
			dnsNames = append(dnsNames, h)
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   dnsNames[0],
			Organization: []string{"SentinelDesk self-signed"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(0, 0, 825), // the longest lifetime browsers accept
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return err
	}
	certOut, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer keyOut.Close()
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return err
	}

	sum := sha256.Sum256(der)
	log.Printf("certificado autofirmado generado en %s (SANs: %s / %s, SHA-256 %x)",
		filepath.Dir(certPath), strings.Join(dnsNames, " "), joinIPs(ips), sum[:8])
	return nil
}

func joinIPs(ips []net.IP) string {
	parts := make([]string, len(ips))
	for i, ip := range ips {
		parts[i] = ip.String()
	}
	return strings.Join(parts, " ")
}
