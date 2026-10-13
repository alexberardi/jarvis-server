//go:build !darwin

package dictate

func nativeMacPoster() MacPoster { return nil }
