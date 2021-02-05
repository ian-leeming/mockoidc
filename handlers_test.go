package mockoidc_test

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oauth2-proxy/mockoidc/v1"
	"github.com/stretchr/testify/assert"
)

func TestMockOIDC_Authorize(t *testing.T) {
	m, err := mockoidc.NewServer(nil)
	assert.NoError(t, err)

	data := url.Values{}
	data.Set("scope", "openid email profile")
	data.Set("response_type", "code")
	data.Set("redirect_uri", "example.com")
	data.Set("state", "testState")
	data.Set("client_id", m.ClientID)
	assert.HTTPError(t, m.Authorize, http.MethodGet, mockoidc.AuthorizeEndpoint, nil)

	// valid request
	assert.HTTPStatusCode(t, m.Authorize, http.MethodGet,
		mockoidc.AuthorizeEndpoint, data, http.StatusFound)

	// Bad client ID
	data.Set("client_id", "wrong_id")
	assert.HTTPStatusCode(t, m.Authorize, http.MethodGet,
		mockoidc.AuthorizeEndpoint, data, http.StatusUnauthorized)
	assert.HTTPBodyContains(t, m.Authorize, http.MethodGet,
		mockoidc.AuthorizeEndpoint, data, mockoidc.InvalidClient)

	// Missing required form values
	for key := range data {
		t.Run(key, func(t *testing.T) {
			badData, _ := url.ParseQuery(data.Encode())
			badData.Del(key)

			assert.HTTPStatusCode(t, m.Authorize, http.MethodGet,
				mockoidc.AuthorizeEndpoint, badData, http.StatusBadRequest)
			assert.HTTPBodyContains(t, m.Authorize, http.MethodGet,
				mockoidc.AuthorizeEndpoint, badData, mockoidc.InvalidRequest)
		})
	}
}

func TestMockOIDC_Token_CodeGrant(t *testing.T) {
	m, err := mockoidc.NewServer(nil)
	assert.NoError(t, err)

	session, _ := m.SessionStore.NewSession(
		"sessionScope", "sessionState", "sessionNonce", mockoidc.DefaultUser())

	assert.HTTPError(t, m.Token, http.MethodPost, mockoidc.TokenEndpoint, nil)

	data := url.Values{}
	data.Set("client_id", m.ClientID)
	data.Set("client_secret", m.ClientSecret)
	data.Set("code", session.SessionID)
	data.Set("grant_type", "authorization_code")

	// Missing parameters result in BadRequest
	for key := range data {
		t.Run(key, func(t *testing.T) {
			badData, _ := url.ParseQuery(data.Encode())
			badData.Del(key)

			rr := testResponse(t, mockoidc.TokenEndpoint, m.Token, http.MethodPost, badData)
			assert.Equal(t, http.StatusBadRequest, rr.Code)

			body, err := ioutil.ReadAll(rr.Body)
			assert.NoError(t, err)
			assert.Contains(t, string(body), mockoidc.InvalidRequest)
		})
	}

	// wrong values won't work
	for key := range data {
		t.Run(key, func(t *testing.T) {
			badData, err := url.ParseQuery(data.Encode())
			assert.NoError(t, err)

			badData.Set(key, "WRONG")
			rr := testResponse(t, mockoidc.TokenEndpoint, m.Token, http.MethodPost, badData)
			if key == "grant_type" {
				assert.Equal(t, http.StatusBadRequest, rr.Code)
			} else {
				assert.Equal(t, http.StatusUnauthorized, rr.Code)
			}
		})
	}

	// good request; check responses
	rr := testResponse(t, mockoidc.TokenEndpoint, m.Token, http.MethodPost, data)
	assert.Equal(t, http.StatusOK, rr.Code)

	tokenResp := make(map[string]interface{})
	err = getJSON(rr, &tokenResp)
	assert.NoError(t, err)

	assert.Contains(t, tokenResp, "access_token")
	assert.Contains(t, tokenResp, "id_token")
	assert.Contains(t, tokenResp, "refresh_token")
	assert.Contains(t, tokenResp, "token_type")
	assert.Contains(t, tokenResp, "expires_in")

	for _, key := range []string{
		"access_token",
		"refresh_token",
		"id_token",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := m.Keypair.VerifyJWT(tokenResp[key].(string))
			assert.NoError(t, err)
		})
	}
}

func TestMockOIDC_Token_RefreshGrant(t *testing.T) {
	m, err := mockoidc.NewServer(nil)
	assert.NoError(t, err)

	session, _ := m.SessionStore.NewSession(
		"sessionScope", "sessionStrate", "sessionNonce", mockoidc.DefaultUser())
	refreshToken, _ := session.RefreshToken(m.Config(), m.Keypair, m.Now())

	assert.HTTPError(t, m.Token, http.MethodPost, mockoidc.TokenEndpoint, nil)

	data := url.Values{}
	data.Set("client_id", m.ClientID)
	data.Set("client_secret", m.ClientSecret)
	data.Set("refresh_token", refreshToken)
	data.Set("grant_type", "refresh_token")

	// good request; check responses
	rr := testResponse(t, mockoidc.TokenEndpoint, m.Token, http.MethodPost, data)
	assert.Equal(t, http.StatusOK, rr.Code)

	tokenResp := make(map[string]interface{})
	err = getJSON(rr, &tokenResp)
	assert.NoError(t, err)

	assert.Contains(t, tokenResp, "access_token")
	assert.Contains(t, tokenResp, "id_token")
	assert.Contains(t, tokenResp, "refresh_token")
	assert.Contains(t, tokenResp, "token_type")
	assert.Contains(t, tokenResp, "expires_in")

	for _, key := range []string{
		"access_token",
		"refresh_token",
		"id_token",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := m.Keypair.VerifyJWT(tokenResp[key].(string))
			assert.NoError(t, err)
		})
	}

	// expired refresh token
	expiredToken, err := session.RefreshToken(
		m.Config(), m.Keypair, m.Now().Add(time.Hour*time.Duration(-24)))
	assert.NoError(t, err)

	data.Set("refresh_token", expiredToken)

	rr = testResponse(t, mockoidc.TokenEndpoint, m.Token, http.MethodPost, data)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	body, err := ioutil.ReadAll(rr.Body)
	assert.NoError(t, err)
	assert.Contains(t, string(body), mockoidc.InvalidRequest)
}

func TestMockOIDC_Discovery(t *testing.T) {
	m := &mockoidc.MockOIDC{
		Server: &http.Server{
			Addr: "127.0.0.1:8080",
		},
	}
	recorder := httptest.NewRecorder()
	m.Discovery(recorder, &http.Request{})

	oidcCfg := make(map[string]interface{})
	err := getJSON(recorder, &oidcCfg)
	assert.NoError(t, err)

	assert.Equal(t, oidcCfg["issuer"], m.Issuer())
	assert.Equal(t, oidcCfg["authorization_endpoint"], m.Issuer()+mockoidc.AuthorizeEndpoint)
	assert.Equal(t, oidcCfg["token_endpoint"], m.Issuer()+mockoidc.TokenEndpoint)
	assert.Equal(t, oidcCfg["userinfo_endpoint"], m.Issuer()+mockoidc.UserinfoEndpoint)
	assert.Equal(t, oidcCfg["jwks_uri"], m.Issuer()+mockoidc.JWKSEndpoint)
}

func getJSON(res *httptest.ResponseRecorder, target interface{}) error {
	return json.NewDecoder(res.Body).Decode(target)
}

func testResponse(t *testing.T, endpoint string, handler http.HandlerFunc,
	method string, values url.Values) *httptest.ResponseRecorder {

	rr := httptest.NewRecorder()
	req, err := http.NewRequest(method, endpoint, strings.NewReader(values.Encode()))
	assert.NoError(t, err)

	if method == http.MethodPost {
		req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Add("Content-Length", strconv.Itoa(len(values.Encode())))
	}
	handler(rr, req)
	return rr
}