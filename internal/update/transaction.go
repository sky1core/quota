package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type replacement struct {
	source      string
	destination string
}

type preparedReplacement struct {
	destination string
	staged      string
	backup      string
	original    os.FileInfo
}

func replaceFiles(ctx context.Context, dir string, files []replacement) error {
	work, err := os.MkdirTemp(dir, ".quota-update-")
	if err != nil {
		return err
	}
	prepared, err := prepareReplacements(work, files)
	if err != nil {
		return errors.Join(err, os.RemoveAll(work))
	}
	rollbackFailed, err := commitReplacements(ctx, prepared)
	if rollbackFailed {
		return fmt.Errorf("%w; recovery files retained at %s", err, work)
	}
	return errors.Join(err, os.RemoveAll(work))
}

func prepareReplacements(work string, files []replacement) ([]preparedReplacement, error) {
	prepared := make([]preparedReplacement, 0, len(files))
	for i, file := range files {
		p := preparedReplacement{
			destination: file.destination,
			staged:      filepath.Join(work, fmt.Sprintf("%d.new", i)),
			backup:      filepath.Join(work, fmt.Sprintf("%d.old", i)),
		}
		info, err := os.Lstat(file.destination)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("refusing to replace non-regular file %s", file.destination)
			}
			p.original = info
			if err := os.Link(file.destination, p.backup); err != nil {
				return nil, fmt.Errorf("backing up %s: %w", file.destination, err)
			}
		}
		if err := copyExecutable(file.source, p.staged); err != nil {
			return nil, fmt.Errorf("preparing %s: %w", file.destination, err)
		}
		prepared = append(prepared, p)
	}
	return prepared, nil
}

func copyExecutable(source, destination string) (err error) {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not a regular executable", source)
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	return out.Sync()
}

func commitReplacements(ctx context.Context, files []preparedReplacement) (bool, error) {
	for i, file := range files {
		err := ctx.Err()
		if err == nil {
			err = unchangedDestination(file)
		}
		if err == nil {
			err = os.Rename(file.staged, file.destination)
		}
		if err != nil {
			rollbackErr := rollbackReplacements(files[:i])
			return rollbackErr != nil, errors.Join(fmt.Errorf("replacing %s: %w", file.destination, err), rollbackErr)
		}
	}
	return false, nil
}

func unchangedDestination(file preparedReplacement) error {
	info, err := os.Lstat(file.destination)
	if file.original == nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if file.original == nil || !os.SameFile(info, file.original) || info.Mode() != file.original.Mode() || info.Size() != file.original.Size() || !info.ModTime().Equal(file.original.ModTime()) {
		return fmt.Errorf("destination changed during update: %s", file.destination)
	}
	return nil
}

func rollbackReplacements(files []preparedReplacement) error {
	var errs []error
	for i := len(files) - 1; i >= 0; i-- {
		file := files[i]
		var err error
		if file.original == nil {
			err = os.Remove(file.destination)
		} else {
			err = os.Rename(file.backup, file.destination)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("rollback failed for %s (backup %s): %w", file.destination, file.backup, err))
		}
	}
	return errors.Join(errs...)
}
