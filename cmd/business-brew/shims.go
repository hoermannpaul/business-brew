package main

import (
	"business-brew/internal/shims"
	"flag"
	"fmt"
	"os"
)

func deployShims(args []string) int {
	flags := flag.NewFlagSet("shims", flag.ContinueOnError)
	directory := flags.String("directory", "", "absolute directory for native installer shims")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *directory == "" || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: business-brew shims --directory /company/managed/shims")
		return 2
	}
	binary, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := shims.Deploy(*directory, binary); err != nil {
		fmt.Fprintln(os.Stderr, "Business Brew:", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "Installer shims created. Company provisioning must place this directory before the real installers in PATH (and configure sudo's command path where applicable).")
	return 0
}
