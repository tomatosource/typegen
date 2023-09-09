# typegen

- slonik-typegen wannabe
- opinionated query formatter
- attempts to formats all
- only gen types from Get/Select query funcs
- `typegen-ignore` in query to have it ignored

## prereqs

```
brew install pgformatter
go install github.com/xo/xo@latest
```

## installation

```
go install github.com/tomatosource/typegen
```

## running

```
DB_CONN=postgres://user:pwd@127.0.0.1:5432/db typegen
```

## major todos

- update generated type names in other pkg files
- make less dogshit slow
- take more args in
- code so gross
