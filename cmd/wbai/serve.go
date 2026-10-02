package main

import (
	"fmt"
	"net/http"
	"time"

	"codex-workbuddyai-setup/internal/gateway"
)

func cmdServe(args []string) error {
	fs := newFlagSet("serve")
	addr := fs.String("addr", defaultAddr, "listen address")
	model := fs.String("model", "", "model used when the request omits one")
	verbose := fs.Bool("verbose", false, "log each request")
	if err := fs.Parse(args); err != nil {
		return err
	}

	chosen := *model
	if chosen == "" {
		chosen = defaultModel()
	}
	if chosen != "" {
		fmt.Printf("Default model: %s\n", chosen)
	} else {
		fmt.Println("Default model: (none yet, run `wbai models`)")
	}

	srv := gateway.NewServer(*addr, chosen, *verbose)
	return srv.Run()
}

func cmdDoctor(args []string) error {
	fs := newFlagSet("doctor")
	addr := fs.String("addr", defaultAddr, "gateway address to probe")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ok := true
	check := func(label string, err error) {
		if err != nil {
			ok = false
			fmt.Printf("  FAIL  %-18s %v\n", label, err)
			return
		}
		fmt.Printf("  ok    %s\n", label)
	}

	fmt.Println("Storage")
	if _, err := storageDir(); err != nil {
		check("directory", err)
	}
	check("credentials.json", requireFile(must(storageFile("credentials.json"))))
	check("upstream.json", requireFile(must(storageFile("upstream.json"))))
	check("workbuddyai-models.json", requireFile(must(codexCatalogPath())))

	fmt.Println("Codex")
	check("config.toml", requireFile(must(codexConfigPath())))

	fmt.Println("Gateway")
	url := "http://" + *addr + "/healthz"
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		check("reachable", fmt.Errorf("%s not reachable: %w", url, err))
	} else {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			check("reachable", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url))
		} else {
			check("reachable", nil)
		}
	}

	if !ok {
		return fmt.Errorf("one or more checks failed")
	}
	fmt.Println("\nAll checks passed.")
	return nil
}
