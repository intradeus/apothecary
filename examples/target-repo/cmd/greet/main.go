// greet prints a greeting for the name given as the first argument.
package main

import (
	"fmt"
	"os"

	"example.local/greeter"
)

func main() {
	name := ""
	if len(os.Args) > 1 {
		name = os.Args[1]
	}
	fmt.Println(greeter.Greeting(name))
}
