package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os/exec"
	"sync"
)

type runner struct {
	dbConn           string
	rootPath         string
	genPath          string
	funcNames        map[string]bool
	typegenFuncNames map[string]bool
}

func (r *runner) init() error {
	// TODO arg
	r.dbConn = "postgres://postgres:safesafe@127.0.0.1:6000/svc_core_dev?sslmode=disable"

	r.rootPath = "./"
	r.genPath = r.rootPath + "models"

	r.funcNames = map[string]bool{
		"Get":                 true,
		"Select":              true,
		"Exec":                true,
		"NamedExec":           true,
		"NamedQuery":          true,
		"Query":               true,
		"Prepare":             true,
		"GetContext":          true,
		"SelectContext":       true,
		"ExecContext":         true,
		"NamedExecContext":    true,
		"QueryContext":        true,
		"PrepareContext":      true,
		"PrepareNamedContext": true,
	}

	r.typegenFuncNames = map[string]bool{
		"Get":           true,
		"Select":        true,
		"GetContext":    true,
		"SelectContext": true,
	}

	// rm -rf gen path incase it still exists from previous broken run
	cmd := exec.Command("rm", "-rf", r.genPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rmrfing %q: %w", r.genPath, err)
	}

	// recreate gen path
	cmd = exec.Command("mkdir", r.genPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("making dir %q: %w", r.genPath, err)
	}

	return nil
}

func (r *runner) run() error {
	defer func() {
		exec.Command("rm", "-rf", r.genPath).Run()
	}()

	enums, err := r.getEnumString()
	if err != nil {
		return fmt.Errorf("generating enum types: %w", err)
	}

	fset := token.NewFileSet()
	parserMode := parser.ParseComments | parser.AllErrors

	pkgMap, err := parser.ParseDir(fset, r.rootPath, nil, parserMode)
	if err != nil {
		return fmt.Errorf("parsing: %w", err)
	}

	errs := make(chan error, 50)
	wg := &sync.WaitGroup{}
	wg.Add(len(pkgMap))

	for pkgName, pkgAst := range pkgMap {
		go r.processPackage(enums, pkgName, pkgAst, fset, wg, errs)
	}

	wg.Wait()
	close(errs)

	return errFromClosedChan(errs)
}
