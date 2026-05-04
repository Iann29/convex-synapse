package docker

import (
	"strings"
	"testing"
)

func TestAsterImagesUseSharedTag(t *testing.T) {
	for name, image := range map[string]string{
		"broker": AsterBrokerImage,
		"cell":   AsterCellImage,
	} {
		if !strings.HasSuffix(image, ":"+AsterImageTag) {
			t.Fatalf("%s image %q does not use shared tag %q", name, image, AsterImageTag)
		}
	}
}

func TestAsterCellEnvClearsFileSource(t *testing.T) {
	env := buildAsterCellEnv(InvokeAsterRequest{
		DeploymentName: "dep",
		InstanceSecret: "secret",
		JSSource:       "globalThis.main = async () => 1;",
		Prewarm:        []string{"1/a", "2/b"},
	}, "cell-1", 7, 11)

	var sawClear, sawInline, sawPrewarm bool
	for _, item := range env {
		switch item {
		case "ASTER_JS=":
			sawClear = true
		case "ASTER_JS_INLINE=globalThis.main = async () => 1;":
			sawInline = true
		case "ASTER_PREWARM=1/a,2/b":
			sawPrewarm = true
		}
	}
	if !sawClear {
		t.Fatal("env must clear ASTER_JS so image defaults cannot collide with ASTER_JS_INLINE")
	}
	if !sawInline {
		t.Fatal("env missing ASTER_JS_INLINE source")
	}
	if !sawPrewarm {
		t.Fatal("env missing comma-joined ASTER_PREWARM")
	}
}
