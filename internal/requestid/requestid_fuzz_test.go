package requestid

import (
	"testing"
)

func FuzzIsValid(f *testing.F) {
	// Seed corpus with valid IDs
	f.Add("0123456789abcdef01234567")
	f.Add("000000000000000000000000")
	f.Add("ffffffffffffffffffffffff")

	// Invalid IDs
	f.Add("")
	f.Add("0123456789abcdef0123456")   // too short
	f.Add("0123456789abcdef012345678") // too long
	f.Add("0123456789ABCDEF01234567")  // uppercase
	f.Add("0123456789ghijkl01234567")  // invalid hex chars
	f.Add("0123456789abcdef 1234567")  // space
	f.Add("0123456789abcdef\n1234567") // newline

	f.Fuzz(func(t *testing.T, input string) {
		result := IsValid(input)

		// If valid, verify properties
		if result {
			if len(input) != 24 {
				t.Errorf("IsValid returned true for len=%d", len(input))
			}
			// All chars should be lowercase hex
			for _, c := range input {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
					t.Errorf("IsValid returned true but contains invalid char: %c", c)
				}
			}
		}

		// Verify consistency: calling twice should return same result
		if IsValid(input) != result {
			t.Error("IsValid not consistent across calls")
		}
	})
}

func FuzzGenerate(f *testing.F) {
	// No input needed - just testing that Generate never panics
	// and always returns valid IDs
	f.Add(0)

	f.Fuzz(func(t *testing.T, _ int) {
		id := Generate()

		// Must be valid
		if !IsValid(id) {
			t.Errorf("Generate returned invalid ID: %s", id)
		}

		// Must be 24 chars
		if len(id) != 24 {
			t.Errorf("Generate returned ID with wrong length: %d", len(id))
		}
	})
}
