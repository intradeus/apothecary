// Type interface for all execution libraries.
package plugins

type Plugin struct {
}

type PluginInterface interface {
	Init()            // Initialize the plugin.
	DependencyCheck() // Check if the plugin and all its dependencies are installed.
	Run()             // Run can be used to either execute a command or get some informations from a plugin.
	Stop()
	Clear()
}
