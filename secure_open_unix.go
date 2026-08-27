//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openNoFollow opens the final path component without following a symlink.
// Callers validate each parent component separately before using this helper.
func openNoFollow(name string, flag int, perm os.FileMode) (*os.File, error) {
	// O_NONBLOCK prevents a raced FIFO/device from stalling the plugin while it
	// is being checked for regular-file status by the caller.
	fd, err := unix.Open(name, flag|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if f == nil {
		_ = unix.Close(fd)
		return nil, os.ErrInvalid
	}
	return f, nil
}

// openRepoNoFollow walks from an open repository directory descriptor. This
// closes the check/use race for every path component, not just the final file.
func openRepoNoFollow(root, rel string) (*os.File, error) {
	dirfd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(dirfd, part, flags, 0)
		if err != nil {
			_ = unix.Close(dirfd)
			return nil, err
		}
		_ = unix.Close(dirfd)
		if i == len(parts)-1 {
			name := filepath.Join(root, filepath.FromSlash(rel))
			f := os.NewFile(uintptr(fd), name)
			if f == nil {
				_ = unix.Close(fd)
				return nil, os.ErrInvalid
			}
			return f, nil
		}
		dirfd = fd
	}
	return nil, os.ErrInvalid
}

func chmodPrivateDir(name string, mode os.FileMode) error {
	f, err := openNoFollow(name, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chmod(mode)
}
