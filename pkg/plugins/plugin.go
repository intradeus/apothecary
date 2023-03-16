package plugin

import (
	"intradeus/apothecary/pkg/plugins/docker"
	"intradeus/apothecary/pkg/plugins/env"
	"intradeus/apothecary/pkg/plugins/example"
	"intradeus/apothecary/pkg/plugins/explorer"
	"strings"
)

var (
	pluginsMap = map[string]PluginInterface{
		"example.example":    &example.Example{},
		"docker.docker":      &docker.Docker{},
		"docker.volume":      &docker.DockerVolume{},
		"docker.network":     &docker.DockerNetwork{},
		"docker.compose":     &docker.DockerCompose{},
		"env.env":            &env.Env{},
		"explorer.directory": &explorer.Directory{},
		"explorer.file":      &explorer.File{},
		"github.pr":          &github.PR{},
		"github.repo":        &github.Repo{},
	}
)

// Type interface for all execution libraries.
type PluginInterface interface {
	Check(args ...any) any // Check if the plugin and all its dependencies are installed.
	Init(args ...any) any  // Initialize the plugin.
	Exec(args ...any) any  // Exec function to execute the plugin to run a process or fetch some data.
	Stop(args ...any) any  // Stop the execution of a process started by the plugin.
	Clear(args ...any) any // Delete any data created by the plugin.
}

func GetPlugin(name string) PluginInterface {
	switch name {
	case "example.example":
		return &example.Example{}
	case "docker.docker":
		return &docker.Docker{}
	default:
		return nil
	}
}

func GetPlugin2(plugin string) PluginInterface {
	selectedPlugin, ok := pluginsMap[strings.ToLower(plugin)]

	if ok {
		return selectedPlugin
	}
	return nil
}
