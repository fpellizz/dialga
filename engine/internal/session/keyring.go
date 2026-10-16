package session

import (
	"errors"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// KeyringService is the service name of the entries in the system keyring;
// the account is the target name.
const KeyringService = "dialga"

// ErrKeyNotFound means the keyring has no entry for the account.
var ErrKeyNotFound = errors.New("no keyring entry")

// Keyring is the system keyring, one entry per target name.
type Keyring interface {
	// Get returns ErrKeyNotFound when there is no entry.
	Get(name string) (string, error)
	Set(name, password string) error
	// Delete returns ErrKeyNotFound when there is no entry.
	Delete(name string) error
	// Usable reports whether the keyring service answers on this machine.
	Usable() bool
}

// probeAccount is looked up to find out whether the keyring service is
// there: a missing entry is a healthy answer.
const probeAccount = "dialga-probe"

// usableTTL is how long a "usable" answer is reused.
const usableTTL = 30 * time.Second

type systemKeyring struct {
	mu   sync.Mutex
	at   time.Time
	last bool
}

// SystemKeyring is the keyring of the operating system: Secret Service on
// Linux, Keychain on macOS, Credential Manager on Windows.
func SystemKeyring() Keyring { return &systemKeyring{} }

func notFound(err error) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrKeyNotFound
	}
	return err
}

func (*systemKeyring) Get(name string) (string, error) {
	v, err := keyring.Get(KeyringService, name)
	return v, notFound(err)
}

func (*systemKeyring) Set(name, password string) error {
	return keyring.Set(KeyringService, name, password)
}

func (*systemKeyring) Delete(name string) error {
	return notFound(keyring.Delete(KeyringService, name))
}

func (k *systemKeyring) Usable() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.at.IsZero() && time.Since(k.at) < usableTTL {
		return k.last
	}
	_, err := keyring.Get(KeyringService, probeAccount)
	k.last = err == nil || errors.Is(err, keyring.ErrNotFound)
	k.at = time.Now()
	return k.last
}
