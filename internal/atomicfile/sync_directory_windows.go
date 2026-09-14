package atomicfile

// Go's portable file API cannot fsync Windows directory handles. File contents
// are synced before publication, but directory durability is not confirmed here.
func syncDirectory(string) error { return nil }
