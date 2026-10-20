package skills

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestAgentArchiveIsSelfContainedAndExcludesRuntimeSecrets(t *testing.T) {
	data, err := AgentArchive()
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{
		"SKILL.md": false, "scripts/bridge.py": false, "references/t3.md": false,
		"references/cloudflare.md": false, "references/local.md": false, "assets/cloudflare/worker.mjs": false,
		"assets/cloudflare/schema.sql": false, "assets/cloudflare/wrangler.json": false,
	}
	for _, file := range archive.File {
		if !strings.HasPrefix(file.Name, "schedulerse-agent/") || strings.Contains(file.Name, "..") {
			t.Fatal("unsafe archive path", file.Name)
		}
		name := strings.TrimPrefix(file.Name, "schedulerse-agent/")
		if _, ok := required[name]; !ok {
			t.Fatal("unexpected file bundled; check for local state or secrets", file.Name)
		}
		required[name] = true
	}
	for name, found := range required {
		if !found {
			t.Error("missing runtime resource", name)
		}
	}
}
