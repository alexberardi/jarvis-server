package update

import "os"

// syncDirOS is a no-op: Windows can't flush a directory handle (NTFS journals the rename
// itself), and FlushFileBuffers on one fails.
func syncDirOS(string) error { return nil }

func syncRootDir(*os.Root, string) error { return nil }
