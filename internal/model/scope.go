package model

import (
	"encoding/json"
	"regexp"
)

var scopeIDPattern = regexp.MustCompile(`^tgscope:v1:[0-9a-f]{32}$`)
var scopeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ScopeID identifies a local, human-managed peer selection. It grants no access.
type ScopeID string

func ParseScopeID(value string) (ScopeID, error) {
	if !scopeIDPattern.MatchString(value) {
		return "", referenceError("scope", "invalid shape or version")
	}
	return ScopeID(value), nil
}

func (id ScopeID) String() string {
	if !scopeIDPattern.MatchString(string(id)) {
		return ""
	}
	return string(id)
}

func (id ScopeID) MarshalJSON() ([]byte, error) {
	if id.String() == "" {
		return nil, referenceError("scope", "zero or invalid value")
	}
	return json.Marshal(string(id))
}

func (id *ScopeID) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || len(data) > maximumReferenceLength+2 || data[0] != '"' {
		return referenceError("scope", "JSON value must be a string")
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return referenceError("scope", "invalid JSON string")
	}
	parsed, err := ParseScopeID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func ValidScopeName(value string) bool { return scopeNamePattern.MatchString(value) }
