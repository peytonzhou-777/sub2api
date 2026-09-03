//go:build unit

package service

import (
	"errors"
	"strings"
)

type openAIPersonaTestEncryptor struct{}

func (openAIPersonaTestEncryptor) Encrypt(plaintext string) (string, error) {
	return "persona-test:" + plaintext, nil
}

func (openAIPersonaTestEncryptor) Decrypt(ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, "persona-test:") {
		return "", errors.New("invalid Persona test ciphertext")
	}
	return strings.TrimPrefix(ciphertext, "persona-test:"), nil
}
