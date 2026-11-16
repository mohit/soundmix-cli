package mixcloud

import (
	"testing"
	"time"
)

func TestCheckShowLength(t *testing.T) {
	if err := CheckShowLength(14*time.Minute + 59*time.Second); err == nil {
		t.Fatal("expected 14:59 to be rejected")
	}
	if err := CheckShowLength(15 * time.Minute); err != nil {
		t.Fatal(err)
	}
}
