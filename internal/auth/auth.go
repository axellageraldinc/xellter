// Package auth implements the admin login: scrypt password, TOTP second factor,
// single-use recovery codes and brute-force rate limiting. The record format is
// the same as the record the app it replaces used, so an existing record can be reused.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

// Record is the stored credential.
type Record struct {
	Version        int      `json:"version"`
	Username       string   `json:"username"`
	Algorithm      string   `json:"algorithm"`
	N              int      `json:"N"`
	R              int      `json:"r"`
	P              int      `json:"p"`
	Salt           string   `json:"salt"`
	Hash           string   `json:"hash"`
	TotpSecret     string   `json:"totpSecret,omitempty"`
	RecoveryHashes []string `json:"recoveryHashes,omitempty"`
}

const (
	scryptN = 131072
	scryptR = 8
	scryptP = 1
)

var (
	userRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)
	hexRe  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	b32Re  = regexp.MustCompile(`^[A-Z2-7]{16,64}$`)
)

func derive(password string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, 32)
}

// NewRecord creates a record with a fresh TOTP secret and recovery codes.
// The plain recovery codes are returned once for display.
func NewRecord(username, password string) (rec Record, recovery []string, err error) {
	if !userRe.MatchString(username) || len(password) < 12 || len(password) > 256 {
		return rec, nil, errors.New("use a valid username and a password of 12-256 bytes")
	}
	salt := make([]byte, 32)
	if _, err = rand.Read(salt); err != nil {
		return
	}
	h, err := derive(password, salt)
	if err != nil {
		return
	}
	secret := make([]byte, 20)
	if _, err = rand.Read(secret); err != nil {
		return
	}
	rec = Record{Version: 1, Username: username, Algorithm: "scrypt", N: scryptN, R: scryptR, P: scryptP,
		Salt: hex.EncodeToString(salt), Hash: hex.EncodeToString(h),
		TotpSecret: strings.TrimRight(base32.StdEncoding.EncodeToString(secret), "=")}
	for i := 0; i < 8; i++ {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		code := strings.ToUpper(hex.EncodeToString(b))[:10]
		recovery = append(recovery, code[:5]+"-"+code[5:])
		sum := sha256.Sum256([]byte(code))
		rec.RecoveryHashes = append(rec.RecoveryHashes, hex.EncodeToString(sum[:]))
	}
	return
}

// ReadRecord loads and validates a record; the file must not be group-writable or world-accessible.
func ReadRecord(file string) (*Record, error) {
	st, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o027 != 0 {
		return nil, errors.New("auth record must be a regular file with mode 0600 or 0640")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.Version != 1 || r.Algorithm != "scrypt" || r.N != scryptN || r.R != scryptR || r.P != scryptP || !userRe.MatchString(r.Username) || !hexRe.MatchString(r.Salt) || !hexRe.MatchString(r.Hash) {
		return nil, errors.New("invalid auth record")
	}
	if r.TotpSecret == "" || !b32Re.MatchString(r.TotpSecret) {
		return nil, errors.New("auth record has no valid TOTP secret (2FA is mandatory)")
	}
	for _, h := range r.RecoveryHashes {
		if !hexRe.MatchString(h) {
			return nil, errors.New("invalid recovery hash")
		}
	}
	return &r, nil
}

// WriteRecord stores a record atomically with mode 0600.
func WriteRecord(file string, r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// OTPAuthURI is the provisioning URI for authenticator apps.
func OTPAuthURI(user, secret string) string {
	return fmt.Sprintf("otpauth://totp/xellter:%s?secret=%s&issuer=xellter&algorithm=SHA1&digits=6&period=30", user, secret)
}

// VerifyTOTP checks an RFC 6238 code (SHA1, 6 digits, 30s, +-1 step).
func VerifyTOTP(secret, code string, at time.Time) bool {
	if len(code) != 6 {
		return false
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return false
	}
	counter := at.Unix() / 30
	for d := int64(-1); d <= 1; d++ {
		var msg [8]byte
		binary.BigEndian.PutUint64(msg[:], uint64(counter+d))
		mac := hmac.New(sha1.New, key)
		mac.Write(msg[:])
		sum := mac.Sum(nil)
		off := sum[len(sum)-1] & 0x0f
		v := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % uint32(math.Pow10(6))
		if subtle.ConstantTimeCompare([]byte(fmt.Sprintf("%06d", v)), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// TOTPNow is the current code for a secret (tests and tooling).
func TOTPNow(secret string, at time.Time) string {
	key, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1000000)
}

// Authenticator verifies logins with rate limiting.
type Authenticator struct {
	rec *Record
	kv  KV
	now func() time.Time

	mu      sync.Mutex
	window  time.Duration
	clients map[string]*bucket
	global  bucket
	hashing bool
}

type bucket struct {
	Start time.Time `json:"start"`
	Count int       `json:"count"`
}

// KV is where the login counters and the used recovery codes are kept (the store).
type KV interface {
	KVGet(k string) string
	KVSet(k, v string)
}

// StateKey and RecoveryKey are the kv keys; a credential reset deletes both.
const (
	StateKey    = "auth.login_state"
	RecoveryKey = "auth.recovery_used"
)

// NewAuthenticator builds one; kv persists attempt counters and used recovery codes across restarts.
func NewAuthenticator(rec *Record, kv KV) *Authenticator {
	a := &Authenticator{rec: rec, kv: kv, now: time.Now, window: 15 * time.Minute, clients: map[string]*bucket{}}
	if kv != nil {
		if b := kv.KVGet(StateKey); b != "" {
			var s struct {
				Global  bucket             `json:"global"`
				Clients map[string]*bucket `json:"clients"`
			}
			if json.Unmarshal([]byte(b), &s) == nil {
				a.global = s.Global
				if s.Clients != nil {
					a.clients = s.Clients
				}
			}
		}
	}
	return a
}

// Username returns the configured admin user.
func (a *Authenticator) Username() string { return a.rec.Username }

func (a *Authenticator) persist() {
	if a.kv == nil {
		return
	}
	b, _ := json.Marshal(map[string]any{"global": a.global, "clients": a.clients})
	a.kv.KVSet(StateKey, string(b))
}

// Result of a login attempt.
type Result struct {
	OK         bool
	RetryAfter time.Duration
}

func eq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// Verify checks username, password and second factor. client identifies the
// caller for rate limiting (IP or similar).
func (a *Authenticator) Verify(username, password, code, client string) Result {
	a.mu.Lock()
	t := a.now()
	ck := fmt.Sprintf("%x", sha256.Sum256([]byte(client)))
	if t.Sub(a.global.Start) >= a.window || a.global.Start.After(t) {
		a.global = bucket{Start: t}
	}
	for k, b := range a.clients {
		if t.Sub(b.Start) >= a.window || b.Start.After(t) {
			delete(a.clients, k)
		}
	}
	b := a.clients[ck]
	if b == nil {
		b = &bucket{Start: t}
	}
	var wait time.Duration
	if b.Count >= 5 {
		wait = a.window - t.Sub(b.Start)
	}
	if a.global.Count >= 30 {
		wait = max(wait, a.window-t.Sub(a.global.Start))
	}
	if wait > 0 {
		a.mu.Unlock()
		return Result{RetryAfter: wait}
	}
	if a.hashing {
		a.mu.Unlock()
		return Result{RetryAfter: 2 * time.Second}
	}
	b.Count++
	a.global.Count++
	if len(a.clients) >= 512 {
		for k := range a.clients {
			delete(a.clients, k)
			break
		}
	}
	a.clients[ck] = b
	a.persist()
	a.hashing = true
	a.mu.Unlock()

	valid := len(username) <= 64 && len(password) <= 256
	pw := password
	if !valid {
		pw = ""
	}
	salt, _ := hex.DecodeString(a.rec.Salt)
	h, err := derive(pw, salt) // unknown usernames pay the same cost
	a.mu.Lock()
	a.hashing = false
	a.mu.Unlock()
	if err != nil || !valid || !eq(hex.EncodeToString(h), a.rec.Hash) || !eq(username, a.rec.Username) {
		return Result{}
	}
	if !a.secondFactor(code) {
		return Result{}
	}
	a.mu.Lock()
	delete(a.clients, ck)
	a.persist()
	a.mu.Unlock()
	return Result{OK: true}
}

func (a *Authenticator) secondFactor(code string) bool {
	raw := strings.TrimSpace(code)
	digits := regexp.MustCompile(`\D`).ReplaceAllString(raw, "")
	if VerifyTOTP(a.rec.TotpSecret, digits, a.now()) {
		return true
	}
	norm := strings.ToUpper(regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(raw, ""))
	if len(norm) < 10 || len(a.rec.RecoveryHashes) == 0 || a.kv == nil {
		return false
	}
	sum := sha256.Sum256([]byte(norm))
	d := hex.EncodeToString(sum[:])
	found := false
	for _, h := range a.rec.RecoveryHashes {
		if eq(h, d) {
			found = true
		}
	}
	if !found {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var used struct {
		Used []string `json:"used"`
	}
	_ = json.Unmarshal([]byte(a.kv.KVGet(RecoveryKey)), &used)
	for _, u := range used.Used {
		if u == d {
			return false
		}
	}
	used.Used = append(used.Used, d)
	b, _ := json.Marshal(used)
	a.kv.KVSet(RecoveryKey, string(b))
	return true
}

// NewToken returns a random session token and its stored hash.
func NewToken() (token, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token = hex.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken is the stored form of a session token.
func HashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

// RandomHex returns 2n hex characters of randomness.
func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RandomPassword returns a strong random password.
func RandomPassword() string { return RandomHex(16) }
