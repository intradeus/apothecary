package docker

// Wrapper for a docker network execution command as a potion type.
type DockerNetworkType struct {
	Overwrite bool
	Name      string
	Options   []string
}
