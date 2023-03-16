package cli

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
)

type WindowsCMD struct {
	Debug bool
}

func (w WindowsCMD) Check() bool {
	if w.Debug {
		fmt.Println("Checking for WindowsCMD")
	}
	return runtime.GOOS != "windows"
}

func (w WindowsCMD) Execute(command ...string) {

}

func (w WindowsCMD) Script(detached bool, file string, args ...string) {
	// Accepted extensions: .bat, .cmd,  .exe, .com
	validExtension := false
	switch filepath.Ext(file) {
	case ".bat", ".cmd", ".exe", ".com":
		validExtension = true
	}

	if validExtension {
		exec.Command(file, args...).Run()
	}

}
