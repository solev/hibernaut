package hibd

import (
	"context"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
)

func TestLeakscanSavedImageUsesSavedAlias(t *testing.T) {
	a, out, _, cfg := newLeakscanApp(t, fake.New())
	writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("clean memory"), []byte("clean disk"))
	writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", "secret-value-123456789")
	found := false
	a.Query = func(_ context.Context, sql string) ([][]string, error) {
		if strings.HasPrefix(sql, "SELECT status_group") {
			return [][]string{{"ready"}}, nil
		}
		if !strings.Contains(sql, "al.alias = 'hibimg-myimg'") {
			t.Errorf("query = %q, want saved image alias hibimg-myimg", sql)
		}
		found = true
		return [][]string{{leakBuildA}}, nil
	}
	code := a.Run(t.Context(), []string{"leakscan", "-image", "myimg", "-json"})
	r, err := hibapi.DecodeResponse(out.Bytes())
	if code != 0 || err != nil || !found || r.LeakScan == nil || r.LeakScan.Target.Name != "myimg" ||
		r.LeakScan.SecretCount != 1 || len(r.LeakScan.Hits) != 0 {
		t.Fatalf("saved image scan = %d, %+v, %v", code, r, err)
	}
}
