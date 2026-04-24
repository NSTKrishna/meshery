package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWriteJSONError_ShapeIsParseableJSON guards the response shape of the
// validation-failure path on /api/workspaces and /api/environments. The
// symptom this prevents is RTK Query's default baseQuery (which dispatches
// on Content-Type) throwing `SyntaxError: Unexpected token 'W', "WorkspaceI"...`
// when the server emitted a plain-text 400 body like
// "WorkspaceID or OrgID cannot be empty". The contract: status code is
// honored, Content-Type is application/json, and the body JSON-parses to
// {"error": "<message>"}.
func TestWriteJSONError_ShapeIsParseableJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONError(rec, "WorkspaceID or OrgID cannot be empty", http.StatusBadRequest)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, resp.StatusCode)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("expected Content-Type %q, got %q — a non-JSON Content-Type is what broke RTK Query", "application/json; charset=utf-8", ct)
	}

	if nosniff := resp.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("expected X-Content-Type-Options: nosniff, got %q", nosniff)
	}

	var decoded map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("expected body to parse as JSON, got %v", err)
	}

	if got := decoded["error"]; got != "WorkspaceID or OrgID cannot be empty" {
		t.Errorf("expected error field %q, got %q", "WorkspaceID or OrgID cannot be empty", got)
	}
}

// TestWriteJSONError_DoesNotStartWithBareWord pins the regression-of-interest:
// a plain-text body beginning with "W" (as http.Error would emit for the
// "WorkspaceID or OrgID cannot be empty" message) is exactly what crashed
// RTK Query's JSON parser. A JSON-encoded body must start with '{'.
func TestWriteJSONError_DoesNotStartWithBareWord(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONError(rec, "WorkspaceID or OrgID cannot be empty", http.StatusBadRequest)

	body := rec.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("expected a non-empty body")
	}
	if body[0] != '{' {
		t.Errorf("expected body to start with '{' (JSON object), got %q — this is the hazard RTK Query trips on", string(body[:min(20, len(body))]))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestWriteMeshkitError_SerializesMeshKitStructure(t *testing.T) {
	rec := httptest.NewRecorder()

	// Use an actual MeshKit error from the server's catalog.
	err := ErrGetResult(fmt.Errorf("record not found"))

	writeMeshkitError(rec, err, http.StatusNotFound)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
	if nosniff := resp.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("expected X-Content-Type-Options: nosniff, got %q", nosniff)
	}

	var decoded struct {
		Error                string   `json:"error"`
		Code                 string   `json:"code"`
		Severity             string   `json:"severity"`
		ProbableCause        []string `json:"probable_cause"`
		SuggestedRemediation []string `json:"suggested_remediation"`
		LongDescription      []string `json:"long_description"`
	}
	if decodeErr := json.NewDecoder(resp.Body).Decode(&decoded); decodeErr != nil {
		t.Fatalf("body did not parse as JSON: %v", decodeErr)
	}
	if decoded.Code != ErrGetResultCode {
		t.Errorf("expected code %q, got %q", ErrGetResultCode, decoded.Code)
	}
	if decoded.Error == "" {
		t.Errorf("expected non-empty error message; decoded = %+v", decoded)
	}
}

func TestWriteMeshkitError_NilFallsBackToGenericMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	writeMeshkitError(rec, nil, http.StatusInternalServerError)

	var decoded map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&decoded); err != nil {
		t.Fatalf("body did not parse as JSON: %v", err)
	}
	if decoded["error"] == nil || decoded["error"] == "" {
		t.Errorf("expected non-empty error field for nil input")
	}
}

func TestWriteMeshkitError_NonMeshkitErrorStillJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeMeshkitError(rec, fmt.Errorf("plain stdlib error"), http.StatusBadRequest)

	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("expected JSON Content-Type even for non-MeshKit errors, got %q", ct)
	}

	var decoded map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&decoded); err != nil {
		t.Fatalf("body did not parse as JSON: %v", err)
	}
	if decoded["error"] != "plain stdlib error" {
		t.Errorf("expected error field to contain original message, got %v", decoded["error"])
	}
}

// TestWriteJSONMessage_SetsHeadersAndEncodesPayload verifies the success-path
// helper matches the defensive-header posture of the error helpers and produces
// parseable JSON. Kept deliberately simple — writeJSONMessage is thin, but it's
// called from many handlers that promote bare-string success responses (e.g.
// "Database reset successful") to {"message": "..."}.
func TestWriteJSONMessage_SetsHeadersAndEncodesPayload(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONMessage(rec, map[string]string{"message": "ok"}, http.StatusAccepted)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
	if nosniff := resp.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("expected X-Content-Type-Options: nosniff, got %q", nosniff)
	}

	var decoded map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("body did not parse as JSON: %v", err)
	}
	if decoded["message"] != "ok" {
		t.Errorf("expected message %q, got %q", "ok", decoded["message"])
	}
}

// TestWriteJSONEmptyObject_SetsHeadersAndWritesEmptyObject verifies the
// empty-object helper honors the given status code, emits the JSON
// Content-Type (without which clients like RTK Query can't trust that "{}"
// is actually JSON), and writes the exact two-byte body "{}". The handler
// call sites migrated to this helper previously wrote "{}" with no
// Content-Type at all.
func TestWriteJSONEmptyObject_SetsHeadersAndWritesEmptyObject(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONEmptyObject(rec, http.StatusOK)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
	if nosniff := resp.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("expected X-Content-Type-Options: nosniff, got %q", nosniff)
	}

	body := rec.Body.String()
	if body != "{}" {
		t.Errorf("expected body %q, got %q", "{}", body)
	}

	// Parity check: the body must be valid JSON that decodes to an empty object.
	var decoded map[string]any
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&decoded); err != nil {
		t.Fatalf("body did not parse as JSON: %v", err)
	}
	if len(decoded) != 0 {
		t.Errorf("expected empty object, got %v", decoded)
	}
}

// TestWriteJSONEmptyObject_HonorsNon200Status confirms the helper is usable
// for any status code a caller might pass (e.g. 201 Created on a resource
// creation that has no payload to return). The plan's call sites all use
// 200, but the helper's signature accepts any status and must honor it.
func TestWriteJSONEmptyObject_HonorsNon200Status(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONEmptyObject(rec, http.StatusCreated)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}
	if body := rec.Body.String(); body != "{}" {
		t.Errorf("expected body %q, got %q", "{}", body)
	}
}
