# rewrite

## takeaways from delay ctrace

- `xo/xo` calls are ~90% of runtime

## current flow

```
- make runner
    + func name maps
    + working dir
    + temp dir
    + db conn str from envvar

- runner.run
    + defer tmp dir cleanup
    + rm/recreate tmp dir
    + gen schema types
        - run xo cli (SLOW AF 70%+ of run time)
    + gen enums 
        - walks temp dir
            + read file line by line
            + looks for "enum" string match
            + adds match line and all lines after to return value
        - returns built up string
    + process queries
        - parse pkg asts from working dir
        - for each pkg ast
            + init a pkg output file 
                - str w pkg name and enum string
            + for all file in pkg 
                - process file
                    + open file
                    + types = replace ast 
                        - walk ast
                            + find query strings
                            + format query string
                                - pg_format to tempfile
                                - read tempfile
                                - return string
                            + replace arg with formatted value
                            + if query should be typegen ignored
                                - continue
                            + queryName, typeStr = genQueryType
                                - run xo cli (SLOW AF 20% of run time)
                            + add typeStr to return string
                            + replace types with new queryName
                                - return arg + var decl
                    + write ast 
                    + optinally rewrite file to disk
                        - read in original file
                        - compare to formatted printed ast 
                        - if different
                            + write to disk
                    + return generated type strings
                - output file str += process file output
            + format output file 
            + write output file 
```


## potential flow

```
- enumStr = start enum factory
    + (query below)
    + parse + use template

- get ASTs
- for each package
  + start package worker

- package worker
    + peek enums
    + create return chan
    + for each file
        + start file workers w return chan
    + read return chan worker times
        + add to output file 
    + write output file if any non-empty types came back

- file worker
    + should re write = false
    + walk ast
        - extract query string
        - format query
        - if same early exit
        - update arg 
        - write flag = true
        - queryName, types = genQueryType
        - update types in parent func
    + if write flag
        - write ast back to disk
    + put types on return chan
```





## useful queries

```sql
      select distinct
        e.enumtypid,
        t.typname,
        e.enumlabel,
        t.typnamespace::regnamespace::text as schema_name,
        e.enumsortorder,
        t.typnamespace::regnamespace::text = any(current_schemas(true)) as in_search_path,
        case
          when t.typnamespace::regnamespace::text = any(current_schemas(false))
            then quote_ident(t.typname)
          else
            quote_ident(t.typnamespace::regnamespace::text) || '.' || quote_ident(t.typname)
        end as searchable_type_name
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
```
