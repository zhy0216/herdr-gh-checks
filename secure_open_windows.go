//go:build windows

package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Windows' reparse-point checks are performed by validateRepoPath and the
// private state directory checks before opening. Keep this implementation
// separate so Unix can use O_NOFOLLOW without breaking native builds.
func openNoFollow(name string, flag int, perm os.FileMode) (*os.File, error) {
	access := uint32(windows.FILE_WRITE_ATTRIBUTES)
	switch flag & (os.O_WRONLY | os.O_RDWR) {
	case os.O_WRONLY:
		access |= windows.GENERIC_WRITE
	case os.O_RDWR:
		access |= windows.GENERIC_READ | windows.GENERIC_WRITE
	default:
		access |= windows.GENERIC_READ
	}
	if flag&(os.O_CREATE|os.O_TRUNC) != 0 {
		access |= windows.GENERIC_WRITE
	}
	if flag&os.O_APPEND != 0 {
		access &^= windows.GENERIC_WRITE
		access |= windows.FILE_APPEND_DATA
	}

	disposition := uint32(windows.OPEN_EXISTING)
	switch {
	case flag&(os.O_CREATE|os.O_EXCL) == (os.O_CREATE | os.O_EXCL):
		disposition = windows.CREATE_NEW
	case flag&(os.O_CREATE|os.O_TRUNC) == (os.O_CREATE | os.O_TRUNC):
		disposition = windows.CREATE_ALWAYS
	case flag&os.O_CREATE != 0:
		disposition = windows.OPEN_ALWAYS
	case flag&os.O_TRUNC != 0:
		disposition = windows.TRUNCATE_EXISTING
	}
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(name),
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), name)
	if f == nil {
		_ = windows.CloseHandle(h)
		return nil, os.ErrInvalid
	}
	return f, nil
}

func openRepoNoFollow(root, rel string) (*os.File, error) {
	return openNoFollow(filepath.Join(root, filepath.FromSlash(rel)), os.O_RDONLY, 0)
}

func chmodPrivateDir(name string, mode os.FileMode) error {
	// Open the directory itself as a reparse point before changing attributes;
	// os.Chmod(name, ...) would resolve a swapped directory link by pathname.
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(name),
		windows.FILE_READ_ATTRIBUTES|windows.FILE_WRITE_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), name)
	if f == nil {
		_ = windows.CloseHandle(h)
		return os.ErrInvalid
	}
	defer f.Close()
	return f.Chmod(mode)
}
