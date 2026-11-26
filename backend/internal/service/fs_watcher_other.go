//go:build !linux

package service

import "context"

func watchLocalRootsPlatform(ctx context.Context, roots []watchLocalRoot, onEvent func(relPath string, overflow bool)) {
	watchLocalRootsPolling(ctx, roots, onEvent)
}
