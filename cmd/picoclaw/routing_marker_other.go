//go:build !unix

package main

import (
	"errors"
	"os"
)

func openOutputMarker(_, _ string) (*os.File, error) {
	return nil, errors.New("workspace marker verification unavailable")
}
