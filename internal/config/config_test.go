package config

import (
	"strings"
	"testing"
)

func TestWebhookURLIsValidatedAndNeverEchoed(t *testing.T) {
	t.Setenv("STUDIO_DISCORD_WEBHOOK_URL", "https://evil.example/api/webhooks/1/TOPSECRETtoken")
	_, err := Load()
	if err == nil {
		t.Fatal("a non-Discord webhook host was accepted")
	}
	if strings.Contains(err.Error(), "TOPSECRET") || strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("error echoes the URL: %v", err)
	}
	t.Setenv("STUDIO_DISCORD_WEBHOOK_URL", "https://discord.com/api/webhooks/1/abc")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultsAreLocalOnly(t *testing.T) {
	if _, err := Load(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
}

func TestCloudIsOffByDefaultAndStrictlyParsed(t *testing.T) {
	c, err := Load()
	if err != nil || c.CloudEnabled {
		t.Fatalf("default: enabled=%v err=%v", c.CloudEnabled, err)
	}
	t.Setenv("STUDIO_CLOUD_ENABLED", "true")
	if c, err = Load(); err != nil || !c.CloudEnabled {
		t.Fatalf("true: enabled=%v err=%v", c.CloudEnabled, err)
	}
	for _, v := range []string{"TRUE", "1", "yes", " true", "on"} {
		t.Setenv("STUDIO_CLOUD_ENABLED", v)
		if _, err := Load(); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestProvidersOffMachineAreRefused(t *testing.T) {
	for _, key := range []string{"OLLAMA_URL", "COMFYUI_URL", "THAIVOICE_URL"} {
		for _, url := range []string{"https://api.example.com", "http://192.168.1.20:8188", "http://10.0.0.5:11434"} {
			t.Setenv(key, url)
			if _, err := Load(); err == nil {
				t.Errorf("%s=%s accepted", key, url)
			}
		}
		t.Setenv(key, "http://127.0.0.1:9999")
		if _, err := Load(); err != nil {
			t.Errorf("%s loopback rejected: %v", key, err)
		}
	}
}

func TestAuthDefaultsServeLocalDevAndFollowThePort(t *testing.T) {
	c, err := Load()
	if err != nil || c.AuthRPID != "localhost" {
		t.Fatalf("%v %q", err, c.AuthRPID)
	}
	want := map[string]bool{"http://localhost:8080": true, "http://127.0.0.1:8080": true, "http://localhost:5173": true, "http://127.0.0.1:5173": true}
	for _, o := range c.AuthOrigins {
		delete(want, o)
	}
	if len(want) != 0 {
		t.Fatalf("missing default origins %v in %v", want, c.AuthOrigins)
	}
	t.Setenv("STUDIO_ADDR", "127.0.0.1:18090")
	if c, err = Load(); err != nil || c.AuthOrigins[0] != "http://localhost:18090" {
		t.Fatalf("%v %v", err, c.AuthOrigins)
	}
}

func TestAuthOriginsAreValidated(t *testing.T) {
	for _, bad := range []string{"studio.example", "http://studio.example", "https://studio.example/path", "ftp://x", "https://u:p@x"} {
		t.Setenv("STUDIO_AUTH_ORIGINS", bad)
		if _, err := Load(); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	t.Setenv("STUDIO_AUTH_ORIGINS", "https://studio.example, http://localhost:3000")
	c, err := Load()
	if err != nil || len(c.AuthOrigins) != 2 || c.AuthOrigins[1] != "http://localhost:3000" {
		t.Fatalf("%v %v", err, c.AuthOrigins)
	}
	t.Setenv("STUDIO_SESSION_IDLE", "5s")
	if _, err := Load(); err == nil {
		t.Fatal("session idle below one minute accepted")
	}
}

func TestServerMustBindToLoopback(t *testing.T) {
	t.Setenv("STUDIO_ADDR", "0.0.0.0:8080")
	if _, err := Load(); err == nil {
		t.Fatal("0.0.0.0 accepted")
	}
}
