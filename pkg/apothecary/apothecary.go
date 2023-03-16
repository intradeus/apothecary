package apothecary

// Apothecary class is the controller of the whole CLI.
// It knows where everything is and how to run it.
type Apothecary struct {
	path    Path
	circles []Circle
}

// NewApothecary creates a new Apothecary instance.
func NewApothecary(projectRoot string, configRoot string) *Apothecary {
	// Get Path instance
	//path := GetPathInstance(projectRoot, configRoot)

	// Parse all the config files to get the object structure
	// 1) Get directories and files for circles

	// Get directories from path :
	//GetDirectories(path)
	return &Apothecary{}
}
