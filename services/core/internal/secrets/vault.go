package secrets

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

var (
	ErrKeyUnavailable = errors.New("master key unavailable")
	ErrInvalidSecret  = errors.New("encrypted secret is invalid")
)

type Encrypted struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

type Vault struct {
	key   [32]byte
	keyID string
}

func LoadVault(path string) (*Vault, error) {
	data, err := ReadPrivateFile(path, 66)
	if err != nil {
		clear(data)
		return nil, fmt.Errorf("read master key: %w", err)
	}
	defer clear(data)
	if bytes.HasSuffix(data, []byte("\n")) {
		data = bytes.TrimSuffix(data, []byte("\n"))
		data = bytes.TrimSuffix(data, []byte("\r"))
	}
	if len(data) != 64 {
		return nil, errors.New("master key file must contain 64 hexadecimal characters")
	}
	var key [32]byte
	defer clear(key[:])
	if _, err := hex.Decode(key[:], data); err != nil {
		return nil, errors.New("master key file contains invalid hexadecimal characters")
	}
	return NewVault(key[:])
}

func NewVault(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("master key must contain 32 bytes")
	}
	var result Vault
	copy(result.key[:], key)
	fingerprint := sha256.Sum256(key)
	result.keyID = hex.EncodeToString(fingerprint[:8])
	return &result, nil
}

func (v *Vault) Seal(ctx context.Context, tenantID, providerID int64, plaintext []byte) (Encrypted, error) {
	if err := ctx.Err(); err != nil {
		return Encrypted{}, err
	}
	if tenantID <= 0 || providerID <= 0 || len(plaintext) == 0 || len(plaintext) > 4096 {
		return Encrypted{}, errors.New("valid provider identity and secret are required")
	}
	aead, err := v.aead()
	if err != nil {
		return Encrypted{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Encrypted{}, fmt.Errorf("generate encryption nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, associatedData(tenantID, providerID))
	if err := ctx.Err(); err != nil {
		return Encrypted{}, err
	}
	return Encrypted{KeyID: v.keyID, Nonce: nonce, Ciphertext: ciphertext}, nil
}

func (v *Vault) Open(ctx context.Context, tenantID, providerID int64, encrypted Encrypted) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID <= 0 || providerID <= 0 || encrypted.KeyID != v.keyID {
		return nil, ErrKeyUnavailable
	}
	aead, err := v.aead()
	if err != nil {
		return nil, err
	}
	if len(encrypted.Nonce) != aead.NonceSize() || len(encrypted.Ciphertext) < aead.Overhead() || len(encrypted.Ciphertext) > 4096+aead.Overhead() {
		return nil, ErrInvalidSecret
	}
	plaintext, err := aead.Open(nil, encrypted.Nonce, encrypted.Ciphertext, associatedData(tenantID, providerID))
	if err != nil || len(plaintext) == 0 {
		return nil, ErrInvalidSecret
	}
	return plaintext, nil
}

func (v *Vault) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(v.key[:])
	if err != nil {
		return nil, fmt.Errorf("initialize secret cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize secret encryption: %w", err)
	}
	return aead, nil
}

func associatedData(tenantID, providerID int64) []byte {
	data := make([]byte, len("tuskrag.model-provider.v1")+16)
	copy(data, "tuskrag.model-provider.v1")
	binary.BigEndian.PutUint64(data[len(data)-16:], uint64(tenantID))
	binary.BigEndian.PutUint64(data[len(data)-8:], uint64(providerID))
	return data
}
