package i18n

import "testing"

func TestMatch(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   Lang
	}{
		{"traditional Chinese", "zh-TW,zh;q=0.9,en-US;q=0.8", LangZhTW},
		{"Chinese without a region", "zh", LangZhTW},
		{"Hong Kong", "zh-HK,zh;q=0.9", LangZhTW},
		// Simplified readers get Traditional rather than English: it is far
		// closer to what they want, and this page names Taiwanese
		// institutions that have no common Simplified rendering here.
		{"simplified Chinese", "zh-CN,zh;q=0.9", LangZhTW},
		{"English", "en-US,en;q=0.9", LangEn},
		{"unshipped language falls back", "ja-JP,ja;q=0.9", LangEn},
		{"empty header", "", LangEn},
		{"wildcard", "*", LangEn},
		// A stated first preference must win. Honouring the lower-weighted
		// Chinese tag here would override what the visitor actually asked for.
		{"English preferred over Chinese", "en-US,zh-TW;q=0.5", LangEn},
		{"Chinese preferred over English", "zh-TW;q=0.9,en;q=0.8", LangZhTW},
		{"q=0 is an explicit refusal", "zh-TW;q=0,ja;q=0.9", LangEn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Match(tt.header); got != tt.want {
				t.Errorf("Match(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestCataloguesAreSymmetric(t *testing.T) {
	for id := range ZhTW {
		if _, ok := EnUS[id]; !ok {
			t.Errorf("EnUS is missing %q", id)
		}
	}
	for id := range EnUS {
		if _, ok := ZhTW[id]; !ok {
			t.Errorf("ZhTW is missing %q", id)
		}
	}
}

func TestFallbackReturnsTheIDNotBlank(t *testing.T) {
	// A missing string should be obviously wrong on the page rather than an
	// invisible gap nobody notices.
	if got := EnUS.T("no.such.key"); got != "no.such.key" {
		t.Errorf("T(missing) = %q", got)
	}
}
