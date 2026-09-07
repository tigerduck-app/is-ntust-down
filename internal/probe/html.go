package probe

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Field is one form input. Order is preserved so a re-POSTed form goes back
// looking as close to the original as possible.
type Field struct{ Name, Value string }

// Form is a parsed HTML <form> and its <input> descendants.
type Form struct {
	ID     string
	Action string
	Fields []Field
}

func (f *Form) Get(name string) string {
	for _, fl := range f.Fields {
		if fl.Name == name {
			return fl.Value
		}
	}
	return ""
}

func (f *Form) Has(name string) bool {
	for _, fl := range f.Fields {
		if fl.Name == name {
			return true
		}
	}
	return false
}

// Set replaces the first field with this name, appending it when absent.
func (f *Form) Set(name, value string) {
	for i := range f.Fields {
		if f.Fields[i].Name == name {
			f.Fields[i].Value = value
			return
		}
	}
	f.Fields = append(f.Fields, Field{Name: name, Value: value})
}

func (f *Form) Values() url.Values {
	v := url.Values{}
	for _, fl := range f.Fields {
		v.Set(fl.Name, fl.Value)
	}
	return v
}

// ParseForms extracts every form on the page.
//
// This uses a real HTML parser rather than the regex scraping the mobile
// clients use. The SSO pages are server-rendered ASP.NET whose markup shifts
// between releases; a regex that assumes double-quoted attributes silently
// stops finding the login form when someone changes a template, and a status
// page that reports "SSO broken" because its own parser broke is worse than
// useless.
func ParseForms(body string) []Form {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}

	var forms []Form
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "form" {
			forms = append(forms, buildForm(n))
			// Forms cannot legally nest; the parser has already flattened any
			// attempt, so there is nothing to find below this node.
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return forms
}

func buildForm(n *html.Node) Form {
	f := Form{ID: attr(n, "id"), Action: attr(n, "action")}
	var collect func(*html.Node)
	collect = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "input" {
			if name := attr(node, "name"); name != "" {
				f.Fields = append(f.Fields, Field{Name: name, Value: attr(node, "value")})
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(n)
	return f
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// FindLoginForm returns the NTUST SSO credential form.
func FindLoginForm(forms []Form) (Form, bool) {
	for _, f := range forms {
		if strings.EqualFold(f.ID, "loginForm") {
			return f, true
		}
	}
	for _, f := range forms {
		if f.Has("Username") && f.Has("Password") {
			return f, true
		}
	}
	return Form{}, false
}

// FindOIDCBridge returns the auto-submitting form that carries an identity
// assertion from the IdP back to the service.
//
// The exclusions matter as much as the match: a logout form or the credential
// form itself would also POST somewhere useful-looking, and following one
// would tear down the session we are in the middle of establishing.
func FindOIDCBridge(forms []Form) (Form, bool) {
	for _, f := range forms {
		if f.Action == "" || strings.Contains(strings.ToLower(f.Action), "logout") {
			continue
		}
		if len(f.Fields) == 0 || f.Has("Username") || f.Has("Password") {
			continue
		}
		isBridge := (f.Has("code") && f.Has("state") && f.Has("iss")) ||
			f.Has("id_token") ||
			f.Has("SAMLResponse")
		if isBridge {
			return f, true
		}
	}
	return Form{}, false
}

// IsSSOLoginPage reports whether we are sitting at the credential prompt.
// The host check is required: a service page can legitimately contain a
// username field without being the SSO wall.
func IsSSOLoginPage(body string, u *url.URL, ssoHost string) bool {
	if u == nil || !strings.Contains(u.Host, ssoHost) {
		return false
	}
	_, ok := FindLoginForm(ParseForms(body))
	return ok
}
