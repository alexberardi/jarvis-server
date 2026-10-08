package update

// ProjectPublicKey is the project's minisign key (id C9A24FB502A25B72 as minisign prints it; its
// bytes in order are 725ba202b54fa2c9, as node-setup's install.sh quotes it): the same key signs the
// jarvis-node-setup and jarvis-admin releases (jarvis-node-setup/install.sh MINISIGN_PUBKEY).
// The release workflow signs SHA256SUMS with its secret half (repo secrets
// MINISIGN_SECRET_KEY + MINISIGN_PASSWORD).
const ProjectPublicKey = "RWRyW6ICtU+iyX4p4RnS24ju0gRsWpxvv6B8pI9G+ZS01q8t8oupAQ8L"

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
