package fault

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestAppImagePinMatchesMakefile fails if the pin and the Makefile's default
// SVC_VERSION drift apart. The svc-image workflow publishes the version its tag
// carries and refuses a tag that disagrees with AppImage; this keeps the local
// build targets on the same version.
func TestAppImagePinMatchesMakefile(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	m := regexp.MustCompile(`(?m)^SVC_VERSION \?= (\S+)$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("Makefile has no SVC_VERSION ?= line")
	}
	want := AppRepository + ":" + string(m[1])
	if AppImage != want {
		t.Fatalf("fault.AppImage = %q, Makefile SVC_VERSION gives %q", AppImage, want)
	}
	if !strings.HasPrefix(AppImage, AppRepository+":") {
		t.Fatalf("AppImage %q is not pinned from AppRepository %q", AppImage, AppRepository)
	}
}
