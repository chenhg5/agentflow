//go:build !windows

package store

import "os"

func openStoreFile(path string) (*os.File, error) { return os.Open(path) }
func replaceStoreFile(from, to string) error      { return os.Rename(from, to) }
