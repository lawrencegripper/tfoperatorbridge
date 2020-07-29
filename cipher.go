package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"os"
)

// TerraformStateCipher is a cipher interface for encrypting and decrypting terraform state
type TerraformStateCipher interface {
	Encrypt(string) (string, error)
	Decrypt(string) (string, error)
}

// TerraformAesStateCipher is a cipher implementation for AES
type TerraformAesStateCipher struct {
	cipherBlock cipher.Block
}

// NewAesCipher creates a new AES cipher
func NewAesCipher(encryptionKey []byte) *TerraformAesStateCipher {
	cipherBlock, err := aes.NewCipher(encryptionKey)
	if err != nil {
		setupLog.Error(err, "unable to setup aes encryption")
		os.Exit(1)
	}
	return &TerraformAesStateCipher{
		cipherBlock: cipherBlock,
	}
}

// Encrypt performs AES encryption using a CFB mode
func (c *TerraformAesStateCipher) Encrypt(plaintext string) (string, error) {
	// Pad the plaintext to a size divisible by the aes block size
	padded, err := c.pad([]byte(plaintext))
	if err != nil {
		return "", err
	}
	ciphertext := make([]byte, aes.BlockSize+len(padded))

	// Write a random byte sequence to iv buffer as the first block in the ciphertext
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", err
	}

	stream := cipher.NewCFBEncrypter(c.cipherBlock, iv)
	stream.XORKeyStream(ciphertext[aes.BlockSize:], padded)

	return string(ciphertext), nil
}

// Decrypt performs AES decryption using a CFB mode
func (c *TerraformAesStateCipher) Decrypt(ciphertext string) (string, error) {
	// Extract the iv from the ciphertext's first block
	iv := []byte(ciphertext[:aes.BlockSize])
	ciphertext = ciphertext[aes.BlockSize:]

	b := []byte(ciphertext)
	cfb := cipher.NewCFBDecrypter(c.cipherBlock, iv)
	cfb.XORKeyStream(b, b)

	// Unpad the decrypted text
	unpadded, err := c.unpad(b)
	if err != nil {
		return "", err
	}
	return string(unpadded), nil
}

// pad pads the input text to the aes block size with the pad size
func (c *TerraformAesStateCipher) pad(text []byte) ([]byte, error) {
	paddingSize := aes.BlockSize - len(text)%aes.BlockSize
	padding := bytes.Repeat([]byte{byte(paddingSize)}, paddingSize)
	padded := append(text, padding...)
	return padded, nil
}

// unpad removes any padding from the input text
func (c *TerraformAesStateCipher) unpad(text []byte) ([]byte, error) {
	length := len(text)
	paddingSize := int(text[length-1])

	if paddingSize > length {
		return []byte{}, fmt.Errorf("unpad error. This could happen when incorrect encryption key is used")
	}
	return text[:(length - paddingSize)], nil
}
