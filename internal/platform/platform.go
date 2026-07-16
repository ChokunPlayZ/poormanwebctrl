package platform

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"
)

type Platform struct {
	OS     string
	Distro string
	Family string
}

func Detect() (Platform, error) {
	if runtime.GOOS != "linux" {
		return Platform{}, fmt.Errorf("provisioning is currently supported on Linux only (detected %s)", runtime.GOOS)
	}
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return Platform{}, fmt.Errorf("detect Linux distribution: %w", err)
	}
	defer f.Close()
	values := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		key, value, ok := strings.Cut(s.Text(), "=")
		if ok {
			values[key] = strings.Trim(value, `"`)
		}
	}
	if err := s.Err(); err != nil {
		return Platform{}, err
	}
	id := values["ID"]
	like := values["ID_LIKE"]
	family := ""
	switch {
	case id == "debian" || id == "ubuntu" || strings.Contains(like, "debian"):
		family = "debian"
	case id == "rhel" || id == "fedora" || id == "centos" || id == "rocky" || id == "almalinux" || strings.Contains(like, "rhel"):
		family = "rhel"
	case id == "alpine":
		family = "alpine"
	default:
		return Platform{}, fmt.Errorf("unsupported Linux distribution %q", id)
	}
	return Platform{OS: "linux", Distro: id, Family: family}, nil
}
