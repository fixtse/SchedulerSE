package skills

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
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

func TestPersonalizedSkillIncludesOriginWithoutCredentials(t *testing.T) {
	origin := "https://192.0.2.5:9443"
	data, err := AgentArchiveForOrigin(origin)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	foundMetadata, foundInstructions := false, false
	for _, file := range archive.File {
		if file.Name != "schedulerse-agent/connection.json" && file.Name != "schedulerse-agent/SKILL.md" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if file.Name == "schedulerse-agent/connection.json" {
			var metadata map[string]string
			if json.Unmarshal(body, &metadata) != nil || metadata["service_url"] != origin || metadata["api_base"] != origin+"/api/agent/v1" || len(metadata) != 2 {
				t.Fatal("incorrect connection metadata", string(body))
			}
			foundMetadata = true
		} else {
			foundInstructions = strings.Contains(string(body), "Service URL: `"+origin+"`")
		}
	}
	if !foundMetadata || !foundInstructions {
		t.Fatal("personalized connection instructions missing")
	}
}
