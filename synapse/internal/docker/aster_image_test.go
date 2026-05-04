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

func TestAsterBrokerEnvAndBindsIncludePostgresModules(t *testing.T) {
	spec := DeploymentSpec{
		Name:                 "dep",
		InstanceSecret:       "secret",
		AsterPostgresURL:     "postgres://convex:convex@pg:5432/convex_dep?sslmode=disable",
		AsterDBSchema:        "convex_dev",
		AsterModulesHostPath: "/srv/convex/data/modules",
	}

	env := buildAsterBrokerEnv(spec)
	for _, want := range []string{
		"ASTER_STORE=postgres",
		"ASTER_DB_URL=postgres://convex:convex@pg:5432/convex_dep?sslmode=disable",
		"ASTER_DB_SCHEMA=convex_dev",
		"ASTER_MODULES_DIR=" + AsterModulesContainerPath,
	} {
		if !containsEnv(env, want) {
			t.Fatalf("broker env missing %q in %v", want, env)
		}
	}

	binds := buildAsterBrokerBinds(spec, "synapse-aster-dep")
	wantBind := "/srv/convex/data/modules:" + AsterModulesContainerPath + ":ro"
	if len(binds) != 2 || binds[1] != wantBind {
		t.Fatalf("broker binds = %v, want socket volume plus %q", binds, wantBind)
	}
}

func TestAsterBrokerEnvDefaultsToMemoryStore(t *testing.T) {
	env := buildAsterBrokerEnv(DeploymentSpec{
		Name:           "dep",
		InstanceSecret: "secret",
	})

	for _, item := range env {
		if strings.HasPrefix(item, "ASTER_STORE=") || strings.HasPrefix(item, "ASTER_DB_URL=") {
			t.Fatalf("memory-store broker env should not include postgres settings: %v", env)
		}
	}
}

func containsEnv(env []string, want string) bool {
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}
