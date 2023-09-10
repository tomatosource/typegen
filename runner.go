package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"os/exec"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"golang.org/x/tools/imports"
)

type runner struct {
	dbConnAddr       string
	dbConn           *pgx.Conn
	rootPath         string
	genPath          string
	funcNames        map[string]bool
	typegenFuncNames map[string]bool
}

func (r *runner) init() error {
	// TODO arg
	r.dbConnAddr = "postgres://postgres:safesafe@127.0.0.1:6000/svc_core_dev?sslmode=disable"

	conn, err := pgx.Connect(context.Background(), r.dbConnAddr)
	if err != nil {
		return fmt.Errorf("opening db conn: %w", err)
	}
	r.dbConn = conn

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
		r.dbConn.Close(context.Background())
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

func (r *runner) processPackage(
	enums, pkgName string,
	pkgAst *ast.Package, fset *token.FileSet,
	wg *sync.WaitGroup, errs chan<- error,
) {
	defer func() {
		wg.Done()
	}()

	fileWg := &sync.WaitGroup{}
	fileWg.Add(len(pkgAst.Files))
	types := make(chan string, len(pkgAst.Files))

	for filename, file := range pkgAst.Files {
		go r.processFile(filename, fset, file, fileWg, types, errs)
	}

	fileWg.Wait()
	close(types)

	typeStrs := []string{}
	for t := range types {
		typeStrs = append(typeStrs, t)
	}

	if len(typeStrs) == 0 {
		return
	}

	pkgFile := fmt.Sprintf(
		"%s\npackage %s\nimport %q\n%s\n%s",
		"// Code generated by github.com/tomatosource/typegen; DO NOT EDIT.",
		pkgName,
		"github.com/google/uuid",
		enums,
		strings.Join(typeStrs, "\n"), // TODO sort
	)

	outputPath := fmt.Sprintf("typegen_%s.go", pkgName)

	formattedPkgFile, err := imports.Process(
		outputPath, []byte(pkgFile), nil,
	)
	if err != nil {
		errs <- fmt.Errorf("formatting pkg %q output file: %w", pkgName, err)
	}

	if err := ioutil.WriteFile(
		outputPath, formattedPkgFile, 0o644,
	); err != nil {
		errs <- fmt.Errorf("writing output file %q: %w", outputPath, err)
	}
}
