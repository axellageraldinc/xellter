package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTOTPMatchesRFC6238Vector(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // "12345678901234567890"
	at := time.Unix(59, 0)
	if got := TOTPNow(secret, at); got != "287082" {
		t.Fatalf("RFC 6238 vector: got %s", got)
	}
	if !VerifyTOTP(secret, "287082", at) || VerifyTOTP(secret, "287083", at) {
		t.Fatal("verify mismatch")
	}
	if !VerifyTOTP(secret, "287082", at.Add(25*time.Second)) { // +-1 step skew
		t.Fatal("one step of skew must be accepted")
	}
	if VerifyTOTP(secret, "287082", at.Add(5*time.Minute)) {
		t.Fatal("stale code accepted")
	}
}

func TestRecordRoundTripRecoveryCodeIsSingleUse(t *testing.T) {
	dir := t.TempDir()
	rec, codes, err := NewRecord("axell", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "auth.json")
	if err := WriteRecord(file, rec); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRecord(file)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAuthenticator(got, "", filepath.Join(dir, "used.json"))
	if !a.Verify("axell", "a-long-enough-password", codes[0], "ip1").OK {
		t.Fatal("recovery code should work once")
	}
	if a.Verify("axell", "a-long-enough-password", codes[0], "ip2").OK {
		t.Fatal("recovery code reused")
	}
	if !a.Verify("axell", "a-long-enough-password", TOTPNow(got.TotpSecret, time.Now()), "ip3").OK {
		t.Fatal("totp login failed")
	}
}

func TestReadRecordRejectsLoosePermissionsAndMissing2FA(t *testing.T) {
	dir := t.TempDir()
	rec, _, _ := NewRecord("axell", "a-long-enough-password")
	file := filepath.Join(dir, "auth.json")
	_ = WriteRecord(file, rec)
	_ = os.Chmod(file, 0o666)
	if _, err := ReadRecord(file); err == nil {
		t.Fatal("world-writable record accepted")
	}
	_ = os.Chmod(file, 0o600)
	rec.TotpSecret = ""
	_ = WriteRecord(file, rec)
	if _, err := ReadRecord(file); err == nil {
		t.Fatal("record without TOTP accepted")
	}
}

func TestWeakPasswordRejected(t *testing.T) {
	if _, _, err := NewRecord("axell", "short"); err == nil {
		t.Fatal("short password accepted")
	}
}
