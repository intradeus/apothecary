package workspace

import "context"

// configFiles / configDigest keep the pre-seam helpers the dirty tests call:
// the devcontainer definition over the engine's own filesystem.
func configFiles(src string) ([]string, error) {
	return devcontainerDef{}.ConfigFiles(context.Background(), osFS{}, src)
}

func configDigest(src string) (string, error) {
	return definitionDigest(context.Background(), devcontainerDef{}, osFS{}, src)
}
