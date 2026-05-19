package harness

// PreviewHarness renders a row preview in an external tool.
type PreviewHarness interface {
	Preview(content string) error
}
