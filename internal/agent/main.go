package agent

import (
	"fmt"
	"os"

	"scriptstui/internal/shellenv"
)

// Main runs the agent for a front end's binary started as `<binary> agent
// <socket>`, and returns the process exit code. Front ends call it before
// anything else when their first argument is Arg.
func Main(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "uso: agent <socket>")
		return 2
	}
	shellenv.EnsurePATH()
	if err := Serve(args[0], Options{}); err != nil {
		fmt.Fprintln(os.Stderr, "agente:", err)
		return 1
	}
	return 0
}
