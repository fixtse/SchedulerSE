// Package skills embeds the portable agent setup kit shipped with the service.
package skills

import (
	"archive/zip"
	"bytes"
	"embed"
	"io/fs"
)

//go:embed schedulerse-agent
var files embed.FS

func AgentArchive() ([]byte, error) {
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
	if err = archive.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
