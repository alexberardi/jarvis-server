package update

// syncDir makes the renames in dir durable: a rename only changes the directory, and until
// that reaches the disk a power cut can undo it (or, with the file's data unsynced, leave a
// zero-length file under the new name). A variable so tests can see where it's called.
var syncDir = syncDirOS
