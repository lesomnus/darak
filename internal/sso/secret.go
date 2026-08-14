//darak:local-state
//
// This file resolves one path: the client secret named by a flag, read once
// before the listener exists. Nothing from a request reaches it, and no user
// owns it — it is this server's credential with the identity provider.
//
// See internal/lint for the check this marker is read by.

package sso

import (
	"fmt"
	"os"
	"strings"
)

// ReadSecret loads the client secret from a file.
//
// A FILE, not a flag: argv is world-readable through /proc, so a secret on the
// command line is published to every user on the box — which on a file server
// is exactly the population its permissions exist to separate. The same
// argument is why internal/auth sends credentials to ntlm_auth over stdin.
//
// And not an environment variable either, which is the usual answer in a
// container. Helpers are started with exec and inherit this process's
// environment, so a secret kept there would be readable by every user who has a
// helper running, through their own /proc/self/environ. A deployment holding
// the secret in an env var should have its entrypoint write it to a file and
// pass the path — the shell doing that is not this process, and the exported
// variable never has to exist.
//
// Trailing whitespace is trimmed because `echo secret > file` is how this file
// gets written, and a stray newline would otherwise produce an authentication
// failure at the provider that says nothing about which end is wrong.
func ReadSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("sso: client secret: %w", err)
	}
	secret := strings.TrimSpace(string(b))
	if secret == "" {
		return "", fmt.Errorf("sso: client secret file %s is empty", path)
	}
	return secret, nil
}
