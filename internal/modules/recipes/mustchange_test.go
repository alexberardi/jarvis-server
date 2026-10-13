package recipes

import (
	"strings"
	"testing"
)

// G8: a must-change-password token is 403 password_change_required, not a 500.
func TestMustChangePasswordToken(t *testing.T) {
	e := setup(t)
	code, body := e.do(t, "GET", "/recipes", nil, "Authorization", "Bearer must-change")
	if code != 403 || !strings.Contains(body, `"code":"password_change_required"`) {
		t.Fatalf("%d %s", code, body)
	}
}
