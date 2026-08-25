// tailscale-multitaild is the v1 multitail supervisor daemon.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jay/tailscale-multitail/internal/config"
	"github.com/jay/tailscale-multitail/internal/runtime"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: tailscale-multitaild run [--config PATH] [--state-root PATH] [--validate-config] [--once]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cp := fs.String("config", config.DefaultPath, "config path")
	root := fs.String("state-root", config.DefaultStateRoot, "state root (test override)")
	valid := fs.Bool("validate-config", false, "validate and exit")
	once := fs.Bool("once", false, "start profiles, print status JSON, and exit")
	fs.Parse(os.Args[2:])
	c, e := config.Load(*cp)
	if e != nil {
		log.Fatal(e)
	}
	if *valid {
		fmt.Println("valid")
		return
	}
	if *root == config.DefaultStateRoot && filepath.IsAbs(*cp) == false {
		log.Fatal("relative config paths require an explicit --state-root for test safety")
	}
	s, e := runtime.New(c, *root)
	if e != nil {
		log.Fatal(e)
	}
	if e = s.Start(context.Background()); e != nil {
		log.Fatal(e)
	}
	defer s.Close()
	if *once {
		inv := s.Inventory()
		json.NewEncoder(os.Stdout).Encode(struct {
			Profiles   any `json:"profiles"`
			Targets    any `json:"targets"`
			Collisions any `json:"canonical_collisions"`
		}{s.Status(), inv.Targets, inv.Collisions()})
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
}
