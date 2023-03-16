// package docker is a plugin group for all docker related plugins
package docker

// Wrapper for a docker execution command as a potion type.
type Docker struct {
	Selection string
}

type DockerOutput struct {
	Success bool
	Message string
	Value   Docker
}

func (example *Docker) Check(args ...any) any {
	return DockerOutput{Success: true, Message: "docker plugin checked"}
}

func (example *Docker) Init(args ...any) any {
	return DockerOutput{Success: true, Message: "docker plugin init"}
}

func (example *Docker) Exec(args ...any) any {
	return DockerOutput{Success: true, Message: "docker plugin run"}
}

func (example *Docker) Stop(args ...any) any {
	return DockerOutput{Success: true, Message: "docker plugin stopped"}
}

func (example *Docker) Clear(args ...any) any {
	return DockerOutput{Success: true, Message: "docker plugin cleared"}
}
