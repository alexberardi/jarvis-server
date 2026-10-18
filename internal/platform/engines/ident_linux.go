package engines

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// procIdentity returns pid's executable and start time (clock ticks since boot, field 22 of
// /proc/<pid>/stat). The exe link is unreadable for a zombie.
func procIdentity(pid int) (exe, start string, err error) {
	p := "/proc/" + strconv.Itoa(pid)
	exe, err = os.Readlink(p + "/exe")
	if err != nil {
		return "", "", err
	}
	stat, err := os.ReadFile(p + "/stat")
	if err != nil {
		return "", "", err
	}
	// The command name (field 2) is parenthesised and may hold spaces; count from its end.
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return "", "", errors.New("malformed " + p + "/stat")
	}
	fields := bytes.Fields(stat[i+1:])
	if len(fields) < 20 {
		return "", "", fmt.Errorf("malformed %s/stat", p)
	}
	return exe, string(fields[19]), nil // field 22 overall
}
