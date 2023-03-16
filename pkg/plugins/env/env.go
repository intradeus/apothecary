package env

// env is a class that manages the environment variables of the project.
// It can read, write and update environment variables from any source.

type Env struct{}

type EnvOutput struct {
	Success bool
	Message string
	Value   any
}

/**
* PluginInterface implementation (public functions)
**/

// Check function to validate that the dependencies for this plugin are met.
func (env *Env) Check(args ...any) any {
	return ExampleOutput{Success: true, Message: "example plugin checked"}
}

// Init function to initialize the plugin, if needed.
func (env *Env) Init(args ...any) any {
	return ExampleOutput{Success: true, Message: "example plugin init"}
}

// Exec function to execute the plugin to run a process or fetch some data.
func (env *Env) Exec(args ...any) any {
	return env.getEnv(args[0].(string), args[1].(string))
}

// Stop function, to stop the process that the plugin executed.
func (env *Env) Stop(args ...any) any {
	return ExampleOutput{Success: true, Message: "example plugin stopped"}
}

// Clear function, to clear any resources that the plugin used, once the work is done.
func (env *Env) Clear(args ...any) any {
	return ExampleOutput{Success: true, Message: "example plugin cleared"}
}

/**
* Other functions
 */

func (env *Env) getEnv(envPath string, envValue string) any {
	return nil
}

func (env *Env) setEnv(envPath string, envValue string) any {
	return nil
}
