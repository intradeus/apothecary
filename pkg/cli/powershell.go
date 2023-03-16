package cli

import (
	"fmt"
	"runtime"
)

type Powershell struct {
	Debug bool
}

func (p Powershell) Check() bool {
	if p.Debug {
		fmt.Println("Checking for Powershell")
	}

	return runtime.GOOS != "windows"
}

func (p Powershell) Execute(command ...string) {

}

func (p Powershell) Script(detached bool, filepath string, args ...string) {

}
