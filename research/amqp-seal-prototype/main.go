// Command amqp-seal-prototype answers the #1308 question with a guided walkthrough.
//
//	go run ./research/amqp-seal-prototype                 # console report; exit 1 on any ✗
//	go run ./research/amqp-seal-prototype -html out.html  # static HTML report; same exit rule
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
	_, mismatches := Matrix(scenarios)

	if *htmlPath == "" {
		RenderConsole(os.Stdout, scenarios)
	} else {
		f, err := os.Create(*htmlPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		RenderHTML(f, scenarios)
		_ = f.Close()
		fmt.Fprintf(os.Stderr, "wrote %s\n", *htmlPath)
		RenderMatrixText(os.Stderr, scenarios)
	}
	if mismatches > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d step(s) fired something other than expected\n", mismatches)
		os.Exit(1)
	}
}
