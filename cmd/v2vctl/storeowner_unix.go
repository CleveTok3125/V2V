//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// adoptDirOwner gives path the owner of the directory it sits in, so the
// service that reads it later is not left out. The operator runs v2vctl as
// root on the host while the server reads the same instance as its own user;
// a write that keeps the writer's owner would hand the store to root, and the
// instance data directory is the operator's statement of who the service is.
//
// Best effort in the common case: when writer and directory already agree,
// nothing is touched. A writer that is not root cannot chown and does not
// need to, because it already owns the result.
func adoptDirOwner(path string) error {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return nil // nothing was written, so there is nothing to adopt
	}
	fileStat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil
	}
	dirStat, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if fileStat.Uid == dirStat.Uid && fileStat.Gid == dirStat.Gid {
		return nil
	}
	return os.Chown(path, int(dirStat.Uid), int(dirStat.Gid))
}
