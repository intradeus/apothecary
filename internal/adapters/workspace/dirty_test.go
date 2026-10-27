package workspace

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The dirty hint follows every build input: the .devcontainer directory, and
// the Dockerfile / compose files the (commented, trailing-comma) configuration
// points at outside it. No recorded digest means clean, never dirty.
func TestConfigDirtyTracksBuildInputs(t *testing.T) {
	a := newTestAdapter(t, &scriptRunner{})
	const name = "apo-dirty-1"
	src := a.srcDir(name)
	writeFile(t, filepath.Join(src, ".devcontainer", "devcontainer.json"), `// the repo's dev container
{
  "name": "demo", // see https://example.test/not-a-comment
  "build": { "dockerfile": "../Dockerfile", },
  "dockerComposeFile": ["compose.yaml"],
}`)
	writeFile(t, filepath.Join(src, "Dockerfile"), "FROM alpine\n")
	writeFile(t, filepath.Join(src, ".devcontainer", "compose.yaml"), "services: {}\n")
	writeFile(t, filepath.Join(src, "README.md"), "unrelated\n")

	files, err := configFiles(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".devcontainer/compose.yaml", ".devcontainer/devcontainer.json", "Dockerfile"}
	if !slices.Equal(files, want) {
		t.Fatalf("configFiles = %v, want %v", files, want)
	}

	ctx := context.Background()
	if d, err := a.ConfigDirty(ctx, name); err != nil || d {
		t.Fatalf("before any build: dirty=%v err=%v, want clean (nothing recorded)", d, err)
	}
	a.recordConfigDigest(name, src, nil)
	if d, err := a.ConfigDirty(ctx, name); err != nil || d {
		t.Fatalf("just built: dirty=%v err=%v", d, err)
	}
	writeFile(t, filepath.Join(src, "README.md"), "still unrelated\n")
	if d, _ := a.ConfigDirty(ctx, name); d {
		t.Fatal("a file outside the build inputs must not read as dirty")
	}
	writeFile(t, filepath.Join(src, "Dockerfile"), "FROM alpine\nRUN apk add git\n")
	if d, _ := a.ConfigDirty(ctx, name); !d {
		t.Fatal("the referenced Dockerfile changed: want dirty")
	}
	a.recordConfigDigest(name, src, nil) // a rebuild
	if d, _ := a.ConfigDirty(ctx, name); d {
		t.Fatal("rebuilt from the new inputs: want clean")
	}
	writeFile(t, filepath.Join(src, ".devcontainer", "compose.yaml"), "services: { db: {} }\n")
	if d, _ := a.ConfigDirty(ctx, name); !d {
		t.Fatal("the compose file changed: want dirty")
	}
}
