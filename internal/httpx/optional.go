package httpx

import "encoding/json"

// Optional distinguishes the three states a field can be in inside a partial
// update body, which a plain pointer cannot:
//
//	{}                      -> Present=false            leave the column alone
//	{"avatarKey": null}     -> Present=true, Null=true   clear the column
//	{"avatarKey": "a/b.jpg"} -> Present=true, Value set  write the column
//
// The contract leans on all three: PATCH /users/me sends only changed fields,
// and `"avatarKey": null` specifically means "remove the avatar".
type Optional[T any] struct {
	Value   T
	Present bool // the key appeared in the JSON body at all
	Null    bool // the key appeared with an explicit null
}

// UnmarshalJSON implements json.Unmarshaler. It is only ever called when the
// key is present, which is what makes the distinction work.
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	o.Present = true
	if string(data) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(data, &o.Value)
}

// MarshalJSON implements json.Marshaler so an Optional round trips in tests.
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if o.Null || !o.Present {
		return []byte("null"), nil
	}
	return json.Marshal(o.Value)
}

// Set reports whether a non-null value was supplied.
func (o Optional[T]) Set() bool { return o.Present && !o.Null }

// Get returns the supplied value and whether one was supplied.
func (o Optional[T]) Get() (T, bool) {
	if !o.Set() {
		var zero T
		return zero, false
	}
	return o.Value, true
}
