package docker

// Wrapper for a docker network execution command as a potion type.
type DockerNetwork struct {
	Overwrite bool
	Name      string
	Options   []string
}

type DockerNetworkOutput struct {
	Success bool
	Message string
	Value   DockerNetwork
}

func (example *DockerNetwork) Check(args ...any) any {
	return DockerNetworkOutput{Success: true, Message: "Network plugin checked"}
}

func (example *DockerNetwork) Init(args ...any) any {
	return DockerNetworkOutput{Success: true, Message: "Network plugin init"}
}

func (example *DockerNetwork) Exec(args ...any) any {
	return DockerNetworkOutput{Success: true, Message: "Network plugin run"}
}

func (example *DockerNetwork) Stop(args ...any) any {
	return DockerNetworkOutput{Success: true, Message: "Network plugin stopped"}
}

func (example *DockerNetwork) Clear(args ...any) any {
	return DockerNetworkOutput{Success: true, Message: "Network plugin cleared"}
}
