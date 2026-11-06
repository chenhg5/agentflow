package store

// WriteAtomicJSON durably replaces a checkpoint, including Windows readers that
// keep the old file open. Callers serialize read/modify/write with their OS lock.
func WriteAtomicJSON(path string, value any) error { return writeCreationJSON(path, value) }

// ReadAtomicJSON uses the same platform sharing contract as run checkpoints.
func ReadAtomicJSON(path string, value any) error { return readJSON(path, value) }
