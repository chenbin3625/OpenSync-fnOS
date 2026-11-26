//go:build !linux

package service

import "context"

func watchLocalRootsPlatform(context.Context, []watchLocalRoot, func([]string, bool), func(bool)) {
}
