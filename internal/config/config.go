// Package config parses the environment into a validated Config. Every target
// URL, cadence, and limit lives here so nothing is hardcoded in a probe.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv   string
	HTTPAddr string
	LogLevel string

	DatabaseURL     string
	DisplayTimezone string

	RawRetentionDays    int
	DailyRetentionDays  int
	RollupInterval      time.Duration
	HistoryDays         int
	HistoryHours        int
	HourlyRetentionDays int

	ProbeTimeout    time.Duration
	MoodleUserAgent string

	SSOBaseURL  string
	SSOEnabled  bool
	SSOUsername string
	SSOPassword string

	BreakerFailureThreshold int
	BreakerBackoffInitial   time.Duration
	BreakerBackoffMax       time.Duration
	MaxLoginAttemptsPerDay  int

	MoodleBaseURL               string
	MoodleSiteInterval          time.Duration
	MoodleSSOStructuralInterval time.Duration
	MoodleSSOLoginInterval      time.Duration

	CourseSelectionBaseURL               string
	CourseSelectionProbePath             string
	CourseSelectionSiteInterval          time.Duration
	CourseSelectionSSOStructuralInterval time.Duration
	CourseSelectionSSOLoginInterval      time.Duration

	MailBaseURL           string
	MailLoginPath         string
	MailSiteInterval      time.Duration
	MailLoginPageInterval time.Duration
	MailSMTPEnabled       bool
	MailSMTPHosts         []string
	MailSMTPInterval      time.Duration

	QueryCourseSemestersURL string
	QueryCourseInterval     time.Duration

	TigerDuckBaseURL     string
	TigerDuckV3ProbePath string
	TigerDuckInterval    time.Duration

	APIRateLimitPerMinute int
	APIRateLimitBurst     int
	APICacheMaxAge        time.Duration
	TrustedProxyCIDRs     []*net.IPNet
}

// RegistryOptions selects which optional checks appear on the page.
func (c *Config) RegistryOptions() RegistryOptions {
	return RegistryOptions{IncludeMailSMTP: c.MailSMTPEnabled}
}

// SSOHost is the IdP hostname the probes compare against when deciding
// whether they are looking at the credential wall.
func (c *Config) SSOHost() string {
	u, err := url.Parse(c.SSOBaseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// CredentialsPresent reports whether credentialed SSO probing can run at all.
func (c *Config) CredentialsPresent() bool {
	return c.SSOEnabled && c.SSOUsername != "" && c.SSOPassword != ""
}

// Load reads .env (when present) and then the process environment, and
// validates the result.
//
// Real environment variables always win over .env, so a container's
// orchestrator can override a file baked into the image.
func Load(dotenvPath string) (*Config, error) {
	if err := loadDotEnv(dotenvPath); err != nil {
		return nil, err
	}

	l := &loader{}
	c := &Config{
		AppEnv:   l.str("APP_ENV", "production"),
		HTTPAddr: l.str("HTTP_ADDR", ":8080"),
		LogLevel: l.str("LOG_LEVEL", "info"),

		DatabaseURL:     l.str("DATABASE_URL", "postgres://isntustup:isntustup@localhost:5432/isntustup?sslmode=disable"),
		DisplayTimezone: l.str("DISPLAY_TIMEZONE", "Asia/Taipei"),

		RawRetentionDays:    l.intVal("RAW_RESULT_RETENTION_DAYS", 14),
		DailyRetentionDays:  l.intVal("DAILY_RETENTION_DAYS", 400),
		RollupInterval:      l.dur("ROLLUP_INTERVAL", 5*time.Minute),
		HistoryDays:         l.intVal("HISTORY_DAYS", 90),
		HistoryHours:        l.intVal("HISTORY_HOURS", 24),
		HourlyRetentionDays: l.intVal("HOURLY_RETENTION_DAYS", 30),

		ProbeTimeout: l.dur("PROBE_TIMEOUT", 15*time.Second),
		MoodleUserAgent: l.str("MOODLE_USER_AGENT",
			"Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) AppleWebKit/605.1.15 "+
				"(KHTML, like Gecko) Mobile/15E148 MoodleMobile 5.1.1 (51100)"),

		SSOBaseURL:  l.url("NTUST_SSO_BASE_URL", "https://ssoam2.ntust.edu.tw"),
		SSOEnabled:  l.boolVal("NTUST_SSO_ENABLED", true),
		SSOUsername: l.str("NTUST_SSO_USERNAME", ""),
		SSOPassword: l.str("NTUST_SSO_PASSWORD", ""),

		BreakerFailureThreshold: l.intVal("SSO_BREAKER_FAILURE_THRESHOLD", 2),
		BreakerBackoffInitial:   l.dur("SSO_BREAKER_BACKOFF_INITIAL", time.Hour),
		BreakerBackoffMax:       l.dur("SSO_BREAKER_BACKOFF_MAX", 12*time.Hour),
		MaxLoginAttemptsPerDay:  l.intVal("SSO_MAX_LOGIN_ATTEMPTS_PER_DAY", 24),

		MoodleBaseURL:               l.url("MOODLE_BASE_URL", "https://moodle2.ntust.edu.tw"),
		MoodleSiteInterval:          l.dur("MOODLE_SITE_INTERVAL", time.Minute),
		MoodleSSOStructuralInterval: l.dur("MOODLE_SSO_STRUCTURAL_INTERVAL", 5*time.Minute),
		MoodleSSOLoginInterval:      l.dur("MOODLE_SSO_LOGIN_INTERVAL", time.Hour),

		CourseSelectionBaseURL:               l.url("COURSESELECTION_BASE_URL", "https://courseselection.ntust.edu.tw"),
		CourseSelectionProbePath:             l.str("COURSESELECTION_PROBE_PATH", "/ChooseList/D01/D01"),
		CourseSelectionSiteInterval:          l.dur("COURSESELECTION_SITE_INTERVAL", time.Minute),
		CourseSelectionSSOStructuralInterval: l.dur("COURSESELECTION_SSO_STRUCTURAL_INTERVAL", 5*time.Minute),
		CourseSelectionSSOLoginInterval:      l.dur("COURSESELECTION_SSO_LOGIN_INTERVAL", time.Hour),

		MailBaseURL:           l.url("MAIL_BASE_URL", "https://mail.ntust.edu.tw"),
		MailLoginPath:         l.str("MAIL_LOGIN_PATH", "/cgi-bin/login?index=1"),
		MailSiteInterval:      l.dur("MAIL_SITE_INTERVAL", time.Minute),
		MailLoginPageInterval: l.dur("MAIL_LOGIN_PAGE_INTERVAL", 5*time.Minute),
		MailSMTPEnabled:       l.boolVal("MAIL_SMTP_CHECK_ENABLED", false),
		MailSMTPHosts:         l.list("MAIL_SMTP_HOSTS", "mg1.ntust.edu.tw:25,mg2.ntust.edu.tw:25"),
		MailSMTPInterval:      l.dur("MAIL_SMTP_INTERVAL", 5*time.Minute),

		QueryCourseSemestersURL: l.url("QUERYCOURSE_SEMESTERS_URL", "https://querycourse.ntust.edu.tw/QueryCourse/api/semestersinfo"),
		QueryCourseInterval:     l.dur("QUERYCOURSE_INTERVAL", 5*time.Minute),

		TigerDuckBaseURL:     l.url("TIGERDUCK_BASE_URL", "https://api.tigerduck.app"),
		TigerDuckV3ProbePath: l.str("TIGERDUCK_V3_PROBE_PATH", "/health"),
		TigerDuckInterval:    l.dur("TIGERDUCK_INTERVAL", time.Minute),

		APIRateLimitPerMinute: l.intVal("API_RATE_LIMIT_PER_MINUTE", 60),
		APIRateLimitBurst:     l.intVal("API_RATE_LIMIT_BURST", 20),
		APICacheMaxAge:        l.dur("API_CACHE_MAX_AGE", 30*time.Second),
		TrustedProxyCIDRs:     l.cidrs("TRUSTED_PROXY_CIDRS", ""),
	}

	if _, err := time.LoadLocation(c.DisplayTimezone); err != nil {
		l.errs = append(l.errs, fmt.Errorf("DISPLAY_TIMEZONE %q is not a known timezone", c.DisplayTimezone))
	}
	if c.HistoryDays < 1 || c.HistoryDays > 400 {
		l.errs = append(l.errs, errors.New("HISTORY_DAYS must be between 1 and 400"))
	}
	if c.HistoryHours < 1 || c.HistoryHours > 168 {
		l.errs = append(l.errs, errors.New("HISTORY_HOURS must be between 1 and 168 (one week)"))
	}
	// Hourly buckets outlive the raw rows they came from, but they can only be
	// recomputed while those rows exist. Keeping them for less time than the
	// strip displays would leave permanent gaps at the left edge.
	if c.HourlyRetentionDays < 1 {
		l.errs = append(l.errs, errors.New("HOURLY_RETENTION_DAYS must be at least 1"))
	}

	// Every problem is reported at once. Fixing one variable per restart is a
	// miserable way to configure a service.
	if len(l.errs) > 0 {
		return nil, errors.Join(l.errs...)
	}
	return c, nil
}

// --- env helpers ---

type loader struct{ errs []error }

func (l *loader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func (l *loader) url(key, def string) string {
	raw := l.str(key, def)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		l.errs = append(l.errs, fmt.Errorf("%s is not an absolute URL: %q", key, raw))
	}
	return strings.TrimRight(raw, "/")
}

func (l *loader) intVal(key string, def int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s is not an integer: %q", key, raw))
		return def
	}
	return n
}

func (l *loader) dur(key string, def time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s is not a duration (try 30s, 5m, 1h): %q", key, raw))
		return def
	}
	if d <= 0 {
		l.errs = append(l.errs, fmt.Errorf("%s must be positive: %q", key, raw))
		return def
	}
	return d
}

func (l *loader) boolVal(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s is not a boolean: %q", key, raw))
		return def
	}
	return b
}

// list splits a comma-separated value, dropping blanks.
func (l *loader) list(key, def string) []string {
	raw := l.str(key, def)
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// cidrs parses the trusted proxy list. An empty list means trust nothing and
// use the peer address, which is the safe default: trusting X-Forwarded-For
// unconditionally lets any client spoof its identity and walk straight past
// the rate limiter.
func (l *loader) cidrs(key, def string) []*net.IPNet {
	raw := l.str(key, def)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, network, err := net.ParseCIDR(part)
		if err != nil {
			l.errs = append(l.errs, fmt.Errorf("%s contains an invalid CIDR: %q", key, part))
			continue
		}
		out = append(out, network)
	}
	return out
}

// loadDotEnv applies a .env file without overriding variables already set in
// the environment. A missing file is not an error — production supplies real
// environment variables.
func loadDotEnv(path string) error {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
