package main

import (
	"fmt"
	"os"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/ckpack"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printUsage()
		os.Exit(0)
	}

	switch args[0] {
	case "dev":
		if err := runDev(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "\ncorekiosk dev error: %v\n", err)
			os.Exit(1)
		}
	case "pack", "build":
		if err := ckpack.RunCLI(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "\ncorekiosk pack error: %v\n", err)
			os.Exit(1)
		}
	case "version", "-v", "--version":
		fmt.Printf("corekiosk %s (protocol %d)\n", ckframework.Version, ckframework.ProtocolVersion)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Println(`corekiosk — activation kiosk developer & packaging CLI

Usage:
  corekiosk dev [flags] [-- <game-args>...]
                            Start Go server (with live reload), Companion Web, and game
  corekiosk pack [flags]    Build Companion Web + Game + Go launcher into a release .zip
  corekiosk version         Print corekiosk framework & protocol version

Dev flags:
  -config <path>            Path to kiosk.json (default: auto-detected upwards)
  -listen <addr>            Server listen address (default: :8080)
  -headless                 Run Go server without opening the native Fyne launcher window
  -no-companion             Skip starting Expo React Native Web dev server
  -no-game                  Skip launching game
  -no-open                  Do not auto-open http://localhost:8081 in browser
  -no-watch                 Disable live Go file watcher / auto-rebuild
  -godot <path>             Explicit path to Godot 4 executable`)
}
