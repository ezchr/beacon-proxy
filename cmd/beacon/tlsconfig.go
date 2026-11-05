package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// HTTPS for joining by address (beacon.toml domain, or tls-cert and tls-key). Bedrock clients try
// https first and wait about 20 seconds before falling back to http, so without a certificate every
// player who types the server's domain waits.

// doorTLS returns the TLS config for the direct-IP door, or nil when HTTPS is not set up.
func doorTLS(ctx context.Context, cfg FileConfig, log *slog.Logger) (*tls.Config, error) {
	switch {
	case cfg.TLSDomain != "" && (cfg.TLSCert != "" || cfg.TLSKey != ""):
		return nil, errors.New("set either domain (automatic certificate) or tls-cert and tls-key, not both")
	case cfg.TLSDomain != "":
		return autocertTLS(ctx, cfg.TLSDomain, cfg.ACMEHTTPAddress, log), nil
	case cfg.TLSCert != "" || cfg.TLSKey != "":
		if cfg.TLSCert == "" || cfg.TLSKey == "" {
			return nil, errors.New("tls-cert and tls-key go together")
		}
		f := &certFiles{cert: cfg.TLSCert, key: cfg.TLSKey}
		if _, err := f.get(nil); err != nil {
			return nil, err
		}
		log.Info("https on for joining by address", "cert", cfg.TLSCert)
		return &tls.Config{GetCertificate: f.get, MinVersion: tls.VersionTLS12}, nil
	}
	return nil, nil
}

// autocertTLS gets and renews a Let's Encrypt certificate for domain. Let's Encrypt checks the
// domain over plain HTTP on port 80, so Beacon answers those checks on challengeAddr (":80", or an
// address a web server on port 80 forwards /.well-known/acme-challenge/ to); certificates are kept
// in the certs folder. BEACON_ACME_STAGING=1 uses Let's Encrypt's staging server, for tests.
func autocertTLS(ctx context.Context, domain, challengeAddr string, log *slog.Logger) *tls.Config {
	if challengeAddr == "" {
		challengeAddr = ":80"
	}
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
		Cache:      autocert.DirCache("certs"),
	}
	if os.Getenv("BEACON_ACME_STAGING") != "" {
		m.Client = &acme.Client{DirectoryURL: "https://acme-staging-v02.api.letsencrypt.org/directory"}
		m.Cache = autocert.DirCache("certs-staging")
	}
	srv := &http.Server{Addr: challengeAddr, Handler: m.HTTPHandler(nil), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		if err := srv.ListenAndServe(); err != nil && ctx.Err() == nil {
			log.Warn("can't answer Let's Encrypt on "+challengeAddr+", so the certificate for "+domain+
				" can't be issued or renewed: free port 80, set acme-http-address, or use tls-cert and tls-key", "err", err)
		}
	}()
	log.Info("https on for joining by address: certificate from Let's Encrypt", "domain", domain)
	c := m.TLSConfig()
	c.MinVersion = tls.VersionTLS12
	return c
}

// certFiles serves a certificate from files, reading them again when they change (a renewal by
// certbot, for example).
type certFiles struct {
	cert, key string
	mu        sync.Mutex
	loaded    *tls.Certificate
	stamp     time.Time
	checked   time.Time
}

func (f *certFiles) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loaded != nil && time.Since(f.checked) < time.Minute {
		return f.loaded, nil
	}
	f.checked = time.Now()
	st, err := os.Stat(f.cert)
	if err != nil {
		if f.loaded != nil {
			return f.loaded, nil
		}
		return nil, err
	}
	if f.loaded != nil && st.ModTime().Equal(f.stamp) {
		return f.loaded, nil
	}
	c, err := tls.LoadX509KeyPair(f.cert, f.key)
	if err != nil {
		if f.loaded != nil {
			return f.loaded, nil
		}
		return nil, fmt.Errorf("tls-cert/tls-key: %w", err)
	}
	f.loaded, f.stamp = &c, st.ModTime()
	return f.loaded, nil
}
