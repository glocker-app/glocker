package e2e

// Vault is the passphrase-protected master key, as it would be stored server-side.
// It holds only wrapped material: the server (and anyone who dumps its DB) learns
// nothing about the master key, the passphrase, or the recovery code from it. The
// master key is wrapped twice — once under the passphrase, once under a recovery
// code — so either can recover it.
type Vault struct {
	Version           int       `json:"version"`
	KDF               string    `json:"kdf"` // passphrase KDF: "pbkdf2-sha256"
	PBKDF2            KDFParams `json:"pbkdf2"`
	Salt              []byte    `json:"salt"`              // base64 in JSON
	WrappedMK         Box       `json:"wrappedMK"`         // under the passphrase
	WrappedMKRecovery Box       `json:"wrappedMKRecovery"` // under the recovery code
}

// aadMKWrap binds the passphrase wrap to its purpose.
const aadMKWrap = "glocker/e2e:mkwrap:v1"

// WrapMasterKey wraps an existing master key under both a passphrase-derived KEK
// and a freshly generated recovery code, and returns the vault plus that recovery
// code. Show the recovery code to the user exactly once — it's the only copy, and
// it's not stored anywhere in the clear. The raw master key stays with the caller;
// only the returned Vault goes to the server.
func WrapMasterKey(mk, passphrase []byte) (v *Vault, recoveryCode string, err error) {
	if len(mk) != MasterKeyLen {
		return nil, "", ErrBadKeyLen
	}

	// Passphrase wrap.
	salt, err := randomBytes(SaltLen)
	if err != nil {
		return nil, "", err
	}
	kek, err := DeriveKEK(passphrase, salt, DefaultKDF)
	if err != nil {
		return nil, "", err
	}
	wp, err := seal(kek, mk, []byte(aadMKWrap))
	if err != nil {
		return nil, "", err
	}

	// Recovery-code wrap.
	code, raw, err := GenerateRecoveryCode()
	if err != nil {
		return nil, "", err
	}
	rk, err := recoveryKEK(raw)
	if err != nil {
		return nil, "", err
	}
	wr, err := seal(rk, mk, []byte(aadRecoveryWrap))
	if err != nil {
		return nil, "", err
	}

	return &Vault{
		Version:           1,
		KDF:               "pbkdf2-sha256",
		PBKDF2:            DefaultKDF,
		Salt:              salt,
		WrappedMK:         wp,
		WrappedMKRecovery: wr,
	}, code, nil
}

// Open recovers the master key from the passphrase. A wrong passphrase produces an
// authentication error (not a silent wrong key), because the GCM tag won't verify.
func (v *Vault) Open(passphrase []byte) ([]byte, error) {
	kek, err := DeriveKEK(passphrase, v.Salt, v.PBKDF2)
	if err != nil {
		return nil, err
	}
	return open(kek, v.WrappedMK, []byte(aadMKWrap))
}

// OpenWithRecovery recovers the master key from the printed recovery code — the
// fallback when the passphrase is lost. The same key comes out either way.
func (v *Vault) OpenWithRecovery(code string) ([]byte, error) {
	raw, err := decodeRecoveryCode(code)
	if err != nil {
		return nil, err
	}
	rk, err := recoveryKEK(raw)
	if err != nil {
		return nil, err
	}
	return open(rk, v.WrappedMKRecovery, []byte(aadRecoveryWrap))
}
