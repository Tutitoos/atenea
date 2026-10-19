package statusline_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Preserve the host boundary rather than pinning and copying its private footer.
func TestHostFooterRemainsOwnedByTheClient(t *testing.T) {
	body, err := os.ReadFile("opencode/atenea.tsx")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, forbidden := range []string{"sidebar_footer:", "replace:", "FooterSection", "projectPath", "hostIsOnboarding"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("widget may replace or reproduce host footer: %s", forbidden)
		}
	}
	for _, required := range []string{"sidebar_content:", `append: "sidebar.footer"`} {
		if !strings.Contains(source, required) {
			t.Errorf("missing additive placement: %s", required)
		}
	}
}

func TestHostFooterRegistration(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("Bun is required for the registration probe; host-footer CI installs it")
	}
	if out, err := exec.Command(bun, "test", "./opencode/atenea.test.ts").CombinedOutput(); err != nil {
		t.Fatalf("footer registration probe: %v\n%s", err, out)
	}
}
