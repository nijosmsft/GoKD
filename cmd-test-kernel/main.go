package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nijosmsft/gokd"
)

func main() {
	sess, err := gokd.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "New() failed: %v\n", err)
		os.Exit(1)
	}
	defer sess.Close()

	connStr := os.Getenv("KDNET_CONN")
	if connStr == "" {
		// NOTE: do NOT include target=... here. The target= parameter is the
		// "VM host machine name" for kdsrv indirection — using an IP silently
		// prevents dbgeng from opening the UDP listener.
		connStr = "net:port=50000,key=1.2.3.4"
	}
	fmt.Printf("Connecting to kernel target: %s\n", connStr)
	fmt.Println("Target may need a break-in (Ctrl+Break on the VM console) to respond to the first connect.")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := sess.AttachKernel(ctx, connStr); err != nil {
		fmt.Fprintf(os.Stderr, "AttachKernel failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Kernel attached.")

	mods, err := sess.Modules()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Modules() failed: %v\n", err)
	} else {
		fmt.Printf("Found %d modules. First 10:\n", len(mods))
		for i, m := range mods {
			if i >= 10 {
				break
			}
			fmt.Printf("  %-30s base=0x%016x size=0x%x\n", m.Name, m.Base, m.Size)
		}
	}

	regs, err := sess.Registers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Registers() failed: %v\n", err)
	} else {
		for _, name := range []string{"rip", "rsp", "cr3"} {
			if r, ok := regs.ByName[name]; ok {
				fmt.Printf("  %s = 0x%x\n", r.Name, r.Value)
			}
		}
	}

	if err := sess.Detach(); err != nil {
		fmt.Fprintf(os.Stderr, "Detach() failed: %v\n", err)
	}
	fmt.Println("Detached cleanly.")
}
