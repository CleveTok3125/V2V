//go:build !windows

package identity

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/google/renameio/v2"
)

// ownerFor answers who a replacement of path should belong to: the file's
// own owner when it exists, otherwise its directory's (the first write,
// where the directory owner is the service user).
//
// A writer running as a different user must not hand the file over: the
// operator runs v2vctl as root while the server reads as its own user,
// and a root-owned store stays unreadable until someone chowns it.
func ownerFor(path string) (uid, gid int, ok bool) {
	for _, candidate := range []string{path, filepath.Dir(path)} {
		fi, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if st, isStat := fi.Sys().(*syscall.Stat_t); isStat {
			return int(st.Uid), int(st.Gid), true
		}
	}
	return 0, 0, false
}

// writeFileAtomic replaces path through a temporary file, carrying the
// previous owner onto the replacement. A non-root writer cannot chown and
// does not need to: it already owns the result.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	uid, gid, carry := ownerFor(path)

	pending, err := renameio.NewPendingFile(path,
		renameio.WithPermissions(perm),
		renameio.WithExistingPermissions())
	if err != nil {
		return err
	}
	defer pending.Cleanup()

	if _, err := pending.Write(data); err != nil {
		return err
	}
	if carry {
		_ = pending.Chown(uid, gid)
	}
	return pending.CloseAtomicallyReplace()
}
