package overlayruntime

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func settingsDirectoryCaseInsensitive(path string) (bool, error) {
	const pcCaseSensitive = 11
	for {
		sensitive, err := unix.Pathconf(path, pcCaseSensitive)
		if err == nil {
			if sensitive < 0 {
				return false, &os.PathError{Op: "pathconf", Path: path, Err: unix.ENOTSUP}
			}
			return sensitive == 0, nil
		}
		if !os.IsNotExist(err) || path == filepath.Dir(path) {
			return false, &os.PathError{Op: "pathconf", Path: path, Err: err}
		}
		path = filepath.Dir(path)
	}
}
