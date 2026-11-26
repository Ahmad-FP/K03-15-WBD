package authentication

import "golang.org/x/crypto/bcrypt"

var ErrPasswordTooLong = bcrypt.ErrPasswordTooLong

func Hash(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

func Check(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
