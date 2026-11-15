package x50signupstore

import "os"

// writeFile seeds a store file with arbitrary bytes.
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// removeWrite makes a directory non-writable so the atomic rename fails.
func removeWrite(dir string) error { return os.Chmod(dir, 0o500) }

// addWrite restores directory permissions.
func addWrite(dir string) error { return os.Chmod(dir, 0o700) }
