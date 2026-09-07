package config

// The service and check registry is code, not configuration.
//
// Only URLs, paths, and cadences come from the environment. If services were
// env-driven too, a typo in .env could silently drop a row from a public
// status page — the page would look healthy simply because it had stopped
// asking the question.

// Check is one question asked of one service.
type Check struct {
	Key    string
	NameID string // i18n message id
}

// Service is one row on the status page.
type Service struct {
	Key    string
	NameID string
	DescID string
	Checks []Check
}

// RegistryOptions selects the checks whose presence depends on configuration.
type RegistryOptions struct {
	// IncludeMailSMTP adds the MX transport check. It is opt-in because many
	// hosting providers block outbound port 25, which would report NTUST as
	// down for reasons that have nothing to do with NTUST.
	IncludeMailSMTP bool
}

// Registry returns every service in display order.
func Registry(opts RegistryOptions) []Service {
	mail := Service{
		Key:    "mail",
		NameID: "service.mail.name",
		DescID: "service.mail.desc",
		Checks: []Check{
			{Key: "mail.site", NameID: "check.site"},
			{Key: "mail.login_page", NameID: "check.login_page"},
		},
	}
	if opts.IncludeMailSMTP {
		mail.Checks = append(mail.Checks, Check{Key: "mail.smtp", NameID: "check.smtp"})
	}

	return []Service{
		{
			Key:    "moodle",
			NameID: "service.moodle.name",
			DescID: "service.moodle.desc",
			Checks: []Check{
				{Key: "moodle.site", NameID: "check.site"},
				{Key: "moodle.sso_page", NameID: "check.sso_page"},
				{Key: "moodle.sso_login", NameID: "check.sso_login"},
			},
		},
		{
			Key:    "courseselection",
			NameID: "service.courseselection.name",
			DescID: "service.courseselection.desc",
			Checks: []Check{
				{Key: "courseselection.site", NameID: "check.site"},
				{Key: "courseselection.sso_page", NameID: "check.sso_page"},
				{Key: "courseselection.sso_login", NameID: "check.sso_login"},
			},
		},
		mail,
		{
			Key:    "querycourse",
			NameID: "service.querycourse.name",
			DescID: "service.querycourse.desc",
			Checks: []Check{
				{Key: "querycourse.api", NameID: "check.api"},
			},
		},
		{
			Key:    "tigerduck-v3",
			NameID: "service.tigerduck_v3.name",
			DescID: "service.tigerduck_v3.desc",
			Checks: []Check{
				{Key: "tigerduck.v3", NameID: "check.api"},
			},
		},
	}
}

// AllCheckKeys returns every check key in registry order.
func AllCheckKeys(opts RegistryOptions) []string {
	var keys []string
	for _, svc := range Registry(opts) {
		for _, c := range svc.Checks {
			keys = append(keys, c.Key)
		}
	}
	return keys
}
