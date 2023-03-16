package docker

/**
*  Wrapper for docker-compose execution command as a plugin.
 */

type DockerCompose struct {
	Selection string
}

type DockerComposeOutput struct {
	Success bool
	Message string
	Value   DockerCompose
}

func (example *DockerCompose) Check(args ...any) any {
	return DockerComposeOutput{Success: true, Message: "compose plugin checked"}
}

func (example *DockerCompose) Init(args ...any) any {
	return DockerComposeOutput{Success: true, Message: "compose plugin init"}
}

func (example *DockerCompose) Exec(args ...any) any {
	return DockerComposeOutput{Success: true, Message: "compose plugin run"}
}

func (example *DockerCompose) Stop(args ...any) any {
	return DockerComposeOutput{Success: true, Message: "compose plugin stopped"}
}

func (example *DockerCompose) Clear(args ...any) any {
	return DockerComposeOutput{Success: true, Message: "compose plugin cleared"}
}
