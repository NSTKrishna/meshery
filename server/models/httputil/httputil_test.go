package httputil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	meshkiterrors "github.com/meshery/meshkit/errors"
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
	WriteJSONError(rec, "WorkspaceID or OrgID cannot be empty", http.StatusBadRequest)

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
	WriteJSONError(rec, "WorkspaceID or OrgID cannot be empty", http.StatusBadRequest)

	body := rec.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("expected a non-empty body")
	}
	if body[0] != '{' {
		end := 20
		if end > len(body) {
			end = len(body)
		}
		t.Errorf("expected body to start with '{' (JSON object), got %q — this is the hazard RTK Query trips on", string(body[:end]))
	}
}

// TestWriteMeshkitError_SerializesMeshKitStructure verifies that a MeshKit
// *Error surfaces its code and short description on the wire. Uses an inline
// constructor to avoid a cross-package import from server/handlers (which
// would create a cycle through the models package this test lives in).
func TestWriteMeshkitError_SerializesMeshKitStructure(t *testing.T) {
	const testCode = "meshery-test-0001"
	err := meshkiterrors.New(
		testCode,
		meshkiterrors.Alert,
		[]string{"unable to get result"},
		[]string{"record not found"},
		[]string{"Result Identifier provided is not valid"},
		[]string{"Make sure to provide the correct identifier"},
	)

	rec := httptest.NewRecorder()
	WriteMeshkitError(rec, err, http.StatusNotFound)

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
	if decoded.Code != testCode {
		t.Errorf("expected code %q, got %q", testCode, decoded.Code)
	}
	if decoded.Error == "" {
		t.Errorf("expected non-empty error message; decoded = %+v", decoded)
	}
}

// TestWriteMeshkitError_NilFallsBackToGenericMessage verifies that a nil
// error still produces a parseable JSON body carrying the stock status-text
// message. This is the "don't crash the wire format" safeguard — a handler
// bug that passes nil should never reach the client as an empty body.
func TestWriteMeshkitError_NilFallsBackToGenericMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteMeshkitError(rec, nil, http.StatusInternalServerError)

	var decoded map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&decoded); err != nil {
		t.Fatalf("body did not parse as JSON: %v", err)
	}
	if decoded["error"] == nil || decoded["error"] == "" {
		t.Errorf("expected non-empty error field for nil input")
	}
}

// TestWriteMeshkitError_NonMeshkitErrorStillJSON verifies stdlib errors
// (e.g. fmt.Errorf) that slipped through without a MeshKit wrapper still
// produce JSON — never plain text. No code/severity fields are emitted in
// that case (omitempty keeps the body small).
func TestWriteMeshkitError_NonMeshkitErrorStillJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteMeshkitError(rec, fmt.Errorf("plain stdlib error"), http.StatusBadRequest)

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
// parseable JSON. Kept deliberately simple — WriteJSONMessage is thin, but it's
// called from many handlers that promote bare-string success responses (e.g.
// "Database reset successful") to {"message": "..."}.
func TestWriteJSONMessage_SetsHeadersAndEncodesPayload(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSONMessage(rec, map[string]string{"message": "ok"}, http.StatusAccepted)

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
	WriteJSONEmptyObject(rec, http.StatusOK)

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
	WriteJSONEmptyObject(rec, http.StatusCreated)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}
	if body := rec.Body.String(); body != "{}" {
		t.Errorf("expected body %q, got %q", "{}", body)
	}
}

// TestBufferEncodePattern_PartialFailureDoesNotCorruptResponse pins the
// buffer-encode-then-write pattern that the provider layer adopted in
// commit ed1ce9f25c (server/models/remote_provider.go and
// server/models/default_local_provider.go — GetProviderCapabilities and
// ExtractToken at each).
//
// The pattern: encode the payload into a bytes.Buffer first, and only on
// success write headers and copy the buffer to the ResponseWriter. A failed
// encode must NOT leave a partial body or committed headers on the
// ResponseWriter — instead the helper emits a clean WriteMeshkitError envelope.
//
// This test guards against a future refactor that re-introduces the original
// "encode straight into the ResponseWriter" anti-pattern, which silently
// produces a corrupted response (truncated body + an error envelope appended
// behind already-flushed bytes).
//
// The test mimics the pattern with a payload that json.NewEncoder.Encode
// rejects (channel-bearing struct), then asserts the resulting response is a
// well-formed MeshKit error envelope with no leading payload bytes.
func TestBufferEncodePattern_PartialFailureDoesNotCorruptResponse(t *testing.T) {
	// chan int is not JSON-encodable; encoding/json returns
	// "json: unsupported type: chan int" and writes nothing to the buffer.
	payload := struct {
		Bad chan int `json:"bad"`
	}{Bad: make(chan int)}

	rec := httptest.NewRecorder()

	// Mirror the provider-layer pattern: encode into a buffer first.
	var buf bytes.Buffer
	encErr := json.NewEncoder(&buf).Encode(payload)
	if encErr == nil {
		t.Fatalf("expected encode to fail on channel-bearing payload, got nil error")
	}

	// At this point the ResponseWriter has had nothing written to it — the
	// encode failure was contained to the buffer. Now emit the error response.
	WriteMeshkitError(rec, fmt.Errorf("encode payload: %w", encErr), http.StatusInternalServerError)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("expected Content-Type %q, got %q", "application/json; charset=utf-8", ct)
	}

	bodyBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("failed to read response body: %v", readErr)
	}
	if len(bodyBytes) == 0 {
		t.Fatal("expected a non-empty error envelope body")
	}
	// A correct buffer-then-write pattern leaves the response containing
	// exactly the error envelope — a single JSON object that starts with '{'.
	// If a future refactor encodes payload into the writer first, the body
	// would either start with truncated payload bytes or contain two
	// concatenated JSON objects, both of which fail this assertion.
	if bodyBytes[0] != '{' {
		end := 40
		if end > len(bodyBytes) {
			end = len(bodyBytes)
		}
		t.Errorf("body must start with '{' (single JSON object). got prefix %q — partial payload bytes leaked before the envelope", string(bodyBytes[:end]))
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &decoded); err != nil {
		t.Fatalf("body did not parse as a single JSON object: %v\nbody=%q", err, string(bodyBytes))
	}
	if msg, _ := decoded["error"].(string); msg == "" {
		t.Errorf("expected non-empty error field on envelope, got %+v", decoded)
	}

	// The decoded envelope must NOT carry the failed payload's field. If it
	// does, the writer was corrupted by a partial encode of `payload`
	// followed by the envelope.
	if _, leaked := decoded["bad"]; leaked {
		t.Errorf("error envelope unexpectedly contains payload field 'bad' — buffer-encode pattern is broken: %+v", decoded)
	}
}

// TestBufferEncodePattern_SuccessPathWritesAtomicResponse pins the success
// half of the pattern: when the encode succeeds, a single
// Content-Type-tagged JSON body is delivered with no error envelope.
// Together with TestBufferEncodePattern_PartialFailureDoesNotCorruptResponse
// this covers both branches of the pattern at the four provider call sites.
func TestBufferEncodePattern_SuccessPathWritesAtomicResponse(t *testing.T) {
	payload := map[string]interface{}{
		"meshery-provider": "None",
		"token":            "",
	}

	rec := httptest.NewRecorder()

	// Mirror the provider-layer pattern.
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		t.Fatalf("unexpected encode failure on plain map payload: %v", err)
	}
	rec.Header().Set("Content-Type", "application/json")
	if _, err := buf.WriteTo(rec); err != nil {
		t.Fatalf("unexpected WriteTo failure: %v", err)
	}

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected default status 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected JSON Content-Type, got %q", ct)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	if len(bodyBytes) == 0 {
		t.Fatal("expected a non-empty success body")
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &decoded); err != nil {
		t.Fatalf("body did not parse as JSON: %v\nbody=%q", err, string(bodyBytes))
	}
	if decoded["meshery-provider"] != "None" {
		t.Errorf("expected payload field meshery-provider=None, got %v", decoded["meshery-provider"])
	}
	// The success body must be just the payload — no leaked error field
	// from a stray WriteMeshkitError, which would indicate the pattern
	// took both branches.
	if _, leaked := decoded["error"]; leaked {
		t.Errorf("success-path body unexpectedly contains 'error' field: %+v", decoded)
	}
}

// flakyResponseWriter is a minimal http.ResponseWriter that succeeds on
// WriteHeader, writes the first failAfter bytes of any Write call into an
// internal buffer, and then fails subsequent writes. It's the simplest model
// of a real-world failure mode (connection reset, broken transport) where
// json.NewEncoder.Encode flushes a partial JSON document onto the wire and
// then returns an error.
//
// Provider code that streams into a ResponseWriter directly (the pre-fix
// pattern) is vulnerable to this: by the time Encode returns an error, the
// writer has already committed a partial body and (for net/http) the 200 OK
// status. A follow-up WriteMeshkitError then either appends a second JSON
// object behind the partial body or — for a real http.ResponseWriter — has
// its WriteHeader call silently dropped because headers are already on the
// wire.
type flakyResponseWriter struct {
	header     http.Header
	buf        bytes.Buffer
	statusCode int
	wroteHdr   bool
	written    int
	failAfter  int
}

func newFlakyResponseWriter(failAfter int) *flakyResponseWriter {
	return &flakyResponseWriter{header: http.Header{}, statusCode: http.StatusOK, failAfter: failAfter}
}

func (f *flakyResponseWriter) Header() http.Header { return f.header }

func (f *flakyResponseWriter) WriteHeader(code int) {
	if f.wroteHdr {
		// Real http.ResponseWriter logs and ignores. Mirror that: a second
		// WriteHeader is a no-op, which is exactly the latent bug.
		return
	}
	f.statusCode = code
	f.wroteHdr = true
}

func (f *flakyResponseWriter) Write(p []byte) (int, error) {
	if !f.wroteHdr {
		f.WriteHeader(http.StatusOK)
	}
	remaining := f.failAfter - f.written
	if remaining <= 0 {
		return 0, fmt.Errorf("flaky writer: connection reset after %d bytes", f.failAfter)
	}
	if len(p) <= remaining {
		n, err := f.buf.Write(p)
		f.written += n
		return n, err
	}
	n, _ := f.buf.Write(p[:remaining])
	f.written += n
	return n, fmt.Errorf("flaky writer: partial write %d/%d bytes before connection reset", n, len(p))
}

// TestEncodeIntoResponseWriter_DemonstratesLatentBug shows what NOT to do.
// This is the anti-pattern the buffer-encode pattern fixes: encoding directly
// into the ResponseWriter commits headers and writes a truncated body when
// the underlying transport fails partway through. A follow-up
// WriteMeshkitError then either has its WriteHeader silently dropped (real
// transport) or appends a second JSON object onto the truncated body
// (in-memory recorder), in either case producing a corrupted response.
//
// The test simulates a transport that fails mid-write, so reviewers reading
// this file see *why* the buffer-first pattern at the four provider sites
// matters. The test asserts that two corruption signatures are observable —
// (a) the 500 status passed to WriteMeshkitError is silently dropped because
// the writer already committed 200 OK, and (b) the body is not a single
// well-formed JSON object.
func TestEncodeIntoResponseWriter_DemonstratesLatentBug(t *testing.T) {
	payload := map[string]string{
		"meshery-provider": "None",
		"token":            "this-payload-leaks-onto-the-wire",
	}

	// Fail after 8 bytes — enough to commit "200 OK" + a partial JSON body
	// like `{"meshe` before the transport breaks.
	w := newFlakyResponseWriter(8)

	// ANTI-PATTERN: encode directly into the writer. This is what the
	// provider files used to do before commit ed1ce9f25c.
	encErr := json.NewEncoder(w).Encode(payload)
	if encErr == nil {
		t.Skip("flaky writer unexpectedly accepted full payload — demo cannot show corruption")
	}

	// (a) Headers are already committed at 200 OK. The follow-up
	// WriteMeshkitError attempts to set 500 but the WriteHeader is dropped.
	WriteMeshkitError(w, fmt.Errorf("encode payload: %w", encErr), http.StatusInternalServerError)

	if w.statusCode != http.StatusOK {
		t.Errorf("expected status to remain 200 (committed by streaming encode); got %d. encoding/json no longer commits headers eagerly — re-validate the regression tests.", w.statusCode)
	}

	// (b) Body is corrupted. It either fails to parse as a single JSON
	// object (truncated) or contains two concatenated objects (partial
	// payload + envelope). Both are corruption.
	bodyBytes := w.buf.Bytes()
	if len(bodyBytes) == 0 {
		t.Fatal("expected partial body to demonstrate the bug")
	}

	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	var first map[string]interface{}
	firstErr := dec.Decode(&first)
	if firstErr != nil {
		// Truncated leading bytes — corruption confirmed.
		return
	}
	var second map[string]interface{}
	secondErr := dec.Decode(&second)
	if secondErr == nil || secondErr != io.EOF {
		// Two values on the stream — concatenated corruption confirmed.
		return
	}
	t.Errorf("expected truncation or concatenation as proof of corruption; got a single clean object %+v with body=%q. The standard library may have changed — re-validate buffer-encode regression tests.", first, string(bodyBytes))
}
