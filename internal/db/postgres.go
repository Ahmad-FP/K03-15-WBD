package db

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

func encryptDSN(plain string) ([]byte, error) {
	gcm, err := gcmCipher()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plain), nil), nil
}

func decryptDSN(ciphertext []byte) (string, error) {
	gcm, err := gcmCipher()
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", errors.New("encrypted credential invalid")
	}
	plain, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
	return string(plain), err
}

func gcmCipher() (cipher.AEAD, error) {
	raw := os.Getenv("DATABASE_CREDENTIALS_KEY")
	if len(raw) != 64 {
		return nil, errors.New("DATABASE_CREDENTIALS_KEY harus berisi 64 karakter hex")
	}
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, errors.New("DATABASE_CREDENTIALS_KEY tidak valid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
