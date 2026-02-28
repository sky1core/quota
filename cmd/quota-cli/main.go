package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/render"
)

type Payload struct {
	Claude any   `json:"claude"`
	Codex  any   `json:"codex"`
	Errors []any `json:"errors"`
}

func main() {
	var (
		jsonOut  = flag.Bool("json", false, "Output JSON")
		timeoutS = flag.Int("timeout", 40, "Timeout seconds")
	)
	flag.Parse()

	timeout := time.Duration(*timeoutS) * time.Second

	payload := Payload{Claude: nil, Codex: nil, Errors: []any{}}

	cq, err := claude.GetQuota(timeout)
	if err != nil {
		payload.Errors = append(payload.Errors, map[string]any{"provider": "claude", "error": err.Error()})
	} else {
		payload.Claude = cq
	}

	kq, err := codex.GetQuota(timeout)
	if err != nil {
		payload.Errors = append(payload.Errors, map[string]any{"provider": "codex", "error": err.Error()})
	} else {
		payload.Codex = kq
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			fmt.Fprintf(os.Stderr, "json encode: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Println(render.Text(map[string]any{"claude": payload.Claude, "codex": payload.Codex, "errors": payload.Errors}))
}
