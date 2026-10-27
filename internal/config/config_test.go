package config

import "testing"

func TestDefaultsAreLocalOnly(t *testing.T) {
	if _, err := Load(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
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

func TestServerMustBindToLoopback(t *testing.T) {
	t.Setenv("STUDIO_ADDR", "0.0.0.0:8080")
	if _, err := Load(); err == nil {
		t.Fatal("0.0.0.0 accepted")
	}
}
