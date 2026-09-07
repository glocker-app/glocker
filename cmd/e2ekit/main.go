// Command e2ekit is a prototype for glockpeek's end-to-end encryption, built up
// one subcommand at a time so the scheme can be validated from the command line
// before it's wired into the agent, server, and browser. See docs/e2e-design.md.
//
// The cycle so far:
//
//	e2ekit create-key [-out master.key]              # generate a master key
//	e2ekit wrap-key  -in master.key [-out vault.json] # wrap it under a passphrase
//	e2ekit unlock    -in vault.json                   # passphrase -> recover the key
//
// The passphrase is read from the terminal (no echo), or from stdin when piped.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"glocker/internal/e2e"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "create-key":
		createKey(os.Args[2:])
	case "wrap-key":
		wrapKey(os.Args[2:])
	case "unlock":
		unlock(os.Args[2:])
	case "recover":
		recoverCmd(os.Args[2:])
	case "encrypt":
		encryptCmd(os.Args[2:])
	case "decrypt":
		decryptCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  e2ekit create-key [-out master.key]")
	fmt.Fprintln(os.Stderr, "  e2ekit wrap-key  -in master.key [-out vault.json]")
	fmt.Fprintln(os.Stderr, "  e2ekit unlock    -in vault.json                                  # passphrase -> master key")
	fmt.Fprintln(os.Stderr, "  e2ekit recover   -in vault.json                                  # recovery code -> master key")
	fmt.Fprintln(os.Stderr, "  e2ekit encrypt   -key master.key [-aad ctx] [-out record.json]   # plaintext on stdin")
	fmt.Fprintln(os.Stderr, "  e2ekit decrypt   -key master.key [-aad ctx] -in record.json")
	os.Exit(2)
}

func createKey(args []string) {
	fs := flag.NewFlagSet("create-key", flag.ExitOnError)
	out := fs.String("out", "master.key", "path to write the master key (base64)")
	_ = fs.Parse(args)

	mk, err := e2e.GenerateMasterKey()
	if err != nil {
		fail(err)
	}
	// Saved base64 for now so it's easy to eyeball and pipe into the next
	// subcommands. It is the raw master key — keep this file private (mode 0600).
	if err := os.WriteFile(*out, []byte(base64.StdEncoding.EncodeToString(mk)+"\n"), 0o600); err != nil {
		fail(err)
	}
	fmt.Printf("✓ master key generated (%d bytes) and written to %s (base64, mode 0600)\n", e2e.MasterKeyLen, *out)
}

func wrapKey(args []string) {
	fs := flag.NewFlagSet("wrap-key", flag.ExitOnError)
	in := fs.String("in", "master.key", "path to the master key (base64) to wrap")
	out := fs.String("out", "vault.json", "path to write the vault")
	_ = fs.Parse(args)

	mk, err := readKey(*in)
	if err != nil {
		fail(err)
	}
	pass, err := readPassphrase("Passphrase (wraps the master key): ")
	if err != nil {
		fail(err)
	}
	if len(pass) == 0 {
		fail(fmt.Errorf("empty passphrase"))
	}

	v, recoveryCode, err := e2e.WrapMasterKey(mk, pass)
	if err != nil {
		fail(err)
	}
	if err := writeJSON(*out, v); err != nil {
		fail(err)
	}
	fmt.Printf("✓ master key wrapped under your passphrase → %s\n", *out)
	fmt.Printf("  PBKDF2-HMAC-SHA256 %d iterations — this vault is what would go to the server (no raw key, no passphrase).\n",
		v.PBKDF2.Iterations)
	fmt.Println()
	fmt.Println("  RECOVERY CODE — write this down. It's shown only once and opens the vault")
	fmt.Println("  if you forget the passphrase. Lose both and the data is unrecoverable.")
	fmt.Printf("      %s\n", recoveryCode)
}

func unlock(args []string) {
	fs := flag.NewFlagSet("unlock", flag.ExitOnError)
	in := fs.String("in", "vault.json", "path to the vault to unlock")
	_ = fs.Parse(args)

	var v e2e.Vault
	if err := readJSON(*in, &v); err != nil {
		fail(err)
	}
	pass, err := readPassphrase("Passphrase: ")
	if err != nil {
		fail(err)
	}

	mk, err := v.Open(pass)
	if err != nil {
		// A wrong passphrase lands here — the GCM tag fails to verify.
		fail(fmt.Errorf("could not unlock (wrong passphrase?): %w", err))
	}
	// Print the recovered key so the round-trip can be checked against master.key.
	fmt.Println(base64.StdEncoding.EncodeToString(mk))
}

// recoverCmd opens the vault with the recovery code instead of the passphrase —
// the fallback path when the passphrase is lost. Prints the master key.
func recoverCmd(args []string) {
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	in := fs.String("in", "vault.json", "path to the vault to recover")
	_ = fs.Parse(args)

	var v e2e.Vault
	if err := readJSON(*in, &v); err != nil {
		fail(err)
	}
	code, err := readPassphrase("Recovery code: ")
	if err != nil {
		fail(err)
	}

	mk, err := v.OpenWithRecovery(string(code))
	if err != nil {
		fail(fmt.Errorf("could not recover (wrong recovery code?): %w", err))
	}
	fmt.Println(base64.StdEncoding.EncodeToString(mk))
}

// encryptCmd encrypts plaintext (read from stdin) under the master key and writes
// the record blob — exactly what the agent would upload to the server.
func encryptCmd(args []string) {
	fs := flag.NewFlagSet("encrypt", flag.ExitOnError)
	keyPath := fs.String("key", "master.key", "path to the master key (base64)")
	aad := fs.String("aad", "", "context to bind (optional; must match on decrypt)")
	out := fs.String("out", "record.json", "path to write the encrypted record")
	_ = fs.Parse(args)

	mk, err := readKey(*keyPath)
	if err != nil {
		fail(err)
	}
	plaintext, err := io.ReadAll(os.Stdin)
	if err != nil {
		fail(err)
	}
	if len(plaintext) == 0 {
		fail(fmt.Errorf("no plaintext on stdin — pipe something in"))
	}

	blob, err := e2e.EncryptRecord(mk, plaintext, []byte(*aad))
	if err != nil {
		fail(err)
	}
	if err := writeJSON(*out, blob); err != nil {
		fail(err)
	}
	fmt.Printf("✓ encrypted %d bytes → %s (nonce + ciphertext only; the server sees no plaintext)\n", len(plaintext), *out)
}

// decryptCmd decrypts a record blob under the master key and prints the plaintext.
func decryptCmd(args []string) {
	fs := flag.NewFlagSet("decrypt", flag.ExitOnError)
	keyPath := fs.String("key", "master.key", "path to the master key (base64)")
	aad := fs.String("aad", "", "context (must match what was used to encrypt)")
	in := fs.String("in", "record.json", "path to the encrypted record")
	_ = fs.Parse(args)

	mk, err := readKey(*keyPath)
	if err != nil {
		fail(err)
	}
	var blob e2e.Box
	if err := readJSON(*in, &blob); err != nil {
		fail(err)
	}

	plaintext, err := e2e.DecryptRecord(mk, blob, []byte(*aad))
	if err != nil {
		fail(fmt.Errorf("could not decrypt (wrong key/aad, or tampered?): %w", err))
	}
	os.Stdout.Write(plaintext)
	if n := len(plaintext); n == 0 || plaintext[n-1] != '\n' {
		fmt.Println()
	}
}

// readKey reads a base64-encoded key file and decodes it.
func readKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
}

// readPassphrase reads without echo from a terminal; when stdin is piped (tests,
// scripting) it reads a single line so the tool stays usable non-interactively.
func readPassphrase(prompt string) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		p, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return p, err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return nil, err
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "e2ekit:", err)
	os.Exit(1)
}
