package explorer

import (
	"fmt"
	"os"
)

type Directory struct {
	Debug       bool
	files       []string
	directories []string
}

type DirectoryOutput struct {
	Success bool
	Message string
	Value   any
}

func (directory Directory) Check(args ...interface{}) DirectoryOutput {
	return DirectoryOutput{Success: true, Message: "Explorer.Directory plugin dependency check"}
}

func (directory Directory) Init() DirectoryOutput {
	return DirectoryOutput{Success: true, Message: "Explorer.Directory plugin initialized"}
}

// Get all files and subdirectories from the current directory
func (directory Directory) Exec(path string) DirectoryOutput {

	entries, err := os.ReadDir("./")
	files := []string{}
	directories := []string{}
	message := ""
	success := true
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				directories = append(directories, e.Name())
			} else {
				files = append(files, e.Name())
			}
		}
	} else {
		success = false
		message = "Error while reading the directory"
	}

	directory.files = files
	directory.directories = directories

	if directory.Debug {
		fmt.Println(directory)
	}

	return DirectoryOutput{Success: success, Message: message, Value: directory}
}

func (directory Directory) Stop() DirectoryOutput {
	return DirectoryOutput{Success: true, Message: "Explorer.Directory plugin stopped", Value: nil}
}

func (directory Directory) Clear() DirectoryOutput {
	return DirectoryOutput{Success: true, Message: "Explorer.Directory plugin cleared", Value: nil}
}
