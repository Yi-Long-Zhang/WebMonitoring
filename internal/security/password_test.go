package security

import "testing"

func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword("a-long-password-for-testing")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if !VerifyPassword(hash, "a-long-password-for-testing") {
		t.Fatal("expected valid password to verify")
	}
	if VerifyPassword(hash, "incorrect-password") {
		t.Fatal("expected invalid password to fail")
	}
}

func TestPasswordMinimumLength(t *testing.T) {
	if _, err := HashPassword("too-short"); err == nil {
		t.Fatal("expected short password to be rejected")
	}
}
