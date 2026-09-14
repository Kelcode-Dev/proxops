package plan

import "testing"

// TestIsPVETemplate_PinNumberAndString pins the M11 wire finding that PVE
// encodes `template` as an integer JSON number. Go's encoding/json decodes
// bare integers as float64, so a real PVE /config payload ("template": 1)
// reaches proxops as map[string]any{"template": float64(1)}. The mock PVE
// stores form-values as map[string]string ("template": "1"). Both shapes
// must classify as a template, or LoadLive would silently fail to key the
// TemplateVM live entry against real PVE.
func TestIsPVETemplate_PinNumberAndString(t *testing.T) {
	if !isPVETemplate(map[string]any{"template": float64(1)}) {
		t.Error("float64(1) is PVE's real JSON form; must classify as template")
	}
	if !isPVETemplate(map[string]any{"template": "1"}) {
		t.Error("string \"1\" is the mock form; must classify as template")
	}
	if !isPVETemplate(map[string]any{"template": 1}) {
		t.Error("int 1 must classify as template")
	}
	if isPVETemplate(map[string]any{"template": float64(0)}) {
		t.Error("template=0 must not classify as template")
	}
	if isPVETemplate(map[string]any{"template": "0"}) {
		t.Error("template=0 must not classify as template")
	}
	if isPVETemplate(map[string]any{}) {
		t.Error("absent template key must not classify as template")
	}
}
