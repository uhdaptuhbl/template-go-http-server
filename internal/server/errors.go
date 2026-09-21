package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// errorCodeInternal is reported when the server failed in a way the client
// cannot act on.
const errorCodeInternal = "internal"

// errorCodePayloadTooLarge is reported when a request body exceeds the
// configured limit.
const errorCodePayloadTooLarge = "payload_too_large"

// errorCodeOverloaded is reported when the in-flight request limit is
// exhausted.
const errorCodeOverloaded = "overloaded"

// errorTitles maps each error code to its human-readable summary. A title
// describes the kind of failure and is the same for every occurrence of one
// code; what varies per request goes in an error object's detail.
//
// A code absent from this map falls back to the code itself, so an error
// response is never malformed because someone added a code and forgot a
// title. It reads badly enough in a client to be caught in review.
var errorTitles = map[string]string{
	errorCodeInternal:             "Internal server error",
	errorCodePayloadTooLarge:      "Request body too large",
	errorCodeOverloaded:           "Server overloaded",
	errorCodeInvalidBody:          "Invalid request body",
	errorCodeUnsupportedMediaType: "Unsupported media type",
}

// errorDocument is the top-level shape of every error response the server
// generates, so that a client parses one thing rather than one per endpoint.
//
// The member names come from JSON:API's error objects, and nothing else of
// that specification is adopted: the media type stays application/json and no
// conformance is claimed. See docs/adr/0010-json-api-response-conventions.md.
type errorDocument struct {
	// Errors is never empty. Its presence is what distinguishes a failure
	// body from a success body without consulting the status line, so a
	// success response must never carry this member.
	Errors []errorObject `json:"errors"`
}

// errorObject is one failure within an error document.
type errorObject struct {
	// Status is the HTTP status as a string, which is how JSON:API spells it.
	// Present so that a body separated from its response still carries it.
	Status string `json:"status,omitempty"`
	// Code is a stable identifier a client may branch on.
	Code string `json:"code"`
	// Title summarises the kind of problem and is identical for every
	// occurrence of one code.
	Title string `json:"title"`
	// Detail describes this occurrence. Absent where the title says
	// everything that can safely be said.
	Detail string `json:"detail,omitempty"`
	// Source locates the problem within the request. Absent unless known.
	Source *errorSource `json:"source,omitempty"`
}

// errorSource locates the part of a request an error object refers to.
type errorSource struct {
	// Pointer is a JSON Pointer (RFC 6901) into the request body.
	Pointer string `json:"pointer"`
}

// errorTitle returns the title for code, falling back to the code itself.
func errorTitle(code string) string {
	title, ok := errorTitles[code]
	if !ok {
		return code
	}

	return title
}

// writeJSONError writes status and an error document carrying a single error
// object with the given code and detail. An empty detail is omitted.
//
// The underlying failure is deliberately never included: detail is chosen by
// the caller, so no driver string, file path, or stack can reach a client
// through this path. Anything worth keeping is logged instead.
func writeJSONError(w http.ResponseWriter, logger *zap.Logger, status int, code, detail string) {
	writeErrorDocument(w, logger, status, []errorObject{newErrorObject(status, code, detail, "")})
}

// newErrorObject builds one error object, filling in the title for code and
// dropping a detail that only restates it.
//
// The restatement is not hypothetical and not a caller's mistake. Several
// errors here carry a message that is the whole of what can be said, and that
// message is also the Go error string, so it cannot simply be left empty at
// the source. Sending it twice under two member names tells a client nothing
// and invites it to display both.
func newErrorObject(status int, code, detail, pointer string) errorObject {
	title := errorTitle(code)

	object := errorObject{
		Status: strconv.Itoa(status),
		Code:   code,
		Title:  title,
	}

	if !strings.EqualFold(detail, title) {
		object.Detail = detail
	}

	if pointer != "" {
		object.Source = &errorSource{Pointer: pointer}
	}

	return object
}

// writeErrorDocument writes status and objects as an error document.
func writeErrorDocument(w http.ResponseWriter, logger *zap.Logger, status int, objects []errorObject) {
	w.Header().Set("Content-Type", jsonMediaType)
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(errorDocument{Errors: objects}); err != nil {
		logger.Error("writing error response",
			zap.Error(err),
			zap.Int("status", status),
			zap.Int("errors", len(objects)),
		)
	}
}

// documentStatus returns the status an error document carrying objects should
// be sent with.
//
// One status shared by every object is that status. Otherwise the response
// takes the most generally applicable code, which is what JSON:API prescribes
// for a document with several errors: 400 when they are all client errors, and
// 500 as soon as one of them is not, because a request that also failed on the
// server is not something the client can fix by correcting its input.
func documentStatus(objects []*ClientError) int {
	if len(objects) == 0 {
		return http.StatusInternalServerError
	}

	status := objects[0].Status

	for _, object := range objects {
		if object.Status != status {
			status = 0

			break
		}
	}

	if status != 0 {
		return status
	}

	for _, object := range objects {
		if object.Status < 400 || object.Status >= 500 {
			return http.StatusInternalServerError
		}
	}

	return http.StatusBadRequest
}
