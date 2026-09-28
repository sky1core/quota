package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func invokedExecutable() (string, error) {
	running, err := os.Executable()
	if err != nil {
		return "", err
	}
	return invokedExecutablePath(os.Args[0], running, exec.LookPath), nil
}

func invokedExecutablePath(arg0, running string, lookPath func(string) (string, error)) string {
	candidate := arg0
	if !strings.Contains(candidate, string(os.PathSeparator)) {
		found, err := lookPath(candidate)
		if err != nil && !errors.Is(err, exec.ErrDot) {
			return running
		}
		candidate = found
	}
	if !filepath.IsAbs(candidate) {
		cwd, err := os.Getwd()
		if err != nil {
			return running
		}
		candidate = cwd + string(os.PathSeparator) + candidate
	}
	if !crossesParentDirectory(candidate) {
		candidate = filepath.Clean(candidate)
	}
	invoked, err := os.Stat(candidate)
	if err != nil {
		return running
	}
	actual, err := os.Stat(running)
	if err != nil || !os.SameFile(invoked, actual) {
		return running
	}
	return candidate
}

func crossesParentDirectory(path string) bool {
	for _, element := range strings.Split(path, string(os.PathSeparator)) {
		if element == ".." {
			return true
		}
	}
	return false
}
