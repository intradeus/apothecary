package apothecary

// Manages the paths of files, directories etc. by providing functions to combine contexts, paths and filenames.
// This is a singleton class.

import (
	"path/filepath"
	"strings"
	"sync"
)

var lock = &sync.Mutex{}

type Path struct {
	ProjectRoot string // The root of all your projects. This is where the .apothecary file is located.
	ConfigRoot  string // The root of all your apothecary config files (circles, grimoires, etc.).
}

var pathInstance *Path

// GetInstance returns the single instance of the path struct
func GetPathInstance(projectRoot string, configRoot string) *Path {
	if pathInstance == nil {
		lock.Lock()
		defer lock.Unlock()
		if pathInstance == nil {
			// Creating a new instance
			pathInstance = &Path{ProjectRoot: projectRoot, ConfigRoot: configRoot}
		}
	}
	return pathInstance
}

// GetFullpath returns the full path of a file or directory, after replacing the placeholders and cleaning the path.
func (path Path) GetFullpath(newPath string) string {
	// You can use {PROJECT_ROOT} to refer to the root of all your projects
	// You can use {CONFIG_ROOT} to refer to the root of the apotheary config files
	newPath = strings.Replace(newPath, "{PROJECT_ROOT}", path.ProjectRoot, 1)
	newPath = strings.Replace(newPath, "{CONFIG_ROOT}", path.ConfigRoot, 1)
	newPath = filepath.Clean(newPath)
	return newPath
}
