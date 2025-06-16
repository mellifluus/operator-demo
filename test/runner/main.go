package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	cmd := exec.Command("ls")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		fmt.Println("Error running ls:", err)
		os.Exit(1)
	}
}
