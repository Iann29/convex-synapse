package synapsetest

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// accessTokenView mirrors api.accessTokenView for strict-decoding in tests.
// (Unexported in api/, so we duplicate the shape — drift is caught by the
// DisallowUnknownFields decoder.)
type accessTokenView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	ScopeID    string     `json:"scopeId,omitempty"`
	CreateTime time.Time  `json:"createTime"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

type createTokenResp struct {
	Token       string          `json:"token"`
	AccessToken accessTokenView `json:"accessToken"`
}

type listTokensResp struct {
	Items      []accessTokenView `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func TestAccessTokens_CreateDefaultScope(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	var got createTokenResp
	h.DoJSON(http.MethodPost, "/v1/create_personal_access_token", u.AccessToken,
		map[string]any{"name": "ci-runner"}, http.StatusCreated, &got)

	if !strings.HasPrefix(got.Token, "syn_") {
		t.Errorf("expected plaintext token to start with syn_, got %q", got.Token)
	}
	if got.AccessToken.ID == "" {
		t.Errorf("expected access token id to be set")
	}
	if got.AccessToken.Name != "ci-runner" {
		t.Errorf("name mismatch: got %q", got.AccessToken.Name)
	}
	if got.AccessToken.Scope != "user" {
		t.Errorf("default scope should be 'user', got %q", got.AccessToken.Scope)
	}
	if got.AccessToken.ScopeID != "" {
		t.Errorf("user-scoped token should not carry scopeId, got %q", got.AccessToken.ScopeID)
	}
	if got.AccessToken.CreateTime.IsZero() {
		t.Errorf("expected createTime to be set")
	}
}

func TestAccessTokens_CreatedTokenAuthenticatesAgainstMe(t *testing.T) {
	// The whole point of PATs: you can use them as Bearer tokens against any
	// authenticated endpoint without going through /v1/auth/login first.
	h := Setup(t)
	u := h.RegisterRandomUser()

	var created createTokenResp
	h.DoJSON(http.MethodPost, "/v1/create_personal_access_token", u.AccessToken,
		map[string]any{"name": "test-bearer"}, http.StatusCreated, &created)

	var me userResp
	h.DoJSON(http.MethodGet, "/v1/me/", created.Token, nil, http.StatusOK, &me)
	if me.ID != u.ID {
		t.Errorf("PAT auth resolved wrong user: got %s want %s", me.ID, u.ID)
	}
}

func TestAccessTokens_ListShowsTokenNoHash(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	var created createTokenResp
	h.DoJSON(http.MethodPost, "/v1/create_personal_access_token", u.AccessToken,
		map[string]any{"name": "list-me"}, http.StatusCreated, &created)

	var list listTokensResp
	h.DoJSON(http.MethodGet, "/v1/list_personal_access_tokens", u.AccessToken,
		nil, http.StatusOK, &list)

	if len(list.Items) != 1 {
		t.Fatalf("expected 1 token in list, got %d (%+v)", len(list.Items), list.Items)
	}
	if list.Items[0].ID != created.AccessToken.ID {
		t.Errorf("listed token id mismatch: %s vs %s", list.Items[0].ID, created.AccessToken.ID)
	}
	if list.Items[0].Name != "list-me" {
		t.Errorf("listed token name mismatch: %q", list.Items[0].Name)
	}
	// list response is decoded with DisallowUnknownFields — if "tokenHash" or
	// any plaintext field were present, the harness decoder would have errored.
	// (Belt-and-braces: also check the raw bytes.)
	resp := h.Do(http.MethodGet, "/v1/list_personal_access_tokens", u.AccessToken, nil)
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if strings.Contains(body, "tokenHash") || strings.Contains(body, "token_hash") {
		t.Errorf("list response leaked token hash field: %s", body)
	}
	if strings.Contains(body, "syn_") {
		t.Errorf("list response leaked plaintext token: %s", body)
	}
}

func TestAccessTokens_DeleteRevokesAuth(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	var created createTokenResp
	h.DoJSON(http.MethodPost, "/v1/create_personal_access_token", u.AccessToken,
		map[string]any{"name": "to-be-deleted"}, http.StatusCreated, &created)

	// Sanity: the token authenticates before deletion.
	h.DoJSON(http.MethodGet, "/v1/me/", created.Token, nil, http.StatusOK, &userResp{})

	h.DoJSON(http.MethodPost, "/v1/delete_personal_access_token", u.AccessToken,
		map[string]string{"id": created.AccessToken.ID}, http.StatusOK,
		&struct {
			ID string `json:"id"`
		}{})

	// After deletion the token must be rejected by the auth middleware.
	env := h.AssertStatus(http.MethodGet, "/v1/me/", created.Token, nil, http.StatusUnauthorized)
	if env.Code != "invalid_token" {
		t.Errorf("expected invalid_token after delete, got %q", env.Code)
	}

	// And the list call should now be empty.
	var list listTokensResp
	h.DoJSON(http.MethodGet, "/v1/list_personal_access_tokens", u.AccessToken,
		nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Errorf("expected empty list after delete, got %+v", list.Items)
	}
}

func TestAccessTokens_UsersOnlySeeOwnTokens(t *testing.T) {
	h := Setup(t)
	alice := h.RegisterUser("alice-tok-"+randHex(4)+"@example.test", "supersecret123", "Alice")
	bob := h.RegisterUser("bob-tok-"+randHex(4)+"@example.test", "supersecret123", "Bob")

	// Alice and Bob each create one token.
	var aliceTok, bobTok createTokenResp
	h.DoJSON(http.MethodPost, "/v1/create_personal_access_token", alice.AccessToken,
		map[string]any{"name": "alice-tok"}, http.StatusCreated, &aliceTok)
	h.DoJSON(http.MethodPost, "/v1/create_personal_access_token", bob.AccessToken,
		map[string]any{"name": "bob-tok"}, http.StatusCreated, &bobTok)

	// Each only sees their own.
	var aliceList, bobList listTokensResp
	h.DoJSON(http.MethodGet, "/v1/list_personal_access_tokens", alice.AccessToken,
		nil, http.StatusOK, &aliceList)
	h.DoJSON(http.MethodGet, "/v1/list_personal_access_tokens", bob.AccessToken,
		nil, http.StatusOK, &bobList)
	if len(aliceList.Items) != 1 || aliceList.Items[0].Name != "alice-tok" {
		t.Errorf("alice should see only her token, got %+v", aliceList.Items)
	}
	if len(bobList.Items) != 1 || bobList.Items[0].Name != "bob-tok" {
		t.Errorf("bob should see only his token, got %+v", bobList.Items)
	}

	// Bob cannot delete Alice's token.
	env := h.AssertStatus(http.MethodPost, "/v1/delete_personal_access_token",
		bob.AccessToken,
		map[string]string{"id": aliceTok.AccessToken.ID},
		http.StatusNotFound)
	if env.Code != "token_not_found" {
		t.Errorf("expected token_not_found when deleting another user's token, got %q", env.Code)
	}

	// Alice's token still works against /v1/me.
	var me userResp
	h.DoJSON(http.MethodGet, "/v1/me/", aliceTok.Token, nil, http.StatusOK, &me)
	if me.ID != alice.ID {
		t.Errorf("alice's token resolved wrong user: %s vs %s", me.ID, alice.ID)
	}
}

func TestAccessTokens_RequireAuth(t *testing.T) {
	h := Setup(t)
	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"create", http.MethodPost, "/v1/create_personal_access_token", map[string]string{"name": "x"}},
		{"list", http.MethodGet, "/v1/list_personal_access_tokens", nil},
		{"delete", http.MethodPost, "/v1/delete_personal_access_token", map[string]string{"id": "00000000-0000-0000-0000-000000000000"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := h.AssertStatus(tc.method, tc.path, "", tc.body, http.StatusUnauthorized)
			if env.Code != "missing_authorization" {
				t.Errorf("expected missing_authorization, got %q", env.Code)
			}
		})
	}
}

func TestAccessTokens_EmptyName(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	for _, name := range []string{"", "   "} {
		t.Run("name="+name, func(t *testing.T) {
			env := h.AssertStatus(http.MethodPost, "/v1/create_personal_access_token",
				u.AccessToken, map[string]any{"name": name}, http.StatusBadRequest)
			if env.Code != "missing_name" {
				t.Errorf("expected missing_name, got %q", env.Code)
			}
		})
	}
}
