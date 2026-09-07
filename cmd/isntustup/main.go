// Command isntustup runs the NTUST + TigerDuck status monitor: the probes, the
// history store, and the public page and API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/SamWang8891/is-ntust-down/internal/config"
	"github.com/SamWang8891/is-ntust-down/internal/probe"
	"github.com/SamWang8891/is-ntust-down/internal/scheduler"
	"github.com/SamWang8891/is-ntust-down/internal/store"
	"github.com/SamWang8891/is-ntust-down/internal/web"
)

func main() {
	var (
		envFile   = flag.String("env", ".env", "path to a .env file (ignored when absent)")
		verifySSO = flag.Bool("verify-sso", false, "run the credentialed SSO probes once, print a redacted result, and exit")
	)
	flag.Parse()

	cfg, err := config.Load(*envFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error:\n%v\n", err)
		os.Exit(2)
	}
	log := newLogger(cfg)

	if *verifySSO {
		os.Exit(runVerifySSO(cfg, log))
	}
	if err := run(cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cfg *config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL, cfg.DisplayTimezone)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	if !cfg.CredentialsPresent() {
		// Not fatal: the site and API checks are still worth serving, and
		// refusing to boot would take down the whole page over one optional
		// capability.
		log.Warn("NTUST SSO credentials are not configured — running structural checks only")
	}

	sched := scheduler.New(st, buildJobs(cfg), scheduler.GuardConfig{
		FailureThreshold:  cfg.BreakerFailureThreshold,
		BackoffInitial:    cfg.BreakerBackoffInitial,
		BackoffMax:        cfg.BreakerBackoffMax,
		MaxAttemptsPerDay: cfg.MaxLoginAttemptsPerDay,
	}, log)

	go sched.Run(ctx)
	go sched.RunMaintenance(ctx, scheduler.MaintenanceConfig{
		Interval:            cfg.RollupInterval,
		RawRetentionDays:    cfg.RawRetentionDays,
		DailyRetentionDays:  cfg.DailyRetentionDays,
		HourlyRetentionDays: cfg.HourlyRetentionDays,
		HistoryHours:        cfg.HistoryHours,
	})

	srv, err := web.NewServer(st, web.Options{
		Services:     config.Registry(cfg.RegistryOptions()),
		HistoryDays:  cfg.HistoryDays,
		HistoryHours: cfg.HistoryHours,
		CacheMaxAge:  cfg.APICacheMaxAge,
		RateLimiter:  web.NewRateLimiter(cfg.APIRateLimitPerMinute, cfg.APIRateLimitBurst, cfg.TrustedProxyCIDRs),
		Log:          log,
	})
	if err != nil {
		return fmt.Errorf("web: %w", err)
	}

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr, "env", cfg.AppEnv)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

// buildJobs turns configuration into the probe schedule.
//
// The credentialed SSO probes are the only guarded jobs, and each is gated on
// its own service's structural check: attempting a login while the login page
// is known broken spends the lockout budget to learn something already known.
func buildJobs(cfg *config.Config) []scheduler.Job {
	moodle := probe.MoodleConfig{
		BaseURL:   cfg.MoodleBaseURL,
		SSOHost:   cfg.SSOHost(),
		UserAgent: cfg.MoodleUserAgent,
		Timeout:   cfg.ProbeTimeout,
		Username:  cfg.SSOUsername,
		Password:  cfg.SSOPassword,
	}
	courses := probe.CourseSelectionConfig{
		BaseURL:   cfg.CourseSelectionBaseURL,
		ProbePath: cfg.CourseSelectionProbePath,
		SSOHost:   cfg.SSOHost(),
		UserAgent: cfg.MoodleUserAgent,
		Timeout:   cfg.ProbeTimeout,
		Username:  cfg.SSOUsername,
		Password:  cfg.SSOPassword,
	}
	mail := probe.MailConfig{
		BaseURL:   cfg.MailBaseURL,
		LoginPath: cfg.MailLoginPath,
		UserAgent: cfg.MoodleUserAgent,
		Timeout:   cfg.ProbeTimeout,
	}
	if cfg.MailSMTPEnabled {
		mail.SMTPHosts = cfg.MailSMTPHosts
	}
	tigerduck := probe.TigerDuckConfig{
		BaseURL:   cfg.TigerDuckBaseURL,
		UserAgent: cfg.MoodleUserAgent,
		Timeout:   cfg.ProbeTimeout,
	}
	if !cfg.CredentialsPresent() {
		moodle.Username, moodle.Password = "", ""
		courses.Username, courses.Password = "", ""
	}

	jobs := []scheduler.Job{
		{Probe: &probe.MoodleSiteProbe{Cfg: moodle}, Interval: cfg.MoodleSiteInterval},
		{Probe: &probe.MoodleSSOStructuralProbe{Cfg: moodle}, Interval: cfg.MoodleSSOStructuralInterval},
		{Probe: &probe.CourseSelectionSiteProbe{Cfg: courses}, Interval: cfg.CourseSelectionSiteInterval},
		{Probe: &probe.CourseSelectionSSOStructuralProbe{Cfg: courses}, Interval: cfg.CourseSelectionSSOStructuralInterval},
		{Probe: &probe.MailSiteProbe{Cfg: mail}, Interval: cfg.MailSiteInterval},
		{Probe: &probe.MailLoginPageProbe{Cfg: mail}, Interval: cfg.MailLoginPageInterval},
		{Probe: &probe.QueryCourseProbe{
			URL: cfg.QueryCourseSemestersURL, UserAgent: cfg.MoodleUserAgent, Timeout: cfg.ProbeTimeout,
		}, Interval: cfg.QueryCourseInterval},
		{Probe: &probe.TigerDuckV3Probe{
			Cfg: tigerduck, ProbePath: cfg.TigerDuckV3ProbePath, ExpectedBasePath: "/v3",
		}, Interval: cfg.TigerDuckInterval},
	}

	if cfg.MailSMTPEnabled {
		jobs = append(jobs, scheduler.Job{
			Probe: &probe.MailSMTPProbe{Cfg: mail}, Interval: cfg.MailSMTPInterval,
		})
	}

	moodleLogin := &probe.MoodleSSOLoginProbe{Cfg: moodle}
	coursesLogin := &probe.CourseSelectionSSOLoginProbe{Cfg: courses}

	if cfg.CredentialsPresent() {
		jobs = append(jobs,
			scheduler.Job{Probe: moodleLogin, Interval: cfg.MoodleSSOLoginInterval,
				Guarded: true, GateCheckKey: "moodle.sso_page"},
			scheduler.Job{Probe: coursesLogin, Interval: cfg.CourseSelectionSSOLoginInterval,
				Guarded: true, GateCheckKey: "courseselection.sso_page"},
		)
		return jobs
	}

	// Unconfigured probes make no network call and report `not_configured`.
	// Running them unguarded is what puts that explanation on the page,
	// instead of a bare "unknown" the reader cannot interpret.
	return append(jobs,
		scheduler.Job{Probe: moodleLogin, Interval: cfg.MoodleSSOLoginInterval},
		scheduler.Job{Probe: coursesLogin, Interval: cfg.CourseSelectionSSOLoginInterval},
	)
}

// runVerifySSO performs one deliberate credentialed attempt against each
// SSO-backed service and reports the outcome, so credentials can be confirmed
// before the scheduler ever starts polling.
func runVerifySSO(cfg *config.Config, log *slog.Logger) int {
	if !cfg.CredentialsPresent() {
		fmt.Fprintln(os.Stderr,
			"NTUST_SSO_USERNAME / NTUST_SSO_PASSWORD are not set (or NTUST_SSO_ENABLED is false).")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	probes := []probe.Probe{
		&probe.MoodleSSOLoginProbe{Cfg: probe.MoodleConfig{
			BaseURL: cfg.MoodleBaseURL, SSOHost: cfg.SSOHost(), UserAgent: cfg.MoodleUserAgent,
			Timeout: cfg.ProbeTimeout, Username: cfg.SSOUsername, Password: cfg.SSOPassword,
		}},
		&probe.CourseSelectionSSOLoginProbe{Cfg: probe.CourseSelectionConfig{
			BaseURL: cfg.CourseSelectionBaseURL, ProbePath: cfg.CourseSelectionProbePath,
			SSOHost: cfg.SSOHost(), UserAgent: cfg.MoodleUserAgent, Timeout: cfg.ProbeTimeout,
			Username: cfg.SSOUsername, Password: cfg.SSOPassword,
		}},
	}

	fmt.Printf("Verifying SSO as %s (one attempt per service)\n\n", maskUsername(cfg.SSOUsername))
	exit := 0
	for _, p := range probes {
		res := p.Run(ctx)
		mark := "FAIL"
		if res.State == probe.StateUp {
			mark = "OK"
		} else {
			exit = 1
		}
		detail := string(res.ReasonCode)
		if detail == "" {
			detail = "-"
		}
		fmt.Printf("  %-4s %-28s state=%s reason=%s latency=%dms\n",
			mark, p.Key(), res.State, detail, res.LatencyMS)
	}

	if exit != 0 {
		fmt.Fprintln(os.Stderr,
			"\nA rejected credential will suspend automatic login checks as soon as the service starts.")
	}
	return exit
}

// maskUsername keeps enough of the student id to confirm which account was
// used without printing it in full.
func maskUsername(u string) string {
	if len(u) <= 3 {
		return strings.Repeat("*", len(u))
	}
	return u[:3] + strings.Repeat("*", len(u)-3)
}

func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
