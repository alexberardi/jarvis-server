package nodesettings

import "testing"

// A removed Forge test install's secret is forgotten for every user, only in its scope.
func TestForgetSecret(t *testing.T) {
	st, err := OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	u1, u2 := int64(1), int64(2)
	for _, set := range []struct {
		scope string
		uid   *int64
	}{{"user", &u1}, {"user", &u2}, {"integration", nil}} {
		if err := st.SetSecret("TOKEN", "v-"+set.scope, set.scope, "", set.uid); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ForgetSecret("TOKEN", "user"); err != nil {
		t.Fatal(err)
	}
	if st.Secret("TOKEN", "user", &u1) != "" || st.Secret("TOKEN", "user", &u2) != "" || st.Secret("TOKEN", "integration", nil) != "v-integration" {
		t.Fatal("ForgetSecret removed the wrong secrets")
	}
}
