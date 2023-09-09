package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (r *runner) getEnumString() (string, error) {
	cmd := exec.Command(
		"xo",
		"schema",
		r.dbConn,
		"--go-pkg=dev",
		"--go-uuid=github.com/google/uuid",
		"--schema=public",
		"--go-field-tag=db:\"{{ .SQLName }}\"",
	)

	var stdErr bytes.Buffer
	cmd.Stderr = &stdErr

	var stdOut bytes.Buffer
	cmd.Stdout = &stdOut

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"generating schema types, running xo:\n%s\n%s\nerr: %w",
			stdOut.String(), stdErr.String(), err,
		)
	}

	enumStr := ""

	if err := filepath.Walk(r.genPath, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !f.IsDir() && strings.HasSuffix(f.Name(), ".go") {
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("opening %q: %w", path, err)
			}
			scanner := bufio.NewScanner(f)

			writeFlag := false
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "enum type") {
					writeFlag = true
					enumStr += "\n"
				}
				if writeFlag {
					enumStr += line + "\n"
				}
			}

			return f.Close()
		}

		return nil
	}); err != nil {
		return "", fmt.Errorf("walking generated schema type files: %w", err)
	}

	return enumStr, nil
}
