//go:build !darwin

package overlayruntime

import "fmt"

func settingsDirectoryCaseInsensitive(path string) (bool, error) {
	return false, fmt.Errorf("cannot establish future settings case aliases on this platform: %s", path)
}
