package cli

import (
	"os/exec"
	"runtime"
)

// browserCmd is the command that opens url in the person's browser.
func browserCmd(goos, url string) *exec.Cmd {
	switch goos {
	case "darwin":
		return exec.Command("open", url)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	}
	return exec.Command("xdg-open", url)
}

// browserFor is the command that opens a page here. A var for tests.
var browserFor = func(url string) *exec.Cmd { return browserCmd(runtime.GOOS, url) }

// openBrowser opens url without waiting for the browser.
func openBrowser(url string) error {
	cmd := browserFor(url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
