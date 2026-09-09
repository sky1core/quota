package keepalive

import (
	"errors"
	"strconv"
	"strings"
)

var (
	claudeMinVersion = mustParseVersion("2.1.259")
	codexMinVersion  = mustParseVersion("0.153.4")
)

type version [3]int

func mustParseVersion(s string) version {
	v, err := parseVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

func parseVersion(s string) (version, error) {
	var v version
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, errors.New("version must have three numeric components")
	}
	for i, p := range parts {
		if p == "" {
			return v, errors.New("version component is empty")
		}
		if len(p) > 1 && p[0] == '0' {
			return v, errors.New("version component has a leading zero")
		}
		for j := 0; j < len(p); j++ {
			if p[j] < '0' || p[j] > '9' {
				return v, errors.New("version component is not numeric")
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, errors.New("version component is out of range")
		}
		v[i] = n
	}
	return v, nil
}

func versionAtLeast(s string, min version) bool {
	v, err := parseVersion(s)
	if err != nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if v[i] != min[i] {
			return v[i] > min[i]
		}
	}
	return true
}
