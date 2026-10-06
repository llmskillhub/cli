package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/llmskillhub/cli/internal/client"
	"github.com/llmskillhub/cli/internal/config"
)

// cmdLogin stores an access token.
//
// The token is created in the browser, on the account page, and pasted here.
// That is deliberately the whole flow: the alternative worth having is a device
// code, and until that exists, asking someone to paste a token they made with
// scopes they chose is honest -- it never invents a credential on their behalf,
// and the scopes are visible at the moment they are granted.
//
// Read from stdin rather than a flag, so the token does not land in shell
// history or in the process list where any other user on the machine can see it.
func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	stdin := fs.Bool("with-token", false, "read the token from stdin without prompting (for scripts)")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	var token string
	if *stdin {
		b, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && b == "" {
			return fmt.Errorf("no token on stdin")
		}
		token = strings.TrimSpace(b)
	} else {
		fmt.Printf("Create a token at %s/account (Access tokens).\n", webOrigin(cfg))
		fmt.Printf("It needs at least the skills:publish scope to publish.\n\n")
		t, err := readSecret("Paste it here: ")
		if err != nil {
			if errors.Is(err, errCancelled) {
				return fmt.Errorf("cancelled")
			}
			return fmt.Errorf("no token given")
		}
		token = strings.TrimSpace(t)
	}
	if token == "" {
		return fmt.Errorf("no token given")
	}
	if !strings.HasPrefix(token, "skh_live_") {
		return fmt.Errorf("that does not look like an access token\n  They start with skh_live_ and are created at %s/account", webOrigin(cfg))
	}

	// Verify before saving. Storing a token that does not work turns the next
	// command's failure into a puzzle about which of the two steps went wrong.
	c := client.New(cfg.API, cfg.Ingest, token)
	me, err := c.Me()
	if err != nil {
		return err
	}
	cfg.Token, cfg.Handle = token, me.Handle
	if err := cfg.Save(); err != nil {
		return err
	}
	// Whatever was cached belonged to the previous token.
	client.ForgetAccess(config.AccessCachePath())
	p, _ := config.Path()
	fmt.Printf("Signed in as %s. Token saved to %s\n", me.Handle, p)
	return nil
}

func cmdLogout() error {
	if err := config.Clear(); err != nil {
		return err
	}
	fmt.Println("Signed out. The token still exists — revoke it at /account if it leaked.")
	return nil
}

func cmdWhoami() error {
	c, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}
	me, err := c.Me()
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", me.Handle)
	fmt.Printf("  api      %s\n", cfg.API)
	fmt.Printf("  ingest   %s\n", cfg.Ingest)
	if me.IsAdmin {
		fmt.Printf("  reviewer yes\n")
	}
	return nil
}

// firstEnv mirrors config.env: the new name wins, the old one still works.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func clientFromConfig() (*client.Client, *config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	if cfg.Token == "" {
		return nil, nil, fmt.Errorf("not signed in\n  Run: llmsh login")
	}
	return newClient(cfg), cfg, nil
}

// webOrigin guesses where the browser UI lives, for the "create a token" hint.
//
// A guess, and said as one: the API and the web app are separate deployments and
// nothing in the API's address reliably gives the other.
func webOrigin(cfg *config.Config) string {
	if v := strings.TrimSpace(firstEnv("LLMSH_WEB", "ALPHAQ_WEB")); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	if strings.Contains(cfg.API, "localhost") {
		return "http://localhost:5173"
	}
	return strings.Replace(cfg.API, "api.", "", 1)
}

// loadConfigOnly is for commands that work without signing in. Installing a
// public skill needs no credential, and requiring one would make the catalogue
// closed for no reason.
func loadConfigOnly() (*config.Config, error) { return config.Load() }

func newClient(cfg *config.Config) *client.Client {
	c := client.New(cfg.API, cfg.Ingest, cfg.Token)
	// Only for the stored token. A token from LLMSH_TOKEN is a CI job's, and
	// a CI job's credentials should not be written to disk -- see config.Load.
	if os.Getenv("LLMSH_TOKEN") == "" && os.Getenv("ALPHAQ_TOKEN") == "" {
		c.CachePath = config.AccessCachePath()
	}
	return c
}
