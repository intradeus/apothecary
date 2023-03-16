package cli

import (
	"fmt"
	"os/exec"
	"strings"
)

type Bash struct {
	Debug bool
}

func (b Bash) Check() bool {
	if b.Debug {
		fmt.Println("Checking for Bash shell")
	}

	err := exec.Command("bash", "--version").Run()

	return err == nil
}

func (b Bash) Execute(command ...string) {
	joinedCommand := strings.Join(command, " ")
	cmd := exec.Command("bash", "-c", joinedCommand)
	val, err := cmd.Output()
	if err != nil {
		fmt.Println(err)
	}
	if b.Debug {
		fmt.Println("Command " + joinedCommand + " \n Returned : " + string(val))
	}
}

func (b Bash) Script(detached bool, filepath string, args ...string) {

}
