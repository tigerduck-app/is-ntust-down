package i18n

import (
	"sort"
	"strconv"
	"strings"
)

// Lang is a supported display language.
type Lang string

const (
	LangZhTW Lang = "zh-TW"
	LangEn   Lang = "en"
)

// DefaultLang is what a request that expresses no usable preference gets.
//
// English rather than Chinese: this page is aimed at NTUST students, but a
// visitor whose browser asks for something else is more likely to read English
// than Traditional Chinese, and being wrong in that direction is recoverable
// with the language switch.
const DefaultLang = LangEn

// HTMLLang is the value for the document's lang attribute.
func (l Lang) HTMLLang() string {
	if l == LangZhTW {
		return "zh-Hant-TW"
	}
	return "en"
}

// Other returns the language the switch offers.
func (l Lang) Other() Lang {
	if l == LangZhTW {
		return LangEn
	}
	return LangZhTW
}

// For returns the catalogue for a language.
func For(l Lang) Catalog {
	if l == LangZhTW {
		return ZhTW
	}
	return EnUS
}

// Parse maps an explicit language request onto a supported language.
func Parse(s string) (Lang, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "zh", "zh-tw", "zh-hant", "zh-hant-tw":
		return LangZhTW, true
	case "en", "en-us", "en-gb":
		return LangEn, true
	}
	return DefaultLang, false
}

// Match picks a language from an Accept-Language header.
//
// Any Chinese variant — Traditional or Simplified, any region — gets zh-TW;
// everything else falls back to English. Simplified-script readers are served
// Traditional deliberately: it is far closer to what they want than English,
// and this page names Taiwanese institutions that have no Simplified form in
// common use here.
//
// Tags are considered in q-value order, so `en-US,zh-TW;q=0.5` gets English:
// the visitor said English first, and honouring a lower-weighted Chinese tag
// would override a stated preference.
func Match(acceptLanguage string) Lang {
	type tag struct {
		name string
		q    float64
		pos  int
	}

	var tags []tag
	for i, part := range strings.Split(acceptLanguage, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, params, _ := strings.Cut(part, ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		q := 1.0
		if _, qv, ok := strings.Cut(params, "q="); ok {
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(qv), 64); err == nil {
				q = parsed
			}
		}
		if q <= 0 {
			// q=0 means "explicitly not this one".
			continue
		}
		tags = append(tags, tag{name: name, q: q, pos: i})
	}

	// Stable by original order within equal q, which is what the header means.
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i].q != tags[j].q {
			return tags[i].q > tags[j].q
		}
		return tags[i].pos < tags[j].pos
	})

	for _, t := range tags {
		if t.name == "*" {
			return DefaultLang
		}
		primary, _, _ := strings.Cut(t.name, "-")
		switch primary {
		case "zh":
			return LangZhTW
		case "en":
			return LangEn
		default:
			// A language we do not ship. English is the fallback, and
			// continuing to scan would let a low-priority zh tag win over a
			// higher-priority language the visitor actually asked for.
			return DefaultLang
		}
	}
	return DefaultLang
}
