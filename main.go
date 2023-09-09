package main

func main() {
	r := &runner{}
	if err := r.init(); err != nil {
		panic(err)
	}

	if err := r.run(); err != nil {
		panic(err)
	}
}
