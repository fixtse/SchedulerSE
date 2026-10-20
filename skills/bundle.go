// Package skills embeds the portable agent setup kit shipped with the service.
package skills

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"strings"
)

//go:embed schedulerse-agent
var files embed.FS

func AgentArchive() ([]byte, error) { return AgentArchiveForOrigin("") }

// AgentArchiveForOrigin personalizes an otherwise portable, secret-free skill.
// The caller validates the request origin before passing it here.
func AgentArchiveForOrigin(origin string) ([]byte, error) {
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	err := fs.WalkDir(files, "schedulerse-agent", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		if path == "schedulerse-agent/SKILL.md" && origin != "" {
			data = []byte(strings.Replace(string(data), "## Connect once", "## This installation\n\nService URL: `"+origin+"`. Read the adjacent `connection.json` for machine-readable\nconnection metadata. The user only supplies the API token privately; do not ask them\nto re-enter this address. Check it from the agent's own environment before setup.\n\n## Connect once", 1))
		}
		writer, err := archive.Create(path)
		if err != nil {
			return err
		}
		_, err = writer.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	if origin != "" {
		writer, err := archive.Create("schedulerse-agent/connection.json")
		if err != nil {
			return nil, err
		}
		if err = json.NewEncoder(writer).Encode(struct {
			ServiceURL string `json:"service_url"`
			APIBase    string `json:"api_base"`
		}{origin, origin + "/api/agent/v1"}); err != nil {
			return nil, err
		}
	}
	if err = archive.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
