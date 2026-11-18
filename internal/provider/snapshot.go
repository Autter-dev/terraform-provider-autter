package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// The API normalizes what it stores (lower-cased triggers, a URL with a
// trailing slash, default widget fields), so its answers are not what the
// configuration says. State keeps what the configuration says, and the
// resource's private state keeps a snapshot: for each configurable field, the
// canonical JSON the API returned after the last apply. A refresh compares the
// API's current value with the snapshot, field by field:
//
//   - equal: the server still holds what Terraform applied; the state value stays;
//   - different: someone changed it outside Terraform; the state takes the API's
//     value, so the next plan shows the drift and apply restores the configuration;
//   - no snapshot (an import): every field is taken from the API.

// snapshotKey is the private state key. Changing its format needs a new key.
const snapshotKey = "applied"

// snapshot maps an API field to its canonical JSON.
type snapshot map[string]string

// takeSnapshot records the given fields of an API answer.
func takeSnapshot(resource map[string]any, fields []string) snapshot {
	s := snapshot{}
	for _, field := range fields {
		s[field] = canonicalJSON(resource[field])
	}
	return s
}

// matches reports whether the API's value of field is the one recorded.
func (s snapshot) matches(field string, value any) bool {
	recorded, ok := s[field]
	return ok && recorded == canonicalJSON(value)
}

// privateState is the part of the framework's private state API this file needs.
type privateState interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// loadSnapshot returns the recorded snapshot, or nil when there is none.
func loadSnapshot(ctx context.Context, private privateState) (snapshot, diag.Diagnostics) {
	if private == nil {
		return nil, nil
	}
	raw, diags := private.GetKey(ctx, snapshotKey)
	if diags.HasError() || len(raw) == 0 {
		return nil, diags
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		// An unreadable snapshot is treated as none: the next refresh takes every field from the API.
		return nil, diags
	}
	s := snapshot{}
	for field, value := range fields {
		s[field] = canonicalText(value)
	}
	return s, diags
}

// saveSnapshot writes s to private state as one JSON object.
func saveSnapshot(ctx context.Context, private privateState, s snapshot) diag.Diagnostics {
	fields := map[string]json.RawMessage{}
	for field, value := range s {
		fields[field] = json.RawMessage(value)
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		var diags diag.Diagnostics
		diags.AddError("Could not record the applied values", err.Error())
		return diags
	}
	return private.SetKey(ctx, snapshotKey, raw)
}
