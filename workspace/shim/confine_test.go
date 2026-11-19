package main

// The probe confine's tests run under Landlock (confine_linux_test.go).

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// confineProbeEnv runs the test binary as the probe: one operation, its
// outcome on stdout ("ok" or "denied: …"), exit 0 either way.
const confineProbeEnv = "SHIM_CONFINE_PROBE"

// confineProbe performs op ("read:<path>", "list:<dir>", "write:<path>",
// "exec:<path>", "kill:<pid>", "confine:<json spec>:<op>").
func confineProbe(op string) {
	kind, arg, _ := strings.Cut(op, ":")
	var err error
	switch kind {
	case "read":
		_, err = os.ReadFile(arg)
	case "list":
		_, err = os.ReadDir(arg)
	case "write":
		err = os.WriteFile(arg, []byte("x"), 0o600)
	case "exec":
		err = exec.Command(arg, "-c", "true").Run()
	case "dial":
		var c net.Conn
		if c, err = net.Dial("unix", arg); err == nil {
			c.Close()
		}
	case "kill":
		pid, _ := strconv.Atoi(arg)
		err = syscall.Kill(pid, 0)
	case "confine":
		// Confine again, wider, then probe: the nested rules intersect.
		spec, inner, _ := strings.Cut(arg, "\x1f")
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "confine", "--spec", spec, "--", exe)
		cmd.Env = append(os.Environ(), confineProbeEnv+"="+inner)
		out, cerr := cmd.CombinedOutput()
		fmt.Print(string(out))
		if cerr != nil {
			fmt.Println("denied:", cerr)
		}
		return
	}
	if err != nil {
		fmt.Println("denied:", err)
		return
	}
	fmt.Println("ok")
}
