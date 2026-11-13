//go:build !darwin && !linux

package tools

import "os"

func openLocalPath(root *os.Root, name string) (*os.File, error) {
	return nil, os.ErrPermission
}
