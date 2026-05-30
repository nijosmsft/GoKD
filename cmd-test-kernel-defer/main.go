// Test variant: AttachKernel with passive mode, then fire BreakIn from a
// separate goroutine after a delay. This lets dbgeng receive the first
// KDNET probe (so target address is known) before we issue SetInterrupt.
// If gokd's normal flow (SetInterrupt inside AttachKernel) is failing
// because target is unknown at that moment, this deferred variant should
// succeed where the normal one hangs.
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
		connStr = "net:port=50000,key=1.2.3.4"
	}

	delayStr := os.Getenv("BREAKIN_DELAY")
	delay := 10 * time.Second
	if delayStr != "" {
		if d, err := time.ParseDuration(delayStr); err == nil {
			delay = d
		}
	}

	fmt.Printf("Connecting to kernel target (PASSIVE): %s\n", connStr)
	fmt.Printf("Will call sess.BreakIn() after %s.\n", delay)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Spawn deferred break-in. Safe to call BreakIn from any goroutine.
	go func() {
		time.Sleep(delay)
		fmt.Printf("[%s] Calling sess.BreakIn() now\n", time.Now().Format("15:04:05.000"))
		if err := sess.BreakIn(); err != nil {
			fmt.Fprintf(os.Stderr, "BreakIn() failed: %v\n", err)
		} else {
			fmt.Println("BreakIn() returned nil")
		}
	}()

	t0 := time.Now()
	if err := sess.AttachKernel(ctx, connStr, gokd.KernelPassive); err != nil {
		fmt.Fprintf(os.Stderr, "AttachKernel failed after %s: %v\n", time.Since(t0), err)
		os.Exit(1)
	}
	fmt.Printf("Kernel attached after %s.\n", time.Since(t0))

	if mods, err := sess.Modules(); err == nil {
		fmt.Printf("Found %d modules. First 5:\n", len(mods))
		for i, m := range mods {
			if i >= 5 {
				break
			}
			fmt.Printf("  %-30s base=0x%016x\n", m.Name, m.Base)
		}
	}

	if regs, err := sess.Registers(); err == nil {
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
