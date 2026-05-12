package synapsetest

import (
	"net/http"
	"sync/atomic"
	"testing"
)

// Project-scoped DNS credentials (v1.6.4+, migration 000016).
//
// The migration adds project_id to dns_credentials so each project
// keeps its own Cloudflare token alongside the project rather than
// pooling them in /admin. The auto-configure flow picks
// project-scoped rows first and falls back to instance-wide ones,
// preserving backward compat with single-tenant installs.
//
// These tests cover the four shape-of-the-feature concerns:
//   - per-project CRUD endpoints work end-to-end
//   - the admin endpoint stays scoped to instance-wide rows only
//   - the lookup hierarchy honours the precedence rule
//   - RBAC + scope guards prevent cross-project leakage

// TestProjectDNSCredentials_Add_AndList confirms a project admin
// can post a Cloudflare token through the project endpoint, the
// returned model carries projectId, and the project list reflects it.
func TestProjectDNSCredentials_Add_AndList(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-1", Name: "client-a.com"}},
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Agency A "+randHex(3))
	proj := createProject(t, h, owner.AccessToken, team.Slug, "ClientA")

	var got dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid", "label": "client-a CF"},
		http.StatusCreated, &got)

	if got.ProjectID == nil {
		t.Fatalf("expected projectId on returned credential, got nil")
	}
	if *got.ProjectID != proj.ID {
		t.Errorf("projectId: got %q want %q", *got.ProjectID, proj.ID)
	}

	// List should return exactly this credential.
	var list listDNSCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/dns_credentials",
		owner.AccessToken, nil, http.StatusOK, &list)
	if len(list.Credentials) != 1 || list.Credentials[0].ID != got.ID {
		t.Errorf("expected 1 credential in list, got %+v", list)
	}
}

// TestProjectDNSCredentials_AdminListIgnoresProjectScoped confirms
// the existing /v1/admin/dns_credentials endpoint stays scoped to
// instance-wide rows only, even when a project has credentials.
// Otherwise a team admin's row would silently leak into the
// instance-admin panel and confuse the "global keys" view.
func TestProjectDNSCredentials_AdminListIgnoresProjectScoped(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-1", Name: "client-b.com"}},
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
	})
	// makeAdminUser doubles as instance admin (single-user instance,
	// first user is auto-instance-admin) and as the project owner.
	admin := makeAdminUser(t, h)
	team := createTeam(t, h, admin.AccessToken, "Agency B "+randHex(3))
	proj := createProject(t, h, admin.AccessToken, team.Slug, "ClientB")

	// Seed one instance-wide credential and one project-scoped.
	var globalCred, projCred dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		admin.AccessToken,
		map[string]string{"token": "valid", "label": "global"},
		http.StatusCreated, &globalCred)
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/dns_credentials/cloudflare",
		admin.AccessToken,
		map[string]string{"token": "valid", "label": "client-b project"},
		http.StatusCreated, &projCred)

	// /admin should only see the global one.
	var adminList listDNSCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/admin/dns_credentials",
		admin.AccessToken, nil, http.StatusOK, &adminList)
	if len(adminList.Credentials) != 1 {
		t.Fatalf("/admin expected exactly 1 (instance-wide) credential, got %d: %+v",
			len(adminList.Credentials), adminList)
	}
	if adminList.Credentials[0].ID != globalCred.ID {
		t.Errorf("/admin returned project-scoped credential: got %q want global %q",
			adminList.Credentials[0].ID, globalCred.ID)
	}
	if adminList.Credentials[0].ProjectID != nil {
		t.Errorf("/admin row should carry projectId=nil, got %+v", adminList.Credentials[0].ProjectID)
	}

	// Project endpoint conversely only sees the project row.
	var projList listDNSCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/dns_credentials",
		admin.AccessToken, nil, http.StatusOK, &projList)
	if len(projList.Credentials) != 1 || projList.Credentials[0].ID != projCred.ID {
		t.Errorf("project endpoint leaked or missed rows: %+v", projList)
	}
}

// TestProjectDNSCredentials_Hierarchy_ProjectWins seeds both a
// project-scoped and an instance-wide credential covering the same
// zone, adds a custom domain, and asserts the auto-configure flow
// picked the project-scoped one. The autoConfigured response field
// carries the credential id used.
func TestProjectDNSCredentials_Hierarchy_ProjectWins(t *testing.T) {
	upsertHits := int64(0)
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-1", Name: "fechasul.com.br"}},
		upsertHits:   &upsertHits,
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
		PublicIP:          "203.0.113.10",
	})
	admin := makeAdminUser(t, h)
	team := createTeam(t, h, admin.AccessToken, "Hier Co "+randHex(3))
	proj := createProject(t, h, admin.AccessToken, team.Slug, "FechaProj")

	// Seed BOTH tiers: global first, project second.
	var globalCred, projCred dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		admin.AccessToken,
		map[string]string{"token": "valid", "label": "global"},
		http.StatusCreated, &globalCred)
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/dns_credentials/cloudflare",
		admin.AccessToken,
		map[string]string{"token": "valid", "label": "project"},
		http.StatusCreated, &projCred)

	depName := "hier-cat-" + randHex(3)
	h.SeedDeployment(proj.ID, depName, "prod", "running", true, admin.ID, 3980, "")

	var dom domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+depName+"/domains",
		admin.AccessToken,
		map[string]any{"domain": "api.fechasul.com.br", "role": "api"},
		http.StatusCreated, &dom)

	if !dom.AutoDNSSuccess {
		t.Fatalf("expected auto-configure success on first add, got reason=%q", dom.AutoDNSReason)
	}
	if dom.AutoDNSCredentialID != projCred.ID {
		t.Errorf("project-scoped credential should have won: got %q want %q (global was %q)",
			dom.AutoDNSCredentialID, projCred.ID, globalCred.ID)
	}
	if atomic.LoadInt64(&upsertHits) != 1 {
		t.Errorf("expected exactly 1 Cloudflare upsert, got %d", atomic.LoadInt64(&upsertHits))
	}
}

// TestProjectDNSCredentials_Hierarchy_FallbackToGlobal covers the
// backward-compat case: a single-operator install with only an
// instance-wide credential and no project-scoped rows. Adding a
// domain in any project must still find the global credential.
func TestProjectDNSCredentials_Hierarchy_FallbackToGlobal(t *testing.T) {
	upsertHits := int64(0)
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-1", Name: "fechasul.com.br"}},
		upsertHits:   &upsertHits,
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
		PublicIP:          "203.0.113.10",
	})
	admin := makeAdminUser(t, h)
	team := createTeam(t, h, admin.AccessToken, "Fallback Co "+randHex(3))
	proj := createProject(t, h, admin.AccessToken, team.Slug, "FallbackProj")

	// Only a GLOBAL credential exists. No project rows.
	var globalCred dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		admin.AccessToken,
		map[string]string{"token": "valid", "label": "global-only"},
		http.StatusCreated, &globalCred)

	depName := "fb-owl-" + randHex(3)
	h.SeedDeployment(proj.ID, depName, "prod", "running", true, admin.ID, 3981, "")

	var dom domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+depName+"/domains",
		admin.AccessToken,
		map[string]any{"domain": "api.fechasul.com.br", "role": "api"},
		http.StatusCreated, &dom)

	if !dom.AutoDNSSuccess {
		t.Fatalf("expected fallback to global to succeed, got reason=%q", dom.AutoDNSReason)
	}
	if dom.AutoDNSCredentialID != globalCred.ID {
		t.Errorf("should have fallen back to global: got %q want %q",
			dom.AutoDNSCredentialID, globalCred.ID)
	}
}

// TestProjectDNSCredentials_CrossProjectLeakageBlocked confirms the
// scope guard: a project admin can't reach into another project's
// credentials by guessing the UUID. The /v1/projects/A/.../{B-cred-id}
// path must 404 even though the row exists.
func TestProjectDNSCredentials_CrossProjectLeakageBlocked(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-1", Name: "leak.com"}},
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "LeakTeam "+randHex(3))
	projA := createProject(t, h, owner.AccessToken, team.Slug, "A")
	projB := createProject(t, h, owner.AccessToken, team.Slug, "B")

	// Credential lives in project A.
	var credA dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+projA.ID+"/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid", "label": "A"},
		http.StatusCreated, &credA)

	// project B's list should be empty — A's credential is invisible.
	var listB listDNSCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+projB.ID+"/dns_credentials",
		owner.AccessToken, nil, http.StatusOK, &listB)
	if len(listB.Credentials) != 0 {
		t.Errorf("project B leaked A's credentials: %+v", listB)
	}

	// DELETE via project B's path against A's credential ID = 404.
	// (The route's project_id WHERE clause kicks zero rows; we
	// surface that as credential_not_found.)
	env := h.AssertStatus(http.MethodDelete,
		"/v1/projects/"+projB.ID+"/dns_credentials/"+credA.ID,
		owner.AccessToken, nil, http.StatusNotFound)
	if env.Code != "credential_not_found" {
		t.Errorf("scope-guarded delete: code %q want credential_not_found", env.Code)
	}

	// Same UUID via /admin/dns_credentials/{id} = 404 too (project
	// rows aren't visible to the instance-admin DELETE).
	env = h.AssertStatus(http.MethodDelete,
		"/v1/admin/dns_credentials/"+credA.ID,
		owner.AccessToken, nil, http.StatusNotFound)
	if env.Code != "credential_not_found" {
		t.Errorf("admin delete should not reach project rows: code %q", env.Code)
	}

	// Credential still exists for project A.
	var listA listDNSCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+projA.ID+"/dns_credentials",
		owner.AccessToken, nil, http.StatusOK, &listA)
	if len(listA.Credentials) != 1 {
		t.Errorf("project A's credential should still exist, got %d rows", len(listA.Credentials))
	}
}

// TestProjectDNSCredentials_ViewerCannotMutate covers RBAC: a project
// viewer (read-only on the project) can list credentials but can't
// add or delete. We seed the viewer's team membership + project
// membership directly via SQL to keep the test focused on the
// dns_credentials gate rather than the team-invite flow.
func TestProjectDNSCredentials_ViewerCannotMutate(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-1", Name: "viewer.com"}},
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
	})
	admin := h.RegisterRandomUser()
	viewer := h.RegisterRandomUser()
	team := createTeam(t, h, admin.AccessToken, "RBAC Co "+randHex(3))
	proj := createProject(t, h, admin.AccessToken, team.Slug, "RBACProj")

	// Direct INSERTs so we don't pull in the team-invite/accept flow.
	// team_members only allows 'admin' or 'member'; the 'viewer' role
	// is a project-level override added in migration 000008. Seeding
	// team_members='member' + project_members='viewer' makes
	// effectiveProjectRole resolve to 'viewer'.
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, viewer.ID); err != nil {
		t.Fatalf("seed team_members: %v", err)
	}
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'viewer')`,
		proj.ID, viewer.ID); err != nil {
		t.Fatalf("seed project_members: %v", err)
	}

	// Viewer can LIST (read-only).
	var list listDNSCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/dns_credentials",
		viewer.AccessToken, nil, http.StatusOK, &list)

	// Viewer cannot POST.
	env := h.AssertStatus(http.MethodPost,
		"/v1/projects/"+proj.ID+"/dns_credentials/cloudflare",
		viewer.AccessToken,
		map[string]string{"token": "valid", "label": "nope"},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("viewer POST: got code %q want forbidden", env.Code)
	}

	// Admin adds one; viewer still can't delete.
	var cred dnsCredentialResp
	h.DoJSON(http.MethodPost,
		"/v1/projects/"+proj.ID+"/dns_credentials/cloudflare",
		admin.AccessToken,
		map[string]string{"token": "valid", "label": "admin-added"},
		http.StatusCreated, &cred)

	env = h.AssertStatus(http.MethodDelete,
		"/v1/projects/"+proj.ID+"/dns_credentials/"+cred.ID,
		viewer.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("viewer DELETE: got code %q want forbidden", env.Code)
	}
}

// TestProjectDNSCredentials_CascadeOnProjectDelete confirms that
// deleting a project removes its scoped credentials via the
// ON DELETE CASCADE on dns_credentials.project_id. No orphan rows
// should remain — those would silently re-appear as fallback
// candidates for unrelated domains if global lookup is ever invoked
// in their post-cascade state.
func TestProjectDNSCredentials_CascadeOnProjectDelete(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-1", Name: "cascade.com"}},
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Cascade Co "+randHex(3))
	proj := createProject(t, h, owner.AccessToken, team.Slug, "CascadeProj")

	var cred dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid", "label": "cascade-test"},
		http.StatusCreated, &cred)

	// Delete the project. Implementation note: /v1/projects/{id}/delete
	// returns 200 with a {id, status:"deleted"} envelope on success.
	h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/delete",
		owner.AccessToken, nil, http.StatusOK)

	// Row should be gone from the DB. We can't reach it via API now
	// (the project is deleted), so a direct SQL count is the cleanest
	// assertion.
	var remaining int
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT count(*) FROM dns_credentials WHERE id = $1`, cred.ID).
		Scan(&remaining); err != nil {
		t.Fatalf("post-cascade count: %v", err)
	}
	if remaining != 0 {
		t.Errorf("expected ON DELETE CASCADE to remove project credential, %d row(s) remain", remaining)
	}
}
