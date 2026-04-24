package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	meshkiterrors "github.com/meshery/meshkit/errors"
)

// Response helpers
// ----------------
//
// These three helpers are the canonical way to write an HTTP response from
// server/handlers. Never use http.Error — it emits Content-Type: text/plain
// which crashes RTK Query's default baseQuery on the UI (see
// docs/content/en/project/contributing/error-contract.md).
//
// Reach for:
//   - writeMeshkitError  — ANY error path. If err wraps a *meshkiterrors.Error
//                          or *ErrorV2, the code/severity/cause/remediation
//                          survive onto the wire. If it doesn't, the .Error()
//                          string is still emitted as JSON.
//   - writeJSONError     — error paths where the message is a bare string with
//                          no MeshKit wrapper. Prefer promoting the string to
//                          a MeshKit error and using writeMeshkitError instead.
//   - writeJSONMessage   — success paths that return a small status or result
//                          payload (e.g. {"message": "deleted"}).

// writeJSONError writes a JSON-encoded {"error": message} body with the given
// HTTP status. Using JSON (instead of http.Error's plain text) keeps client
// response parsers — notably RTK Query's default baseQuery, which parses by
// Content-Type and treats application/json as JSON — from choking on error
// bodies that happen to start with a letter (e.g. "WorkspaceID or OrgID ...").
func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// errorResponse is the wire shape for all non-2xx responses from Meshery Server.
// Fields mirror github.com/meshery/meshkit/errors.Error; omitempty keeps the
// body small for bare-string errors that carry no MeshKit metadata.
type errorResponse struct {
	Error                string   `json:"error"`
	Code                 string   `json:"code,omitempty"`
	Severity             string   `json:"severity,omitempty"`
	ProbableCause        []string `json:"probable_cause,omitempty"`
	SuggestedRemediation []string `json:"suggested_remediation,omitempty"`
	LongDescription      []string `json:"long_description,omitempty"`
}

// writeMeshkitError writes a JSON error response that preserves MeshKit error
// metadata (code, severity, probable cause, remediation) when err is (or wraps)
// a *meshkiterrors.Error or *meshkiterrors.ErrorV2. Non-MeshKit errors still
// produce a JSON body — they just carry only the .Error() string, matching
// writeJSONError's shape so clients never see plain text from this package.
//
// Prefer this over http.Error for every handler error path. RTK Query's
// baseQuery dispatches on Content-Type and crashes on plain-text bodies that
// happen to start with a letter (e.g. "Status Code: 404 ...").
func writeMeshkitError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	resp := errorResponse{}
	if err == nil {
		resp.Error = http.StatusText(status)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	resp.Error = err.Error()

	// Populate MeshKit fields only when the error carries them. GetCode etc.
	// return the "None" sentinel for non-MeshKit errors; treat that as absent.
	if code := meshkiterrors.GetCode(err); code != "" && code != "None" {
		resp.Code = code
		resp.Severity = severityString(meshkiterrors.GetSeverity(err))
		if short := meshkiterrors.GetSDescription(err); short != "" && short != "None" {
			// Use ShortDescription as the user-facing `error` when available —
			// err.Error() on a MeshKit error concatenates every field with pipes.
			resp.Error = short
		}
		if long := meshkiterrors.GetLDescription(err); long != "" && long != "None" {
			resp.LongDescription = []string{long}
		}
		if cause := meshkiterrors.GetCause(err); cause != "" && cause != "None" {
			resp.ProbableCause = []string{cause}
		}
		if remedy := meshkiterrors.GetRemedy(err); remedy != "" && remedy != "None" {
			resp.SuggestedRemediation = []string{remedy}
		}
	}

	_ = json.NewEncoder(w).Encode(resp)
}

// severityString converts a MeshKit Severity enum to the string label used on
// the wire. Kept here (not in MeshKit) because MeshKit's Severity.String is
// not yet exported in all versions we pin.
func severityString(s meshkiterrors.Severity) string {
	switch s {
	case meshkiterrors.Emergency:
		return "EMERGENCY"
	case meshkiterrors.Alert:
		return "ALERT"
	case meshkiterrors.Critical:
		return "CRITICAL"
	case meshkiterrors.Fatal:
		return "FATAL"
	default:
		return "ERROR"
	}
}

// writeJSONMessage encodes an arbitrary payload as JSON with the given status
// code. Use for success responses that currently write a bare string (e.g.
// "Database reset successful") — promote them to a structured message.
func writeJSONMessage(w http.ResponseWriter, payload any, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeJSONEmptyObject writes "{}" with Content-Type: application/json.
// Several handlers return an empty object to indicate "no data, but request
// succeeded"; this helper centralizes the pattern and guarantees the header
// so clients (notably RTK Query's Content-Type-dispatching baseQuery) never
// see a bare "{}" with no declared Content-Type.
func writeJSONEmptyObject(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte("{}"))
}

const (
	defaultPageSize = 25
	queryParamTrue  = "true"
)

func getPaginationParams(req *http.Request) (page, offset, limit int, search, order, sortOnCol, status string) {

	urlValues := req.URL.Query()
	page, _ = strconv.Atoi(urlValues.Get("page"))
	limitstr := urlValues.Get("pagesize")
	if limitstr != "all" {
		limit, _ = strconv.Atoi(limitstr)
		if limit == 0 {
			limit = defaultPageSize
		}
	}

	search = urlValues.Get("search")
	order = urlValues.Get("order")
	sortOnCol = urlValues.Get("sort")
	status = urlValues.Get("status")

	if page < 0 {
		page = 0
	}
	offset = page * limit

	if sortOnCol == "" {
		sortOnCol = "updated_at"
	}
	return
}

// Extracts specified boolean query parameters from the request and returns a map of params and their value.
func extractBoolQueryParams(r *http.Request, params ...string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, param := range params {
		val, err := strconv.ParseBool(r.URL.Query().Get(param))
		if err != nil {
			val = false
		}
		result[param] = val
	}
	return result, nil
}

// TODO: Remone completely after confirm is no more needed
// func getLatestKubeVersionFromRegistry(reg *registry.RegistryManager) string {
// 	entities, _, _, _ := reg.GetEntities(&v1beta1.ModelFilter{
// 		Name: "kubernetes",
// 	})

// 	versions := []string{}

// 	for _, entity := range entities {
// 		modelDef, err := utils.Cast[*model.ModelDefinition](entity)
// 		if err != nil {
// 			continue
// 		}
// 		versions = append(versions, modelDef.Model.Version)
// 	}
// 	if len(versions) == 0 {
// 		return ""
// 	}

// 	versions = utils.SortDottedStringsByDigits(versions)

// 	return versions[0]
// }
