package example

// Define your plugin here, and implement all the functions below
// Don't forget to map the name to this structure, in pkg\plugins\plugin.go

type Example struct{}

type ExampleOutput struct {
	Success bool
	Message string
	Value   Example // Or anything you want to return
}

// Check function to validate that the dependencies for this plugin are met.
func (example *Example) Check(args ...any) any {
	return ExampleOutput{Success: true, Message: "example plugin checked"}
}

// Init function to initialize the plugin, if needed.
func (example *Example) Init(args ...any) any {
	return ExampleOutput{Success: true, Message: "example plugin init"}
}

// Exec function to execute the plugin.
func (example *Example) Exec(args ...any) any {
	return ExampleOutput{Success: true, Message: "example plugin run"}
}

// Stop function, to stop the process that the plugin executed.
func (example *Example) Stop(args ...any) any {
	return ExampleOutput{Success: true, Message: "example plugin stopped"}
}

// Clear function, to clear any resources that the plugin used, once the work is done.
func (example *Example) Clear(args ...any) any {
	return ExampleOutput{Success: true, Message: "example plugin cleared"}
}
