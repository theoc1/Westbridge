package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/dmalkin/westbridge/internal/auth"
	"golang.org/x/term"
)

func adminCommand(args []string) error {
	if len(args) != 2 || args[0] != "bootstrap-admin" {
		return errors.New("usage: westbridge bootstrap-admin LOGIN")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("run bootstrap-admin in a terminal to enter the password securely")
	}
	fmt.Fprint(os.Stderr, "Password (at least 12 characters): ")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	confirmation, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	if string(password) != string(confirmation) {
		return errors.New("passwords do not match")
	}
	store, err := auth.Open(envOr("WB_DB_PATH", "data/westbridge.db"))
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	user, err := store.Bootstrap(args[1], string(password))
	if err != nil {
		return err
	}
	fmt.Printf("Administrator %s created.\n", user.Login)
	return nil
}
