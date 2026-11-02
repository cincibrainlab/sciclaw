//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openDirectoryAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openOutputMarker(workspace, marker string) (*os.File, error) {
	root, err := os.Open(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(filepath.Join(workspace, "outputs"), string(filepath.Separator)), string(filepath.Separator)) {
		next, err := openDirectoryAt(root, component)
		root.Close()
		if err != nil {
			return nil, err
		}
		root = next
	}
	defer root.Close()
	fd, err := unix.Openat(int(root.Fd()), marker, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), marker), nil
}
