package docker

import "intradeus/apothecary/pkg/cli"

// Wrapper for docker volumes commands as a plugin.
type DockerVolume struct {
	Selection string
}

type DockerVolumeOutput struct {
	Success bool
	Message string
	Value   DockerVolume
}

func (example *DockerVolume) Check(args ...any) interface{} {
	var message string
	var success bool
	if cli.GetShell("").Where("docker") {
		success = true
	}

	return DockerVolumeOutput{Success: success, Message: message}
}

func (example *DockerVolume) Init(args ...any) interface{} {
	return DockerVolumeOutput{Success: true, Message: "compose plugin init"}
}

func (example *DockerVolume) Exec(args ...any) interface{} {
	return DockerVolumeOutput{Success: true, Message: "compose plugin run"}
}

func (example *DockerVolume) Stop(args ...any) interface{} {
	return DockerVolumeOutput{Success: true, Message: "compose plugin stopped"}
}

func (example *DockerVolume) Clear(args ...any) interface{} {
	return DockerVolumeOutput{Success: true, Message: "compose plugin cleared"}
}
