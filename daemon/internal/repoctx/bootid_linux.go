//go:build linux

package repoctx

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// readBootIdentity uses /proc/stat's btime line — the boot time in seconds since
// the epoch. Preferred over /proc/sys/kernel/random/boot_id because btime is
// what a container sees consistently: boot_id is namespaced on some kernels and
// can differ from the host's without a reboot having happened.
func readBootIdentity() (string, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "btime" {
			return "btime-" + fields[1], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("repoctx: no btime line in /proc/stat")
}
