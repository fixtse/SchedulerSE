// Package agentkit embeds the standalone bridge and reminder delivery guides.
package agentkit

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"strings"
)

//go:embed schedulerse-delivery
var files embed.FS

func DeliveryArchive() ([]byte, error) { return DeliveryArchiveForOrigin("") }

// DeliveryFile returns a bundled text file, or generated connection metadata.
// Paths are relative to the kit root; only embedded files can be read.
func DeliveryFile(name, origin string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, fs.ErrNotExist
	}
	if name == "connection.json" {
		return json.Marshal(struct {
			ServiceURL   string `json:"service_url"`
			APIBase      string `json:"api_base"`
			MCPURL       string `json:"mcp_url"`
			MCPTransport string `json:"mcp_transport"`
		}{origin, origin + "/api/agent/v1", origin + "/api/agent/v1/mcp", "streamable-http"})
	}
	data, err := files.ReadFile("schedulerse-delivery/" + name)
	if err != nil {
		return nil, err
	}
	if name == "README.md" && origin != "" {
		data = []byte(strings.Replace(string(data), "## Connect once", "## This installation\n\nService URL: `"+origin+"`. See `connection.json` for connection metadata.\n\n## Connect once", 1))
	}
	return data, nil
}

// DeliveryArchiveForOrigin personalizes the secret-free standalone runtime kit.
// The caller validates origin against the request host.
func DeliveryArchiveForOrigin(origin string) ([]byte, error) {
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	err := fs.WalkDir(files, "schedulerse-delivery", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := DeliveryFile(strings.TrimPrefix(path, "schedulerse-delivery/"), origin)
		if err != nil {
			return err
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
		writer, err := archive.Create("schedulerse-delivery/connection.json")
		if err != nil {
			return nil, err
		}
		data, err := DeliveryFile("connection.json", origin)
		if err != nil {
			return nil, err
		}
		if _, err = writer.Write(data); err != nil {
			return nil, err
		}
	}
	if err = archive.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
