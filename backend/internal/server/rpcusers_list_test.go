package server

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"reflect"
	"strings"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
)

// The RPC user list returns each user's id, name and permissions as stored.
func TestListRpcUsers(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	if r, err := s.ListRpcUsers(ctx, api.ListRpcUsersRequestObject{}); err != nil || len(r.(api.ListRpcUsers200JSONResponse)) != 0 {
		t.Fatalf("沒有使用者應回空清單: %#v %v", r, err)
	}
	for _, u := range []store.RpcUser{
		{ID: "ru-1", Username: "dapp", Permissions: []string{"eth:*", "net:peerCount"}},
		{ID: "ru-2", Username: "explorer", Permissions: []string{"eth:getBlockByNumber"}},
	} {
		if err := s.store.CreateRpcUser(ctx, u, "root"); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.ListRpcUsers(ctx, api.ListRpcUsersRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]api.RpcUser{}
	for _, u := range r.(api.ListRpcUsers200JSONResponse) {
		got[u.Username] = u
	}
	if d := got["dapp"]; d.Id != "ru-1" || !reflect.DeepEqual(d.Permissions, []string{"eth:*", "net:peerCount"}) || d.CreatedAt.IsZero() {
		t.Fatalf("dapp: %+v", d)
	}
	if e := got["explorer"]; e.Id != "ru-2" || len(e.Permissions) != 1 || len(got) != 2 {
		t.Fatalf("explorer: %+v", got)
	}
}

// The method catalog the picker shows is exactly the allowlist validatePermissions enforces: every
// namespace and every listed method is accepted.
func TestGetRpcMethodCatalog(t *testing.T) {
	r, err := (&Server{}).GetRpcMethodCatalog(context.Background(), api.GetRpcMethodCatalogRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	cat := r.(api.GetRpcMethodCatalog200JSONResponse)
	if len(cat) == 0 {
		t.Fatal("目錄是空的")
	}
	for _, ns := range cat {
		if e := validatePermissions([]string{ns.Namespace + ":*"}); e != nil {
			t.Errorf("%s:* 應合法: %+v", ns.Namespace, e)
		}
		for _, m := range ns.Methods {
			if e := validatePermissions([]string{ns.Namespace + ":" + m.Method}); e != nil {
				t.Errorf("%s:%s 應合法: %+v", ns.Namespace, m.Method, e)
			}
		}
	}
}

// The platform public key nodes install next to Besu: RS256, a PKIX public key PEM whose SHA-256
// matches the published digest, and nothing private in the response.
func TestGetPlatformRpcPublicKey(t *testing.T) {
	s, _ := accessTestServer(t)
	signer, err := loadOrCreateRpcSigner(context.Background(), s.store, bytesRepeat(32))
	if err != nil {
		t.Fatal(err)
	}
	s.rpcSigner = signer
	r, err := s.GetPlatformRpcPublicKey(context.Background(), api.GetPlatformRpcPublicKeyRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	k := r.(api.GetPlatformRpcPublicKey200JSONResponse)
	if k.Algorithm != "RS256" {
		t.Fatalf("algorithm = %q", k.Algorithm)
	}
	block, _ := pem.Decode([]byte(k.PublicKeyPem))
	if block == nil || block.Type != "PUBLIC KEY" {
		t.Fatalf("不是公鑰 PEM: %q", k.PublicKeyPem)
	}
	if _, err := x509.ParsePKIXPublicKey(block.Bytes); err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(block.Bytes); k.Sha256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 對不上公鑰: %s", k.Sha256)
	}
	raw, _ := json.Marshal(k)
	if strings.Contains(string(raw), "PRIVATE") {
		t.Fatalf("回應帶了私鑰: %s", raw)
	}
}
