package agentskills

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func checkDirectories(path string) error {
	parent := filepath.Dir(path)
	if parent != path {
		if err := checkDirectories(parent); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: expected a directory, symlinks are not supported", path)
	}
	return nil
}

func validateTree(path string) error {
	if err := checkDirectories(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: skill is not a regular directory", path)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err = root.Lstat("SKILL.md")
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%s: missing or non-regular SKILL.md", path)
	}
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("%s: symlinks and special files are not supported", filepath.Join(path, name))
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return fmt.Errorf("%s: unsupported permissions", filepath.Join(path, name))
		}
		return nil
	})
	return err
}

func copyTree(source, destination string) error {
	if err := checkDirectories(filepath.Dir(destination)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	src, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer dst.Close()
	type directory struct {
		name string
		mode fs.FileMode
	}
	var directories []directory
	err = fs.WalkDir(src.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := src.Lstat(name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name != "." {
				if err := dst.Mkdir(name, 0o700); err != nil {
					return err
				}
			}
			directories = append(directories, directory{name, info.Mode().Perm()})
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: source changed to a non-regular file", name)
		}
		input, err := src.Open(name)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := dst.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		if copyErr == nil {
			copyErr = output.Chmod(info.Mode().Perm())
		}
		if copyErr == nil {
			copyErr = output.Sync()
		}
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := dst.Chmod(directories[i].name, directories[i].mode); err != nil {
			return err
		}
	}
	return nil
}
