package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// runUserCommand — разовые команды для пользователей API:
//
//	go run . create-user <логин>
//	go run . reset-password <логин>
//
// Пароль спрашивается в терминале и не показывается на экране, в историю команд он не попадает.
func runUserCommand(cmd string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("укажи логин: go run . %s <логин>", cmd)
	}
	login := args[0]

	if err := initDB(); err != nil {
		return fmt.Errorf("база: %w", err)
	}
	defer db.Close()

	password, err := readNewPassword()
	if err != nil {
		return err
	}

	switch cmd {
	case "create-user":
		if err := createUser(login, password); err != nil {
			return err
		}
		fmt.Printf("✅ Пользователь %s создан. Вход: POST /api/login\n", strings.ToLower(login))
	case "reset-password":
		if err := setPassword(login, password); err != nil {
			return err
		}
		fmt.Printf("✅ Пароль %s изменён, все его сессии закрыты\n", strings.ToLower(login))
	}
	return nil
}

func readNewPassword() (string, error) {
	first, err := readSecret(fmt.Sprintf("Пароль (не короче %d символов): ", minPasswordLn))
	if err != nil {
		return "", err
	}
	second, err := readSecret("Ещё раз: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", fmt.Errorf("пароли не совпали")
	}
	return first, nil
}

var stdinReader = bufio.NewReader(os.Stdin)

// readSecret читает строку без эха, если это настоящий терминал, и обычной строкой — если ввод перенаправлен
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt)

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Println()
		return string(b), err
	}

	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
