//go:build !aix && !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !solaris && !windows

package main

import (
	"os"
	"path/filepath"
)

func openNoFollow(name string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(name, flag, perm)
}

func openRepoNoFollow(root, rel string) (*os.File, error) {
	return openNoFollow(filepath.Join(root, filepath.FromSlash(rel)), os.O_RDONLY, 0)
}

func chmodPrivateDir(name string, mode os.FileMode) error {
	return os.Chmod(name, mode)
}
