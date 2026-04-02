package changelog

import "strings"

// TypeRole classifies an API type as request, response, or unknown.
type TypeRole string

const (
	RoleRequest  TypeRole = "request"
	RoleResponse TypeRole = "response"
	RoleUnknown  TypeRole = "unknown"
)

// oapi-codegen naming conventions for request/response types.
var requestSuffixes = []string{
	"Request",
	"Filter",
	"Query",
	"Instruction",
	"Input",
	"SearchQuery",
}

var responseSuffixes = []string{
	"Result",
	"Response",
	"SearchResult",
	"SearchQueryResult",
	"Error",
	"CreatedResult",
}

// requestContains are substrings that indicate request types.
var requestContains = []string{
	"Filter",
	"SortRequest",
}

// responseContains are substrings that indicate response types.
var responseContains = []string{
	"Result",
	"Response",
}

// ClassifyRole assigns a role to a type name based on naming conventions.
func ClassifyRole(name string) TypeRole {
	// Check exact suffix matches (longest first for priority)
	for _, s := range responseSuffixes {
		if strings.HasSuffix(name, s) && len(name) > len(s) {
			return RoleResponse
		}
	}
	for _, s := range requestSuffixes {
		if strings.HasSuffix(name, s) && len(name) > len(s) {
			return RoleRequest
		}
	}

	// Check substring patterns
	for _, s := range requestContains {
		if strings.Contains(name, s) {
			return RoleRequest
		}
	}
	for _, s := range responseContains {
		if strings.Contains(name, s) {
			return RoleResponse
		}
	}

	return RoleUnknown
}

// BuildRoleMap classifies all types in a PackageInfo.
func BuildRoleMap(pkg *PackageInfo) map[string]TypeRole {
	roles := make(map[string]TypeRole)

	for name := range pkg.Structs {
		roles[name] = ClassifyRole(name)
	}
	for name := range pkg.Enums {
		roles[name] = ClassifyRole(name)
	}
	for name := range pkg.Aliases {
		roles[name] = ClassifyRole(name)
	}

	return roles
}
