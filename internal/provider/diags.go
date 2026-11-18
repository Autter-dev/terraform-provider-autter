package provider

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/Autter-dev/terraform-provider-autter/internal/client"
)

// apiDiagnostic turns an API error into a diagnostic. action says what was
// being done: "create the alert rule \"Checkout errors\"".
func apiDiagnostic(action string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return diag.NewErrorDiagnostic("Could not "+action, err.Error())
	}
	message := apiErr.Message
	switch apiErr.Code {
	case client.CodeUnauthorized:
		return diag.NewErrorDiagnostic("Autter rejected the API token",
			message+"\n\nSet api_token or AUTTER_API_TOKEN to a personal access token (Settings → Access Tokens) with the api:read and api:write scopes.")
	case client.CodeInsufficientScope:
		return diag.NewErrorDiagnostic("The token lacks the api:read / api:write scope",
			message+"\n\nCreate a token with the api:read and api:write scopes, or one without scope restrictions.")
	case client.CodeInsufficientRole:
		return diag.NewErrorDiagnostic("Your role cannot "+action, message)
	case client.CodeFeatureDisabled:
		return diag.NewErrorDiagnostic("The feature is turned off for this organization", message)
	case client.CodeRepositoryNotFound:
		if candidates := apiErr.Candidates(); len(candidates) > 0 {
			message += "\n\nCandidates: " + strings.Join(candidates, ", ")
		}
		return diag.NewErrorDiagnostic("Repository not found", message)
	case client.CodeNameConflict:
		return diag.NewErrorDiagnostic("More than one resource has this name",
			fmt.Sprintf("%s\n\nIds: %s. Rename all but one in the Autter dashboard, then run Terraform again.", message, strings.Join(apiErr.IDs(), ", ")))
	case client.CodeInvalidArgument:
		return diag.NewErrorDiagnostic("Autter refused the configuration", message)
	case client.CodeLimitReached:
		return diag.NewErrorDiagnostic("Plan limit reached", message)
	case client.CodeRateLimited:
		return diag.NewErrorDiagnostic("Rate limited", message)
	}
	return diag.NewErrorDiagnostic("Could not "+action, fmt.Sprintf("The Autter API answered %d (%s): %s", apiErr.Status, apiErr.Code, message))
}
