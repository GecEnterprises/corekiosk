package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckdiscovery"
)

func runDiscover(args []string) error {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 4*time.Second, "Scan window")
	methods := fs.String("methods", "udp,mdns,ssdp", "Comma-separated methods: udp,mdns,ssdp")
	udpPort := fs.Int("udp-port", ckdiscovery.DefaultUDPPort, "UDP probe/offer port")
	jsonOut := fs.Bool("json", false, "Print JSON instead of a table")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	results, err := ckdiscovery.Scan(ctx, ckdiscovery.ScanOptions{
		Timeout: *timeout,
		Methods: splitDiscoverMethods(*methods),
		UDPPort: *udpPort,
	})
	if err != nil {
		return err
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}
	if len(results) == 0 {
		fmt.Println("No corekiosk servers found.")
		fmt.Println("Check that the tablet/PC are on the same LAN and that Windows Firewall allows UDP 8099, 5353, and 1900.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SERVER ID\tNAME\tHOSTS\tPORT\tMETHODS")
	for _, r := range results {
		name := r.Name
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n",
			r.ServerID,
			name,
			strings.Join(r.Hosts, ", "),
			r.Port,
			strings.Join(r.Methods, ","),
		)
	}
	return w.Flush()
}

func splitDiscoverMethods(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
