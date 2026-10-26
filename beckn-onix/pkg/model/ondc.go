package model

import (
	"context"
	"strconv"
	"strings"
)

// ONDC (Beckn 1.x) wire-format helpers.
//
// ONDC retail 1.2.x requests carry context.core_version and context.message_id,
// while ONIX was written around context.version and context.messageId. The
// helpers below let the rest of ONIX read either spelling, and let the response
// writer reply in the ONDC style. Nothing here changes behaviour for a request
// that does not use core_version.

// ContextKeyONDC marks a request context as an ONDC-style request, i.e. one whose
// protocol version came from context.core_version.
const ContextKeyONDC ContextKey = "ondc_wire"

// IsONDC reports whether the request in ctx is an ONDC-style request.
func IsONDC(ctx context.Context) bool {
	v, _ := ctx.Value(ContextKeyONDC).(bool)
	return v
}

// ResolveVersion returns the protocol version from a parsed context map. It
// prefers context.version and falls back to context.core_version. The second
// result is true when the ONDC spelling (core_version) was the one used.
func ResolveVersion(reqContext map[string]interface{}) (version string, ondc bool) {
	if v, ok := reqContext["version"].(string); ok && v != "" {
		return v, false
	}
	if v, ok := reqContext["core_version"].(string); ok && v != "" {
		return v, true
	}
	return "", false
}

// ONDC error codes used by Shim 1 (seller side, from the ONDC error-codes
// document: https://github.com/ONDC-Official/developer-docs, file
// protocol-network-extension/error-codes.md).
const (
	ONDCCodeInvalidRequest   = "30000"
	ONDCCodeInvalidSignature = "30016"
	ONDCCodeStaleRequest     = "30022"
	ONDCCodeInternalError    = "31001"
)

// ONDC error classes for the "type" field of an error.
const (
	ONDCTypeContext = "CONTEXT-ERROR"
	ONDCTypeCore    = "CORE-ERROR"
	ONDCTypePolicy  = "POLICY-ERROR"
	ONDCTypeSchema  = "JSON-SCHEMA-ERROR"
)

// ToONDCError converts an ONIX error into the ONDC form: a numeric code and a
// type. A code that is already numeric (for example one emitted by a Rego policy
// or by the ondcguard step) is kept as is. All other ONIX codes are mapped by
// their prefix. The input is not modified.
//
// This is the single place to change if ONDC's expected "type" values differ.
func ToONDCError(e *Error) *Error {
	if e == nil {
		return nil
	}
	out := &Error{Message: e.Message, Details: e.Details, cause: e.cause}
	if _, err := strconv.Atoi(e.Code); err == nil {
		out.Code = e.Code
		out.Type = ondcTypeForNumericCode(e.Code)
		return out
	}
	switch {
	case strings.HasPrefix(e.Code, "AUT_"):
		out.Code, out.Type = ONDCCodeInvalidSignature, ONDCTypeCore
	case strings.HasPrefix(e.Code, "SCH_"):
		out.Code, out.Type = ONDCCodeInvalidRequest, ONDCTypeSchema
	case strings.HasPrefix(e.Code, "POL_"):
		out.Code, out.Type = ONDCCodeInvalidRequest, ONDCTypePolicy
	default:
		out.Code, out.Type = ONDCCodeInternalError, ONDCTypeCore
	}
	return out
}

func ondcTypeForNumericCode(code string) string {
	switch code {
	case ONDCCodeStaleRequest:
		return ONDCTypeContext
	case ONDCCodeInvalidSignature, ONDCCodeInternalError:
		return ONDCTypeCore
	default:
		return ONDCTypePolicy
	}
}
