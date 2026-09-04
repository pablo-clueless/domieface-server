package httpx_test

import (
	"encoding/json"
	"testing"

	"domieface/com/internal/httpx"
)

// body mirrors the PATCH /users/me shape, which is where the three-state
// distinction actually matters.
type body struct {
	DisplayName httpx.Optional[string] `json:"displayName"`
	AvatarKey   httpx.Optional[string] `json:"avatarKey"`
}

func TestOptionalDistinguishesAbsentFromNull(t *testing.T) {
	cases := []struct {
		name        string
		json        string
		wantPresent bool
		wantNull    bool
		wantValue   string
	}{
		{"absent", `{}`, false, false, ""},
		{"explicit null", `{"avatarKey":null}`, true, true, ""},
		{"value", `{"avatarKey":"uploads/a1b2c3/avatar.jpg"}`, true, false, "uploads/a1b2c3/avatar.jpg"},
		{"empty string is a value, not null", `{"avatarKey":""}`, true, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got body
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if got.AvatarKey.Present != tc.wantPresent {
				t.Errorf("Present: got %v, want %v", got.AvatarKey.Present, tc.wantPresent)
			}
			if got.AvatarKey.Null != tc.wantNull {
				t.Errorf("Null: got %v, want %v", got.AvatarKey.Null, tc.wantNull)
			}
			if got.AvatarKey.Value != tc.wantValue {
				t.Errorf("Value: got %q, want %q", got.AvatarKey.Value, tc.wantValue)
			}
		})
	}
}

func TestOptionalSetAndGet(t *testing.T) {
	var got body
	if err := json.Unmarshal([]byte(`{"displayName":"Ada L.","avatarKey":null}`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	name, ok := got.DisplayName.Get()
	if !ok || name != "Ada L." {
		t.Errorf("displayName: got (%q, %v), want (\"Ada L.\", true)", name, ok)
	}
	if got.AvatarKey.Set() {
		t.Error("an explicit null must not count as set")
	}
}

// A field the client did not send must leave the stored value alone, which is
// only decidable because Present survives decoding.
func TestOptionalUntouchedFieldIsNotPresent(t *testing.T) {
	var got body
	if err := json.Unmarshal([]byte(`{"displayName":"Ada L."}`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AvatarKey.Present {
		t.Error("avatarKey was not in the body but decoded as present")
	}
}
