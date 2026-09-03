// Command amqp-seal-prototype answers the #1308 question with a guided walkthrough.
//
//	go run ./research/amqp-seal-prototype                 # console report
//	go run ./research/amqp-seal-prototype -html out.html  # static HTML report
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	htmlPath := flag.String("html", "", "write a self-contained static HTML report to this path instead of the console")
	flag.Parse()

	scenarios := AllScenarios(NewWorld())

	if *htmlPath == "" {
		RenderConsole(os.Stdout, scenarios)
		return
	}
	f, err := os.Create(*htmlPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	RenderHTML(f, scenarios)
	fmt.Fprintf(os.Stderr, "wrote %s\n", *htmlPath)
}
