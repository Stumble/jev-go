package jev

import "testing"

func TestEqualJSON(t *testing.T) {
	if !equalJSON([]byte(`{"a":1,"b":[true]}`), []byte(`{"b":[true],"a":1}`)) {
		t.Fatal("object member order should not affect equality")
	}
	if equalJSON([]byte(`9007199254740992`), []byte(`9007199254740993`)) {
		t.Fatal("large integers must not lose precision")
	}
	for _, value := range []string{`not-json`, `1 2`} {
		if equalJSON([]byte(value), []byte(`1`)) {
			t.Fatalf("invalid JSON compared equal: %s", value)
		}
	}
}
