package main

import (
	"strings"
	"testing"
	"time"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("WB_AMI_USER", "westbridge")
	t.Setenv("WB_AMI_SECRET", "secret")
	t.Setenv("WB_ROOM", "1000")
	t.Setenv("WB_ORIGINATE_CONTEXT", "conference-out")
}

func TestLoadConfigDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Listen != ":8080" {
		t.Errorf("Listen = %q, want :8080", cfg.Listen)
	}
	if cfg.AMIAddr != "127.0.0.1:5038" {
		t.Errorf("AMIAddr = %q, want 127.0.0.1:5038", cfg.AMIAddr)
	}
	if cfg.OriginateCallerID != "Westbridge <0000>" {
		t.Errorf("OriginateCallerID = %q", cfg.OriginateCallerID)
	}
	if cfg.OriginateTimeout != 30*time.Second {
		t.Errorf("OriginateTimeout = %s, want 30s", cfg.OriginateTimeout)
	}
	if cfg.ResyncInterval != 30*time.Second {
		t.Errorf("ResyncInterval = %s, want 30s", cfg.ResyncInterval)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("WB_LISTEN", "127.0.0.1:9000")
	t.Setenv("WB_ORIGINATE_TIMEOUT", "45s")
	t.Setenv("WB_RESYNC_INTERVAL", "5s")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Listen != "127.0.0.1:9000" {
		t.Errorf("Listen = %q", cfg.Listen)
	}
	if cfg.OriginateTimeout != 45*time.Second {
		t.Errorf("OriginateTimeout = %s", cfg.OriginateTimeout)
	}
	if cfg.ResyncInterval != 5*time.Second {
		t.Errorf("ResyncInterval = %s", cfg.ResyncInterval)
	}
}

func TestLoadConfigReportsEveryMissingVariable(t *testing.T) {
	t.Setenv("WB_AMI_USER", "")
	t.Setenv("WB_AMI_SECRET", "")
	t.Setenv("WB_ROOM", "1000")
	t.Setenv("WB_ORIGINATE_CONTEXT", "conference-out")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig succeeded, want error")
	}
	for _, want := range []string{"WB_AMI_USER", "WB_AMI_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoadConfigRejectsBadDuration(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"WB_ORIGINATE_TIMEOUT", "banana"},
		{"WB_RESYNC_INTERVAL", "-5s"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(tc.name, tc.value)
			if _, err := loadConfig(); err == nil {
				t.Fatalf("loadConfig(%s=%s) succeeded, want error", tc.name, tc.value)
			}
		})
	}
}
