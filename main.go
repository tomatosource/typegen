package main

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"hash/fnv"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/brianvoe/gofakeit/v6"
	"golang.org/x/tools/imports"
)

// TODO
// update generated type names in other pkg files
// make less dogshit slow

type errorSlice []error

type runner struct {
	dbConn           string
	rootPath         string
	genPath          string
	funcNames        map[string]bool
	typegenFuncNames map[string]bool
}

func main() {
	r := &runner{
		dbConn:   os.Getenv("DB_CONN"),
		rootPath: "./",
		funcNames: map[string]bool{
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
		},
		typegenFuncNames: map[string]bool{
			"Get":           true,
			"Select":        true,
			"GetContext":    true,
			"SelectContext": true,
		},
	}

	r.genPath = r.rootPath + "models"

	if err := r.run(); err != nil {
		log.Fatal(err.Error())
	}
}

func (r *runner) run() error {
	defer func() {
		exec.Command("rm", "-rf", r.genPath).Run()
	}()

	if err := r.tidyDir(); err != nil {
		return fmt.Errorf("tidying output dir %q: %w", r.genPath, err)
	}

	if err := r.genSchemaTypes(); err != nil {
		return fmt.Errorf("genning schema types in %q: %w", r.genPath, err)
	}

	enums, err := r.genEnums()
	if err != nil {
		return fmt.Errorf("genning temp enum file: %w", err)
	}

	if err := r.processQueries(enums); err != nil {
		return fmt.Errorf("walking dir %q: %w", r.rootPath, err)
	}

	return nil
}

func (r *runner) processQueries(enums string) error {
	fset := token.NewFileSet()
	parserMode := parser.ParseComments | parser.AllErrors

	pkgMap, err := parser.ParseDir(fset, r.rootPath, nil, parserMode)
	if err != nil {
		return fmt.Errorf("parsing: %w", err)
	}

	errs := []error{}
	for pkgName, pkgAst := range pkgMap {
		pkgFile := fmt.Sprintf(
			"package %s\nimport %q\n%s\n",
			pkgName, "github.com/google/uuid", enums,
		)
		for filename, file := range pkgAst.Files {
			types, err := r.processFile(filename, fset, file)
			if err != nil {
				errs = append(errs, err)
			}
			pkgFile += types
		}

		outputPath := fmt.Sprintf("typegen_%s.go", pkgName)
		formattedPkgFile, err := imports.Process(outputPath, []byte(pkgFile), nil)
		if err != nil {
			errs = append(
				errs,
				fmt.Errorf("formatting pkg %q output file: %w", pkgName, err),
			)
		}

		if err := ioutil.WriteFile(outputPath, formattedPkgFile, 0o644); err != nil {
			errs = append(
				errs,
				fmt.Errorf("writing output file %q: %w", outputPath, err),
			)
		}
	}

	if len(errs) > 0 {
		return errorSlice(errs)
	}
	return nil
}

func (r *runner) processFile(
	filename string, fset *token.FileSet, astFile *ast.File,
) (string, error) {
	in, err := os.Open(filename)
	if err != nil {
		return "", fmt.Errorf("opening: %w", err)
	}

	types, err := r.replaceAst(astFile, fset, filename)
	if err != nil {
		return "", fmt.Errorf("replacing ast: %w", err)
	}

	var buf bytes.Buffer
	if err = printer.Fprint(&buf, fset, astFile); err != nil {
		return "", fmt.Errorf("pretty printing ast: %w", err)
	}

	res, err := format.Source(buf.Bytes())
	if err != nil {
		return "", fmt.Errorf("gofmting: %w", err)
	}

	src, err := ioutil.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("reading: %w", err)
	}

	if !bytes.Equal(src, res) {
		if err = ioutil.WriteFile(filename, res, 0); err != nil {
			return "", fmt.Errorf("writing file back: %w", err)
		}
	}

	return types, nil
}

func (r *runner) replaceAst(
	f *ast.File, fset *token.FileSet, filename string,
) (string, error) {
	errs := []error{}
	outputType := ""
	var parentFunc *ast.FuncDecl

	ast.Inspect(f, func(n ast.Node) bool {
		if funcDecl, ok := n.(*ast.FuncDecl); ok {
			parentFunc = funcDecl
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if fun, ok := call.Fun.(*ast.SelectorExpr); ok {
				if r.funcNames[fun.Sel.Name] {
					for _, callArg := range call.Args {
						if litArg, ok := callArg.(*ast.BasicLit); ok {
							sqlStmt := litArg.Value
							if !strings.HasPrefix(sqlStmt, "`") {
								return true
							}
							src := strings.Trim(sqlStmt, "`")
							formattedQuery, err := formatQuery(src)
							if err != nil {
								errs = append(errs, fmt.Errorf(
									"format failed at %s: %v", fset.Position(litArg.Pos()), err,
								))
								return true
							}

							formattedArgValue, err := formatArgValue(
								formattedQuery, fset.Position(fun.Pos()),
							)
							if err != nil {
								errs = append(errs, fmt.Errorf(
									"indenting query str: %w", err,
								))
								return true
							}
							litArg.Value = formattedArgValue

							if r.typegenFuncNames[fun.Sel.Name] {
								if strings.Contains(formattedQuery, "typegen-ignore") {
									return true
								}

								queryName, typeStr, err := r.genQueryType(formattedQuery)
								if err != nil {
									// TODO format query with line numbers/filename
									errs = append(errs, fmt.Errorf(
										"generating query type for \n------\n%s\n--------\n%w",
										formattedQuery, err,
									))
									return true
								}
								outputType += typeStr + "\n"

								// TODO move all this shit out to err func
								if parentFunc == nil {
									// warn lvl log
									return true
								}

								resultsList := parentFunc.Type.Results.List
								if len(resultsList) < 2 {
									// warn lvl log
									return true
								}

								ident, _ := resultsList[0].Type.(*ast.Ident)
								if at, ok := resultsList[0].Type.(*ast.ArrayType); ok {
									if x, ok := at.Elt.(*ast.Ident); ok {
										ident = x
									}
								}

								if ident == nil {
									// warn lvl log
									return true
								}

								oldTypeName := ident.Name
								ident.Name = queryName

								// TODO var type
								// todo go through function statements
								for _, stmt := range parentFunc.Body.List {
									if declStmt, ok := stmt.(*ast.DeclStmt); ok {
										if genDecl, ok := declStmt.Decl.(*ast.GenDecl); ok {
											for _, spec := range genDecl.Specs {
												if valSpec, ok := spec.(*ast.ValueSpec); ok {
													ident, _ := valSpec.Type.(*ast.Ident)
													if at, ok := valSpec.Type.(*ast.ArrayType); ok {
														if x, ok := at.Elt.(*ast.Ident); ok {
															ident = x
														}
													}
													if ident.Name == oldTypeName {
														ident.Name = queryName
													}
												}
											}
										}
									}
								}
								// if type param
								// get ident and replace like above
							}
						}
					}
				}
			}
		}

		return true
	})

	if len(errs) > 0 {
		return "", errorSlice(errs)
	}

	return outputType, nil
}

func formatQuery(query string) (string, error) {
	tmpFile, err := ioutil.TempFile(os.TempDir(), "sql-")
	if err != nil {
		return "", fmt.Errorf("creating tmp file: %w", err)
	}
	defer func() {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
	}()

	if _, err = tmpFile.WriteString(query); err != nil {
		return "", fmt.Errorf("writing query to tmpfile: %w", err)
	}

	cmd := exec.Command("pg_format", "--inplace", "--comma-break", "--tabs", tmpFile.Name())
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("running pg_format: %w", err)
	}

	formattedQuery, err := ioutil.ReadFile(tmpFile.Name())
	if err != nil {
		return "", fmt.Errorf("reading formatted query from temp file: %w", err)
	}

	return string(formattedQuery), nil
}

func formatArgValue(query string, funPos token.Position) (string, error) {
	line, err := readLine(funPos.Filename, funPos.Line)
	if err != nil {
		return "", fmt.Errorf("reading line: %w", err)
	}

	indentationLevel := len(line) - len(strings.TrimLeft(line, " \t\n"))

	leadingIndent := strings.Repeat("\t", indentationLevel+1)
	fq := leadingIndent + strings.ReplaceAll(
		query, "\n", fmt.Sprintf("\n%s", leadingIndent),
	)

	return "`\n" + fq[0:len(fq)-1] + "`", nil
}

func readLine(filename string, lineNo int) (string, error) {
	r, err := os.Open(filename)
	if err != nil {
		return "", fmt.Errorf("opening %q: %w", filename, err)
	}
	defer r.Close()

	currLine := 1
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if currLine == lineNo {
			return sc.Text(), sc.Err()
		}
		currLine++
	}
	return "", io.EOF
}

func (r *runner) tidyDir() error {
	cmd := exec.Command("rm", "-rf", r.genPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rmrfing %q: %w", r.genPath, err)
	}

	cmd = exec.Command("mkdir", r.genPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("making dir %q: %w", r.genPath, err)
	}

	return nil
}

func (r *runner) genSchemaTypes() error {
	return exec.Command(
		"xo",
		"schema",
		r.dbConn,
		"--go-pkg=dev",
		"--go-uuid=github.com/google/uuid",
		"--schema=public",
		"--go-field-tag=db:\"{{ .SQLName }}\"",
	).Run()
}

func (r *runner) genQueryType(query string) (string, string, error) {
	queryName := nameQuery(query)

	q := query
	q = strings.ReplaceAll(q, "\n", " ")
	q = strings.ReplaceAll(q, "\t", " ")
	q = regexp.MustCompile(`\$[0-9]+`).ReplaceAllString(q, "%%x int%%")

	cmd := exec.Command(
		"xo",
		"query",
		r.dbConn,
		"--go-pkg=dev",
		"--go-uuid=github.com/google/uuid",
		"--schema=public",
		"--go-field-tag=db:\"{{ .SQLName }}\"",
		fmt.Sprintf("--type=%s", queryName),
		fmt.Sprintf("--query=%s", q),
	)

	var stdErr bytes.Buffer
	cmd.Stderr = &stdErr

	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("running xo:\n%s\nerr: %w", stdErr.String(), err)
	}

	fileName := fmt.Sprintf("./models/%s.xo.go", strings.ToLower(queryName))
	fset := token.NewFileSet()
	parserMode := parser.ParseComments | parser.AllErrors

	astFile, err := parser.ParseFile(
		fset,
		fileName,
		nil,
		parserMode,
	)
	if err != nil {
		return "", "", fmt.Errorf("parsing: %w", err)
	}

	var queryTypeStr string
	ast.Inspect(astFile, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.TypeSpec:
			if t.Name.Name == queryName {
				chunk, err := getFileChunk(fileName, n.Pos(), n.End())
				if err != nil {
					// TODO err slice
					return true
				}

				queryTypeStr = "type " + chunk + "\n"
			}
		}
		return true
	})

	return queryName, queryTypeStr, nil
}

func getFileChunk(filename string, pos, end token.Pos) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", fmt.Errorf("opening %q: %w", filename, err)
	}
	defer file.Close()

	_, err = file.Seek(int64(pos-1), 0)
	if err != nil {
		return "", fmt.Errorf("seeking to %d in %q: %w", pos-1, filename, err)
	}

	data := make([]byte, end-pos)
	_, err = io.ReadFull(file, data)
	return string(data), err
}

func nameQuery(query string) string {
	h := fnv.New64()
	h.Write([]byte(query))
	seed := h.Sum64()
	gofakeit.Seed(int64(seed))
	return strings.ReplaceAll(
		strings.ToLower(gofakeit.Adjective())+strings.Title(gofakeit.Noun()),
		" ", "",
	)
}

func (r *runner) genEnums() (string, error) {
	// TODO string builder
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
		return "", err
	}

	return enumStr, nil
}

func (es errorSlice) Error() string {
	var errorStrings []string
	for _, err := range []error(es) {
		errorStrings = append(errorStrings, err.Error())
	}
	return strings.Join(errorStrings, "\n")
}
