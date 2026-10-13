package auth

import "testing"

func TestPasswordHashSaltAndVerification(t *testing.T) {
	one, err := Hash("a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	two, err := Hash("a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("password salts reused")
	}
	if !Verify(one, "a sufficiently long password") {
		t.Fatal("password rejected")
	}
	if Verify(one, "incorrect password") {
		t.Fatal("wrong password accepted")
	}
	if Verify("$argon2id$v=19$m=999999999,t=999,p=999$anything$anything", "password") {
		t.Fatal("unbounded hash parameters accepted")
	}
	if _, err := Hash("short"); err == nil {
		t.Fatal("short password accepted")
	}
}
