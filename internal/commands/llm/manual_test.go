//go:build manual

package llm

import (
	"fmt"
	"os"
	"testing"

	"github.com/stempit/meshcore-go-bot/internal/config"
)

// TestManualLLM is a manual harness for iterating on LLM prompts and models
// (formerly cmd/llm). The manual build tag keeps it out of ordinary
// `go test ./...` runs, so it never fires real API calls by accident.
//
//	go test -tags manual ./internal/commands/llm -v -run TestManualLLM
func TestManualLLM(t *testing.T) {
	// config.yaml lives in the repo root; go test starts in the package dir.
	if err := os.Chdir("../.."); err != nil {
		t.Fatal(err)
	}
	if err := config.Load("config.yaml"); err != nil {
		t.Fatalf("failed to load configuration: %v", err)
	}

	for _, q := range []string{
		"круче Кант или Гегель?",
		"как определить съедобный гриб?",
		"как разжечь огонь без спичек?",
		"кто победит - майк тайсон или джеки чан?",
	} {
		fmt.Println(ProcessLLMMessage(q))
	}
}
