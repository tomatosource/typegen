package main

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"strings"

	"github.com/jackc/pgx/v5"
)

const query = `
      select distinct
        e.enumtypid,
        t.typname,
        e.enumlabel,
        t.typnamespace::regnamespace::text as schema_name,
        e.enumsortorder
      from
        pg_enum as e
      join
        pg_type as t
      on
        t.oid = e.enumtypid
      order by
        t.typnamespace::regnamespace::text,
        t.typname,
        e.enumsortorder;
`

const templateStr = `
// {{.GoName}} is the '{{.DBName}}' enum type 
type {{.GoName}} uint16

// {{.GoName}} values.
const ({{range $index, $element := .Values}}
	// {{$element.GoName}} is the '{{$element.DBName}}' {{$.DBName}}.
	{{.GoName}} {{$.GoName}} = {{addOne $index}}{{end}}
)

// String satisfies the [fmt.Stringer] interface.
func ({{.ReceiverName}} {{.GoName}}) String() string {
	switch {{.ReceiverName}} { {{range .Values}}
	case {{.GoName}}:
		return "{{.DBName}}"{{end}}
	}
	return fmt.Sprintf("{{.GoName}}(%d)", {{.ReceiverName}})
}

// MarshalText marshals [{{.GoName}}] into text.
func ({{.ReceiverName}} {{.GoName}}) MarshalText() ([]byte, error) {
	return []byte(b.String()), nil
}

// UnmarshalText unmarshals [{{.GoName}}] from text.
func ({{.ReceiverName}} *{{.GoName}}) UnmarshalText(buf []byte) error {
	switch str := string(buf); str { {{range .Values}}
	case "{{.DBName}}":
		*{{$.ReceiverName}} = {{.GoName}}{{end}}
	default:
		return ErrInvalid{{.GoName}}(str)
	}
	return nil
}

// Value satisfies the [driver.Valuer] interface.
func ({{.ReceiverName}} {{.GoName}}) Value() (driver.Value, error) {
	return {{.ReceiverName}}.String(), nil
}

// Scan satisfies the [sql.Scanner] interface.
func ({{.ReceiverName}} *{{.GoName}}) Scan(v interface{}) error {
	switch x := v.(type) {
	case []byte:
		return {{.ReceiverName}}.UnmarshalText(x)
	case string:
		return {{.ReceiverName}}.UnmarshalText([]byte(x))
	}
	return ErrInvalid{{.GoName}}(fmt.Sprintf("%T", v))
}

// Null{{.GoName}} represents a null '{{.DBName}}' enum for schema 'public'.
type Null{{.GoName}} struct {
	{{.GoName}} {{.GoName}}
	// Valid is true if [{{.GoName}}] is not null.
	Valid bool
}

// Value satisfies the [driver.Valuer] interface.
func (n{{.ReceiverName}} Null{{.GoName}}) Value() (driver.Value, error) {
	if !n{{.ReceiverName}}.Valid {
		return nil, nil
	}
	return n{{.ReceiverName}}.{{.GoName}}.Value()
}

// Scan satisfies the [sql.Scanner] interface.
func (n{{.ReceiverName}} *Null{{.GoName}}) Scan(v interface{}) error {
	if v == nil {
		n{{.ReceiverName}}.{{.GoName}}, n{{.ReceiverName}}.Valid = 0, false
		return nil
	}
	err := n{{.ReceiverName}}.{{.GoName}}.Scan(v)
	n{{.ReceiverName}}.Valid = err == nil
	return err
}

// ErrInvalid{{.GoName}} is the invalid [{{.GoName}}] error.
type ErrInvalid{{.GoName}} string

// Error satisfies the error interface.
func (err ErrInvalid{{.GoName}}) Error() string {
	return fmt.Sprintf("invalid {{.GoName}}(%s)", string(err))
}
`

var tmpl = template.Must(
	template.
		New("enum").
		Funcs(template.FuncMap{
			"addOne": func(i int) int { return i + 1 },
		}).
		Parse(templateStr))

type enumValue struct {
	DBName string
	GoName string
}

type Enum struct {
	GoName       string
	DBName       string
	ReceiverName string
	Values       []enumValue
}

func (r *runner) getEnumString() (string, error) {
	conn, err := pgx.Connect(context.Background(), r.dbConn)
	if err != nil {
		return "", fmt.Errorf("opening db conn: %w", err)
	}
	defer conn.Close(context.Background())

	rows, err := conn.Query(context.Background(), `
  `)
	if err != nil {
		return "", fmt.Errorf("querying for enums: %w", err)
	}

	defer rows.Close()

	enums := map[string]*Enum{}
	for rows.Next() {
		vs := rows.RawValues()
		enumName := string(vs[1])
		enumVal := string(vs[2])
		formattedEnumName := formatEnumName(enumName)

		if e, ok := enums[enumName]; ok {
			e.Values = append(e.Values, enumValue{
				DBName: enumVal,
				GoName: formatEnumVal(formattedEnumName, enumVal),
			})
		} else {
			enums[enumName] = &Enum{
				GoName:       formattedEnumName,
				ReceiverName: formatReceiverName(enumName),
				DBName:       enumName,
				Values: []enumValue{
					{
						DBName: enumVal,
						GoName: formatEnumVal(formattedEnumName, enumVal),
					},
				},
			}
		}
	}

	retStr := strings.Builder{}
	for _, e := range enums {
		retStr.WriteString(renderEnumTemplate(e))
	}

	return retStr.String(), nil
}

func formatEnumName(dbName string) string {
	return snakeCaseToPascalCase(dbName)
}

func formatReceiverName(dbName string) string {
	r := ""
	words := strings.Split(strings.ToLower(dbName), "_")
	for _, word := range words {
		if len(word) > 0 {
			r += strings.ToLower(word[0:1])
		}
	}
	return r
}

func formatEnumVal(enum, val string) string {
	return fmt.Sprintf(
		"%s%s",
		enum,
		snakeCaseToPascalCase(val),
	)
}

func snakeCaseToCamelCase(input string) string {
	words := strings.Split(strings.ToLower(input), "_")
	for i, word := range words {
		if i == 0 {
			continue
		} else {
			words[i] = strings.Title(word)
		}
	}
	return strings.Join(words, "")
}

func snakeCaseToPascalCase(input string) string {
	words := strings.Split(strings.ToLower(input), "_")
	for i, word := range words {
		words[i] = strings.Title(word)
	}
	return strings.Join(words, "")
}

func renderEnumTemplate(e *Enum) string {
	var buf bytes.Buffer
	tmpl.Execute(&buf, e)
	return buf.String()
}
