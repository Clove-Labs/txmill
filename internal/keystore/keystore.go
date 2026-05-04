package keystore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/argon2"
)

const (
	fileVersion = 1
	keyLen      = 32
	saltLen     = 16

	kdfArgon2id  = "argon2id"
	cipherAESGCM = "aes-256-gcm"
)

var defaultKDF = kdfParams{
	Name:    kdfArgon2id,
	Time:    1,
	Memory:  64 * 1024,
	Threads: 4,
	KeyLen:  keyLen,
}

type kdfParams struct {
	Name    string `json:"name"`
	Salt    []byte `json:"salt"`
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"`
	Threads uint8  `json:"threads"`
	KeyLen  uint32 `json:"key_len"`
}

type cipherBlob struct {
	Name       string `json:"name"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

type fileFormat struct {
	Version int        `json:"version"`
	Address string     `json:"address"`
	KDF     kdfParams  `json:"kdf"`
	Cipher  cipherBlob `json:"cipher"`
}

type Keystore struct {
	dir      string
	password []byte
}

func New(dir, password string) (*Keystore, error) {
	if password == "" {
		return nil, errors.New("keystore: password is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("keystore: mkdir: %w", err)
	}
	return &Keystore{dir: dir, password: []byte(password)}, nil
}

func (k *Keystore) Generate() (common.Address, error) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		return common.Address{}, fmt.Errorf("keystore: generate key: %w", err)
	}
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	if err := k.write(addr, priv); err != nil {
		return common.Address{}, err
	}
	return addr, nil
}

func (k *Keystore) Load(addr common.Address) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(k.path(addr))
	if err != nil {
		return nil, fmt.Errorf("keystore: read: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("keystore: parse: %w", err)
	}
	if f.Version != fileVersion {
		return nil, fmt.Errorf("keystore: unsupported version %d", f.Version)
	}
	if f.KDF.Name != kdfArgon2id {
		return nil, fmt.Errorf("keystore: unsupported kdf %q", f.KDF.Name)
	}
	if f.Cipher.Name != cipherAESGCM {
		return nil, fmt.Errorf("keystore: unsupported cipher %q", f.Cipher.Name)
	}

	dk := argon2.IDKey(k.password, f.KDF.Salt, f.KDF.Time, f.KDF.Memory, f.KDF.Threads, f.KDF.KeyLen)
	gcm, err := newGCM(dk)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, f.Cipher.Nonce, f.Cipher.Ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("keystore: decrypt: %w", err)
	}
	priv, err := crypto.ToECDSA(plaintext)
	if err != nil {
		return nil, fmt.Errorf("keystore: parse key: %w", err)
	}
	if got := crypto.PubkeyToAddress(priv.PublicKey); got != addr {
		return nil, fmt.Errorf("keystore: address mismatch: file=%s want=%s", got.Hex(), addr.Hex())
	}
	return priv, nil
}

func (k *Keystore) List() ([]common.Address, error) {
	entries, err := os.ReadDir(k.dir)
	if err != nil {
		return nil, fmt.Errorf("keystore: readdir: %w", err)
	}
	addrs := make([]common.Address, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		hexAddr := strings.TrimSuffix(name, ".json")
		if !common.IsHexAddress(hexAddr) {
			continue
		}
		addrs = append(addrs, common.HexToAddress(hexAddr))
	}
	return addrs, nil
}

func (k *Keystore) write(addr common.Address, priv *ecdsa.PrivateKey) error {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("keystore: salt: %w", err)
	}

	kdf := defaultKDF
	kdf.Salt = salt
	dk := argon2.IDKey(k.password, salt, kdf.Time, kdf.Memory, kdf.Threads, kdf.KeyLen)

	gcm, err := newGCM(dk)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("keystore: nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, crypto.FromECDSA(priv), nil)

	doc := fileFormat{
		Version: fileVersion,
		Address: addr.Hex(),
		KDF:     kdf,
		Cipher: cipherBlob{
			Name:       cipherAESGCM,
			Nonce:      nonce,
			Ciphertext: ct,
		},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("keystore: marshal: %w", err)
	}

	path := k.path(addr)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("keystore: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("keystore: rename: %w", err)
	}
	return nil
}

func (k *Keystore) path(addr common.Address) string {
	return filepath.Join(k.dir, strings.ToLower(addr.Hex())+".json")
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("keystore: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keystore: gcm: %w", err)
	}
	return gcm, nil
}
