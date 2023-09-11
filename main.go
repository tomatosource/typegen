package main

import "os"

func main() {
	r := &runner{}
	// TODO runtime arg
	if err := r.init(os.Getenv("DB_CONN")); err != nil {
		panic(err)
	}

	if err := r.run(); err != nil {
		panic(err)
	}
}
