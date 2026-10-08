package update

// ProjectPublicKey is jarvisd's release signing key (id 57B308024B7CF265 as minisign prints it).
// It is jarvisd's own key, separate from the node-setup/admin key (whose password was lost).
// The release workflow signs SHA256SUMS with its secret half (repo secrets
// MINISIGN_SECRET_KEY + MINISIGN_PASSWORD).
const ProjectPublicKey = "RWRl8nxLAgizV2ZlGLPIfxp71+OvcD6PSbdoRy/evF9EbXxpwsQTB3/F"

// extraTrustedKey is an additional trusted key, set only by test builds through
//
//	-ldflags "-X github.com/alexberardi/jarvis-server/internal/update.extraTrustedKey=<key>"
//
// (the CI upgrade job signs its fake releases with a throwaway key). Release builds never set
// it: whoever controls the build flags controls the binary anyway. It is not an environment
// variable on purpose — a runtime knob would let anyone who can set jarvisd's environment
// swap the trust root.
var extraTrustedKey string

// TrustedKeys are the keys a release signature may verify against.
func TrustedKeys() ([]PublicKey, error) {
	k, err := ParsePublicKey(ProjectPublicKey)
	if err != nil {
		return nil, err
	}
	keys := []PublicKey{k}
	if extraTrustedKey != "" {
		x, err := ParsePublicKey(extraTrustedKey)
		if err != nil {
			return nil, err
		}
		keys = append(keys, x)
	}
	return keys, nil
}

// TestKeyBuild reports whether this binary trusts a build-time test key besides the project's.
func TestKeyBuild() bool { return extraTrustedKey != "" }
