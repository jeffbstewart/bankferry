package plaid

import (
	"context"
	"fmt"
)

// Sandbox test institutions worth knowing by name.
//
// Sandbox does not run a bank's own OAuth flow. Plaid: "institution-specific
// OAuth flows cannot be tested in Sandbox; OAuth panes for Platypus
// institutions will be shown instead." Selecting Chase in Sandbox therefore
// does exercise the OAuth code path — the redirect, the state, the resume —
// but the pane you see is Plaid's generic one.
const (
	// SandboxOAuthInstitution is Platypus OAuth Bank, which always runs the
	// OAuth flow.
	SandboxOAuthInstitution = "ins_127287"

	// SandboxNonOAuthInstitution is First Platypus Bank, which never does.
	SandboxNonOAuthInstitution = "ins_109508"
)

// InstitutionInfo is what Plaid knows about an institution.
type InstitutionInfo struct {
	ID   string
	Name string

	// OAuth reports that the institution has an OAuth login flow. Plaid:
	// "This will be true if OAuth is supported for any Items associated with
	// the institution, even if the institution also supports non-OAuth
	// connections."
	//
	// So it answers "could this Item have been linked over OAuth", not
	// "was it". The definitive per-link evidence is whether the link server
	// armed its OAuth window, which it logs.
	OAuth bool
}

type wireInstitutionGet struct {
	Institution struct {
		InstitutionID string `json:"institution_id"`
		Name          string `json:"name"`
		OAuth         bool   `json:"oauth"`
	} `json:"institution"`
}

// FetchInstitution looks up an institution by ID.
func (c *DataClient) FetchInstitution(ctx context.Context, institutionID string) (InstitutionInfo, error) {
	if institutionID == "" {
		return InstitutionInfo{}, fmt.Errorf("plaid: institution ID is required")
	}

	var resp wireInstitutionGet
	err := c.post(ctx, "institutions get by id", "/institutions/get_by_id", map[string]any{
		"institution_id": institutionID,
		"country_codes":  []string{"US"},
	}, &resp)
	if err != nil {
		return InstitutionInfo{}, err
	}

	return InstitutionInfo{
		ID:    resp.Institution.InstitutionID,
		Name:  resp.Institution.Name,
		OAuth: resp.Institution.OAuth,
	}, nil
}
