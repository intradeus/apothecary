package apothecary

// Apothecary class is the controller of the whole CLI.
// It knows where everything is and how to run it.
type Apothecary struct {
	path    Path
	circles []Circle
}
