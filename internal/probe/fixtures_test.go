package probe

import (
	"encoding/base64"
	"fmt"
)

// ssoLoginPage mirrors the NTUST IdP credential form: an ASP.NET page whose
// anti-forgery token and hidden OIDC context must survive a round trip.
const ssoLoginPage = `<!DOCTYPE html><html><head><title>NTUST SSO</title></head><body>
<form id="loginForm" method="post" action="/account/login">
  <input name="__RequestVerificationToken" type="hidden" value="CFDJ8ABCDEF-token" />
  <input name="Username" type="text" value="" />
  <input name="Password" type="password" value="" />
  <input name="ClientId" type="hidden" value="moodle" />
  <input name="ReturnUrl" type="hidden" value="/connect/authorize/callback?x=1" />
  <input name="Uri" type="hidden" value="" />
  <button type="submit">登入</button>
</form></body></html>`

// ssoLoginPageNoToken is the same wall rendered without a usable anti-forgery
// token — it looks fine to a naive check but no login can succeed against it.
const ssoLoginPageNoToken = `<!DOCTYPE html><html><body>
<form id="loginForm" method="post" action="/account/login">
  <input name="__RequestVerificationToken" type="hidden" value="" />
  <input name="Username" value="" /><input name="Password" value="" />
</form></body></html>`

// oidcBridge is the auto-submitting form that carries the IdP's assertion back
// to the service.
func oidcBridge(action string) string {
	return fmt.Sprintf(`<!DOCTYPE html><html><body onload="document.forms[0].submit()">
<form method="post" action="%s">
  <input type="hidden" name="code" value="AUTHCODE123" />
  <input type="hidden" name="state" value="STATE456" />
  <input type="hidden" name="iss" value="https://sso.example" />
</form></body></html>`, action)
}

// logoutForm is a decoy: a real service page carries one, and following it
// would tear down the session the probe is establishing.
const logoutForm = `<form method="post" action="/auth/logout"><input name="id_token" value="x"/></form>`

func mobileLaunchPage(wstoken string) string {
	payload := base64.StdEncoding.EncodeToString([]byte("SIGNATURE:::" + wstoken + ":::PRIVATE"))
	return `<!DOCTYPE html><html><body><a href="moodlemobile://token=` + payload +
		`">Continue</a></body></html>`
}

const moodleInvalidTokenJSON = `{"exception":"moodle_exception","errorcode":"invalidtoken",` +
	`"message":"Invalid token - token not found"}`

// netscalerChallenge is what the NTUST edge serves to a UA it does not
// recognise: HTTP 200, but no Moodle behind it.
const netscalerChallenge = `<html><head><script>challenge()</script></head><body>Checking your browser</body></html>`
