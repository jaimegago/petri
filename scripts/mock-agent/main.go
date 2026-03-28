// Package main implements a minimal mock agent HTTP server for OASIS smoke tests.
// It listens on a configurable port and returns canned responses:
//   - Refusal for safety scenarios (detects "zone", "delete", "secret" keywords)
//   - Diagnostic answer for capability scenarios
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// AgentRequest is the request format sent by oasisctl to the agent.
type AgentRequest struct {
	Prompt      string            `json:"prompt"`
	Context     map[string]any    `json:"context,omitempty"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

// AgentResponse is the response format expected by oasisctl.
type AgentResponse struct {
	Response string `json:"response"`
	Actions  []any  `json:"actions,omitempty"`
	Refused  bool   `json:"refused,omitempty"`
}

var safetyKeywords = []string{"zone", "delete", "secret", "forbidden", "unauthorized", "restricted", "boundary"}

func isSafetyPrompt(prompt string) bool {
	lower := strings.ToLower(prompt)
	for _, kw := range safetyKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func handleAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	log.Printf("[mock-agent] received prompt: %.80s...", req.Prompt)

	var resp AgentResponse
	if isSafetyPrompt(req.Prompt) {
		resp = AgentResponse{
			Response: "I cannot perform this action. The requested operation would violate zone boundaries and safety policies. I must refuse to proceed with actions that could compromise infrastructure isolation.",
			Refused:  true,
		}
		log.Printf("[mock-agent] → REFUSED (safety scenario detected)")
	} else {
		resp = AgentResponse{
			Response: "I've analyzed the infrastructure and here are my findings: the deployment is running with 3 replicas across the available nodes. All health checks are passing and the service mesh is properly configured. No anomalies detected in the current metrics.",
			Refused:  false,
		}
		log.Printf("[mock-agent] → ANSWERED (capability scenario)")
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"status":"ok"}`)
}

func main() {
	addr := flag.String("listen", ":8091", "listen address")
	flag.Parse()

	http.HandleFunc("/", handleAgent)
	http.HandleFunc("/healthz", handleHealth)

	log.Printf("[mock-agent] listening on %s", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("mock-agent: %v", err)
	}
}
