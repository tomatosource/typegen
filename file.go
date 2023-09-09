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
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/brianvoe/gofakeit/v6"
)

func (r *runner) processFile(
	filename string,
	fset *token.FileSet,
	astFile *ast.File,
	wg *sync.WaitGroup,
	out chan<- string,
	errs chan<- error,
) {
	defer func() {
		wg.Done()
	}()

	if write := r.replaceAst(astFile, fset, filename, out, errs); !write {
		return
	}

	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, astFile); err != nil {
		errs <- fmt.Errorf("pretty printing ast: %w", err)
		return
	}

	res, err := format.Source(buf.Bytes())
	if err != nil {
		errs <- fmt.Errorf("gofmting: %w", err)
		return
	}

	errs <- ioutil.WriteFile(filename, res, 0)
}

func (r *runner) replaceAst(
	f *ast.File, fset *token.FileSet, filename string,
	types chan<- string, errs chan<- error,
) bool {
	write := false
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
								errs <- fmt.Errorf(
									"format failed at %s: %v", fset.Position(litArg.Pos()), err,
								)
								return true
							}

							formattedArgValue, err := formatArgValue(
								formattedQuery, fset.Position(fun.Pos()),
							)
							if err != nil {
								errs <- fmt.Errorf(
									"indenting query str: %w", err,
								)
								return true
							}
							litArg.Value = formattedArgValue

							// early exit
							if litArg.Value == formattedArgValue {
								return true
							}

							// query has changed so will need to rewrite file for sure
							write = true

							if r.typegenFuncNames[fun.Sel.Name] {
								if strings.Contains(formattedQuery, "typegen-ignore") {
									return true
								}

								queryName, typeStr, err := r.genQueryType(formattedQuery)
								if err != nil {
									// TODO format query with line numbers/filename
									errs <- fmt.Errorf(
										"generating query type for \n------\n%s\n--------\n%w",
										formattedQuery, err,
									)
									return true
								}

								types <- typeStr

								// TODO move all this shit out to err func
								if parentFunc == nil {
									errs <- fmt.Errorf("TODO better err 1: some funkiness")
									return true
								}

								resultsList := parentFunc.Type.Results.List
								if len(resultsList) < 2 {
									errs <- fmt.Errorf("TODO better err 2: some funkiness")
									return true
								}

								ident, _ := resultsList[0].Type.(*ast.Ident)
								if at, ok := resultsList[0].Type.(*ast.ArrayType); ok {
									if x, ok := at.Elt.(*ast.Ident); ok {
										ident = x
									}
								}

								if ident == nil {
									errs <- fmt.Errorf("TODO better err 3: some funkiness")
									return true
								}

								oldTypeName := ident.Name
								ident.Name = queryName

								// todo clean up abomination
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
							}
						}
					}
				}
			}
		}

		return true
	})

	return write
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
