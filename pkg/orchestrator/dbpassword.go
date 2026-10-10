package orchestrator

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"

	pulumiprov "github.com/jaimegago/petri/pkg/provisioners/pulumi"
	"github.com/jaimegago/petri/pkg/types"
)

const (
	// dbPasswordCredential is the lab credential type holding the encrypted
	// Postgres administrator password.
	dbPasswordCredential = "postgres_password"
	// dbPasswordConfigKey is the Pulumi secret config key the Azure program
	// reads the administrator password from (config.requireSecret).
	dbPasswordConfigKey = "dbPassword"

	dbPasswordLength = 32
	// dbPasswordSymbols are the non-alphanumeric characters a password may
	// carry: URL-unreserved, so the password is safe in a connection string.
	dbPasswordSymbols  = "-_."
	dbPasswordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789" + dbPasswordSymbols
	// dbPasswordMaxAttempts bounds the draws for a password holding every
	// character class; one draw misses a class about one time in five.
	dbPasswordMaxAttempts = 64
)

// needsDBPassword reports whether the level declares a Postgres database,
// which the Pulumi program creates with a petri-generated admin password.
func needsDBPassword(spec types.LevelSpec) bool {
	for _, db := range spec.Databases {
		if db.Type == "postgresql" {
			return true
		}
	}
	return false
}

// provisionDBPassword generates the lab's Postgres administrator password,
// stores it encrypted as a lab credential, and hands it to the stack as
// secret config. The plaintext lives only in memory: it is never logged,
// never written decrypted, and never placed in an error.
func (o *Orchestrator) provisionDBPassword(ctx context.Context, opts CreateOptions, workDir, stackName string) error {
	password, err := generateDBPassword(rand.Reader)
	if err != nil {
		return fmt.Errorf("generating postgres password: %w", err)
	}
	enc, err := o.deps.Cipher.Encrypt([]byte(password))
	if err != nil {
		return fmt.Errorf("encrypting postgres password: %w", err)
	}
	if err := o.deps.State.StoreCredential(ctx, &types.LabCredential{
		ID:             uuid.New(),
		LabID:          opts.Lab.ID,
		CredentialType: dbPasswordCredential,
		EncryptedValue: enc,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("storing postgres password credential: %w", err)
	}
	if err := o.deps.PulumiProv.SetSecretConfig(ctx, pulumiprov.SecretConfigOptions{
		WorkDir:   workDir,
		StackName: stackName,
		Key:       dbPasswordConfigKey,
		Value:     password,
		Env:       opts.CloudEnv,
	}); err != nil {
		return fmt.Errorf("setting postgres password config: %w", err)
	}
	return nil
}

// generateDBPassword draws a password uniformly from dbPasswordAlphabet,
// redrawing until it holds an upper-case letter, a lower-case letter, a digit
// and a symbol. That satisfies Azure's administrator password policy (three
// of the four classes, 8–128 characters) with margin.
func generateDBPassword(r io.Reader) (string, error) {
	size := big.NewInt(int64(len(dbPasswordAlphabet)))
	buf := make([]byte, dbPasswordLength)
	for attempt := 0; attempt < dbPasswordMaxAttempts; attempt++ {
		for i := range buf {
			n, err := rand.Int(r, size)
			if err != nil {
				return "", fmt.Errorf("reading randomness: %w", err)
			}
			buf[i] = dbPasswordAlphabet[n.Int64()]
		}
		if hasAllPasswordClasses(string(buf)) {
			return string(buf), nil
		}
	}
	return "", fmt.Errorf("no password with every character class in %d draws", dbPasswordMaxAttempts)
}

// hasAllPasswordClasses reports whether s holds an upper-case letter, a
// lower-case letter, a digit and one of dbPasswordSymbols.
func hasAllPasswordClasses(s string) bool {
	var upper, lower, digit, symbol bool
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= '0' && c <= '9':
			digit = true
		case strings.ContainsRune(dbPasswordSymbols, c):
			symbol = true
		}
	}
	return upper && lower && digit && symbol
}
