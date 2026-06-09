package main

import (
	"log"
	"runtime/debug"

	"github.com/pkg/errors"

	"github.com/pianoyeg94/waap/cmd"
)

type stackTracer interface {
	StackTrace() errors.StackTrace
}

func init() {
	log.SetFlags(log.Ldate | log.Lmicroseconds | log.Lshortfile | log.LUTC)
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			if err, ok := r.(stackTracer); ok {
				for _, f := range err.StackTrace() {
					log.Printf("%v | func %n()\n", f, f)
				}
			}
			log.Fatalf("Panic recovered: %v\nStacktrace: %s\n", r, string(debug.Stack()))
		}
	}()

	if err := cmd.Execute(); err != nil {
		if err, ok := err.(stackTracer); ok {
			for _, f := range err.StackTrace() {
				log.Printf("%v | func %n()\n", f, f)
			}
		}
		log.Println(err.Error())
	}
}
