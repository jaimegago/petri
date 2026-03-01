package cli

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// promptSecret prints prompt to stderr and reads a secret value from stdin
// with terminal echo disabled so the input is not visible.
// A newline is printed after the hidden input to keep output clean.
func promptSecret(prompt string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", prompt)
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr) // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("reading secret input for %q: %w", prompt, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// credentialKey returns a standardised map key for a named credential.
func credentialKey(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, " ", "_"))
}
