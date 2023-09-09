package main

import (
	"os"
	"runtime/trace"
)

func main() {
	traceFile, err := os.Create("trace.out")
	if err != nil {
		panic(err)
	}
	defer traceFile.Close()
	trace.Start(traceFile)
	defer trace.Stop()

	r := &runner{}
	if err := r.init(); err != nil {
		panic(err)
	}

	if err := r.run(); err != nil {
		panic(err)
	}
}
