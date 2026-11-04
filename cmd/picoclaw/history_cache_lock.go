package main

import (
	"context"
	"os"
	"time"
)

func lockHistoryCache(ctx context.Context, root string) (*os.File, error) {
	f, err := os.OpenFile(root+string(os.PathSeparator)+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		locked, err := tryHistoryCacheLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if locked {
			return f, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
