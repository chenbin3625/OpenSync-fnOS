//go:build !linux

package native

func filesystemID(string) (int32, int32) {
	return 0, 0
}
