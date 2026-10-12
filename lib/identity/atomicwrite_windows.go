//go:build windows

package identity

import (
	"io/fs"

	"github.com/google/renameio/v2/maybe"
)

// writeFileAtomic falls back to the plain write on Windows, where atomic
// replace is not reliably available. There is no owner to carry: the
// operator and the service run as the same user.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	return maybe.WriteFile(path, data, perm)
}
