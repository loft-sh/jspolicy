package random

import (
	"math/rand"
	"time"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

var letterRunes = []rune("abcdefghijklmnopqrstuvwxyz")

// RandomString creates a new random string with the given length
func RandomString(length int) string {
	b := make([]rune, length)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

var moreLetterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

// RandomFullAlphabetString creates a new random string with the given length
func RandomFullAlphabetString(length int) string {
	b := make([]rune, length)
	for i := range b {
		b[i] = moreLetterRunes[rand.Intn(len(moreLetterRunes))]
	}
	return string(b)
}
