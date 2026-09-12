package config

import (
	"strings"
	"testing"
)

// withCredentials turns the credentialed probes, and with them the lockout
// guard, on for the configuration under test.
func withCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("NTUST_SSO_ENABLED", "true")
	t.Setenv("NTUST_SSO_USERNAME", "B11234567")
	t.Setenv("NTUST_SSO_PASSWORD", "hunter2")
}

func TestDefaultLoginCapLeavesHeadroomOverTheDefaultSchedule(t *testing.T) {
	withCredentials(t)
	t.Setenv("SSO_MAX_LOGIN_ATTEMPTS_PER_DAY", "")
	t.Setenv("MOODLE_SSO_LOGIN_INTERVAL", "")
	t.Setenv("COURSESELECTION_SSO_LOGIN_INTERVAL", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Hourly checks put 24 attempts in every 24-hour window. A cap of exactly
	// 24 is reached by the schedule alone and skips every 25th check.
	if cfg.MaxLoginAttemptsPerDay <= 24 {
		t.Fatalf("default cap = %d, want more than the 24 attempts hourly checks make", cfg.MaxLoginAttemptsPerDay)
	}
}

func TestACapTheScheduleWouldReachIsRejected(t *testing.T) {
	withCredentials(t)
	t.Setenv("SSO_MAX_LOGIN_ATTEMPTS_PER_DAY", "24")
	t.Setenv("MOODLE_SSO_LOGIN_INTERVAL", "1h")
	t.Setenv("COURSESELECTION_SSO_LOGIN_INTERVAL", "1h")

	_, err := Load("")
	if err == nil || !strings.Contains(err.Error(), "SSO_MAX_LOGIN_ATTEMPTS_PER_DAY") {
		t.Fatalf("err = %v, want a complaint about SSO_MAX_LOGIN_ATTEMPTS_PER_DAY", err)
	}
}

func TestTheCapIsOnlyCheckedWhenLoginChecksRun(t *testing.T) {
	t.Setenv("NTUST_SSO_USERNAME", "")
	t.Setenv("NTUST_SSO_PASSWORD", "")
	t.Setenv("SSO_MAX_LOGIN_ATTEMPTS_PER_DAY", "24")
	t.Setenv("MOODLE_SSO_LOGIN_INTERVAL", "1h")

	if _, err := Load(""); err != nil {
		t.Fatalf("Load: %v; without credentials there are no login attempts to cap", err)
	}
}
