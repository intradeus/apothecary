// Package greeter builds greeting lines. Pure functions only — printing is
// cmd/greet's job.
package greeter

import "fmt"

// Greeting returns the standard greeting for name; anonymous callers get a
// generic one.
func Greeting(name string) string {
	if name == "" {
		return "Hello there!"
	}
	return fmt.Sprintf("Hello, %s!", name)
}
