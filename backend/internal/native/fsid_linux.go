//go:build linux

package native

import "syscall"

// filesystemID returns the stable kernel filesystem ID (fsid) for path p.
// Unlike st.Dev, which changes across reboots on Btrfs subvolumes and hot-plugged
// USB disks, fsid identifies the underlying filesystem consistently.
func filesystemID(p string) (int32, int32) {
	var sfs syscall.Statfs_t
	if err := syscall.Statfs(p, &sfs); err != nil {
		return 0, 0
	}
	return sfs.Fsid.X__val[0], sfs.Fsid.X__val[1]
}
