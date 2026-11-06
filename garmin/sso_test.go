package garmin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestSubmitMFA checks that the verification code is posted to the right
// endpoint with the CSRF token and form fields Garmin expects.
func TestSubmitMFA(t *testing.T) {
	var gotPath string
	var gotForm url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		r.ParseForm()
		gotForm = r.PostForm
		w.Write([]byte(`<title>Success</title> embed?ticket=ST-123-abc"`))
	}))
	defer srv.Close()

	mfaPage := `<title>Enter MFA code for login</title>
		<input type="hidden" name="_csrf" value="csrf-token-42" />`

	sess := newSSOSession()
	body, err := submitMFA(sess, srv.URL, url.Values{"id": {"gauth-widget"}}, mfaPage,
		func() (string, error) { return " 654321 ", nil })
	if err != nil {
		t.Fatalf("submitMFA: %v", err)
	}

	if gotPath != "/sso/verifyMFA/loginEnterMfaCode" {
		t.Errorf("path: got %q", gotPath)
	}
	if gotForm.Get("mfa-code") != "654321" {
		t.Errorf("mfa-code: got %q (should be trimmed)", gotForm.Get("mfa-code"))
	}
	if gotForm.Get("_csrf") != "csrf-token-42" {
		t.Errorf("_csrf: got %q", gotForm.Get("_csrf"))
	}
	if gotForm.Get("fromPage") != "setupEnterMfaCode" || gotForm.Get("embed") != "true" {
		t.Errorf("unexpected form: %v", gotForm)
	}
	if m := ticketRe.FindStringSubmatch(body); m == nil || m[1] != "ST-123-abc" {
		t.Errorf("ticket not found in response: %q", body)
	}
}

func TestSubmitMFACancelled(t *testing.T) {
	sess := newSSOSession()
	page := `<input name="_csrf" value="x">`

	if _, err := submitMFA(sess, "http://unused", url.Values{}, page,
		func() (string, error) { return "", errors.New("cancelled") }); err == nil {
		t.Error("expected error when the prompt is cancelled")
	}
	if _, err := submitMFA(sess, "http://unused", url.Values{}, page,
		func() (string, error) { return "   ", nil }); err == nil {
		t.Error("expected error for an empty code")
	}
	if _, err := submitMFA(sess, "http://unused", url.Values{}, "<html>no token</html>",
		func() (string, error) { return "1", nil }); err == nil {
		t.Error("expected error when the MFA page has no CSRF token")
	}
}
