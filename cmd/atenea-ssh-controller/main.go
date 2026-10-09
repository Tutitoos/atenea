package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/Tutitoos/atenea/internal/sshcontrol/controller"
)

// The controller deliberately exposes no SSH commands in this foundation.
func main() {
	rootFlag := flag.String("root", "", "absolute per-user state directory")
	stopFlag := flag.Bool("stop", false, "stop the current user's controller")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	root := *rootFlag
	if root == "" {
		var err error
		root, err = controller.Root()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	id, err := controller.InstallationID(root)
	if err == nil && *stopFlag {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, err = controller.Call(ctx, root, id, "stop")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err == nil {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		err = controller.Serve(ctx, root, id)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
