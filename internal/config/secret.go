package config

import (
	"encoding"
	"encoding/json"
	"fmt"
	"log/slog"
)

const redacted = "[REDACTED]"

// jsonNull is the JSON literal for an absent value. Decoding it clears the
// secret rather than storing the four characters.
const jsonNull = "null"

// SecretString holds a sensitive value loaded from configuration. It always
// redacts in string and slog representations; use Expose when passing the raw
// value into APIs that need a plain string (see Expose godoc).
//
// The value and pointer receivers below are intentional: reading a secret must
// work on a value, while unmarshalling into one needs a pointer.
//
// Nothing in this repository declares a field of this type yet, because no
// setting the service loads is a credential. That is expected: the type is
// here so that the first one is declared with it rather than as a plain
// string. A package composing its own settings uses it like any other field
// type,
//
//	type Database struct {
//		DSN config.SecretString `env:"SERVICE_DATABASE_DSN,overwrite"`
//	}
//
// and every renderer of the composed configuration -- LogEffective, JSON
// encoding, fmt verbs, slog -- then redacts it with nothing further to write.
type SecretString string

// Compile-time checks that redaction and JSON contracts stay wired.
var _ fmt.Stringer = SecretString("")
var _ slog.LogValuer = SecretString("")
var _ encoding.TextMarshaler = SecretString("")
var _ encoding.TextUnmarshaler = (*SecretString)(nil)
var _ json.Marshaler = SecretString("")
var _ json.Unmarshaler = (*SecretString)(nil)

// Expose returns the underlying secret for constructors, drivers, and other
// call sites that require the real value. Never log the return value.
//
// Expose is not required by any standard interface. It is an explicit,
// grep-friendly escape hatch so that a review can tell an intentional use of
// the plaintext value from an accidental one. Because the underlying type is
// string, callers could also write string(s); prefer Expose for clarity.
// See docs/adr/0006-configuration-loading.md.
func (s SecretString) Expose() string {
	return string(s)
}

// String implements fmt.Stringer and always returns a redacted placeholder so
// accidental use with fmt or legacy string-based loggers does not leak secrets.
func (SecretString) String() string {
	return redacted
}

// LogValue implements slog.LogValuer so structured loggers record a redacted value.
func (SecretString) LogValue() slog.Value {
	return slog.StringValue(redacted)
}

// MarshalText implements encoding.TextMarshaler so go-envconfig (and
// other plain-text encoders) can encode secrets as raw strings without JSON
// quoting.
func (SecretString) MarshalText() ([]byte, error) {
	return []byte(redacted), nil
}

// UnmarshalText implements encoding.TextUnmarshaler so go-envconfig (and
// other plain-text decoders) can populate secrets from raw env values without
// JSON quoting.
func (s *SecretString) UnmarshalText(text []byte) error {
	*s = SecretString(text)
	return nil
}

// MarshalJSON always encodes the redacted placeholder so JSON payloads and
// logs never leak the secret.
func (SecretString) MarshalJSON() ([]byte, error) {
	//nolint:wrapcheck // this doesn't need to wrap the error.
	return json.Marshal(redacted)
}

// UnmarshalJSON decodes a JSON string into the secret (for example config
// files or API bodies). It does not accept non-string JSON kinds.
func (s *SecretString) UnmarshalJSON(data []byte) error {
	if string(data) == jsonNull {
		*s = ""
		return nil
	}
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("secret string: %w", err)
	}
	*s = SecretString(v)
	return nil
}
