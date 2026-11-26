//go:build !linux

package service

import "context"

func watchLocalRootsPlatform(ctx context.Context, roots []watchLocalRoot, onEvents func(paths []string, fullScan bool)) {
	watchLocalRootsPolling(ctx, roots, onEvents)
}
