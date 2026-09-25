package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/dmalkin/westbridge/internal/auth"
	"github.com/dmalkin/westbridge/internal/database"
	"golang.org/x/term"
)

func adminCommand(args []string) error {
	if len(args) != 2 || (args[0] != "bootstrap-admin" && args[0] != "reset-password") {
		return errors.New("usage: westbridge {bootstrap-admin|reset-password} LOGIN")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("run %s in a terminal to enter the password securely", args[0])
	}
	fmt.Fprint(os.Stderr, "Password (at least 3 characters): ")
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
	db, err := database.Open(envOr("WB_DB_PATH", "data/westbridge.db"))
	if err != nil {
		return err
	}
	store := auth.New(db)
	defer func() { _ = db.Close() }()
	if args[0] == "reset-password" {
		user, err := store.ResetPassword(args[1], string(password))
		if err != nil {
			return err
		}
		fmt.Printf("Password reset for %s. Existing sessions revoked.\n", user.Login)
		return nil
	}
	user, err := store.Bootstrap(args[1], string(password))
	if err != nil {
		return err
	}
	fmt.Printf("Administrator %s created.\n", user.Login)
	return nil
}
