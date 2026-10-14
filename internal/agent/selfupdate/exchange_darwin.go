package selfupdate

import "golang.org/x/sys/unix"

// exchange swaps two directory entries atomically (renamex_np RENAME_SWAP, APFS and HFS+).
func exchange(a, b string) error { return unix.RenamexNp(a, b, unix.RENAME_SWAP) }
