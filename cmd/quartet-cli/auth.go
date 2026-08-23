package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/fanlv/quartet/types/model"
	"golang.org/x/term"
)

const authUsage = `quartet-cli auth — manage the login session

Usage:
  quartet-cli auth login [--username NAME]
  quartet-cli auth me
  quartet-cli auth logout
`

func runAuthGroup(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, authUsage)
		return errUsage
	}
	switch args[0] {
	case "login":
		return cmdAuthLogin(args[1:])
	case "me":
		return cmdAuthMe(args[1:])
	case "logout":
		return cmdAuthLogout(args[1:])
	case "-h", "--help", "help":
		fmt.Print(authUsage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown auth command %q\n\n%s", args[0], authUsage)
		return errUsage
	}
}

func cmdAuthLogin(args []string) error {
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	username := fs.String("username", "", "login username")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("auth login takes no positional arguments")
	}
	if strings.TrimSpace(*username) == "" {
		fmt.Fprint(os.Stderr, "Username: ")
		if _, err := fmt.Fscanln(os.Stdin, username); err != nil {
			return err
		}
	}
	fmt.Fprint(os.Stderr, "Password: ")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	client := newClient()
	client.credentialErr = nil
	raw, err := json.Marshal(model.LoginRequest{Username: *username, Password: string(password)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, client.baseURL+"/api/v1/auth/login", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("backend returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var principal model.AuthPrincipal
	if err := json.Unmarshal(body, &principal); err != nil {
		return err
	}
	var cookie string
	for _, item := range resp.Cookies() {
		if item.Name == "quartet_session" {
			cookie = item.Value
			break
		}
	}
	if cookie == "" {
		return errors.New("login response did not include quartet_session cookie")
	}
	if err := saveStoredSession(client.baseURL, storedSession{Cookie: cookie, CSRFToken: principal.CSRFToken}); err != nil {
		return err
	}
	fmt.Printf("logged in as %s (%s)\n", principal.User.DisplayName, principal.User.Username)
	return nil
}
func cmdAuthMe(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("auth me takes no arguments")
	}
	raw, err := newClient().do(context.Background(), http.MethodGet, "/api/v1/auth/me", nil)
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}
func cmdAuthLogout(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("auth logout takes no arguments")
	}
	client := newClient()
	requestErr := func() error {
		_, err := client.do(context.Background(), http.MethodPost, "/api/v1/auth/logout", nil)
		return err
	}()
	if err := saveStoredSession(client.baseURL, storedSession{}); err != nil {
		return err
	}
	if requestErr != nil {
		return fmt.Errorf("local session cleared; backend logout failed: %w", requestErr)
	}
	fmt.Println("logged out")
	return nil
}
