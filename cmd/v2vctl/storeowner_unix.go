//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// adoptDirOwner gives path the owner of the directory it sits in, so the
// service reading it later is not left out. The operator runs v2vctl as
// root while the server reads as its own user; a write keeping the
// writer's owner would hand the store to root. The instance data directory
// states who the service is.
//
// Best effort: when writer and directory already agree, nothing is
// touched. A non-root writer cannot chown and needs nothing, owning the
// result already.
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
