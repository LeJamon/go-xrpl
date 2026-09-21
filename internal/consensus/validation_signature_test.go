package consensus

import (
	"errors"
	"testing"
)

func TestValidationCheckSignatureCachesCompletedResults(t *testing.T) {
	var key [32]byte
	calls := 0
	check := func() (bool, error) {
		calls++
		return true, nil
	}

	v := &Validation{}
	valid, err := v.CheckSignature(key, check)
	if err != nil || !valid {
		t.Fatalf("first check: valid=%t err=%v", valid, err)
	}
	valid, err = v.CheckSignature(key, func() (bool, error) {
		calls++
		return false, nil
	})
	if err != nil || !valid {
		t.Fatalf("cached check: valid=%t err=%v", valid, err)
	}
	if calls != 1 {
		t.Fatalf("cached result called callback %d times", calls)
	}

	key[0] = 1
	valid, err = v.CheckSignature(key, func() (bool, error) {
		calls++
		return false, nil
	})
	if err != nil || valid {
		t.Fatalf("changed input: valid=%t err=%v", valid, err)
	}
	if calls != 2 {
		t.Fatalf("changed input did not rerun callback: %d calls", calls)
	}
	valid, err = v.CheckSignature(key, func() (bool, error) {
		calls++
		return true, nil
	})
	if err != nil || valid {
		t.Fatalf("cached false result: valid=%t err=%v", valid, err)
	}
	if calls != 2 {
		t.Fatalf("cached false result called callback %d times", calls)
	}
}

func TestValidationCheckSignatureRetriesAfterError(t *testing.T) {
	var key [32]byte
	calls := 0
	checkErr := errors.New("serialization failed")
	v := &Validation{}
	valid, err := v.CheckSignature(key, func() (bool, error) {
		calls++
		return false, checkErr
	})
	if valid || err != checkErr {
		t.Fatalf("failed check: valid=%t err=%v", valid, err)
	}

	valid, err = v.CheckSignature(key, func() (bool, error) {
		calls++
		return true, nil
	})
	if err != nil || !valid {
		t.Fatalf("retry: valid=%t err=%v", valid, err)
	}
	if calls != 2 {
		t.Fatalf("retry did not rerun callback: %d calls", calls)
	}
}

func TestValidationCheckSignaturePanicLeavesResultRetryable(t *testing.T) {
	var key [32]byte
	v := &Validation{}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic callback did not propagate")
			}
		}()
		_, _ = v.CheckSignature(key, func() (bool, error) {
			panic("verification unavailable")
		})
	}()

	valid, err := v.CheckSignature(key, func() (bool, error) {
		return true, nil
	})
	if err != nil || !valid {
		t.Fatalf("retry after panic: valid=%t err=%v", valid, err)
	}
}
