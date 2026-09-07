package probe

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// TigerDuckConfig describes the monitored TigerDuck backend.
type TigerDuckConfig struct {
	BaseURL   string
	UserAgent string
	Timeout   time.Duration
}

func (c TigerDuckConfig) url(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + path
}

// --- tigerduck.v3 ---

// TigerDuckV3Probe checks the live API version.
type TigerDuckV3Probe struct {
	Cfg              TigerDuckConfig
	ProbePath        string // default /health
	ExpectedBasePath string // default /v3
}

func (p *TigerDuckV3Probe) Key() string { return "tigerduck.v3" }

// Run checks /health for liveness and then /version for identity.
//
// Health alone is not enough: a rollback or a misconfigured deploy answers
// /health perfectly while serving a different API version than the apps
// expect, which is an outage for every client even though nothing is "down".
func (p *TigerDuckV3Probe) Run(ctx context.Context) Result {
	s, err := NewSession(p.Cfg.Timeout, p.Cfg.UserAgent)
	if err != nil {
		return down(0, 0, ReasonUnreachable)
	}
	started := time.Now()

	health, err := s.GetJSON(ctx, p.Cfg.url(p.ProbePath))
	if err != nil {
		return down(int(time.Since(started).Milliseconds()), 0, classifyTransport(err))
	}
	if health.Status != 200 {
		return down(int(time.Since(started).Milliseconds()), health.Status, classifyStatus(health.Status))
	}
	var hb struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(health.Body), &hb); err != nil || hb.Status != "ok" {
		return down(int(time.Since(started).Milliseconds()), health.Status, ReasonUnexpectedBody)
	}

	version, err := s.GetJSON(ctx, p.Cfg.url("/version"))
	latency := int(time.Since(started).Milliseconds())
	if err != nil {
		return down(latency, 0, classifyTransport(err))
	}
	if version.Status != 200 {
		return down(latency, version.Status, classifyStatus(version.Status))
	}
	var vb struct {
		APIBasePath string `json:"api_base_path"`
	}
	if err := json.Unmarshal([]byte(version.Body), &vb); err != nil {
		return down(latency, version.Status, ReasonUnexpectedBody)
	}
	if vb.APIBasePath != p.ExpectedBasePath {
		return down(latency, version.Status, ReasonUnexpectedBody)
	}
	return up(latency, version.Status)
}
