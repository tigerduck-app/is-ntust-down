package probe

import (
	"net/url"
	"testing"
)

func TestParseFormsExtractsLoginFields(t *testing.T) {
	forms := ParseForms(ssoLoginPage)
	if len(forms) != 1 {
		t.Fatalf("got %d forms, want 1", len(forms))
	}
	f := forms[0]
	if f.ID != "loginForm" {
		t.Errorf("ID = %q, want loginForm", f.ID)
	}
	if f.Action != "/account/login" {
		t.Errorf("Action = %q, want /account/login", f.Action)
	}
	if got := f.Get("__RequestVerificationToken"); got != "CFDJ8ABCDEF-token" {
		t.Errorf("anti-forgery token = %q", got)
	}
	if !f.Has("Username") || !f.Has("Password") {
		t.Error("credential fields missing")
	}
}

func TestFormSetReplacesRatherThanDuplicates(t *testing.T) {
	f := ParseForms(ssoLoginPage)[0]
	f.Set("Username", "B11234567")
	f.Set("Password", "secret")

	var seen int
	for _, fl := range f.Fields {
		if fl.Name == "Username" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("Username appears %d times, want 1", seen)
	}
	if f.Get("Username") != "B11234567" {
		t.Errorf("Username = %q", f.Get("Username"))
	}
	if f.Values().Encode() == "" {
		t.Error("encoded form is empty")
	}
}

func TestFindOIDCBridge(t *testing.T) {
	t.Run("finds the assertion form", func(t *testing.T) {
		f, ok := FindOIDCBridge(ParseForms(oidcBridge("/auth/oidc/")))
		if !ok {
			t.Fatal("bridge not found")
		}
		if f.Get("code") != "AUTHCODE123" {
			t.Errorf("code = %q", f.Get("code"))
		}
	})

	t.Run("skips logout forms", func(t *testing.T) {
		if _, ok := FindOIDCBridge(ParseForms(logoutForm)); ok {
			t.Error("followed a logout form, which would tear down the session")
		}
	})

	t.Run("skips the credential form", func(t *testing.T) {
		if _, ok := FindOIDCBridge(ParseForms(ssoLoginPage)); ok {
			t.Error("mistook the login form for a bridge")
		}
	})
}

func TestIsSSOLoginPageRequiresTheSSOHost(t *testing.T) {
	ssoURL, _ := url.Parse("https://ssoam2.ntust.edu.tw/account/login")
	otherURL, _ := url.Parse("https://courseselection.ntust.edu.tw/ChooseList/D01/D01")

	if !IsSSOLoginPage(ssoLoginPage, ssoURL, "ssoam2.ntust.edu.tw") {
		t.Error("did not recognise the SSO wall")
	}
	// A service page can carry a username field of its own without being the
	// SSO wall; treating it as one would abort a successful login.
	if IsSSOLoginPage(ssoLoginPage, otherURL, "ssoam2.ntust.edu.tw") {
		t.Error("treated a service page as the SSO wall")
	}
}

func TestRedactStripsCredentialMaterial(t *testing.T) {
	in := "https://moodle2.ntust.edu.tw/launch.php?passport=deadbeef&wstoken=abc123&service=x"
	got := Redact(in)
	for _, secret := range []string{"deadbeef", "abc123"} {
		if contains(got, secret) {
			t.Errorf("Redact leaked %q: %s", secret, got)
		}
	}
	if !contains(got, "service=x") {
		t.Errorf("Redact removed non-sensitive context: %s", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
