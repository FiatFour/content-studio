package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderURLTricksAreRefused(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1@evil.example", "http://localhost.evil.example:8188", "http://127.0.0.1.evil.example",
		"http://evil.example#@127.0.0.1", "http://0.0.0.0:8188", "ftp://127.0.0.1:21", "file:///etc/passwd",
		"http://user:pw@127.0.0.1:8188", "//127.0.0.1:8188", "127.0.0.1:8188", "http://[::ffff:10.0.0.1]:8188",
	} {
		t.Setenv("COMFYUI_URL", u)
		if _, err := Load(); err == nil {
			t.Errorf("COMFYUI_URL=%q accepted", u)
		}
	}
	for _, u := range []string{"http://localhost:8188", "http://127.0.0.1:8188", "http://[::1]:8188", "https://127.0.0.1:8188"} {
		t.Setenv("COMFYUI_URL", u)
		if _, err := Load(); err != nil {
			t.Errorf("COMFYUI_URL=%q rejected: %v", u, err)
		}
	}
}

func writeTLS(t *testing.T) (cert, key string) {
	t.Helper()
	d := t.TempDir()
	cert, key = filepath.Join(d, "c.pem"), filepath.Join(d, "k.pem")
	for _, p := range []string{cert, key} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cert, key
}

func TestNonLoopbackBindNeedsTLSOrExplicitOverride(t *testing.T) {
	cert, key := writeTLS(t)
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "192.168.1.5:8080", "studio.lan:8080", "[::]:8080"} {
		t.Setenv("STUDIO_ADDR", addr)
		t.Setenv("STUDIO_TLS_CERT", "")
		t.Setenv("STUDIO_TLS_KEY", "")
		t.Setenv("STUDIO_ALLOW_INSECURE_REMOTE", "")
		if _, err := Load(); err == nil {
			t.Errorf("%s accepted without TLS", addr)
		}
		t.Setenv("STUDIO_TLS_CERT", cert)
		t.Setenv("STUDIO_TLS_KEY", key)
		if c, err := Load(); err != nil || c.TLSCert != cert {
			t.Errorf("%s with TLS: %v", addr, err)
		}
		t.Setenv("STUDIO_TLS_CERT", "")
		t.Setenv("STUDIO_TLS_KEY", "")
		t.Setenv("STUDIO_ALLOW_INSECURE_REMOTE", "true")
		if c, err := Load(); err != nil || !c.AllowInsecureRemote {
			t.Errorf("%s with the override: %v", addr, err)
		}
	}
	t.Setenv("STUDIO_ALLOW_INSECURE_REMOTE", "yes")
	if _, err := Load(); err == nil {
		t.Error("a non-boolean override was accepted")
	}
}

func TestTLSConfigValidation(t *testing.T) {
	cert, key := writeTLS(t)
	t.Setenv("STUDIO_TLS_CERT", cert)
	if _, err := Load(); err == nil {
		t.Error("cert without key accepted")
	}
	t.Setenv("STUDIO_TLS_CERT", "")
	t.Setenv("STUDIO_TLS_KEY", key)
	if _, err := Load(); err == nil {
		t.Error("key without cert accepted")
	}
	t.Setenv("STUDIO_TLS_CERT", "/nonexistent/secret-path/cert.pem")
	_, err := Load()
	if err == nil {
		t.Fatal("missing cert file accepted")
	}
	if strings.Contains(err.Error(), "secret-path") {
		t.Errorf("error echoes the path: %v", err)
	}
	t.Setenv("STUDIO_TLS_CERT", cert)
	if c, err := Load(); err != nil || c.TLSCert == "" {
		t.Errorf("loopback with TLS: %v", err)
	}
	t.Setenv("STUDIO_TLS_CERT", "")
	t.Setenv("STUDIO_TLS_KEY", "")
	if c, err := Load(); err != nil || c.TLSCert != "" || c.Addr != "127.0.0.1:8080" {
		t.Errorf("defaults changed: %+v %v", c, err)
	}
}

func TestLimitSettingsAreValidatedAndHaveDefaults(t *testing.T) {
	c, err := Load()
	if err != nil || c.MaxBodyBytes != 0 || c.ReadTimeout.Seconds() != 30 || c.WriteTimeout.Seconds() != 60 || c.IdleTimeout.Seconds() != 120 || c.StreamTimeout.Minutes() != 15 {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	for key, v := range map[string]string{"STUDIO_MAX_BODY_BYTES": "10", "STUDIO_RATE_ANON_PER_MIN": "0", "STUDIO_RATE_AUTH_PER_MIN": "-1",
		"STUDIO_READ_TIMEOUT": "0s", "STUDIO_WRITE_TIMEOUT": "soon", "STUDIO_IDLE_TIMEOUT": "1ms", "STUDIO_STREAM_TIMEOUT": "x"} {
		t.Setenv(key, v)
		if _, err := Load(); err == nil {
			t.Errorf("%s=%s accepted", key, v)
		}
		t.Setenv(key, "")
	}
	t.Setenv("STUDIO_MAX_BODY_BYTES", "4096")
	t.Setenv("STUDIO_RATE_ANON_PER_MIN", "30")
	if c, err := Load(); err != nil || c.MaxBodyBytes != 4096 || c.RateAnonPerM != 30 {
		t.Errorf("valid values: %+v %v", c, err)
	}
}
